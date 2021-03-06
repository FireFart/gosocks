package socks

// https://tools.ietf.org/html/rfc1928

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	log "github.com/sirupsen/logrus"
)

func (p *Proxy) handle(conn io.ReadWriteCloser) {
	defer conn.Close()
	defer func() {
		log.Debugln("client connection closed")
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if c, ok := conn.(net.Conn); ok {
		log.Debugf("got connection from %s", c.RemoteAddr().String())
	} else {
		log.Debug("got connection")
	}
	if err := p.socks(ctx, conn); err != nil {
		// send error reply
		log.Errorf("socks error: %v", err.Err)
		if err := p.socksErrorReply(ctx, conn, err.Reason); err != nil {
			log.Error(err)
			return
		}
	}
}

func (p *Proxy) socks(ctx context.Context, conn io.ReadWriteCloser) *Error {
	defer func() {
		if err := p.Proxyhandler.Cleanup(); err != nil {
			log.Errorf("error on cleanup: %v", err)
		}
	}()

	if err := p.handleConnect(ctx, conn); err != nil {
		return err
	}

	request, err := p.handleRequest(ctx, conn)
	if err != nil {
		return err
	}

	log.Infof("Connecting to %s", request.getDestinationString())

	// Should we assume connection succeed here?
	remote, err := p.Proxyhandler.PreHandler(*request)
	if err != nil {
		return err
	}
	defer remote.Close()

	var ip net.Addr
	if r, ok := remote.(net.Conn); ok {
		ip = r.LocalAddr()
	} else {
		ip = nil
	}
	err = p.handleRequestReply(ctx, conn, ip)
	if err != nil {
		return err
	}

	log.Debug("beginning of data copy")

	wg := &sync.WaitGroup{}
	errChannel1 := make(chan error, 1)
	errChannel2 := make(chan error, 1)
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	wg.Add(2)

	go p.copyClientToRemote(ctx2, conn, remote, wg, errChannel1)
	go p.copyRemoteToClient(ctx2, remote, conn, wg, errChannel2)
	go p.Proxyhandler.Refresh(ctx2)

	log.Debug("waiting for copy to finish")
	wg.Wait()
	// stop refreshing the connection
	cancel()
	if err := <-errChannel1; err != nil {
		return &Error{Reason: RequestReplyHostUnreachable, Err: err}
	}
	if err := <-errChannel2; err != nil {
		return &Error{Reason: RequestReplyHostUnreachable, Err: err}
	}
	log.Debug("end of connection handling")

	return nil
}

func (p *Proxy) copyClientToRemote(ctx context.Context, client io.ReadCloser, remote io.WriteCloser, wg *sync.WaitGroup, errChannel chan<- error) {
	defer wg.Done()
	defer close(errChannel)

	select {
	case <-p.Done:
		errChannel <- nil
		return
	default:
		if err := p.Proxyhandler.CopyFromClientToRemote(ctx, client, remote); err != nil {
			errChannel <- fmt.Errorf("error on copy from Client to Remote: %v", err)
			return
		}
		errChannel <- nil
		return
	}
}

func (p *Proxy) copyRemoteToClient(ctx context.Context, remote io.ReadCloser, client io.WriteCloser, wg *sync.WaitGroup, errChannel chan<- error) {
	defer wg.Done()
	defer close(errChannel)

	select {
	case <-p.Done:
		errChannel <- nil
		return
	default:
		if err := p.Proxyhandler.CopyFromRemoteToClient(ctx, remote, client); err != nil {
			errChannel <- fmt.Errorf("error on copy from Remote to Client: %v", err)
			return
		}
		errChannel <- nil
		return
	}
}

func (p *Proxy) socksErrorReply(ctx context.Context, conn io.ReadWriteCloser, reason RequestReplyReason) error {
	// send error reply
	repl, err := requestReply(nil, reason)
	if err != nil {
		return err
	}
	err = connectionWrite(ctx, conn, repl, p.Timeout)
	if err != nil {
		return err
	}

	return nil
}

func (p *Proxy) handleConnect(ctx context.Context, conn io.ReadWriteCloser) *Error {
	buf, err := connectionRead(ctx, conn, p.Timeout)
	if err != nil {
		return &Error{Reason: RequestReplyConnectionRefused, Err: err}
	}
	header, err := parseHeader(buf)
	if err != nil {
		return &Error{Reason: RequestReplyConnectionRefused, Err: err}
	}
	switch header.Version {
	case Version4:
		return &Error{Reason: RequestReplyCommandNotSupported, Err: fmt.Errorf("socks4 not yet implemented")}
	case Version5:
	default:
		return &Error{Reason: RequestReplyCommandNotSupported, Err: fmt.Errorf("version %#x not yet implemented", byte(header.Version))}
	}

	methodSupported := false
	for _, x := range header.Methods {
		if x == MethodNoAuthRequired {
			methodSupported = true
			break
		}
	}
	if !methodSupported {
		return &Error{Reason: RequestReplyMethodNotSupported, Err: fmt.Errorf("we currently only support no authentication")}
	}
	reply := make([]byte, 2)
	reply[0] = byte(Version5)
	reply[1] = byte(MethodNoAuthRequired)
	err = connectionWrite(ctx, conn, reply, p.Timeout)
	if err != nil {
		return &Error{Reason: RequestReplyGeneralFailure, Err: fmt.Errorf("could not send connect reply: %w", err)}
	}
	return nil
}

func (p *Proxy) handleRequest(ctx context.Context, conn io.ReadWriteCloser) (*Request, *Error) {
	buf, err := connectionRead(ctx, conn, p.Timeout)
	if err != nil {
		return nil, &Error{Reason: RequestReplyGeneralFailure, Err: fmt.Errorf("error on ConnectionRead: %w", err)}
	}
	request, err2 := parseRequest(buf)
	if err2 != nil {
		return nil, err2
	}
	return request, nil
}

func (p *Proxy) handleRequestReply(ctx context.Context, conn io.ReadWriteCloser, addr net.Addr) *Error {
	repl, err := requestReply(addr, RequestReplySucceeded)
	if err != nil {
		return &Error{Reason: RequestReplyGeneralFailure, Err: fmt.Errorf("error on requestReply: %w", err)}
	}
	err = connectionWrite(ctx, conn, repl, p.Timeout)
	if err != nil {
		return &Error{Reason: RequestReplyGeneralFailure, Err: fmt.Errorf("error on RequestResponse: %w", err)}
	}

	return nil
}

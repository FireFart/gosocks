package socks

import (
	"context"
	"fmt"
	"io"
	"time"
)

// connectionRead reads all data from a connection
func connectionRead(ctx context.Context, conn io.ReadCloser, timeout time.Duration) ([]byte, error) {
	var ret []byte

	ctx2, done := context.WithTimeout(ctx, timeout)
	defer done()

	readDone := make(chan bool, 1)
	errChannel := make(chan error, 1)

	go func() {
		bufLen := 1024
		for {
			buf := make([]byte, bufLen)
			i, err := conn.Read(buf)
			if err != nil {
				errChannel <- err
				return
			}
			ret = append(ret, buf[:i]...)
			if i < bufLen {
				readDone <- true
				return
			}
		}
	}()

	select {
	case <-ctx2.Done():
		return nil, fmt.Errorf("timeout when reading on connection")
	case err := <-errChannel:
		return nil, err
	case <-readDone:
		return ret, nil
	}
}

// connectionWrite makes sure to write all data to a connection
func connectionWrite(ctx context.Context, conn io.WriteCloser, data []byte, timeout time.Duration) error {
	ctx2, done := context.WithTimeout(ctx, timeout)
	defer done()

	writeDone := make(chan bool, 1)
	errChannel := make(chan error, 1)

	go func() {
		toWriteLeft := len(data)
		for {
			written, err := conn.Write(data)
			if err != nil {
				errChannel <- err
				return
			}
			if written == toWriteLeft {
				writeDone <- true
				return
			}
			toWriteLeft -= written
		}
	}()

	select {
	case <-ctx2.Done():
		return fmt.Errorf("timeout when writing to connection")
	case err := <-errChannel:
		return err
	case <-writeDone:
		return nil
	}
}

// Package gfclienttest provides test helpers for the gfclient module.
package gfclienttest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"github.gatech.edu/jon-codes/cs6200-tests/internal/ioext"
)

type Status int32

const (
	StatusOk Status = iota
	StatusFileNotFound
	StatusError
	StatusInvalid
	StatusUnknown = -1
)

var statusName = map[Status]string{
	StatusOk:           "OK",
	StatusFileNotFound: "FILE_NOT_FOUND",
	StatusError:        "ERROR",
	StatusInvalid:      "INVALID",
	StatusUnknown:      "UNKNOWN",
}

func ParseStatus(status int) Status {
	if status == 0 {
		return StatusOk
	} else if status == 1 {
		return StatusFileNotFound
	} else if status == 2 {
		return StatusError
	} else if status == 3 {
		return StatusInvalid
	} else {
		return StatusUnknown
	}
}

func (s Status) String() string {
	return statusName[s]
}

type ClientOpts struct {
	Name string
	Addr string
}

type Client struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	inbuf  bytes.Buffer
	outbuf bytes.Buffer
	errbuf bytes.Buffer
}

type ClientState struct {
	ReturnCode    int32
	Status        Status
	BytesReceived uint64
	FileLen       uint64
	Header        []byte
	Data          []byte
}

const defaultBinPath = "../gfclient_test"

func clientBinPath() string {
	binPath := defaultBinPath
	if envPath := os.Getenv("BIN_PATH"); envPath != "" {
		binPath = envPath
	}
	return binPath
}

func NewClient(ctx context.Context, addr string) (*Client, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("could not parse address %q: %w", addr, err)
	}

	if host == "" {
		host = "127.0.0.1"
	}

	cmd := exec.CommandContext(ctx, clientBinPath(), "-addr", host, "-port", port)
	cmd.Env = ([]string{"DEBUG=1"})

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &Client{}, fmt.Errorf("could not create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Client{}, fmt.Errorf("could not create stdout pipe: %w", err)
	}

	inbuf := bytes.Buffer{}
	outbuf := bytes.Buffer{}
	errbuf := bytes.Buffer{}

	cmd.Stderr = &errbuf

	c := &Client{
		cmd:    cmd,
		in:     ioext.MultiWriteCloser(stdin, &inbuf),
		out:    ioext.TeeReadCloser(stdout, &outbuf),
		inbuf:  inbuf,
		outbuf: outbuf,
		errbuf: errbuf,
	}

	err = cmd.Start()
	if err != nil {
		return c, fmt.Errorf("could not start client: %w", err)
	}

	return c, nil
}

func (c *Client) Close() error {
	if err := c.in.Close(); err != nil {
		return fmt.Errorf("could not close stdin: %w", err)
	}

	return c.cmd.Wait()
}

func readInt32(ctx context.Context, r io.Reader, ptr *int32) error {
	done := make(chan error, 1)

	go func() {
		done <- binary.Read(r, binary.NativeEndian, ptr)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// func readUint8(ctx context.Context, r io.Reader, ptr *uint8) error {
// 	done := make(chan error, 1)

// 	go func() {
// 		done <- binary.Read(r, binary.BigEndian, ptr)
// 	}()

// 	select {
// 	case <-ctx.Done():
// 		return ctx.Err()
// 	case err := <-done:
// 		return err
// 	}
// }

func readUint64(ctx context.Context, r io.Reader, ptr *uint64) error {
	done := make(chan error, 1)

	go func() {
		done <- binary.Read(r, binary.NativeEndian, ptr)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func readData(ctx context.Context, r io.Reader, buf []byte) error {
	done := make(chan error, 1)

	go func() {
		_, err := io.ReadFull(r, buf)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

const maxBuffer uint64 = 4 * 1024 * 1024 * 1024 // 4GB, arbitrary limit to prevent OOM and slice range errors

func readBinary(ctx context.Context, r io.Reader) ([]byte, error) {
	var length uint64

	err := readUint64(ctx, r, &length)
	if err != nil {
		return []byte{}, fmt.Errorf("could not read binary length: %w", err)
	}

	if length > maxBuffer {
		return []byte{}, fmt.Errorf("could not read binary data: length %d > 4GB", length)
	}

	buf := make([]byte, length)
	if err := readData(ctx, r, buf); err != nil {
		return []byte{}, fmt.Errorf("could not read binary data: %w", err)
	}

	return buf, nil
}

func writePath(ctx context.Context, path string, w io.Writer) error {
	done := make(chan error, 1)

	go func() {
		_, err := fmt.Fprintf(w, "%s\n", path)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *Client) Request(ctx context.Context, path string) (ClientState, error) {
	if err := writePath(ctx, path, c.in); err != nil {
		return ClientState{}, fmt.Errorf("Request: %w", err)
	}

	var (
		returnCode    int32
		status        int32
		bytesReceived uint64
		fileLen       uint64
		header        []byte
		data          []byte
	)

	for _, field := range []struct {
		ptr  *int32
		name string
	}{
		{name: "returnCode", ptr: &returnCode},
		{name: "status", ptr: &status},
	} {
		if err := readInt32(ctx, c.out, field.ptr); err != nil {
			return ClientState{}, fmt.Errorf("could not read %q field: %w", field.name, err)
		}
	}

	for _, field := range []struct {
		ptr  *uint64
		name string
	}{
		{name: "bytesReceived", ptr: &bytesReceived},
		{name: "fileLen", ptr: &fileLen},
	} {
		if err := readUint64(ctx, c.out, field.ptr); err != nil {
			return ClientState{}, fmt.Errorf("could not read %q field: %w", field.name, err)
		}
	}

	for _, field := range []struct {
		name string
		buf  *[]byte
	}{
		{name: "header", buf: &header},
		{name: "data", buf: &data},
	} {
		var err error
		*field.buf, err = readBinary(ctx, c.out)
		if err != nil {
			return ClientState{}, fmt.Errorf("could not read %q field: %w", field.name, err)
		}
	}

	return ClientState{
		ReturnCode:    returnCode,
		Status:        Status(status),
		BytesReceived: bytesReceived,
		FileLen:       fileLen,
		Header:        header,
		Data:          data,
	}, nil

}

func (c *Client) Stderr() string {
	return c.errbuf.String()
}

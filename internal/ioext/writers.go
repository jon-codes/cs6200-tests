package ioext

import (
	"errors"
	"io"
)

type nopWriteCloser struct{}

func (n *nopWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (n *nopWriteCloser) Close() error {
	return nil
}

// NopWriteCloser returns a WriteCloser with no-op Write and Close methods. It's
// useful in testing and in cases where a WriteCloser is required but  no actual
// writing needs to occur.
func NopWriteCloser() io.WriteCloser {
	return &nopWriteCloser{}
}

type multiWriteCloser struct {
	writer  io.Writer
	closers []io.Closer
}

func (m *multiWriteCloser) Write(p []byte) (int, error) {
	return m.writer.Write(p)
}

func (m *multiWriteCloser) Close() error {
	var errs []error
	for _, c := range m.closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// MultiWriteCloser creates a WriteCloser that duplicates its writes to all provided
// writers, similar to io.MultiWriter. If a writer implements io.WriteCloser,
// its Close method will be called when the returned WriteCloser is closed.
//
// If no writers are provided, it returns a NopWriteCloser.
// Writes to the returned WriteCloser are written to all writers using io.MultiWriter.
// When Close is called, all writers that implement io.WriteCloser will be closed
// in the order they were provided.
func MultiWriteCloser(writers ...io.Writer) io.WriteCloser {
	if len(writers) == 0 {
		return NopWriteCloser()
	}

	m := &multiWriteCloser{
		writer:  io.MultiWriter(writers...),
		closers: make([]io.Closer, 0, len(writers)),
	}

	for _, w := range writers {
		if closer, ok := w.(io.WriteCloser); ok {
			m.closers = append(m.closers, closer)
		}
	}

	return m
}

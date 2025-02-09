package ioext

import (
	"errors"
	"io"
)

type nopReadCloser struct{}

func (n *nopReadCloser) Read(p []byte) (int, error) {
	return len(p), nil
}

func (n *nopReadCloser) Close() error {
	return nil
}

// NopReadCloser returns a ReadCloser with no-op Read and Close methods.
// It mirrors the functionality of io.NopCloser
func NopReadCloser() io.ReadCloser {
	return &nopReadCloser{}
}

type teeReadCloser struct {
	reader  io.Reader
	closers []io.Closer
}

func (t *teeReadCloser) Read(p []byte) (int, error) {
	return t.reader.Read(p)
}

func (t *teeReadCloser) Close() error {
	var errs []error
	// close writer, then reader:
	for i := len(t.closers) - 1; i >= 0; i-- {
		if err := t.closers[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// TeeReadCloser returns a ReadCloser that writes to w what it reads from r.
// All reads from r area written to w. When the returned ReadCloser is closed,
// both r and w (if it implements io.Closer) will be closed in reverse order.
//
// If r is nil, TeeReadCloser returns nil.
func TeeReadCloser(r io.ReadCloser, w io.Writer) io.ReadCloser {
	if r == nil {
		return nil
	}

	t := &teeReadCloser{
		reader:  io.TeeReader(r, w),
		closers: make([]io.Closer, 0, 2),
	}

	t.closers = append(t.closers, r)

	if wc, ok := w.(io.Closer); ok {
		t.closers = append(t.closers, wc)
	}

	return t
}

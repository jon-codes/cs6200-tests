package gfclienttest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"

	"github.gatech.edu/jon-codes/cs6200-tests/internal/ioext"
	"golang.org/x/sync/errgroup"
)

type ServerOpts struct {
	ReadFunc  func(ctx context.Context, r io.ReadCloser) error
	WriteFunc func(ctx context.Context, w io.WriteCloser) error
}

type Server struct {
	listener *net.TCPListener
	opts     ServerOpts
	req      bytes.Buffer
	res      bytes.Buffer
}

func NewServer(opts ServerOpts) (*Server, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return &Server{}, fmt.Errorf("could not resolve addr: %w", err)
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return &Server{}, fmt.Errorf("could not create listener: %w", err)
	}

	return &Server{
		listener: listener,
		opts:     opts,
	}, nil
}

func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

func (s *Server) Serve(ctx context.Context) error {
	conn, err := s.listener.AcceptTCP()
	if err != nil {
		return err
	}

	reader := ioext.TeeReadCloser(conn, &s.req)
	writer := ioext.MultiWriteCloser(conn, &s.res)

	if err := s.readReq(ctx, reader); err != nil {
		return err
	}

	if err := s.writeRes(ctx, writer); err != nil {
		return err
	}
	return nil

}

func (s *Server) readReq(ctx context.Context, r io.ReadCloser) error {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if s.opts.ReadFunc != nil {
			return s.opts.ReadFunc(gCtx, r)
		} else {
			chunk := make([]byte, 1024)

			for {
				_, err := r.Read(chunk)
				if err != nil {
					if err == io.EOF {
						return fmt.Errorf("connection closed prematurely")
					} else {
						return err
					}
				}
				if idx := bytes.Index(s.req.Bytes(), []byte("\r\n\r\n")); idx > 0 {
					break
				}
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

func (s *Server) writeRes(ctx context.Context, w io.WriteCloser) error {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if s.opts.WriteFunc != nil {
			return s.opts.WriteFunc(gCtx, w)
		} else {
			_, err := w.Write([]byte("GETFILE ERROR\r\n\r\n"))
			return err
		}
	})

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

func (s *Server) Close() error {
	return s.listener.Close()
}

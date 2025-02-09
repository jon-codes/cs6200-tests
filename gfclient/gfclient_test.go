package gfclient_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"embed"
	"errors"
	"fmt"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	gft "github.gatech.edu/jon-codes/cs6200-tests/internal/gfclienttest"
	"golang.org/x/sync/errgroup"
)

//go:embed testdata/*
var testFS embed.FS

func TestClientRequest(t *testing.T) {
	t.Parallel()

	path := "/test/file.jpg"
	wantReq := fmt.Sprintf("GETFILE GET %s\r\n\r\n", path)

	ctx := createTestCtx(t, 1*time.Second)

	var got []byte

	client, server := newTestClientServer(t, ctx, gft.ServerOpts{
		ReadFunc: func(ctx context.Context, r io.ReadCloser) error {
			scanner := bufio.NewScanner(r)
			scanner.Split(onCRLFCRLF)

			if scanner.Scan() {
				got = scanner.Bytes()
			}

			if err := scanner.Err(); err != nil {
				return err
			}

			return nil
		}},
	)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		var err error
		_, err = client.Request(ctx, path)
		return err
	})

	g.Go(func() error {
		return server.Serve(ctx)
	})

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(string(got), wantReq); diff != "" {
		detail := "desc: The client should issue a correct request to the server."
		t.Errorf("req header mismatch (-got, +want)\n%s\n%s", diff, detail)
	}
}

func TestClientProtocol(t *testing.T) {
	t.Parallel()

	type res struct {
		success bool
		status  gft.Status
	}

	table := map[string]struct {
		header string
		want   res
	}{
		"scheme missing":      {header: "OK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"scheme too short":    {header: "GET OK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"scheme with space":   {header: "GET FILE OK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"scheme no space":     {header: "GETFILEOK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"scheme matching len": {header: "GETFAIL OK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"scheme too long":     {header: "GETFAILS OK 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},

		"status missing": {header: "GETFILE 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status unknown": {header: "GETFILE OKAY 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},

		"status ok len missing": {header: "GETFILE OK \r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status ok no space":    {header: "GETFILE OK1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status ok neg len":     {header: "GETFILE OK -1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status ok alpha len":   {header: "GETFILE OK one\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status ok valid":       {header: "GETFILE OK 1\r\n\r\nx", want: res{success: true, status: gft.StatusOk}},
		"status ok len 64 bit":  {header: "GETFILE OK 18446744073709551616\r\n\r\nx", want: res{success: false, status: gft.StatusOk}},

		"status not found with len": {header: "GETFILE FILE_NOT_FOUND 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status not found valid":    {header: "GETFILE FILE_NOT_FOUND\r\n\r\n", want: res{success: true, status: gft.StatusFileNotFound}},

		"status error with len": {header: "GETFILE ERROR 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status error valid":    {header: "GETFILE ERROR\r\n\r\n", want: res{success: true, status: gft.StatusError}},

		"status invalid with len": {header: "GETFILE INVALID 1\r\n\r\nx", want: res{success: false, status: gft.StatusInvalid}},
		"status invalid valid":    {header: "GETFILE INVALID\r\n\r\n", want: res{success: true, status: gft.StatusInvalid}},

		"delim missing": {header: "GETFILE FILE_NOT_FOUND", want: res{success: false, status: gft.StatusInvalid}},
	}

	for name, tt := range table {
		t.Run(name, func(t *testing.T) {
			ctx := createTestCtx(t, 1*time.Second)

			writeFunc := func(ctx context.Context, w io.WriteCloser) error {
				if _, err := io.WriteString(w, tt.header); err != nil {
					return err
				}
				if err := w.Close(); err != nil { // close client after write finish, to prevent it from hanging:
					return err
				}
				return nil
			}

			got := performRequest(t, ctx, gft.ServerOpts{WriteFunc: writeFunc})

			if got.ReturnCode > 0 {
				detail := "desc: \"gfc_perform()\" must return either zero (success), or negative (failure)."
				t.Errorf("%q ReturnCode = %d, want <= 0\n%s", tt.header, got.ReturnCode, detail)
			}

			if got.ReturnCode < 0 && tt.want.success {
				detail := "desc: \"gfc_perform()\" must return zero to indicate a successful request."
				t.Errorf("%q ReturnCode = %d, want = 0\n%s", tt.header, got.ReturnCode, detail)
			}

			if got.ReturnCode == 0 && !tt.want.success {
				detail := "dec: \"gfc_perform()\" must return a negative value to indicate request failure."
				t.Errorf("%q ReturnCode = %d, want < 0\n%s", tt.header, got.ReturnCode, detail)
			}

			if tt.want.status != got.Status {
				detail := fmt.Sprintf("desc: \"gfc_get_status()\" should return %d when the request status is %v.", tt.want.status, tt.want.status)
				t.Errorf("%q Status = %d (%v), want = %d (%v)\n%s", tt.header, got.Status, got.Status, tt.want.status, tt.want.status, detail)
			}
		})
	}
}

func onCRLFCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.Index(data, []byte{'\r', '\n', '\r', '\n'}); i >= 0 {
		return i + 4, data[:i+4], nil
	}

	if atEOF {
		return 0, nil, errors.New("read EOF before finding delimiter")
	}

	// request more data
	return 0, nil, nil
}

func TestClientHeaderFunc(t *testing.T) {
	t.Parallel()

	t.Run("single chunk", func(t *testing.T) {
		data := "hello, world!"
		header := fmt.Sprintf("GETFILE OK %d\r\n\r\n", len(data))

		ctx := createTestCtx(t, 1*time.Second)

		writeFunc := func(ctx context.Context, w io.WriteCloser) error {
			if _, err := io.WriteString(w, header); err != nil {
				return err
			}
			if _, err := io.WriteString(w, data); err != nil {
				return err
			}
			return nil
		}

		got := performRequest(t, ctx, gft.ServerOpts{WriteFunc: writeFunc})

		if diff := cmp.Diff(string(got.Header), header); diff != "" {
			detail := "desc: The function pointer passed to \"gfc_set_headerfunc()\" should be called with the header bytes."
			t.Errorf("%q Header mismatch (-got, +want)\n%s\n%s", header, diff, detail)
		}
	})

	t.Run("multi chunk", func(t *testing.T) {
		data := "hello, world!"
		header := fmt.Sprintf("GETFILE OK %d\r\n\r\n", len(data))

		ctx := createTestCtx(t, 1*time.Second)

		g, ctx := errgroup.WithContext(ctx)

		client, server := newTestClientServer(t, ctx, gft.ServerOpts{
			WriteFunc: func(ctx context.Context, w io.WriteCloser) error {
				if _, err := io.WriteString(w, header[:5]); err != nil {
					return err
				}
				time.Sleep(50 * time.Millisecond)
				if _, err := io.WriteString(w, header[5:]); err != nil {
					return err
				}
				time.Sleep(50 * time.Millisecond)
				if _, err := io.WriteString(w, data); err != nil {
					return err
				}
				return nil
			},
		})

		var got gft.ClientState

		g.Go(func() error {
			var err error
			got, err = client.Request(ctx, "/")
			return err
		})

		g.Go(func() error {
			return server.Serve(ctx)
		})

		if err := g.Wait(); err != nil {
			t.Fatal(err)
		}

		if diff := cmp.Diff(string(got.Header), header); diff != "" {
			detail := "desc: The function pointer passed to \"gfc_set_headerfunc()\" should be called with the header bytes."
			t.Errorf("%q Header mismatch (-got, +want)\n%s\n%s", header, diff, detail)
		}
	})
}

func TestClientWriteFunc(t *testing.T) {
	t.Parallel()

	t.Run("1k single chunk", func(t *testing.T) {
		data := readTestFile(t, "testdata/1K.txt")
		header := fmt.Sprintf("GETFILE OK %d\r\n\r\n", len(data))
		toWrite := append([]byte(header), data...)

		ctx := createTestCtx(t, 5*time.Second)

		writeFunc := func(ctx context.Context, w io.WriteCloser) error {
			if _, err := w.Write(toWrite); err != nil {
				return err
			}

			return nil
		}

		got := performRequest(t, ctx, gft.ServerOpts{WriteFunc: writeFunc})

		wantLen := len(data)
		gotLen := len(got.Data)
		if wantLen != gotLen {
			detail := "desc: The function pointer passed to \"gfc_set_writefunc()\" should receive a total byte count that equals the transferred file size."
			t.Errorf("writefunc data len = %d, actual = %d, want equality\n%s", gotLen, wantLen, detail)
		}

		wantSum := sha1.Sum(data)
		gotSum := sha1.Sum(got.Data)
		if diff := cmp.Diff(fmt.Sprintf("%x", gotSum), fmt.Sprintf("%x", wantSum)); diff != "" {
			detail := "desc: The function pointer passed to \"gfc_set_writefunc()\" should receive data that matches the SHA-1 checksum of the transferred file."
			t.Errorf("writefunc sha1 hash mismatch (-got, +want)\n%s\n%s", diff, detail)
		}
	})

	t.Run("1M multi chunk", func(t *testing.T) {
		const (
			minChunkSize = 64        // 64 bytes
			maxChunkSize = 64 * 1024 // 64KB
		)

		data := readTestFile(t, "testdata/1M.txt")
		header1 := "GETFILE OK "
		header2 := fmt.Sprintf("%d\r\n\r\n", len(data))
		data1 := data[:minChunkSize-len(header2)]

		ctx := createTestCtx(t, 10*time.Second)

		writeFunc := func(ctx context.Context, w io.WriteCloser) error {

			chunk1 := []byte(header1)
			if _, err := w.Write(chunk1); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)

			chunk2 := append([]byte(header2), data1...)
			if _, err := w.Write(chunk2); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)

			totalWritten := minChunkSize - len(header2)
			remaining := data[totalWritten:]

			for len(remaining) > 0 {
				delta := maxChunkSize - minChunkSize
				n, err := rand.Int(rand.Reader, big.NewInt(int64(delta)))
				if err != nil {
					return err
				}
				chunkSize := int(n.Int64()) + minChunkSize

				// Take the smaller of chunkSize or remaining data length
				if chunkSize > len(remaining) {
					chunkSize = len(remaining)
				}

				// Write chunk
				nw, err := w.Write(remaining[:chunkSize])
				totalWritten += nw
				if err != nil {
					return err
				}

				remaining = remaining[chunkSize:]

				time.Sleep(5 * time.Millisecond)
			}

			return nil
		}

		got := performRequest(t, ctx, gft.ServerOpts{WriteFunc: writeFunc})

		wantLen := len(data)
		gotLen := len(got.Data)
		if wantLen != gotLen {
			detail := "desc: The function pointer passed to \"gfc_set_writefunc()\" should receive a total byte count that equals the transferred file size."
			t.Errorf("writefunc data len = %d, actual = %d, want equality\n%s", gotLen, wantLen, detail)
		}

		wantSum := sha1.Sum(data)
		gotSum := sha1.Sum(got.Data)
		if diff := cmp.Diff(fmt.Sprintf("%x", gotSum), fmt.Sprintf("%x", wantSum)); diff != "" {
			detail := "desc: The function pointer passed to \"gfc_set_writefunc()\" should receive data that matches the SHA-1 checksum of the transferred file."
			t.Errorf("writefunc sha1 hash mismatch (-got, +want)\n%s\n%s", diff, detail)
		}
	})
}

func TestClientBytesReceived(t *testing.T) {
	t.Parallel()

	data := readTestFile(t, "testdata/1K.txt")
	header := fmt.Sprintf("GETFILE OK %d\r\n\r\n", len(data))
	toWrite := append([]byte(header), data[:100]...)

	ctx := createTestCtx(t, 1*time.Second)

	writeFunc := func(ctx context.Context, w io.WriteCloser) error {
		if _, err := w.Write(toWrite); err != nil {
			return err
		}
		w.Close() // close mid-transfer
		return nil
	}

	got := performRequest(t, ctx, gft.ServerOpts{WriteFunc: writeFunc})

	wantLen := len(data)
	gotLen := got.FileLen
	if wantLen != int(gotLen) {
		detail := "desc: \"gfc_get_filelen()\" should return the declared header file size."
		t.Errorf("%q FileLen = %d, want = %d\n%s", header, gotLen, wantLen, detail)
	}

	wantBytesReceived := len(toWrite) - len(header)
	gotBytesReceived := got.BytesReceived
	if wantBytesReceived != int(gotBytesReceived) {
		detail := "desc: \"gfc_get_bytesreceived()\" should return the number of data (non-header) bytes received before connection close."
		t.Errorf("%q BytesReceived = %d, want = %d\n%s", header, gotBytesReceived, wantBytesReceived, detail)
	}
}

func createTestCtx(t testing.TB, timeout time.Duration) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(func() { cancel() })

	return ctx
}

func readTestFile(t testing.TB, path string) []byte {
	t.Helper()

	f, err := testFS.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newTestClientServer(t testing.TB, ctx context.Context, opts gft.ServerOpts) (*gft.Client, *gft.Server) {
	t.Helper()

	server, err := gft.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}

	client, err := gft.NewClient(ctx, server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	return client, server
}

func performRequest(t testing.TB, ctx context.Context, opts gft.ServerOpts) gft.ClientState {
	t.Helper()

	client, server := newTestClientServer(t, ctx, opts)

	g, ctx := errgroup.WithContext(ctx)

	var got gft.ClientState

	g.Go(func() error {
		var err error
		got, err = client.Request(ctx, "/")
		return err
	})

	g.Go(func() error {
		return server.Serve(ctx)
	})

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	return got
}

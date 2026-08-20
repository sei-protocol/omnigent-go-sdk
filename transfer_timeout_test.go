package omnigent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// transferServer answers an upload and trickles a download, always making
// progress and never stalling.
func transferServer(t *testing.T, chunks int, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"f1"}`))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for range chunks {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 1024))
			w.(http.Flusher).Flush()
			time.Sleep(delay)
		}
	}))
}

// trickleReader yields one chunk per delay, so a body takes a predictable time to
// send without ever stalling.
type trickleReader struct {
	chunks, sent int
	delay        time.Duration
}

func (tr *trickleReader) Read(p []byte) (int, error) {
	if tr.sent >= tr.chunks {
		return 0, io.EOF
	}
	time.Sleep(tr.delay)
	tr.sent++
	return copy(p, fmt.Sprintf("%1023d\n", tr.sent)), nil
}

// TestTransfersOutliveTheUnaryBudget pins that a file transfer is not bounded by
// the whole-exchange timeout sized for an RPC.
//
// A transfer's duration is its size over the network's rate. Charging it to the
// unary budget makes a large file impossible to move at any link speed, and the
// failure looks like a timeout rather than a bound the caller chose.
func TestTransfersOutliveTheUnaryBudget(t *testing.T) {
	t.Parallel()

	const (
		chunks = 10
		delay  = 40 * time.Millisecond
		unary  = 150 * time.Millisecond
	)
	srv := transferServer(t, chunks, delay)
	defer srv.Close()

	client, err := New(srv.URL, WithUnaryTimeout(unary))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()
	files := client.Files().ForSession("s1")

	if _, err := files.Upload(context.Background(), "big.bin",
		&trickleReader{chunks: chunks, delay: delay}); err != nil {
		t.Errorf("upload of a body that outlives the unary budget: %v", err)
	}

	var sink strings.Builder
	n, err := files.Download(context.Background(), "f1", &sink, 1<<20)
	if err != nil {
		t.Errorf("download that outlives the unary budget: %v", err)
	}
	if want := int64(chunks * 1024); n != want {
		t.Errorf("downloaded %d bytes, want %d: the body was cut short", n, want)
	}
}

// TestWithTransferTimeoutBoundsATransfer pins that a caller who wants one ceiling
// for every transfer gets it, rather than having to set a deadline per call.
func TestWithTransferTimeoutBoundsATransfer(t *testing.T) {
	t.Parallel()

	srv := transferServer(t, 10, 40*time.Millisecond)
	defer srv.Close()

	client, err := New(srv.URL, WithTransferTimeout(80*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	var sink strings.Builder
	_, err = client.Files().ForSession("s1").Download(context.Background(), "f1", &sink, 1<<20)
	if err == nil {
		t.Fatal("a transfer outran WithTransferTimeout and still succeeded")
	}
}

// TestTransferHonoursTheCallersContext pins the bound that replaces the unary
// budget. With no whole-transfer timeout by default, the context is the only
// limit, so it has to work.
func TestTransferHonoursTheCallersContext(t *testing.T) {
	t.Parallel()

	srv := transferServer(t, 20, 40*time.Millisecond)
	defer srv.Close()

	client, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var sink strings.Builder
	start := time.Now()
	if _, err := client.Files().ForSession("s1").Download(ctx, "f1", &sink, 1<<20); err == nil {
		t.Fatal("the context deadline did not stop the download")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the download ran %v past a 100ms deadline", elapsed)
	}
}

// TestUnaryCallsKeepTheirBudget pins that the transfer client did not loosen the
// bound on ordinary calls.
func TestUnaryCallsKeepTheirBudget(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	defer srv.Close()

	client, err := New(srv.URL, WithUnaryTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Sessions().Get(context.Background(), "s1", GetSessionOptions{}); err == nil {
		t.Fatal("a unary call outran WithUnaryTimeout and still succeeded")
	} else if errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("the call was rejected rather than timed out: %v", err)
	}
}

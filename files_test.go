package omnigent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestUploadStreamsTheBodyRatherThanBuffering pins the reason Upload takes a
// reader.
//
// A path-taking upload reads the file; a body built in memory holds it. This one
// streams through a pipe, so the request holds one buffer whatever the size.
//
// Proved by rendezvous rather than by measuring the heap. The heap is a
// process-wide number, so a test that reads it returns a verdict on whatever else
// the suite is running at the time. Here the reader refuses to produce its second
// chunk until the server has read part of the first: an implementation that
// buffered the whole body before sending would have to drain the reader first, so
// the two would wait on each other and the upload would never finish.
func TestUploadStreamsTheBodyRatherThanBuffering(t *testing.T) {
	t.Parallel()

	const (
		chunk  = 64 << 10
		chunks = 32
	)

	served := make(chan struct{})
	var received int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		part, err := r.MultipartReader()
		if err != nil {
			t.Errorf("MultipartReader: %v", err)
			return
		}
		for {
			p, err := part.NextPart()
			if err != nil {
				break
			}
			// Signal on the first byte of body that arrives, which is what releases
			// the reader below.
			buf := make([]byte, 1)
			if n, err := io.ReadFull(p, buf); n > 0 && err == nil {
				received += int64(n)
				close(served)
			}
			n, _ := io.Copy(io.Discard, p)
			received += n
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file_1","filename":"big.bin"}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	// A deadline, so a buffering implementation fails here instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := &rendezvousReader{t: t, chunk: chunk, remaining: chunk * chunks, released: served}
	file, err := client.Files().ForSession("conv_1").Upload(ctx, "big.bin", body)
	if err != nil {
		t.Fatalf("Upload: %v — the body was not streamed", err)
	}

	if want := int64(chunk * chunks); received != want {
		t.Errorf("server received %d bytes, want %d", received, want)
	}
	if file.ID != "file_1" {
		t.Errorf("file.ID = %q, want file_1", file.ID)
	}
	if !body.waited {
		t.Error("the reader never had to wait, so the rendezvous proved nothing")
	}
}

// rendezvousReader yields the first chunk, then blocks until the server reports
// that it has read part of it. A buffering implementation deadlocks on that wait.
//
// remaining counts bytes, not calls: io.Copy chooses the buffer size, so a
// per-call count delivers whatever multiple of that size the copy happens to use.
type rendezvousReader struct {
	t         *testing.T
	chunk     int
	remaining int
	released  <-chan struct{}
	sent      int
	waited    bool
}

func (r *rendezvousReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	// After the first chunk, not before it: the server cannot have read anything
	// until something was written, so waiting first would deadlock either way and
	// prove nothing about buffering.
	if r.sent > 0 && !r.waited {
		select {
		case <-r.released:
		case <-time.After(10 * time.Second):
			r.t.Error("the server read nothing while the body was still being written")
			return 0, io.ErrUnexpectedEOF
		}
		r.waited = true
	}
	n := min(len(p), r.chunk, r.remaining)
	clear(p[:n])
	r.remaining -= n
	r.sent += n
	return n, nil
}

// TestUploadSurfacesAReadFailureRatherThanHanging pins the pipe's close contract.
//
// The goroutine writing the multipart body owns the pipe writer and closes it on
// every path. Without that, a failing reader leaves the request side waiting for
// bytes that will never come.
func TestUploadSurfacesAReadFailureRatherThanHanging(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Files().ForSession("conv_1").Upload(
		context.Background(), "broken.bin", failingReader{})
	if err == nil {
		t.Fatal("Upload = nil error for a reader that fails mid-body")
	}
}

func TestDownloadRequiresABound(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var sink bytes.Buffer
	if _, err := client.Files().ForSession("conv_1").Download(
		context.Background(), "file_1", &sink, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("error = %v, want ErrInvalidArgument for an absent bound", err)
	}
}

func TestFilesRequireASession(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	files := client.Files().ForSession("")
	if _, err := files.Get(context.Background(), "file_1"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Get error = %v, want ErrInvalidArgument", err)
	}
	if err := files.Delete(context.Background(), "file_1"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Delete error = %v, want ErrInvalidArgument", err)
	}
	if _, err := files.Upload(context.Background(), "n", strings.NewReader("x")); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Upload error = %v, want ErrInvalidArgument", err)
	}
}

// zeroes is an endless reader, so a test can name a size without holding it.
// failingReader fails after one chunk, which is the interesting case: the body
// has already started, so the request is in flight.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("the reader failed mid-body")
}

// heapInUse reports live heap bytes after a collection, so a growth measurement
// is about retention rather than garbage.

// TestSessionFileKeepsTheBodyItDecodedFrom pins the escape hatch on the one
// surface with no schema.
//
// Every session file route publishes an empty response schema, so the named
// fields are observation rather than contract. A caller reaching for a field this
// package does not name has no other route to it.
func TestSessionFileKeepsTheBodyItDecodedFrom(t *testing.T) {
	t.Parallel()

	// A field this package does not name, which is the whole point.
	const body = `{"id":"file_1","filename":"a.txt","bytes":12,"purpose":"assistants","x_server_added":{"nested":true}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	file, err := client.Files().ForSession("conv_1").Get(context.Background(), "file_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if file.ID != "file_1" || file.Filename == nil || *file.Filename != "a.txt" {
		t.Errorf("named fields lost: %+v", file)
	}
	if file.Raw == nil {
		t.Fatal("Raw is nil, so the documented escape hatch reaches nothing")
	}
	if got := file.Raw["purpose"]; got != "assistants" {
		t.Errorf(`Raw["purpose"] = %v, want "assistants"`, got)
	}
	nested, ok := file.Raw["x_server_added"].(map[string]any)
	if !ok || nested["nested"] != true {
		t.Errorf(`Raw["x_server_added"] = %v, want the nested object`, file.Raw["x_server_added"])
	}
	// The named fields must also survive in Raw, so a caller reads one source.
	if got := file.Raw["id"]; got != "file_1" {
		t.Errorf(`Raw["id"] = %v, want file_1`, got)
	}
}

// TestRejectedUploadLeavesNoGoroutine pins the pipe's other half.
//
// Upload starts the writer goroutine before doUpload validates anything, so a
// rejection that never builds a request leaves nothing to close the pipe and the
// goroutine blocks on its first write forever. Measured before the fix: three
// rejected uploads, three permanently blocked goroutines.
func TestRejectedUploadLeavesNoGoroutine(t *testing.T) {
	base := runtime.NumGoroutine()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A dot segment is rejected inside the request path, after the goroutine
	// starts and before any request exists.
	for range 5 {
		if _, err := client.Files().ForSession(".").Upload(
			context.Background(), "a.txt", strings.NewReader("payload")); err == nil {
			t.Fatal("Upload = nil error for a path segment that cannot be resolved")
		}
	}

	for range 20 {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines %d -> %d after 5 rejected uploads", base, runtime.NumGoroutine())
}

// TestDownloadWritesAtMostTheBound pins what the caller's writer receives, which
// is the assertion the first version of this test missed.
//
// Reading one byte past the bound is how a file that fits is told from one that
// was truncated. That byte must not reach a writer that declared a capacity, and
// the returned count must match what the writer got.
func TestDownloadWritesAtMostTheBound(t *testing.T) {
	t.Parallel()

	const bound = 10
	for _, size := range []int{bound - 1, bound, bound + 1, 4096} {
		t.Run(fmt.Sprintf("body_%d", size), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(bytes.Repeat([]byte("A"), size))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			var sink bytes.Buffer
			n, err := client.Files().ForSession("c").Download(
				context.Background(), "f", &sink, bound)

			if sink.Len() > bound {
				t.Errorf("wrote %d bytes to a writer that declared %d", sink.Len(), bound)
			}
			if int(n) != sink.Len() {
				t.Errorf("returned %d but the writer received %d", n, sink.Len())
			}
			if size > bound {
				if !errors.Is(err, ErrTruncated) {
					t.Errorf("error = %v, want ErrTruncated for a body past the bound", err)
				}
			} else if err != nil {
				t.Errorf("error = %v for a body that fits", err)
			}
		})
	}
}

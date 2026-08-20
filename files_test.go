package omnigent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// TestUploadDoesNotHoldTheFileInMemory pins the reason Upload takes a reader.
//
// A path-taking upload reads the file; a body built in memory holds it. This one
// streams through a pipe, so the request holds one buffer whatever the size.
func TestUploadDoesNotHoldTheFileInMemory(t *testing.T) {
	t.Parallel()

	const size = 32 << 20 // larger than any buffer this package should allocate

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

	before := heapInUse()
	file, err := client.Files().ForSession("conv_1").Upload(
		context.Background(), "big.bin", io.LimitReader(zeroes{}, size))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	growth := heapInUse() - before

	if received != size {
		t.Errorf("server received %d bytes, want %d", received, size)
	}
	if file.ID != "file_1" {
		t.Errorf("file.ID = %q, want file_1", file.ID)
	}
	// Generous, because a test cannot control the collector. The point is that
	// growth does not track the payload.
	if growth > size/4 {
		t.Errorf("heap grew %d bytes uploading %d; the body is being buffered", growth, size)
	}
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

func TestDownloadRefusesToWritePastTheCallersBound(t *testing.T) {
	t.Parallel()

	const bound = 1 << 10
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(zeroes{}, bound*4))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var sink bytes.Buffer
	written, err := client.Files().ForSession("conv_1").Download(
		context.Background(), "file_1", &sink, bound)
	if err == nil {
		t.Fatal("Download = nil error for a body past the bound")
	}
	if written > bound {
		t.Errorf("wrote %d bytes past a %d-byte bound", written, bound)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want it to name the bound", err)
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
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// failingReader fails after one chunk, which is the interesting case: the body
// has already started, so the request is in flight.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("the reader failed mid-body")
}

// heapInUse reports live heap bytes after a collection, so a growth measurement
// is about retention rather than garbage.
func heapInUse() int64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return int64(stats.HeapInuse)
}

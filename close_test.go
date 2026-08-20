package omnigent

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCloseReleasesTheConnectionPool pins the lever for a program that cannot
// share one client.
//
// [New] gives each client its own pool, so without [Client.Close] a client
// constructed per tenant or per credential refresh strands the goroutines that
// serve its idle connections until the server's own timeout expires.
func TestCloseReleasesTheConnectionPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"conv_1"}`))
	}))
	defer server.Close()

	persistConns := func() int {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		return strings.Count(string(buf[:n]), "net/http.(*persistConn).readLoop")
	}

	const clients = 25
	settle := func() {
		for range 20 {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
		}
	}

	settle()
	before := persistConns()

	for range clients {
		c, err := New(server.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		var out map[string]any
		if err := c.doJSON(t.Context(), http.MethodGet,
			[]string{"v1", "sessions", "conv_1"}, nil, nil, &out); err != nil {
			t.Fatalf("doJSON: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	settle()
	after := persistConns()

	// Each un-closed pool would strand one reader goroutine per client.
	if after-before >= clients {
		t.Errorf("connection readers grew by %d across %d closed clients; Close released nothing",
			after-before, clients)
	}
}

// TestCloseLeavesACallerSuppliedClientAlone pins the ownership boundary: a
// transport the caller built is the caller's to drain.
func TestCloseLeavesACallerSuppliedClientAlone(t *testing.T) {
	t.Parallel()

	supplied := &http.Client{}
	c, err := New("http://127.0.0.1:1", WithHTTPClient(supplied))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.ownsTransport {
		t.Error("ownsTransport is true for a caller-supplied client")
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

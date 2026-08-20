package omnigent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUploadRefusesAFilenameThatWouldForgeAMultipartHeader pins the rejection
// mime/multipart does not do. CreateFormFile escapes a quote and a backslash and
// passes CR and LF through, so without the guard the name ends the
// Content-Disposition line and the rest is read as another header or part.
func TestUploadRefusesAFilenameThatWouldForgeAMultipartHeader(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	for _, name := range []string{
		"ok.txt\r\nX-Injected: yes",
		"ok.txt\nX-Injected: yes",
		"ok\x00.txt",
	} {
		_, err := client.Files().ForSession("s1").Upload(context.Background(), name, strings.NewReader("x"))
		if err == nil {
			t.Fatalf("filename %q was accepted", name)
		}
		if !strings.Contains(err.Error(), "forge a multipart header") {
			t.Fatalf("filename %q: got %v, want the multipart-header refusal", name, err)
		}
	}
	if reached {
		t.Fatal("a rejected filename still reached the server")
	}
}

// TestAPIErrorSanitizesEveryServerChosenField pins that no field the server
// picks can forge a second log line or flood one. Code and RequestID were the two
// that reached the rendered message raw.
func TestAPIErrorSanitizesEveryServerChosenField(t *testing.T) {
	err := &APIError{
		StatusCode: 400,
		Code:       "bad\r\nFATAL: forged line from the code field",
		RequestID:  "id\r\nFATAL: forged line from the request id" + strings.Repeat("A", 500),
	}
	got := err.Error()
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("rendered error carries a control byte: %q", got)
	}
	if n := len(strings.Split(got, "\n")); n != 1 {
		t.Fatalf("rendered error spans %d lines", n)
	}
	// The request id is bounded independently of the prose fields.
	if len(got) > 600 {
		t.Fatalf("rendered error is %d bytes; the caps did not apply: %q", len(got), got)
	}
}

// TestResolveAgentBoundsWhatTheServerCanPutInAnError pins both caps. The listing
// route is unbounded and every name in it is the server's choice, so an
// unbounded render is an unbounded error string.
func TestResolveAgentBoundsWhatTheServerCanPutInAnError(t *testing.T) {
	const agents = 1000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, 0, agents)
		for i := range agents {
			items = append(items, map[string]any{
				"name": fmt.Sprintf("agent-%d\r\nFATAL: forged\r\n", i) + strings.Repeat("B", 200),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "has_more": false})
	}))
	defer srv.Close()

	client, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Sessions().ResolveAgent(context.Background(), "absent")
	if err == nil {
		t.Fatal("want ErrNotFound")
	}
	msg := err.Error()
	if lines := strings.Count(msg, "\n"); lines != 0 {
		t.Fatalf("error spans %d extra lines: %q", lines+1, msg)
	}
	if len(msg) > 1200 {
		t.Fatalf("error is %d bytes; the render is unbounded", len(msg))
	}
	if !strings.Contains(msg, "...") {
		t.Fatalf("truncation was not reported: %q", msg)
	}
	t.Logf("bounded error is %d bytes on 1 line", len(msg))
}

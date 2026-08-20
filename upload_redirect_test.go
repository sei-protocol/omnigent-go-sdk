package omnigent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUploadClassifiesARedirectItCannotFollow pins the classification net/http
// skips on an upload.
//
// A 307 or 308 is followed by replaying the request, and a streamed body cannot
// be replayed, so net/http returns the response without consulting the redirect
// policy. Every other method has its redirects classified; an upload reported a
// bare 307, which distinguishes neither a hostile location from a path-rewriting
// proxy nor either from an ordinary API failure.
func TestUploadClassifiesARedirectItCannotFollow(t *testing.T) {
	t.Parallel()

	var elsewhereReached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereReached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"stolen"}`))
	}))
	defer elsewhere.Close()

	tests := []struct {
		name     string
		status   int
		location func(own string) string
		want     error
		// message names the one thing the operator has to act on.
		message string
	}{
		{
			name:     "off-host location is the security case",
			status:   http.StatusTemporaryRedirect,
			location: func(string) string { return elsewhere.URL + "/v1/steal" },
			want:     ErrUnsafeRedirect,
			message:  "does not name",
		},
		{
			name:     "same-host path rewrite is a configuration case",
			status:   http.StatusTemporaryRedirect,
			location: func(string) string { return "/rewritten/upload" },
			want:     ErrRedirectNotFollowed,
			message:  "point the base URL",
		},
		{
			name:     "308 is classified like 307",
			status:   http.StatusPermanentRedirect,
			location: func(string) string { return "/rewritten/upload" },
			want:     ErrRedirectNotFollowed,
			message:  "point the base URL",
		},
		{
			name:     "an unparseable location is refused, not excused",
			status:   http.StatusTemporaryRedirect,
			location: func(string) string { return "http://[::1]:namedport/x" },
			want:     ErrUnsafeRedirect,
			message:  "does not parse",
		},
		{
			name:     "a scheme this package does not speak is refused",
			status:   http.StatusTemporaryRedirect,
			location: func(string) string { return "file:///etc/passwd" },
			want:     ErrUnsafeRedirect,
			message:  "does not name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", tc.location(r.Host))
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			client, err := New(srv.URL, WithAuthHeader("X-Secret", "topsecret"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = client.Close() }()

			_, err = client.Files().ForSession("s1").Upload(
				context.Background(), "f.txt", strings.NewReader("payload"))
			if err == nil {
				t.Fatal("a redirected upload reported success")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want it to wrap %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error does not say what to do: %v", err)
			}
			// The credential never travels: net/http declines to replay the body,
			// and this classification must not change that.
			if elsewhereReached {
				t.Error("the redirect target was reached")
			}
			// A location is never rendered — only a host, as the redirect policy does.
			if strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "/etc/passwd") {
				t.Errorf("error rendered a location or a credential: %v", err)
			}
		})
	}
}

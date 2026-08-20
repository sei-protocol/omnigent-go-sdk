package omnigent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The credentials these tests hand the client. Every assertion here is that
// they did NOT reach somewhere, so the values say so rather than being named
// after what they are.
const (
	bearerFixture = "bearer-must-not-travel"
	cookieFixture = "cookie-must-not-travel"
)

// recorder is a server that records what reached it, so a test can assert that
// nothing did.
type recorder struct {
	server   *httptest.Server
	requests chan *http.Request
	bodies   chan string
}

// newRecorder starts a server that records each request and answers with body.
func newRecorder(t *testing.T, body string) *recorder {
	t.Helper()

	rec := &recorder{
		requests: make(chan *http.Request, 8),
		bodies:   make(chan string, 8),
	}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		rec.requests <- r.Clone(r.Context())
		rec.bodies <- string(payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *recorder) hits() int { return len(r.requests) }

// otherHostURL renders the recorder's address under a different hostname that
// still resolves to it, so a test can cross a host boundary without DNS.
func (r *recorder) otherHostURL(t *testing.T, path string) string {
	t.Helper()

	parsed, err := url.Parse(r.server.URL)
	if err != nil {
		t.Fatalf("parse recorder URL: %v", err)
	}
	// 127.0.0.1 and localhost are the same machine and different hostnames,
	// which is exactly the hop Go's own header-stripping rule looks at.
	if parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("recorder listens on %q, expected 127.0.0.1", parsed.Hostname())
	}
	return "http://localhost:" + parsed.Port() + path
}

// redirectTo answers every request with status and a Location of target.
func redirectTo(status int, target func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target())
		w.WriteHeader(status)
	}
}

const sessionJSON = `{"id":"conv_1","agent_id":"ag_1","status":"idle","created_at":1}`

// TestRedirectsNeverCarryCredentialsOffTheBaseURL is the finding: with no
// CheckRedirect, net/http follows up to ten hops and strips only Authorization,
// Cookie, Www-Authenticate and Cookie2 when the host changes. A custom identity
// header — the trusted-proxy one WithAuthHeader sets — is not on that list, and a
// cross-host 307 or 308 replays the request body as well.
//
// Against feat/go-client-v2 every cross-host subtest here fails: the elsewhere
// recorder receives the request, complete with X-Forwarded-Email and, for the
// 307, the prompt text.
func TestRedirectsNeverCarryCredentialsOffTheBaseURL(t *testing.T) {
	t.Parallel()

	const identity = "someone@example.test"

	tests := []struct {
		name string
		// target renders the Location header from the elsewhere recorder.
		target func(t *testing.T, elsewhere *recorder) string
		status int
		call   func(ctx context.Context, c *Client) error
	}{
		{
			name:   "a 302 to another hostname",
			status: http.StatusFound,
			target: func(t *testing.T, e *recorder) string { return e.otherHostURL(t, "/v1/sessions/conv_1") },
			call: func(ctx context.Context, c *Client) error {
				var out map[string]any
				return c.doJSON(ctx, http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)
			},
		},
		{
			name:   "a 302 to another port on the same hostname",
			status: http.StatusFound,
			target: func(_ *testing.T, e *recorder) string { return e.server.URL + "/v1/sessions/conv_1" },
			call: func(ctx context.Context, c *Client) error {
				var out map[string]any
				return c.doJSON(ctx, http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)
			},
		},
		{
			name:   "a 307, which would replay the request body",
			status: http.StatusTemporaryRedirect,
			target: func(t *testing.T, e *recorder) string {
				return e.otherHostURL(t, "/v1/sessions/conv_1/events")
			},
			call: func(ctx context.Context, c *Client) error {
				// A POST, so the case covers a redirect that would replay or
				// rewrite a request body rather than just re-issue a read.
				body := map[string]any{"type": "message", "text": "the caller's prompt text"}
				var out map[string]any
				return c.doJSON(ctx, http.MethodPost,
					[]string{"v1", "sessions", "conv_1", "events"}, nil, body, &out)
			},
		},
		{
			name:   "a 308, likewise",
			status: http.StatusPermanentRedirect,
			target: func(t *testing.T, e *recorder) string {
				return e.otherHostURL(t, "/v1/sessions/conv_1/events")
			},
			call: func(ctx context.Context, c *Client) error {
				// A POST, so the case covers a redirect that would replay or
				// rewrite a request body rather than just re-issue a read.
				body := map[string]any{"type": "message", "text": "the caller's prompt text"}
				var out map[string]any
				return c.doJSON(ctx, http.MethodPost,
					[]string{"v1", "sessions", "conv_1", "events"}, nil, body, &out)
			},
		},
		{
			name:   "a redirected stream, which uses the other http.Client",
			status: http.StatusFound,
			target: func(t *testing.T, e *recorder) string {
				return e.otherHostURL(t, "/v1/sessions/conv_1/stream")
			},
			call: func(ctx context.Context, c *Client) error {
				_, err := collectSeq(c.Stream(ctx, "conv_1", StreamOptions{}))
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			elsewhere := newRecorder(t, sessionJSON)
			var target string
			origin := httptest.NewServer(redirectTo(tc.status, func() string { return target }))
			defer origin.Close()
			target = tc.target(t, elsewhere)

			client, err := New(origin.URL,
				WithAuthHeader("X-Forwarded-Email", identity),
				WithBearerToken(bearerFixture),
				WithSessionCookie("ap_session", cookieFixture),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = tc.call(context.Background(), client)
			if !errors.Is(err, ErrUnsafeRedirect) {
				t.Fatalf("error = %v, want it to wrap ErrUnsafeRedirect", err)
			}
			if hits := elsewhere.hits(); hits != 0 {
				got := <-elsewhere.requests
				t.Fatalf("the redirect target received %d request(s); first had X-Forwarded-Email=%q, "+
					"Authorization=%q, Cookie=%q",
					hits, got.Header.Get("X-Forwarded-Email"),
					got.Header.Get("Authorization"), got.Header.Get("Cookie"))
			}
		})
	}
}

// TestRedirectsNeverStepDownToPlainHTTP covers the hop Go permits because
// shouldCopyHeaderOnRedirect compares hostnames and never schemes: same host,
// https to http, credential intact and now in cleartext.
func TestRedirectsNeverStepDownToPlainHTTP(t *testing.T) {
	t.Parallel()

	// A plain-http recorder, and a TLS origin that redirects to it on the same
	// hostname and port-independent of it — the scheme is what must be refused.
	plaintext := newRecorder(t, sessionJSON)
	plaintextURL, err := url.Parse(plaintext.server.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var origin *httptest.Server
	origin = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originURL, _ := url.Parse(origin.URL)
		// Same hostname as the origin, plain http: the exact shape Go follows.
		w.Header().Set("Location", "http://"+originURL.Hostname()+":"+plaintextURL.Port()+r.URL.Path)
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()

	client, err := New(origin.URL,
		WithHTTPClient(origin.Client()),
		WithBearerToken(bearerFixture),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out map[string]any
	err = client.doJSON(context.Background(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("error = %v, want it to wrap ErrUnsafeRedirect", err)
	}
	if hits := plaintext.hits(); hits != 0 {
		t.Fatalf("the plaintext listener received %d request(s); a credential travelled in clear", hits)
	}
}

// TestRedirectsOnTheSameEndpointStillWork is the other half of the policy: the
// ordinary redirect a server uses to normalise a path is not a security event,
// and refusing it would be a regression rather than a fix.
func TestRedirectsOnTheSameEndpointStillWork(t *testing.T) {
	t.Parallel()

	var hops int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions/conv_1", func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/moved/conv_1", http.StatusFound)
	})
	mux.HandleFunc("/moved/conv_1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want the credential to survive a same-endpoint hop",
				r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, sessionJSON)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(server.URL, WithBearerToken("tok"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var session map[string]any
	err = client.doJSON(context.Background(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &session)
	if err != nil {
		t.Fatalf("doJSON: %v, want the same-host same-scheme redirect to be followed", err)
	}
	if session["id"] != "conv_1" {
		t.Errorf(`session["id"] = %v, want conv_1`, session["id"])
	}
	if hops != 1 {
		t.Errorf("origin handler ran %d times, want 1", hops)
	}
}

// TestSendInputCannotReportADroppedWriteAsSuccess is S7. A 302 on a POST is
// rewritten to GET by every HTTP client there is, so the input is not delivered
// — and the GET's 200 then decodes into an acknowledgement. Against
// feat/go-client-v2 this test fails with err == nil and ItemID == "item_9" for a
// message the server never received.
func TestSendInputCannotReportADroppedWriteAsSuccess(t *testing.T) {
	t.Parallel()

	var posts, gets int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions/conv_1/events", func(w http.ResponseWriter, r *http.Request) {
		posts++
		// Same host, same scheme: only the method rewrite makes this unsafe.
		http.Redirect(w, r, "/accepted", http.StatusFound)
	})
	mux.HandleFunc("/accepted", func(w http.ResponseWriter, r *http.Request) {
		gets++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"queued":true,"item_id":"item_9"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var accepted map[string]any
	err = client.doJSON(context.Background(), http.MethodPost,
		[]string{"v1", "sessions", "conv_1", "events"}, nil,
		map[string]any{"type": "message", "text": "hello"}, &accepted)
	if err == nil {
		t.Fatalf("the write = %+v, nil error: the input was dropped by a redirect and reported queued",
			accepted)
	}
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("error = %v, want it to wrap ErrUnsafeRedirect", err)
	}
	if accepted != nil {
		t.Errorf("accepted = %+v, want nil alongside the error", accepted)
	}
	if posts != 1 {
		t.Errorf("the events route saw %d POSTs, want 1", posts)
	}
	if gets != 0 {
		t.Errorf("the redirect target was fetched %d times; the write should not have become a read", gets)
	}
}

// TestRedirectChainIsCapped keeps the stdlib's own bound, which supplying a
// CheckRedirect replaces rather than supplements.
func TestRedirectChainIsCapped(t *testing.T) {
	t.Parallel()

	var hops int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", hops), http.StatusFound)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out map[string]any
	err = client.doJSON(context.Background(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("error = %v, want it to wrap ErrUnsafeRedirect", err)
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("error %q does not report the redirect cap", err)
	}
	if hops > maxRedirects+1 {
		t.Errorf("followed %d hops, want at most %d", hops, maxRedirects+1)
	}
}

// collectSeq drains a stream and returns its terminal error, for tests that care
// only about how it ended.
func collectSeq(stream func(yield func(Event, error) bool)) ([]Event, error) {
	var (
		events []Event
		failed error
	)
	for event, err := range stream {
		if err != nil {
			failed = err
			continue
		}
		events = append(events, event)
	}
	return events, failed
}

// TestRedirectsNeverCarryAttackerChosenBasicAuth is the maintainer's finding on
// checkRedirect refuses a Location carrying userinfo, because net/http turns it
// into an Authorization: Basic header on the replayed request — the same rule New
// applies to a base URL.
//
// The hop here clears every other gate — same host, same port, same scheme, same
// method — so the userinfo arm is the only thing that can reject it. net/http
// synthesizes Basic only when no Authorization header is already set, which is
// why this uses WithAuthHeader rather than WithBearerToken: a bearer caller is
// incidentally safe, and testing that case would pass with the fix reverted.
func TestRedirectsNeverCarryAttackerChosenBasicAuth(t *testing.T) {
	t.Parallel()

	var landed bool
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions/conv_1", func(w http.ResponseWriter, r *http.Request) {
		// A same-host Location differing only by userinfo.
		w.Header().Set("Location", "http://evil:pw@"+r.Host+"/moved/conv_1")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/moved/conv_1", func(w http.ResponseWriter, r *http.Request) {
		landed = true
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, sessionJSON)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(server.URL, WithAuthHeader("X-Forwarded-User", "svc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out map[string]any
	if err = client.doJSON(context.Background(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out); err == nil {
		t.Fatal("doJSON succeeded, want ErrUnsafeRedirect for a Location carrying userinfo")
	} else if !errors.Is(err, ErrUnsafeRedirect) {
		t.Errorf("error = %v, want ErrUnsafeRedirect", err)
	}
	if landed {
		t.Errorf("the redirect was followed; the replayed request carried Authorization %q "+
			"that the caller never configured", gotAuth)
	}
}

// TestSameEndpointIgnoresHostCase covers the other half: DNS names are
// case-insensitive, so a Location differing from the base URL only in the case of
// its host names the same server and must still be followed. Fail-closed, so the
// bug this guards against is a refused legitimate redirect rather than a leak.
func TestSameEndpointIgnoresHostCase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		origin, next string
		want         bool
	}{
		{"same host, differing case", "http://Example.COM/a", "http://example.com/b", true},
		{"same host, same case", "http://example.com/a", "http://example.com/b", true},
		{"genuinely different host", "http://example.com/a", "http://evil.com/b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origin, err := url.Parse(tc.origin)
			if err != nil {
				t.Fatalf("parse origin: %v", err)
			}
			next, err := url.Parse(tc.next)
			if err != nil {
				t.Fatalf("parse next: %v", err)
			}
			if got := sameEndpoint(origin, next); got != tc.want {
				t.Errorf("sameEndpoint(%q, %q) = %v, want %v", tc.origin, tc.next, got, tc.want)
			}
		})
	}
}

// TestRedirectRefusesASchemeThePackageDoesNotSpeak covers the gate that runs
// ahead of the host and port comparisons.
//
// Those comparisons reason about hosts and ports, and effectivePort answers only
// for http and https, so a same-host Location naming any other scheme used to
// read as the same endpoint and clear every gate. A caller-supplied transport
// that registers a protocol would then carry the credential there.
func TestRedirectRefusesASchemeThePackageDoesNotSpeak(t *testing.T) {
	t.Parallel()

	for _, scheme := range []string{"unix", "ftp", "file", "ws", "wss", "gopher"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()

			var origin string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host := strings.TrimPrefix(origin, "http://")
				w.Header().Set("Location", scheme+"://"+host+r.URL.Path)
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			origin = server.URL

			// A credential, because the point of the gate is that it is not carried.
			client, err := New(server.URL, WithAuthHeader("X-Trusted-Identity", "svc@sei.io"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			var out map[string]any
			err = client.doJSON(context.Background(), http.MethodGet,
				[]string{"v1", "sessions", "conv_1"}, nil, nil, &out)
			if err == nil {
				t.Fatalf("doJSON = nil error, want a refusal for a %s:// Location", scheme)
			}
			if !errors.Is(err, ErrUnsafeRedirect) {
				t.Errorf("error = %v, want it to wrap ErrUnsafeRedirect", err)
			}
		})
	}
}

// TestRefusedRedirectErrorCarriesNoCredential pins the redaction that
// [checkRedirect]'s own messages imply and net/http would otherwise undo.
//
// net/http wraps a CheckRedirect refusal in a *url.Error whose URL field is the
// verbatim Location header, so the rendered error reproduces whatever the server
// chose to put there. A refusal fires exactly when something hostile is
// happening, and it lands in the caller's log.
func TestRefusedRedirectErrorCarriesNoCredential(t *testing.T) {
	t.Parallel()

	secrets := []struct {
		name     string
		location func(host string) string
		leaked   []string
	}{
		{
			name:     "a password in userinfo",
			location: func(host string) string { return "https://svc:s3cr3t-password@" + host + "/v1/x" },
			leaked:   []string{"s3cr3t-password"},
		},
		{
			name:     "an oauth state and code on a redirect to login",
			location: func(string) string { return "https://login.evil.tld/authorize?state=OPAQUE-STATE&code=AUTHCODE-abc123" },
			leaked:   []string{"AUTHCODE-abc123", "OPAQUE-STATE"},
		},
		{
			name:     "a token in the query string",
			location: func(host string) string { return "http://" + host + "/v1/x?access_token=ghs_REALTOKEN" },
			leaked:   []string{"ghs_REALTOKEN"},
		},
	}

	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var origin string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", tc.location(strings.TrimPrefix(origin, "http://")))
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			origin = server.URL

			client, err := New(server.URL, WithAuthHeader("X-Trusted-Identity", "svc@sei.io"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			var out map[string]any
			err = client.doJSON(context.Background(), http.MethodGet,
				[]string{"v1", "sessions", "conv_1"}, nil, nil, &out)
			if err == nil {
				t.Fatal("doJSON = nil error, want a refusal")
			}
			if !errors.Is(err, ErrUnsafeRedirect) {
				t.Fatalf("error = %v, want it to wrap ErrUnsafeRedirect", err)
			}
			for _, secret := range tc.leaked {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error renders %q, which the server chose:\n  %v", secret, err)
				}
			}
		})
	}
}

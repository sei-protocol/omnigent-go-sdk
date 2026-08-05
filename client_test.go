package omnigent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		opts    []Option
		wantErr string
	}{
		{name: "defaults when base URL is empty"},
		{name: "http", baseURL: "http://example.test:6767"},
		{name: "https with a path prefix", baseURL: "https://example.test/omnigent"},
		{name: "rejects a non-http scheme", baseURL: "ftp://example.test", wantErr: "http or https"},
		{name: "rejects a host-less URL", baseURL: "http://", wantErr: "no host"},
		{name: "rejects an unparseable URL", baseURL: "http://[::1", wantErr: "parse base URL"},
		{
			name:    "rejects a nil http client",
			opts:    []Option{WithHTTPClient(nil)},
			wantErr: "nil client",
		},
		{
			name:    "rejects an unnamed auth header",
			opts:    []Option{WithAuthHeader("", "someone@example.test")},
			wantErr: "empty header name",
		},
		{
			name:    "rejects an empty bearer token",
			opts:    []Option{WithBearerToken("")},
			wantErr: "empty token",
		},
		{
			name:    "rejects an unnamed session cookie",
			opts:    []Option{WithSessionCookie("", "value")},
			wantErr: "empty cookie name",
		},
		{
			name:    "rejects a negative idle timeout",
			opts:    []Option{WithStreamIdleTimeout(-time.Second)},
			wantErr: "negative duration",
		},
		{
			name:    "rejects a negative unary timeout",
			opts:    []Option{WithUnaryTimeout(-time.Second)},
			wantErr: "negative duration",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(tc.baseURL, tc.opts...)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("New(%q) = nil error, want one containing %q", tc.baseURL, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("New(%q) error = %q, want it to contain %q", tc.baseURL, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) = %v, want no error", tc.baseURL, err)
			}
			if !strings.HasSuffix(client.baseURL.Path, "/") {
				t.Errorf("base path = %q, want a trailing slash so a mount prefix survives", client.baseURL.Path)
			}
			if client.unary.Timeout == 0 {
				t.Error("unary client has no timeout; a unary call could hang forever")
			}
			if client.stream.Timeout != 0 {
				t.Errorf("stream client timeout = %s, want 0: a whole-exchange deadline severs a healthy stream",
					client.stream.Timeout)
			}
			if client.unary.Transport != client.stream.Transport {
				t.Error("unary and stream clients have different transports; they should share a connection pool")
			}
		})
	}
}

func TestClientRequestShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		opts      []Option
		call      func(context.Context, *Client) error
		wantPath  string
		wantQuery string
		wantHead  map[string]string
	}{
		{
			name: "create posts JSON to the sessions collection",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateSession(ctx, SessionCreateRequest{AgentID: "ag_1"})
				return err
			},
			wantPath: "/v1/sessions",
			wantHead: map[string]string{"Content-Type": "application/json", "Accept": "application/json"},
		},
		{
			name:    "get honours a mounted base path",
			baseURL: "%s/omnigent",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv_1", GetSessionOptions{})
				return err
			},
			wantPath: "/omnigent/v1/sessions/conv_1",
		},
		{
			name: "get serialises its options as query parameters",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv_1", GetSessionOptions{
					IncludeItems:    Ptr(false),
					IncludeLiveness: Ptr(true),
					RefreshState:    Ptr(false),
				})
				return err
			},
			wantPath:  "/v1/sessions/conv_1",
			wantQuery: "include_items=false&include_liveness=true&refresh_state=false",
		},
		{
			name: "delete opts into branch cleanup",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.DeleteSession(ctx, "conv_1", DeleteSessionOptions{DeleteBranch: true})
				return err
			},
			wantPath:  "/v1/sessions/conv_1",
			wantQuery: "delete_branch=true",
		},
		{
			name:     "send posts to the undocumented events route",
			call:     func(ctx context.Context, c *Client) error { _, err := c.SendMessage(ctx, "conv_1", "hi"); return err },
			wantPath: "/v1/sessions/conv_1/events",
		},
		{
			name: "a session id needing escaping cannot traverse the path",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv/../admin", GetSessionOptions{})
				return err
			},
			wantPath: "/v1/sessions/conv%2F..%2Fadmin",
		},
		{
			name: "proxy header auth rides every request",
			opts: []Option{WithAuthHeader("X-Forwarded-Email", "someone@example.test")},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv_1", GetSessionOptions{})
				return err
			},
			wantPath: "/v1/sessions/conv_1",
			wantHead: map[string]string{"X-Forwarded-Email": "someone@example.test"},
		},
		{
			name: "bearer auth rides every request",
			opts: []Option{WithBearerToken("tok"), WithUserAgent("test-agent/1")},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv_1", GetSessionOptions{})
				return err
			},
			wantPath: "/v1/sessions/conv_1",
			wantHead: map[string]string{"Authorization": "Bearer tok", "User-Agent": "test-agent/1"},
		},
		{
			name: "cookie auth rides every request",
			opts: []Option{WithSessionCookie("ap_session", "sess")},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "conv_1", GetSessionOptions{})
				return err
			},
			wantPath: "/v1/sessions/conv_1",
			wantHead: map[string]string{"Cookie": "ap_session=sess"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A buffered channel rather than a shared variable, so the handler's
			// write and this goroutine's read are ordered.
			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Clone(r.Context())
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"conv_1","agent_id":"ag_1","status":"idle","created_at":1,"queued":true}`))
			}))
			defer server.Close()

			baseURL := server.URL
			if strings.Contains(tc.baseURL, "%s") {
				baseURL = strings.Replace(tc.baseURL, "%s", server.URL, 1)
			}
			client, err := New(baseURL, tc.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tc.call(context.Background(), client); err != nil {
				t.Fatalf("call: %v", err)
			}
			var got *http.Request
			select {
			case got = <-requests:
			default:
				t.Fatal("the server saw no request")
			}
			if got.URL.EscapedPath() != tc.wantPath {
				t.Errorf("path = %q, want %q", got.URL.EscapedPath(), tc.wantPath)
			}
			if got.URL.RawQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", got.URL.RawQuery, tc.wantQuery)
			}
			for name, want := range tc.wantHead {
				if have := got.Header.Get(name); have != want {
					t.Errorf("header %s = %q, want %q", name, have, want)
				}
			}
		})
	}
}

func TestClientRequiresIdentifiers(t *testing.T) {
	t.Parallel()

	client, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"create without an agent", func() error { _, err := client.CreateSession(ctx, SessionCreateRequest{}); return err }, "AgentID is required"},
		{"get without a session", func() error { _, err := client.GetSession(ctx, "", GetSessionOptions{}); return err }, "sessionID is required"},
		{"delete without a session", func() error { _, err := client.DeleteSession(ctx, "", DeleteSessionOptions{}); return err }, "sessionID is required"},
		{"send without a session", func() error { _, err := client.SendMessage(ctx, "", "hi"); return err }, "sessionID is required"},
		{"send without a type", func() error { _, err := client.SendInput(ctx, "conv_1", SessionEventInput{}); return err }, "Type is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestClientDecodesResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"conv_1","agent_id":"ag_1","status":"running","created_at":1700000000}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"id":"conv_1","deleted":true,"object":"conversation.deleted"}`))
		default:
			_, _ = w.Write([]byte(`{"queued":true,"item_id":"item_9"}`))
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	session, err := client.CreateSession(ctx, SessionCreateRequest{AgentID: "ag_1"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "conv_1" || session.Status != "running" || session.CreatedAt != 1700000000 {
		t.Errorf("session = %+v, want conv_1/running/1700000000", session)
	}

	accepted, err := client.SendMessage(ctx, "conv_1", "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !accepted.Queued || accepted.ItemID != "item_9" {
		t.Errorf("accepted = %+v, want queued with item_9", accepted)
	}

	if err := client.Interrupt(ctx, "conv_1"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	deleted, err := client.DeleteSession(ctx, "conv_1", DeleteSessionOptions{})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if deleted.ID != "conv_1" {
		t.Errorf("deleted.ID = %q, want conv_1", deleted.ID)
	}
}

func TestClientContextCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.GetSession(ctx, "conv_1", GetSessionOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetSession error = %v, want it to wrap context.Canceled", err)
	}
}

func TestUserMessage(t *testing.T) {
	t.Parallel()

	input := UserMessage("hello")
	if input.Type != InputTypeMessage {
		t.Errorf("Type = %q, want %q", input.Type, InputTypeMessage)
	}
	if input.Data["role"] != "user" {
		t.Errorf("role = %v, want user", input.Data["role"])
	}
	content, ok := input.Data["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one part", input.Data["content"])
	}
	if content[0]["type"] != "input_text" || content[0]["text"] != "hello" {
		t.Errorf("content part = %#v, want an input_text part carrying the text", content[0])
	}
}

func TestWithSessionCookieEmitsOneCookieHeader(t *testing.T) {
	t.Parallel()

	// header.Add would emit two Cookie lines. RFC 6265 allows exactly one on a
	// request, and the server's ASGI framework reads only the first.
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"conv_1","deleted":true,"object":"conversation.deleted"}`))
	}))
	defer server.Close()

	client, err := New(server.URL,
		WithSessionCookie("ap_session", "first"),
		WithSessionCookie("csrf", "second"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.DeleteSession(context.Background(), "conv_1", DeleteSessionOptions{}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got := <-requests
	if lines := got.Header.Values("Cookie"); len(lines) != 1 {
		t.Fatalf("Cookie header lines = %d %q, want exactly 1", len(lines), lines)
	}
	if want := "ap_session=first; csrf=second"; got.Header.Get("Cookie") != want {
		t.Errorf("Cookie = %q, want %q", got.Header.Get("Cookie"), want)
	}
}

func TestClientArgumentErrorsAreMatchable(t *testing.T) {
	t.Parallel()

	client, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	calls := map[string]func() error{
		"create without an agent": func() error {
			_, err := client.CreateSession(ctx, SessionCreateRequest{})
			return err
		},
		"get without a session": func() error {
			_, err := client.GetSession(ctx, "", GetSessionOptions{})
			return err
		},
		"delete without a session": func() error {
			_, err := client.DeleteSession(ctx, "", DeleteSessionOptions{})
			return err
		},
		"send without a session": func() error { _, err := client.SendMessage(ctx, "", "hi"); return err },
		"send without a type": func() error {
			_, err := client.SendInput(ctx, "conv_1", SessionEventInput{})
			return err
		},
		"a non-http base URL":     func() error { _, err := New("ftp://example.test"); return err },
		"an option given no name": func() error { _, err := New("", WithAuthHeader("", "x")); return err },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call()
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("error = %v, want it to wrap ErrInvalidArgument", err)
			}
			// An argument error is not a server response, so it must not look
			// like one to a caller switching on the sentinels.
			if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrValidation) {
				t.Errorf("error = %v, want it not to masquerade as a server rejection", err)
			}
		})
	}
}

// TestNewRejectsUserinfoInTheBaseURL is S8's first half. net/http turns a base
// URL's userinfo into an Authorization: Basic header on every request — see
// Client.send in net/http/client.go — so a URL copied out of a browser silently
// becomes a credential this package never agreed to send, on a scheme the server
// does not offer. Against feat/go-client-v2 New returns a working Client here.
func TestNewRejectsUserinfoInTheBaseURL(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{
		"http://someone:s3cr3t@127.0.0.1:6767",
		"https://someone:s3cr3t@example.test",
		"https://someone@example.test",
	} {
		t.Run(baseURL, func(t *testing.T) {
			t.Parallel()

			client, err := New(baseURL)
			if err == nil {
				t.Fatalf("New(%q) = %v, nil error: userinfo would become Basic auth", baseURL, client)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("error = %v, want it to wrap ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), "userinfo") {
				t.Errorf("error %q does not say what was wrong", err)
			}
		})
	}
}

// TestNewNeverEchoesAPasswordFromTheBaseURL is S8's second half. Every rejection
// path in New used to render the base URL with %q, and url.Parse's own error does
// the same, so a password reached the error string — the one place a credential
// most reliably ends up in a log. Against feat/go-client-v2 the unparseable case
// fails: the message contains the password verbatim.
func TestNewNeverEchoesAPasswordFromTheBaseURL(t *testing.T) {
	t.Parallel()

	const password = "pa55word-must-not-appear"

	tests := []struct {
		name    string
		baseURL string
	}{
		{"unparseable, so only url.Parse's message is available", "http://someone:" + password + "@[::1"},
		{"userinfo on an otherwise valid URL", "https://someone:" + password + "@example.test"},
		{"a scheme this package does not speak", "ftp://someone:" + password + "@example.test"},
		{"no host at all", "http://someone:" + password + "@"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(tc.baseURL)
			if err == nil {
				t.Fatalf("New(%q) = nil error, want one", tc.baseURL)
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("error %q leaks the base URL's password", err)
			}
		})
	}
}

// TestNewRefusesAPlaintextCredentialOffTheMachine is S6. Sending a bearer token
// over plain http to a host that is not this machine puts it on a network in
// clear, and feat/go-client-v2 accepts that silently. Refusing is the only
// fail-closed answer a library can give: it has no logger to warn into.
func TestNewRefusesAPlaintextCredentialOffTheMachine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		opts    []Option
		wantErr bool
	}{
		{
			name:    "bearer token over http to a remote host",
			baseURL: "http://api.example.test",
			opts:    []Option{WithBearerToken("tok")},
			wantErr: true,
		},
		{
			name:    "proxy identity header over http to a remote host",
			baseURL: "http://api.example.test",
			opts:    []Option{WithAuthHeader("X-Forwarded-Email", "someone@example.test")},
			wantErr: true,
		},
		{
			name:    "session cookie over http to a remote host",
			baseURL: "http://api.example.test",
			opts:    []Option{WithSessionCookie("ap_session", "sess")},
			wantErr: true,
		},
		{
			name:    "a remote IP is no different from a name",
			baseURL: "http://198.51.100.7:6767",
			opts:    []Option{WithBearerToken("tok")},
			wantErr: true,
		},
		{
			name:    "the option order does not change the answer",
			baseURL: "http://api.example.test",
			opts:    []Option{WithBearerToken("tok"), WithUserAgent("test/1")},
			wantErr: true,
		},
		// Loopback over http is legitimate and must keep working: nothing leaves
		// the machine, which is what makes DefaultBaseURL a reasonable default.
		{name: "the default base URL with a credential", opts: []Option{WithBearerToken("tok")}},
		{name: "loopback by IPv4", baseURL: "http://127.0.0.1:6767", opts: []Option{WithBearerToken("tok")}},
		{name: "loopback by IPv6", baseURL: "http://[::1]:6767", opts: []Option{WithBearerToken("tok")}},
		{name: "loopback by name", baseURL: "http://localhost:6767", opts: []Option{WithBearerToken("tok")}},
		{
			name:    "a name reserved under localhost",
			baseURL: "http://omnigent.localhost:6767",
			opts:    []Option{WithBearerToken("tok")},
		},
		// And so are https anywhere, plain http with no credential, and the
		// explicit opt-in.
		{name: "https to a remote host", baseURL: "https://api.example.test", opts: []Option{WithBearerToken("tok")}},
		{name: "http to a remote host with no credential", baseURL: "http://api.example.test"},
		{
			name:    "the explicit opt-in",
			baseURL: "http://api.example.test",
			opts:    []Option{WithBearerToken("tok"), WithInsecureCredentialTransport()},
		},
		{
			name:    "the opt-in before the credential",
			baseURL: "http://api.example.test",
			opts:    []Option{WithInsecureCredentialTransport(), WithBearerToken("tok")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(tc.baseURL, tc.opts...)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("New(%q) = %v, want no error", tc.baseURL, err)
				}
				if client == nil {
					t.Fatal("New returned a nil client and no error")
				}
				return
			}
			if err == nil {
				t.Fatalf("New(%q) = nil error: a credential would travel in cleartext", tc.baseURL)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("error = %v, want it to wrap ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), "cleartext") {
				t.Errorf("error %q does not say what the risk is", err)
			}
			if !strings.Contains(err.Error(), "WithInsecureCredentialTransport") {
				t.Errorf("error %q does not name the opt-out", err)
			}
		})
	}
}

// TestPathSegmentsCannotTraverseThePath is S5. resolve's comment claimed escaping
// stopped an identifier traversing the path, which held for slashes, '?', '#' and
// spaces but not for dot segments: url.PathEscape leaves '.' alone, so ".." reached
// the URL intact and RFC 3986 reference resolution walked it back up. Against
// feat/go-client-v2 every subtest here fails — the call succeeds and the server
// sees a request on a path the caller never named.
func TestPathSegmentsCannotTraverseThePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, c *Client) error
	}{
		{
			name: "get with a parent-directory session id",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, "..", GetSessionOptions{})
				return err
			},
		},
		{
			name: "get with a current-directory session id",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSession(ctx, ".", GetSessionOptions{})
				return err
			},
		},
		{
			name: "delete with a parent-directory session id",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.DeleteSession(ctx, "..", DeleteSessionOptions{})
				return err
			},
		},
		{
			name: "send with a parent-directory session id",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.SendMessage(ctx, "..", "hi")
				return err
			},
		},
		{
			name: "stream with a parent-directory session id",
			call: func(ctx context.Context, c *Client) error {
				_, err := collectSeq(c.Stream(ctx, "..", StreamOptions{}))
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			paths := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.EscapedPath()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"conv_1","agent_id":"ag_1","status":"idle","created_at":1}`))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = tc.call(context.Background(), client)
			if err == nil {
				t.Fatal("call = nil error, want a dot segment to be rejected")
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("error = %v, want it to wrap ErrInvalidArgument", err)
			}
			if len(paths) != 0 {
				t.Errorf("the server was reached at %q; the request should never have been sent", <-paths)
			}
		})
	}
}

// TestOptionIsSealedAndDecoupledFromClient is D1. `type Option func(*Client) error`
// baked *Client into an exported signature — so nothing in Client could ever move
// — and left the type open, so a third party could construct options this package
// has never seen. Against feat/go-client-v2 this test fails at the first check:
// Option is a func type, not a sealed interface.
func TestOptionIsSealedAndDecoupledFromClient(t *testing.T) {
	t.Parallel()

	option := reflect.TypeFor[Option]()
	if option.Kind() != reflect.Interface {
		t.Fatalf("Option is a %s; an interface with an unexported method is what seals it", option.Kind())
	}
	if option.NumMethod() != 1 {
		t.Fatalf("Option has %d methods, want exactly 1", option.NumMethod())
	}
	method := option.Method(0)
	if method.PkgPath == "" {
		t.Errorf("Option's only method %s is exported, so any package can implement it", method.Name)
	}
	// The signature must not mention Client, or the coupling has only moved.
	if signature := method.Type.String(); strings.Contains(signature, "Client") {
		t.Errorf("Option's method is %s, which is still coupled to Client", signature)
	}
	// And the options this package ships must satisfy it.
	for _, opt := range []Option{
		WithHTTPClient(http.DefaultClient),
		WithAuthHeader("X-Forwarded-Email", "someone@example.test"),
		WithBearerToken("tok"),
		WithSessionCookie("ap_session", "sess"),
		WithInsecureCredentialTransport(),
		WithUserAgent("test/1"),
		WithStreamIdleTimeout(time.Second),
		WithUnaryTimeout(time.Second),
	} {
		if opt == nil {
			t.Error("an option constructor returned nil")
		}
	}
}

// serverInnerBudget is the longest the server may legitimately take before it
// answers a unary call, taken from its own constants rather than guessed. POST
// /v1/sessions/{id}/events — the call a turn-driving caller makes every turn —
// waits up to 5s for the stream relay to subscribe and then forwards the event
// to the runner with a 60s read timeout, all of it awaited before the 202.
// Session create's 10s runner notify plus 30s host launch is inside this.
const serverInnerBudget = 65 * time.Second

// TestUnaryDefaultsClearTheServersInnerBudgets is P1. The default whole-exchange
// timeout was 30s, which is not a margin over the budgets above but a tie with
// the smaller of them, so this package aborted requests the server was still
// legitimately serving — and on session create it aborted holding no session id
// for a session the server went on to create, leaking it. The transport's
// response-header bound was 30s too and covers the same wait, so raising only
// http.Client.Timeout would have left the abort exactly where it was. Against
// the previous defaults both checks here fail.
func TestUnaryDefaultsClearTheServersInnerBudgets(t *testing.T) {
	t.Parallel()

	client, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.unary.Timeout <= serverInnerBudget {
		t.Errorf("unary timeout = %s, want more than the server's own %s inner budget",
			client.unary.Timeout, serverInnerBudget)
	}
	transport, ok := client.unary.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unary transport is %T, want *http.Transport", client.unary.Transport)
	}
	// The header bound is not a second opinion on the same wait: the slow routes
	// withhold their headers until they are done, so this is what a unary call
	// actually spends its budget waiting on.
	if transport.ResponseHeaderTimeout <= serverInnerBudget {
		t.Errorf("response header timeout = %s, want more than the server's own %s inner budget",
			transport.ResponseHeaderTimeout, serverInnerBudget)
	}
}

// TestWithUnaryTimeout covers P1's other half: before it there was no option
// touching the whole-exchange timeout at all, so a caller who needed a different
// one had to supply a whole http.Client and lose this package's header bound with
// it. Against the previous package this file does not compile.
func TestWithUnaryTimeout(t *testing.T) {
	t.Parallel()

	// A caller's own client, with a header bound this package must leave alone.
	const suppliedHeaderTimeout = 3 * time.Second
	supplied := &http.Client{
		Timeout:   7 * time.Second,
		Transport: &http.Transport{ResponseHeaderTimeout: suppliedHeaderTimeout},
	}

	tests := []struct {
		name       string
		opts       []Option
		wantUnary  time.Duration
		wantHeader time.Duration
	}{
		{
			name:       "the default bounds the exchange and the headers alike",
			wantUnary:  defaultUnaryTimeout,
			wantHeader: defaultUnaryTimeout,
		},
		{
			name:       "raising it carries the header bound along",
			opts:       []Option{WithUnaryTimeout(5 * time.Minute)},
			wantUnary:  5 * time.Minute,
			wantHeader: 5 * time.Minute,
		},
		{
			name:       "lowering it stops at the header bound's floor",
			opts:       []Option{WithUnaryTimeout(time.Second)},
			wantUnary:  time.Second,
			wantHeader: minResponseHeaderTimeout,
		},
		{
			name:       "zero restores the default",
			opts:       []Option{WithUnaryTimeout(0)},
			wantUnary:  defaultUnaryTimeout,
			wantHeader: defaultUnaryTimeout,
		},
		{
			name:       "it outranks a supplied client's own timeout",
			opts:       []Option{WithHTTPClient(supplied), WithUnaryTimeout(time.Minute)},
			wantUnary:  time.Minute,
			wantHeader: suppliedHeaderTimeout,
		},
		{
			name:       "whichever order the two are written in",
			opts:       []Option{WithUnaryTimeout(time.Minute), WithHTTPClient(supplied)},
			wantUnary:  time.Minute,
			wantHeader: suppliedHeaderTimeout,
		},
		{
			name:       "and a supplied client's timeout stands when it is not given",
			opts:       []Option{WithHTTPClient(supplied)},
			wantUnary:  7 * time.Second,
			wantHeader: suppliedHeaderTimeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := New("", tc.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if client.unary.Timeout != tc.wantUnary {
				t.Errorf("unary timeout = %s, want %s", client.unary.Timeout, tc.wantUnary)
			}
			// Whatever the unary bound is, the stream must not carry it.
			if client.stream.Timeout != 0 {
				t.Errorf("stream client timeout = %s, want 0: a whole-exchange deadline severs a healthy stream",
					client.stream.Timeout)
			}
			transport, ok := client.unary.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("unary transport is %T, want *http.Transport", client.unary.Transport)
			}
			if transport.ResponseHeaderTimeout != tc.wantHeader {
				t.Errorf("response header timeout = %s, want %s",
					transport.ResponseHeaderTimeout, tc.wantHeader)
			}
		})
	}
}

// TestUnaryTimeoutBoundsARealExchange proves the configured value reaches the
// wire rather than only the struct, in both directions.
func TestUnaryTimeoutBoundsARealExchange(t *testing.T) {
	t.Parallel()

	// The handler answers only after this long, standing in for the server's own
	// wait on a runner. It writes nothing first, which is what the real routes
	// do: their headers land with their bodies.
	const serverWait = 200 * time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverWait)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"conv_1","agent_id":"ag_1","status":"idle","created_at":1}`))
	}))
	// Cleanup rather than defer: the subtests below are parallel, so they run
	// after this function returns but before its cleanups do.
	t.Cleanup(server.Close)

	t.Run("a budget under the server's wait aborts the call", func(t *testing.T) {
		t.Parallel()

		client, err := New(server.URL, WithUnaryTimeout(serverWait/10))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// context.Background carries no deadline of its own, so a deadline here
		// can only have come from the whole-exchange timeout.
		_, err = client.GetSession(context.Background(), "conv_1", GetSessionOptions{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetSession error = %v, want it to wrap context.DeadlineExceeded", err)
		}
	})

	t.Run("a budget over it does not", func(t *testing.T) {
		t.Parallel()

		client, err := New(server.URL, WithUnaryTimeout(50*serverWait))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := client.GetSession(context.Background(), "conv_1", GetSessionOptions{}); err != nil {
			t.Fatalf("GetSession: %v", err)
		}
	})
}

// TestATightUnaryDeadlineDoesNotSeverAHealthyStream is the invariant the fix for
// P1 must not break, and the reason the header bound has a floor rather than
// simply following the unary budget down. Both of a Client's http.Clients share
// one transport, so a caller who tightens the unary budget would otherwise
// tighten the only bound a stream has before its first byte — and GET
// /v1/sessions/{id}/stream waits for the relay to subscribe before sending one.
func TestATightUnaryDeadlineDoesNotSeverAHealthyStream(t *testing.T) {
	t.Parallel()

	// Every wait the stream makes the client sit through — the one before the
	// response headers included — is longer than the unary budget below.
	const (
		gap         = 30 * time.Millisecond
		heartbeats  = 5
		unaryBudget = gap / 3
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		time.Sleep(gap)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = controller.Flush()
		for range heartbeats {
			time.Sleep(gap)
			_, _ = io.WriteString(w, frame("session.heartbeat", `{"type":"session.heartbeat"}`))
			_ = controller.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = controller.Flush()
	}))
	defer server.Close()

	client, err := New(server.URL,
		WithUnaryTimeout(unaryBudget),
		// Generously above gap: this test is about the unary deadline, and a
		// watchdog firing would prove nothing about it.
		WithStreamIdleTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := collect(t, client.Stream(context.Background(), "conv_1", StreamOptions{}))
	if err != nil {
		t.Fatalf("stream = %v, want it to reach the sentinel: no unary deadline may reach a stream", err)
	}
	if len(got) != heartbeats {
		t.Errorf("events = %v, want %d heartbeats", got, heartbeats)
	}
}

func TestLoopbackNameMatchingIsCaseInsensitive(t *testing.T) {
	// RFC 4343 makes a hostname comparison case-insensitive and RFC 6761 §6.3
	// reserves the localhost name, so an uppercase spelling is the same loopback
	// host. Refusing it would reject a legitimate local development setup.
	for _, host := range []string{"localhost", "LOCALHOST", "LocalHost", "app.LOCALHOST"} {
		t.Run(host, func(t *testing.T) {
			_, err := New("http://"+host+":6767", WithBearerToken("token"))
			if err != nil {
				t.Fatalf("New over http to loopback %q = %v, want nil", host, err)
			}
		})
	}
}

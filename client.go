package omnigent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the server's advertised self-hosted address. It is plain
// http, which is legitimate because it is loopback: nothing leaves the machine.
// See the package doc's security notes for what that does and does not permit.
const DefaultBaseURL = "http://127.0.0.1:6767"

const (
	// defaultUnaryTimeout bounds one whole non-streaming exchange: connect,
	// request, response headers and body. Streaming deliberately does not carry
	// it; see the package doc.
	//
	// It has to clear the server's own inner budgets, because the two routes
	// that wait on a runner are awaited before they answer and neither sends a
	// byte early. POST /v1/sessions/{id}/events waits up to 5s for the stream
	// relay to subscribe, then forwards the event to the runner with a 60s read
	// timeout — 30s on the native-terminal path. Session create notifies the
	// runner (10s), then waits up to 30s for a host launch; a client that gives
	// up there loses the id of a session the server goes on to create, which
	// leaks it. 90s clears that ~65s worst case with margin for the store
	// writes and access checks either route makes around it.
	//
	// A deadline for one call belongs on that call's context. This bound is the
	// backstop that stops a wedged connection hanging forever, and
	// [WithUnaryTimeout] moves it.
	defaultUnaryTimeout = 90 * time.Second

	// minResponseHeaderTimeout is the floor on how long the server may take to
	// send response headers. That is the one deadline safe to share between
	// unary and streaming calls, because it stops applying once the body starts
	// — and both of a Client's http.Clients share one transport, so there is
	// only one of it to set.
	//
	// The effective value is this or the unary budget, whichever is larger.
	// Below that budget it would silently decide every unary call's real
	// deadline, since the slow routes withhold their headers for the whole wait.
	// The floor is what keeps a caller who tightens the unary budget from
	// bounding a stream's open latency to something the server cannot meet: GET
	// /v1/sessions/{id}/stream also waits for the relay to subscribe before its
	// first byte.
	minResponseHeaderTimeout = 30 * time.Second

	// defaultStreamIdleTimeout is three times the server's 15-second stream
	// heartbeat cadence: long enough to ride out one missed keepalive, short
	// enough to notice a dead transport well before a caller does.
	defaultStreamIdleTimeout = 45 * time.Second

	// maxRedirects caps a redirect chain. Supplying a CheckRedirect replaces
	// net/http's own cap rather than adding to it, so this package has to keep
	// one. Measured against the stdlib rather than assumed: net/http stops after
	// 10 requests, i.e. 9 followed redirects, so this is one hop more permissive
	// by construction. Both are far past any legitimate chain.
	maxRedirects = 10
)

// Client talks to one omnigent server. It is safe for concurrent use, and
// callers should share a single Client so its connections are pooled.
type Client struct {
	baseURL *url.URL
	// unary carries a whole-exchange timeout; stream and transfer are the same
	// client with that timeout replaced. All three share one transport, so one
	// connection pool.
	unary       *http.Client
	stream      *http.Client
	transfer    *http.Client
	header      http.Header
	idleTimeout time.Duration
	// ownsTransport records that [New] built the transport, so [Client.Close]
	// drains only a pool this package created.
	ownsTransport bool
}

// config is the state an [Option] writes. It is unexported on purpose: an
// option that took *Client would freeze this package's internals into its
// exported signature, and nothing could ever move out of Client again.
type config struct {
	// origin is sent as the Origin header when a deployment gates on it.
	origin     string
	httpClient *http.Client
	header     http.Header

	idleTimeout time.Duration

	// unaryTimeout is [WithUnaryTimeout]'s value. Zero means no option set one,
	// which is what lets a Timeout on a client from [WithHTTPClient] survive
	// while an explicit value still outranks it: the option resolves its own
	// zero to defaultUnaryTimeout, so zero here is unambiguous.
	unaryTimeout time.Duration

	// transferTimeout is [WithTransferTimeout]'s value. Zero is the default and
	// means no whole-transfer bound, so unlike unaryTimeout it needs no sentinel:
	// the option's own zero and an unset option want the same thing.
	transferTimeout time.Duration

	// credentialed records that an auth option ran, which is what makes the
	// base URL's scheme a security question rather than a preference.
	credentialed bool

	// allowPlaintextCredential is [WithInsecureCredentialTransport].
	allowPlaintextCredential bool
}

// Option customises a [Client] during [New].
//
// The interface is sealed: its only implementations are this package's With*
// functions. That keeps the option set something this package can reason about
// — every option's effect is visible here — and leaves room to change what an
// option configures without changing what an option is.
type Option interface {
	apply(*config) error
}

// optionFunc adapts a function to [Option]. Unexported, so the interface stays
// sealed.
type optionFunc func(*config) error

func (f optionFunc) apply(cfg *config) error { return f(cfg) }

// New returns a Client for the server at baseURL, which may be empty to mean
// [DefaultBaseURL].
//
// The returned Client sends no credentials unless an auth option is supplied.
// That is deliberate: the server's identity mode is a deployment choice, so
// there is no scheme to guess. Pick the one matching the deployment with
// [WithAuthHeader], [WithBearerToken], or [WithSessionCookie].
//
// baseURL must carry no userinfo, and — once an auth option is supplied — must
// be https unless its host is loopback. Both are rejected here rather than at
// the first request; see the package doc's security notes for why, and for
// [WithInsecureCredentialTransport], the explicit opt-out of the second.
//
// Errors from here never quote baseURL back verbatim. A base URL is a place a
// password can hide, and an error message is the least controlled place a
// credential can end up.
func New(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		// Only the reason, not the URL: url.Parse's own error quotes back the
		// string it was handed, password and all.
		return nil, fmt.Errorf("%w: parse base URL: %s (value withheld: a base URL may carry a credential)",
			ErrInvalidArgument, parseFailureReason(err))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: base URL %q: want an http or https scheme, got %q",
			ErrInvalidArgument, redactURL(parsed), parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: base URL %q has no host", ErrInvalidArgument, redactURL(parsed))
	}
	if parsed.User != nil {
		// net/http turns userinfo into an Authorization: Basic header on every
		// request, including one it was redirected into. Basic is not one of the
		// server's identity modes, so this can only be an accident — and a
		// silent one, which is the dangerous kind.
		return nil, fmt.Errorf(
			"%w: base URL %q carries userinfo, which net/http would send as Basic auth on every request; "+
				"drop it and pass the credential with WithBearerToken, WithAuthHeader or WithSessionCookie",
			ErrInvalidArgument, redactURL(parsed))
	}
	// A trailing slash makes reference resolution in resolve append to any path
	// prefix the base carries (a reverse-proxied mount) instead of replacing its
	// last segment.
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}

	cfg := config{header: http.Header{}, idleTimeout: defaultStreamIdleTimeout}
	for _, opt := range opts {
		if err := opt.apply(&cfg); err != nil {
			return nil, err
		}
	}
	// After every option, so the answer does not depend on the order they were
	// written in.
	if cfg.credentialed && !cfg.allowPlaintextCredential && sendsCredentialInClear(parsed) {
		return nil, fmt.Errorf(
			"%w: base URL %q is plain http and its host is not loopback, so a credential would "+
				"travel in cleartext; use https, or pass WithInsecureCredentialTransport to accept that",
			ErrInvalidArgument, redactURL(parsed))
	}

	if cfg.origin != "" {
		// Set once here rather than per request: it is a property of the client's
		// identity, not of any one call.
		cfg.header.Set("Origin", cfg.origin)
	}
	client := &Client{baseURL: parsed, header: cfg.header, idleTimeout: cfg.idleTimeout}
	base := cfg.httpClient
	client.ownsTransport = base == nil
	if base == nil {
		timeout := cfg.unaryTimeout
		if timeout == 0 {
			timeout = defaultUnaryTimeout
		}
		base = &http.Client{Timeout: timeout, Transport: defaultTransport(timeout)}
	}
	// Copy rather than mutate: a client handed to WithHTTPClient may be shared,
	// and installing a redirect policy on it would change behaviour elsewhere.
	unary := *base
	if cfg.unaryTimeout > 0 {
		// An explicit WithUnaryTimeout outranks a Timeout on a client from
		// WithHTTPClient, whichever order the two were written in.
		unary.Timeout = cfg.unaryTimeout
	}
	if unary.CheckRedirect == nil {
		unary.CheckRedirect = checkRedirect
	}
	client.unary = &unary
	streaming := unary
	streaming.Timeout = 0
	client.stream = &streaming
	transferring := unary
	transferring.Timeout = cfg.transferTimeout
	client.transfer = &transferring
	return client, nil
}

// defaultTransport clones the stdlib default and bounds header latency, which
// is the only timeout a streaming request can safely carry.
//
// The bound is unaryTimeout or minResponseHeaderTimeout, whichever is larger;
// both of those comments say why that is the pair to choose between.
func defaultTransport(unaryTimeout time.Duration) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = max(unaryTimeout, minResponseHeaderTimeout)
	return transport
}

// stripRedirectURL drops the *url.Error net/http wraps a refused redirect in.
//
// net/http sets that wrapper's URL field to the verbatim Location header, so the
// rendered error carries whatever the server put there — a password in userinfo,
// an OAuth state and code on a 302-to-login. [checkRedirect] takes care to print
// only hosts; this is where that care would otherwise be undone. The inner error
// still wraps [ErrUnsafeRedirect], so errors.Is keeps working.
func stripRedirectURL(err error) error {
	if !errors.Is(err, ErrUnsafeRedirect) {
		return err
	}
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		return wrapped.Err
	}
	return err
}

// checkRedirect is the redirect policy both of a Client's http.Clients carry.
//
// Following a redirect is not free for an API client. net/http replays the
// request at the new location and strips only Authorization, Cookie,
// Www-Authenticate and Cookie2 when the hop leaves the original host — so a
// custom identity header, which is exactly what [WithAuthHeader] sets, travels
// on. It compares hostnames and not schemes, so an https-to-http hop on the
// same host puts the credential on the wire in clear. And a 307 or 308 replays
// the request body, which for this package is the caller's prompt text.
//
// So a redirect is followed only when it stays on the same server, keeps the
// scheme at least as strong, and leaves the method alone. Anything else is
// [ErrUnsafeRedirect]: the base URL named a server, and a response is not
// entitled to name a different one.
//
// A caller who supplies an http.Client with its own CheckRedirect keeps it —
// see [WithHTTPClient] — and owns the consequences.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", ErrUnsafeRedirect, maxRedirects)
	}
	origin := via[0]
	switch {
	case !reachableScheme(req.URL.Scheme):
		// Ahead of every other gate, because the gates below reason about hosts
		// and ports and a scheme they do not recognise slips past all of them:
		// [effectivePort] defaults anything that is not https to 80, so a
		// same-host Location naming another scheme reads as the same endpoint.
		// A caller-supplied transport with a registered protocol then carries the
		// credential there and the call reports success.
		return fmt.Errorf("%w: %s redirected to scheme %q, which this package does not speak",
			ErrUnsafeRedirect, origin.URL.Host, req.URL.Scheme)
	case !sameEndpoint(origin.URL, req.URL):
		return fmt.Errorf("%w: %s redirected to %s, which the base URL does not name; "+
			"the request's credentials were not sent there",
			ErrUnsafeRedirect, origin.URL.Host, req.URL.Host)
	case origin.URL.Scheme == "https" && req.URL.Scheme != "https":
		return fmt.Errorf("%w: %s redirected from https to %s, which would send its credentials in cleartext",
			ErrUnsafeRedirect, origin.URL.Host, req.URL.Scheme)
	case req.URL.User != nil:
		// Same vector [New] rejects in the base URL, one layer down. Location is
		// server-controlled, and a same-host https://user:pw@host/… clears the
		// three gates above: sameEndpoint compares hostname and port, the scheme
		// is unchanged, and the method is unchanged. net/http then synthesizes
		// Authorization: Basic from that userinfo onto the replayed request — but
		// only when no Authorization header is already set, so a bearer-token
		// caller is incidentally safe while [WithAuthHeader] and
		// [WithSessionCookie] callers would carry an attacker-chosen credential
		// to the real server. Rejected rather than stripped: a response is not
		// entitled to name a credential any more than it is entitled to name a
		// different host.
		return fmt.Errorf("%w: %s redirected to a location carrying userinfo, which net/http "+
			"would send as Basic auth on the replayed request",
			ErrUnsafeRedirect, origin.URL.Host)
	case req.Method != origin.Method:
		// Deliberately fail-closed on every method rewrite, including the 303
		// that defines POST→GET rather than dropping a write by accident. This
		// server never answers 303 — it issues no See Other at all, and its only
		// redirects are 302s on the browser login routes, which an API client
		// does not call. So a method rewrite here means something unintended is
		// in the path, and refusing it costs nothing the API can legitimately do.
		return fmt.Errorf("%w: a redirect rewrote %s %s as %s, which would drop the request body "+
			"and report the write as a success",
			ErrUnsafeRedirect, origin.Method, origin.URL.Path, req.Method)
	}
	return nil
}

// sameEndpoint reports whether next addresses the same server as origin.
//
// The hostname must match exactly. So must the port, except that an http-to-
// https upgrade may move from one scheme's default port to the other's, which
// is how a server redirects a plaintext request to its TLS listener.
func sameEndpoint(origin, next *url.URL) bool {
	// Case-insensitively, because DNS names are: a Location differing from the
	// base URL only in the case of its host names the same server, and treating
	// it as off-host would refuse a legitimate redirect. Matches the folding
	// [isLoopback] already applies.
	if !strings.EqualFold(origin.Hostname(), next.Hostname()) {
		return false
	}
	if effectivePort(origin) == effectivePort(next) {
		return true
	}
	return origin.Scheme == "http" && next.Scheme == "https" &&
		effectivePort(origin) == "80" && effectivePort(next) == "443"
}

// effectivePort is u's port, defaulted from its scheme.
func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	// No default port for a scheme this package does not speak. Returning one
	// would make two different endpoints compare equal.
	return ""
}

// reachableScheme reports whether this package will carry a request, and its
// credentials, over the named scheme.
func reachableScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// sendsCredentialInClear reports whether a credential sent to u would cross a
// network unencrypted. Loopback does not: nothing leaves the machine, which is
// what makes [DefaultBaseURL] a reasonable default.
func sendsCredentialInClear(u *url.URL) bool {
	return u.Scheme != "https" && !isLoopback(u)
}

// isLoopback reports whether u's host is this machine.
func isLoopback(u *url.URL) bool {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// RFC 6761 reserves localhost, and every name under it, for the loopback
	// interface. RFC 4343 makes the comparison case-insensitive, so LOCALHOST
	// is the same host and must not be refused as if it were remote.
	host = strings.ToLower(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// parseFailureReason renders a url.Parse failure without the URL it was handed.
func parseFailureReason(err error) string {
	var parseErr *url.Error
	if errors.As(err, &parseErr) {
		return parseErr.Err.Error()
	}
	return err.Error()
}

// redactURL renders u for an error message with any userinfo dropped.
func redactURL(u *url.URL) string {
	if u.User == nil {
		return u.String()
	}
	scrubbed := *u
	scrubbed.User = nil
	return scrubbed.String()
}

// Close releases the connections this client holds open.
//
// [New] gives each client its own connection pool, so a program that constructs
// one client per tenant or per credential refresh accumulates idle connections
// and the goroutines that serve them — on both ends, until the server's own idle
// timeout expires. Sharing one client is still the right shape, and this is the
// lever for the cases where that is not possible.
//
// It does not cancel a stream in flight: a subscription ends when its caller
// stops ranging or its context is done, not when the pool is drained. Calling
// Close on a client still in use is safe, and later calls simply open new
// connections.
//
// A client supplied through [WithHTTPClient] is the caller's to manage, so its
// transport is left alone. Close returns nil in that case, and always: it is an
// error return so that a future release can report one without breaking callers.
func (c *Client) Close() error {
	if !c.ownsTransport {
		return nil
	}
	// One transport backs both clients, so draining either drains the pool.
	c.unary.CloseIdleConnections()
	return nil
}

// WithHTTPClient makes the Client issue its requests through httpClient.
//
// The Client copies httpClient rather than holding it, and derives its
// streaming client from that copy by clearing Timeout — so a timeout set here
// applies to unary calls only and never truncates a stream. Set
// Transport.ResponseHeaderTimeout if header latency should stay bounded; the
// transport is the caller's here, so this package does not touch it.
//
// A Timeout on httpClient stands unless [WithUnaryTimeout] is also given, which
// outranks it. Note what that bound has to clear either way: the server awaits a
// runner before it answers a session create, and again when it forwards an event
// to one, so a half-minute Timeout carried over from another API client aborts
// calls this server is still legitimately serving.
//
// A nil CheckRedirect is replaced with this package's policy, which refuses to
// carry credentials off the base URL's host or down to plain http; see
// [ErrUnsafeRedirect]. Setting CheckRedirect yourself keeps yours, including
// nothing at all, and with it the responsibility for what the credentials do.
func WithHTTPClient(httpClient *http.Client) Option {
	return optionFunc(func(cfg *config) error {
		if httpClient == nil {
			return fmt.Errorf("WithHTTPClient: %w: nil client", ErrInvalidArgument)
		}
		cfg.httpClient = httpClient
		return nil
	})
}

// WithAuthHeader sends name: value on every request.
//
// Use it for the trusted-proxy identity header, whose name is a deployment
// setting rather than a constant — X-Forwarded-Email is only the default.
//
// Note that net/http does not strip a custom header across a redirect, the way
// it strips Authorization and Cookie. This package's redirect policy is what
// keeps that from mattering; see [ErrUnsafeRedirect].
func WithAuthHeader(name, value string) Option {
	return optionFunc(func(cfg *config) error {
		if name == "" {
			return fmt.Errorf("WithAuthHeader: %w: empty header name", ErrInvalidArgument)
		}
		cfg.header.Set(name, value)
		cfg.credentialed = true
		return nil
	})
}

// WithBearerToken sends token as an Authorization: Bearer header, the fallback
// the server accepts from non-browser clients in its OIDC and accounts modes.
func WithBearerToken(token string) Option {
	return optionFunc(func(cfg *config) error {
		if token == "" {
			return fmt.Errorf("WithBearerToken: %w: empty token", ErrInvalidArgument)
		}
		cfg.header.Set("Authorization", "Bearer "+token)
		cfg.credentialed = true
		return nil
	})
}

// WithSessionCookie sends value as the server's session cookie, which an
// interactive login mints. name is the cookie's name: ap_session over plain
// HTTP, __Host-ap_session under HTTPS.
//
// Applying it more than once appends to the single Cookie header rather than
// emitting a second one, which is what RFC 6265 requires of a client and what
// the server's ASGI framework reads.
func WithSessionCookie(name, value string) Option {
	return optionFunc(func(cfg *config) error {
		if name == "" {
			return fmt.Errorf("WithSessionCookie: %w: empty cookie name", ErrInvalidArgument)
		}
		cookie := (&http.Cookie{Name: name, Value: value}).String()
		if existing := cfg.header.Get("Cookie"); existing != "" {
			cookie = existing + "; " + cookie
		}
		cfg.header.Set("Cookie", cookie)
		cfg.credentialed = true
		return nil
	})
}

// WithInsecureCredentialTransport permits sending a credential over plain http
// to a host that is not loopback, which [New] otherwise refuses.
//
// It exists because "refuse" is the only fail-closed answer available to a
// library: a warning has nowhere to go — there is no logger here, and writing
// to stderr from a package is not this package's call — and silence is how a
// token ends up on a shared network. Refusing by default puts the decision
// where the deployment knowledge is, and this option is how a caller records
// having made it.
//
// The legitimate cases are ones where the plaintext hop is not really a network:
// a sidecar on the same pod, a port-forward, a mesh that terminates TLS for you.
// On anything reachable by a third party it is not a trade-off, it is a leak.
func WithInsecureCredentialTransport() Option {
	return optionFunc(func(cfg *config) error {
		cfg.allowPlaintextCredential = true
		return nil
	})
}

// WithInternalClientOrigin sets the Origin header a deployment checks to tell an
// internal caller from a browser one.
//
// A deployment that gates on Origin rejects a request carrying none, and a Go
// client sends none by default because it is not a browser. Announce the caller
// explicitly rather than have the package guess: guessing means every consumer
// inherits an origin claim it never made.
func WithInternalClientOrigin(origin string) Option {
	return optionFunc(func(cfg *config) error {
		if origin == "" {
			return fmt.Errorf("%w: origin is empty", ErrInvalidArgument)
		}
		cfg.origin = origin
		return nil
	})
}

// WithUserAgent sets the User-Agent header on every request.
func WithUserAgent(userAgent string) Option {
	return optionFunc(func(cfg *config) error {
		cfg.header.Set("User-Agent", userAgent)
		return nil
	})
}

// WithUnaryTimeout sets how long one whole non-streaming exchange may take —
// connect, request, response headers and body — on every call except
// [Client.Stream], which carries no whole-exchange deadline at all: a stream's
// liveness is [WithStreamIdleTimeout] and its deadline is the caller's context.
// Zero restores the default.
//
// The response-header bound on the transport this package builds follows this
// value, because it covers the same wait and a tighter one would decide the
// deadline instead. It does not follow the value below the floor a stream open
// needs: a Client's streaming and unary calls share one transport, and the
// server withholds a stream's first byte until the event relay has subscribed.
//
// Under [WithHTTPClient] the transport belongs to the caller, so this sets the
// whole-exchange timeout alone — outranking any Timeout on the supplied client,
// whichever order the two options are written in — and leaves
// Transport.ResponseHeaderTimeout as the caller set it.
//
// Prefer a context deadline for anything call-specific. This is one value for
// every unary call; its job is to stop a wedged connection hanging forever
// rather than to express a latency policy.
// WithTransferTimeout bounds one whole file transfer: [SessionFiles.Upload] and
// [SessionFiles.Download].
//
// Separate from [WithUnaryTimeout] because the two bound different things. A
// unary call's duration is the server's thinking time, which a client can put a
// number on. A transfer's duration is the file's size over the network's rate,
// which it cannot: the bound that stops a wedged RPC is the bound that makes a
// large upload impossible, and one value cannot be both.
//
// Zero, the default, sets no whole-transfer bound. The context passed to the call
// is then the only limit on how long it may run, which is the same arrangement
// the streaming calls use. Set this when a caller wants one ceiling for every
// transfer instead of a deadline per call.
//
// Transport.ResponseHeaderTimeout still applies, so a server that accepts a
// connection and then says nothing fails without waiting for this.
func WithTransferTimeout(d time.Duration) Option {
	return optionFunc(func(cfg *config) error {
		if d < 0 {
			return fmt.Errorf("WithTransferTimeout: %w: negative duration %s", ErrInvalidArgument, d)
		}
		cfg.transferTimeout = d
		return nil
	})
}

func WithUnaryTimeout(d time.Duration) Option {
	return optionFunc(func(cfg *config) error {
		if d < 0 {
			return fmt.Errorf("WithUnaryTimeout: %w: negative duration %s", ErrInvalidArgument, d)
		}
		if d == 0 {
			d = defaultUnaryTimeout
		}
		cfg.unaryTimeout = d
		return nil
	})
}

// WithStreamIdleTimeout sets how long [Client.Stream] tolerates silence before
// treating the transport as dead. Zero restores the default of three heartbeat
// intervals. A per-call [StreamOptions.IdleTimeout] overrides it.
func WithStreamIdleTimeout(d time.Duration) Option {
	return optionFunc(func(cfg *config) error {
		if d < 0 {
			return fmt.Errorf("WithStreamIdleTimeout: %w: negative duration %s", ErrInvalidArgument, d)
		}
		if d == 0 {
			d = defaultStreamIdleTimeout
		}
		cfg.idleTimeout = d
		return nil
	})
}

// resolve turns path segments and a query into an absolute URL.
//
// Each segment is percent-escaped, so an identifier carrying a slash, a '?', a
// '#' or a space becomes one segment instead of several. Escaping alone is not
// enough: url.PathEscape leaves '.' as it is, so a segment of "." or ".." would
// reach the URL intact and RFC 3986 reference resolution would then walk it back
// up the path. Those two are rejected — no identifier this API accepts is a dot
// segment — which is what makes "a segment cannot traverse the path" true rather
// than nearly true.
func (c *Client) resolve(segments []string, query url.Values) (*url.URL, error) {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("%w: path segment %q would traverse the request path",
				ErrInvalidArgument, segment)
		}
		escaped = append(escaped, url.PathEscape(segment))
	}
	ref, err := url.Parse(strings.Join(escaped, "/"))
	if err != nil {
		return nil, fmt.Errorf("%w: build request path %v: %w", ErrInvalidArgument, segments, err)
	}
	resolved := c.baseURL.ResolveReference(ref)
	resolved.RawQuery = query.Encode()
	return resolved, nil
}

// newRequest builds an authenticated request, JSON-encoding body when non-nil.
func (c *Client) newRequest(
	ctx context.Context,
	method string,
	segments []string,
	query url.Values,
	body any,
) (*http.Request, error) {
	target, err := c.resolve(segments, query)
	if err != nil {
		return nil, err
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s %s body: %w", method, target.Path, err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), payload)
	if err != nil {
		return nil, fmt.Errorf("build %s %s request: %w", method, target.Path, err)
	}
	for name, values := range c.header {
		req.Header[name] = append([]string(nil), values...)
	}
	if body != nil {
		// Required on session create, which rejects any other body type with 415.
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// doJSON runs a unary request and decodes its JSON body into out, which may be
// nil to discard it.
func (c *Client) doJSON(
	ctx context.Context,
	method string,
	segments []string,
	query url.Values,
	body, out any,
) error {
	req, err := c.newRequest(ctx, method, segments, query, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.unary.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, req.URL.Path, stripRedirectURL(err))
	}
	defer func() {
		// Drain before closing so the connection returns to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, req.URL.Path, err)
	}
	return nil
}

// doUpload sends a streamed body under a caller-supplied content type.
//
// Separate from [Client.doJSON] because that one encodes a value into memory
// before sending, which is exactly what an upload must not do. The body here is a
// reader the transport drains, so the request holds one buffer rather than the
// whole file.
func (c *Client) doUpload(ctx context.Context, segments []string, contentType string, body io.Reader, out any) error {
	target, err := c.resolve(segments, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), body)
	if err != nil {
		return fmt.Errorf("build POST %s request: %w", target.Path, err)
	}
	for name, values := range c.header {
		req.Header[name] = append([]string(nil), values...)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	// The transfer client, not the unary one: how long a body takes to send is
	// the caller's file size over the network's rate, which no fixed bound sized
	// for an RPC can predict. ctx is the bound.
	resp, err := c.transfer.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", target.Path, stripRedirectURL(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect {
		return classifyUnfollowedRedirect(target, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode POST %s response: %w", target.Path, err)
	}
	return nil
}

// doDownload copies a response body to w, refusing to write past maxBytes.
//
// It reads one byte past the bound on purpose. Stopping exactly at the limit
// cannot tell a file that fits from one that was truncated, and reporting a
// truncated download as a complete one is the failure worth avoiding.
func (c *Client) doDownload(ctx context.Context, segments []string, w io.Writer, maxBytes int64) (int64, error) {
	target, err := c.resolve(segments, nil)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("build GET %s request: %w", target.Path, err)
	}
	for name, values := range c.header {
		req.Header[name] = append([]string(nil), values...)
	}

	// The transfer client, not the unary one. A download's length is the server's
	// choice and its rate is the network's, so a fixed whole-exchange bound sized
	// for an RPC truncates a large file that is arriving perfectly well. maxBytes
	// bounds the size; ctx bounds the time.
	resp, err := c.transfer.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", target.Path, stripRedirectURL(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, newAPIError(resp)
	}

	// CopyN to the caller's writer, then probe the body separately. Reading one
	// past the bound is how a file that fits is told from one that was truncated,
	// but that byte must not reach a writer that declared a smaller capacity.
	written, err := io.CopyN(w, resp.Body, maxBytes)
	switch {
	case errors.Is(err, io.EOF):
		// Body ended within the bound, which is the ordinary case.
		return written, nil
	case err != nil:
		return written, fmt.Errorf("GET %s: read body: %w", target.Path, err)
	}

	var probe [1]byte
	if n, _ := resp.Body.Read(probe[:]); n > 0 {
		return written, fmt.Errorf("GET %s: %w: body exceeds the caller's %d-byte bound",
			target.Path, ErrTruncated, maxBytes)
	}
	return written, nil
}

// classifyUnfollowedRedirect names a 307 or 308 an upload could not follow.
//
// net/http replays the request to follow those two, and a streamed body cannot be
// replayed, so it returns the response and [checkRedirect] never runs. Every
// other method gets its redirects classified; without this an upload reports a
// bare 307, which tells an operator running a path-rewriting proxy nothing and
// tells a caller watching for [ErrUnsafeRedirect] nothing either.
//
// [checkRedirect]'s own gates decide which case it is, so the policy keeps one
// home: a location the base URL does not name is the security case, and one it
// does name is a configuration the caller can fix.
func classifyUnfollowedRedirect(origin *url.URL, resp *http.Response) error {
	next, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		// Unparseable is not benign: it is a location that cannot be shown to be on
		// this server. Refuse it as if it named another one.
		return fmt.Errorf("%w: %s answered %d with a Location that does not parse",
			ErrUnsafeRedirect, origin.Host, resp.StatusCode)
	}
	next = origin.ResolveReference(next)

	if !reachableScheme(next.Scheme) || !sameEndpoint(origin, next) || next.User != nil {
		// Hosts only, never the location itself — the same care [stripRedirectURL]
		// exists to preserve.
		return fmt.Errorf("%w: %s answered %d redirecting an upload to %s, which the base "+
			"URL does not name; the request's credentials were not sent there",
			ErrUnsafeRedirect, origin.Host, resp.StatusCode, next.Host)
	}
	return fmt.Errorf("%w: %s answered %d for %s, and a streamed upload body cannot be "+
		"replayed to follow it; point the base URL at the route that serves the upload",
		ErrRedirectNotFollowed, origin.Host, resp.StatusCode, origin.Path)
}

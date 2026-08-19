// Package omnigent is a Go client for the omnigent server.
//
// # Scope of this milestone
//
// This package is being rebuilt to match the shape of the upstream Python client
// in omnigent-ai/omnigent, under sdks/python-client. That client is an
// agent-interaction library; this one was a typed transport, and the rebuild
// closes the difference.
//
// What is here now is the foundation: [Client] and its options, the redirect
// policy, the error surface, the event union, and [Client.Stream]. The session
// surface, files, the turn loop, transcript blocks and caller-side tool dispatch
// arrive in the milestones after this one.
//
// # Hand-authored types
//
// Every type here is written by hand against spec/openapi.json, a pinned snapshot
// of the server's OpenAPI document and this package's contract of record. No code
// generator runs, and none is committed.
//
// The guard that replaces the generator's own runs in one direction:
// TestEveryDeclaredFieldExistsInTheSpec walks every exported struct field's JSON
// tag and fails when the description does not declare that property. So this
// package cannot claim a field the server does not have. It deliberately does not
// assert the reverse, because reaching every route is not a goal — the surface is
// meant to be smaller than the document, not equal to it.
//
// The consequence to keep in mind: a route or field the document does not carry is
// a hand-written contract nothing checks. The events route is registered
// include_in_schema=False, so its body and responses appear nowhere in the
// document. Anything reached that way is named here as it arrives.
//
// # Timeouts
//
// Go's http.Client.Timeout is a deadline on the whole exchange including reading
// the response body, so any non-zero value severs a healthy long-lived stream.
// This package therefore keeps two clients over one transport: unary calls carry
// a whole-exchange timeout, and the streaming client's is zero.
//
// The unary bound defaults to 90 seconds, and [WithUnaryTimeout] moves it. It is
// that long because the two slowest routes wait on a runner before they answer
// and neither sends a byte early, so the client's deadline has to clear the
// server's own inner budgets or it aborts calls the server was still going to
// answer. Posting an event waits up to 5 seconds for the stream relay to
// subscribe and then forwards to the runner under a 60-second read timeout — 30
// seconds on the native-terminal path. Creating a session notifies the runner
// (10 seconds) and may wait 30 more for a host to launch one; giving up there is
// the worse failure, because the id of a session the server goes on to create is
// lost, which leaks it. A deadline for one call still belongs on that call's
// context: this bound is the backstop against a wedged connection, not a latency
// policy.
//
// Liveness on the stream is enforced instead by an idle watchdog — the server
// emits a heartbeat frame every 15 seconds of queue silence, so a read that
// blocks for longer than [StreamOptions.IdleTimeout] means the transport is gone.
// The watchdog measures time blocked on a read, not wall-clock time: it is
// suspended while the caller's own loop body runs, so a slow event handler cannot
// be mistaken for a dead server. Response-header latency stays bounded for both
// clients by the one transport's ResponseHeaderTimeout, which is the unary bound
// or a 30-second floor, whichever is larger — the floor because opening a stream
// waits on the same relay subscription that a post does.
//
// # Cancellation
//
// Every call takes a context.Context, and cancelling it is the only way to stop
// a stream. Note what that does and does not do: dropping the stream ends the
// *subscription*, not the agent's turn. The turn runs on the server side
// independently of any subscriber. To actually stop work, post an interrupt with
// [Client.Interrupt].
//
// # Errors
//
// Every error this package returns is matchable with errors.Is against one of
// the sentinels in errors.go, and a server response also unwraps to [APIError]
// with errors.As for the status, the server's error code, and the X-Request-Id
// to quote when reporting it. [ErrInvalidArgument] is the odd one out: it means
// this package rejected the call before sending anything.
//
// # Security
//
// A client that holds a credential has to be careful about four things, and this
// package's answer to each is fail-closed rather than best-effort.
//
// Transport. A credential must not cross a network in clear. [New] therefore
// refuses a plain-http base URL once an auth option is supplied, unless the host
// is loopback — which is why [DefaultBaseURL] works as it is: nothing leaves the
// machine. A deployment where the plaintext hop is genuinely not a network (a
// sidecar, a port-forward, a mesh that terminates TLS ahead of you) opts in with
// [WithInsecureCredentialTransport]. There is no warn-and-continue option: a
// library has no logger to warn into, and silence is how a token ends up on a
// shared segment.
//
// Redirects. Neither of this package's http.Clients follows a redirect off the
// base URL's host, down from https to http, or across a method rewrite. Go's own
// rule strips only Authorization, Cookie, Www-Authenticate and Cookie2 on a
// cross-host hop, which leaves a custom identity header — [WithAuthHeader]'s, the
// trusted-proxy one — travelling to whatever host the response named; it compares
// hostnames and not schemes, so an https-to-http hop keeps the credential and
// loses the encryption; a cross-host 307 or 308 replays the request body, which
// here is the caller's prompt; and a 302 on a POST becomes a GET, so a dropped
// write would return 200. All four are [ErrUnsafeRedirect]. A caller supplying an
// http.Client with its own CheckRedirect keeps it, and owns that.
//
// Base URLs. A base URL carrying userinfo is rejected rather than quietly
// becoming Basic auth on every request, and no error from [New] echoes the base
// URL's password — the parse-failure path reports the reason without the value,
// because url.Parse's own error quotes back what it was handed.
//
// Error values. [APIError.Error] renders the status, the server's error code and
// message, and the request id, and never the response body: a non-2xx body may
// come from a proxy rather than this API, and an error string is the one thing
// that reliably reaches a log aggregator. [APIError.Header] has the headers that
// carry a credential by specification removed. See [APIError] for the detail.
//
// What this package does not do: it never touches TLS configuration. There is no
// option to skip verification, pin a root, or reach a tls.Config, because a
// client that can be talked into trusting anything is not a security boundary.
// A deployment with a private CA configures it where such things belong — the
// system trust store, or a transport the caller builds and passes to
// [WithHTTPClient].
//
// # Reconnection
//
// The stream is live-tail only. There is no resume: the server emits no SSE
// id: field, honours no Last-Event-ID, and drops events published while nobody
// is subscribed. When a stream ends with [ErrStreamInterrupted] or
// [ErrStreamIdle], recover by fetching the snapshot with the session snapshot route,
// opening a fresh stream, and deduping persisted items by id. Reconnection is
// routine rather than exceptional — some deployments cap HTTP stream duration at
// a few minutes.
// package omnigent
package omnigent

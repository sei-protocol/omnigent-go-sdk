// Package omnigent is a Go client library for the omnigent server.
//
// # Scope
//
// This release carries [Client] and its options, the redirect and credential
// policy, the error surface, the 52-variant [Event] union, and [Client.Stream]
// for reading one session's server-sent events.
//
// It also carries the two namespaces. [Client.Sessions] covers a session's
// lifecycle, the events posted to it, and its listings; [Client.Files] covers the
// session-scoped file routes. Every listing is an iter.Seq2 that follows the
// server's cursor, and [Sessions.ChildrenTree] walks a subtree under bounds the
// caller sets.
//
// It also carries the turn loop. [Client.Chat] binds a chat to one session, and
// [Chat.Send] posts a prompt and reads until the turn ends, running the tools in a
// [ToolRegistry] and answering the approvals the server raises. [BlockStream] folds those events into the [Block] set a renderer
// switches over, and the transforms in transform.go drop or merge parts of it.
//
// A caller who wants the answer rather than the sequence uses [Chat.Query], which
// performs that fold and returns the text and the files, or [Chat.QueryStream] to
// read the text as it arrives.
//
// Where a turn ends is a caller's choice, because two harness families put it in
// different places; see [TurnEnd]. The stricter rule is the default.
//
// Building against this package needs Go 1.25 or newer. go.mod declares that
// floor and CI builds it. The floor tracks the consumer rather than the language
// features used here, which reach back to 1.23 for iter.Seq2.
//
//	client, err := omnigent.New("http://127.0.0.1:6767", omnigent.WithBearerToken(token))
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
//	for event, err := range client.Stream(ctx, sessionID, omnigent.StreamOptions{}) {
//		if err != nil {
//			return err
//		}
//		if delta, ok := event.(omnigent.OutputTextDeltaEvent); ok {
//			fmt.Print(delta.Delta)
//		}
//	}
//
// # Optional fields
//
// On a type this package decodes, every optional field is a pointer, so a caller
// can tell "the server sent zero" from "the server sent nothing". Slices and maps
// are the exception: nil already carries that distinction, and a pointer to a
// slice reads badly at a call site. [Ptr] is how a caller sets one.
//
// On a request type the caller fills — [SessionCreateRequest] and
// [SessionEventInput] — an optional field is a plain value with omitempty, and
// leaving it zero is how a caller declines to send it. Where the server needs an
// explicit value rather than an absence, a named method carries it rather than a
// magic empty string: [Sessions.ClearModelOverride], not ModelOverride = "".
//
// The conformance tests enforce the first rule for every mirrored type. A
// hand-authored type is outside that gate, so it holds the rule by review.
//
// # Enumerated values
//
// enums.go names every value the description declares for an enumerated field.
// The fields themselves stay plain strings, so a value this build has never seen
// still decodes rather than failing. A switch over those constants therefore
// needs a default arm.
//
// # Generated types
//
// The wire types come from spec/openapi.json, a pinned snapshot of the server's
// OpenAPI document and this package's contract of record. bin/generate.sh runs
// spec/preprocess.py over it and then oapi-codegen, into internal/api. Every
// type in this package's own files is a one-line declaration over that package,
// so the field documentation lives in internal/api/api.gen.go and in the
// document itself, not here. See
// docs/adr/0001-generate-wire-types-behind-a-facade.md.
//
// Eight tests hold the types to the description, across five dimensions: the
// mapping is complete in both directions, every exported field names a property
// the description declares, its Go type and optionality match, a container's
// declared value or element type matches, every declared enum value has a
// constant, and the decoder's variant set equals the discriminator mapping.
//
// What they do not check is presence: a property the description declares and
// this package omits passes. That is deliberate, because reaching every route is
// not a goal — the surface is meant to be smaller than the document, not equal to
// it. So this package cannot contradict the server about a field it declares, and
// can be silent about one it does not.
//
// The consequence to keep in mind: a route or field the document does not carry is
// a hand-written contract nothing checks, in either direction. Four are reached
// today, and each is named where it is used.
//
// The events route is registered include_in_schema=false, so neither its body nor
// its responses appear in the document. [SessionEventInput] and [EventAccepted]
// are this package's own statement of that shape, and [Sessions.Interrupt] and
// [Sessions.Compact] ride it.
//
// The create route takes a raw body and dispatches on Content-Type, so the
// document carries no request schema and [SessionCreateRequest] is hand-written.
//
// The session file routes publish an empty response schema, so [SessionFile] is
// hand-written and the file surface sits wholly outside the gate. It keeps the
// decoded body on [SessionFile.Raw] for that reason.
//
// The agent and item listings type their payload as heterogeneous, so
// [Sessions.ListAgents] and [Sessions.ListItems] narrow it by hand.
//
// # Turns
//
// A turn is one prompt and the events it produces. [Chat.Send] drives one: it
// subscribes, posts the prompt, and reads until the turn ends.
//
// The prompt is posted from [StreamOptions.OnSubscribed], so the subscription
// always exists first and the turn cannot be answered with nobody listening. Two
// consequences a caller can rely on: a stream that fails to open posts nothing, and
// a [Turn] nobody reads posts nothing. A Turn is single-use for the same reason a
// second post is not free — it would be a second turn the caller did not ask for,
// and the server would answer both.
//
// Two things run inside the loop, before the turn's end is read, because the server
// parks a turn on each of them and the terminal event only follows once they are
// answered: a client tool call, and an approval request. Both are answered even
// when they fail — an unregistered tool posts an output naming the mismatch rather
// than leaving the turn parked, and an approval with no decision is declined.
//
// Declining is this package's own behaviour and not a policy. It cannot know what a
// caller would approve, and accepting authorises the pending tool to run with the
// session owner's execution identity — not the approver's. A caller with a policy
// supplies [StreamHooks.OnElicitation]; a hook that panics also declines.
//
// One obligation is the caller's. A session that may have a response still running
// needs [TurnOptions.PriorResponseIDs], built from [Sessions.Get] — its
// [SessionResponse.ActiveResponseID] names the response in flight. Without them a response that predates this turn can end this read, and
// the caller gets another turn's ending. Give [Chat.Send] a context with a deadline
// too: [TurnEndsOnIdleStatus] waits for an edge that a mismatched harness never
// sends, and the stream's heartbeat means no timeout of this package's fires.
//
// # What upstream has and this package does not
//
// The Python client's public surface is the reference for this one, and two of its
// symbols are deliberately absent rather than pending. This section states what
// will not be built.
//
// LocalServer starts a server process and waits for it to listen. Managing a
// server's lifecycle is not a client's job in Go, where a caller already has
// os/exec and a health check, and a helper that owned a subprocess would own its
// signals and its logs too.
//
// tool is a decorator the server's runtime consumes to load tools inside an agent
// image. A Go caller registers the client half with [ToolRegistry], which is the
// part that runs in this process.
//
// # Timeouts
//
// Go's http.Client.Timeout is a deadline on the whole exchange including reading
// the response body, so any non-zero value severs a healthy long-lived stream.
// This package therefore keeps three clients over one transport. Unary calls
// carry a whole-exchange timeout. The streaming client's is zero, and so is the
// transfer client's: a file's duration is its size over the network's rate, so
// the bound that stops a wedged call is the bound that makes a large upload
// impossible, and one number cannot be both.
//
// The unary bound defaults to 90 seconds, and [WithUnaryTimeout] moves it. It is
// that long because the slowest routes wait on a runner before they answer and
// send no byte early, so a shorter deadline aborts calls the server was still
// going to answer — and abandoning a session create leaks the session the server
// goes on to make. The arithmetic behind the number sits beside the constant in
// client.go. A deadline for one call still belongs on that call's context: this
// bound is the backstop against a wedged connection, not a latency policy.
//
// A transfer — [SessionFiles.Upload] or [SessionFiles.Download] — is bounded by
// its context instead, so a large file is limited by what the caller allows
// rather than by a number sized for an RPC. [WithTransferTimeout] sets one
// ceiling for every transfer when a caller wants one. Header latency stays
// bounded either way by the transport, so a server that accepts a connection and
// then says nothing still fails fast.
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
// [Sessions.Interrupt].
//
// # Errors
//
// A server response and every rejected argument are matchable with errors.Is
// against one of the sentinels in errors.go. A transport or codec failure is
// returned wrapped and matches none of them, so a caller that switches on the
// sentinels needs a default arm. A server response also unwraps to [APIError]
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
// [ErrStreamIdle], recover by fetching the session snapshot,
// opening a fresh stream, and deduping persisted items by id. Reconnection is
// routine rather than exceptional — some deployments cap HTTP stream duration at
// a few minutes.
//
// Recover by fetching the snapshot with [Sessions.Get], opening a fresh stream, and
// deduping the persisted items from [Sessions.ListItems] by id.
package omnigent

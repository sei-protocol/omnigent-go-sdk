// Package omnigent is a Go client for the omnigent server's session API.
//
// It reaches eight session calls — create, get, update, delete, send input,
// resolve an elicitation, interrupt, and the event stream — plus the agent and
// session listings. That is a working subset rather than the API: openapi.json
// publishes dozens of further session operations (items, resources, policies,
// permissions, comments, fork, agent swap) that this package does not call.
//
// # Quickstart
//
//	client, err := omnigent.New(omnigent.DefaultBaseURL)
//	if err != nil {
//		return err
//	}
//
//	session, err := client.CreateSession(ctx, omnigent.SessionCreateRequest{AgentID: agentID})
//	if err != nil {
//		return err
//	}
//
//	// Open the stream before posting, so the turn's first events cannot be
//	// missed: the server buffers nothing for absent subscribers. OnSubscribed
//	// runs once the subscription is live, and only once.
//	opts := omnigent.StreamOptions{
//		OnSubscribed: func(ctx context.Context, sub omnigent.Subscription) error {
//			_, err := client.SendMessage(ctx, sub.SessionID, "hello")
//			return err
//		},
//	}
//
//	// Take the deltas as a preview, not as the answer: some harnesses send
//	// none, and the reply itself is a committed conversation item that can be
//	// posted after the turn's terminal event. So this loop reads to the end of
//	// the turn and stops there.
//	var preview strings.Builder
//	var responseID string
//	for event, err := range client.Stream(ctx, session.ID, opts) {
//		if err != nil {
//			return err
//		}
//		var done bool
//		switch ev := event.(type) {
//		case omnigent.OutputTextDeltaEvent:
//			preview.WriteString(ev.Delta)
//		case omnigent.ResponseCompletedEvent:
//			responseID, done = ev.Response.ID, true
//		case omnigent.ResponseFailedEvent:
//			responseID, done = ev.Response.ID, true
//		case omnigent.IncompleteEvent:
//			responseID, done = ev.Response.ID, true
//		case omnigent.ResponseCancelledEvent:
//			responseID, done = ev.Response.ID, true
//		}
//		if done {
//			break
//		}
//	}
//
//	// Deltas, where a harness sends them, are this turn's by definition, so a
//	// non-empty preview is already the reply.
//	if text := preview.String(); strings.TrimSpace(text) != "" {
//		fmt.Print(text)
//		return nil
//	}
//
//	// Otherwise the reply is read from the session, and polled for because the
//	// commit is not ordered against the terminal event. The window is the
//	// caller's policy; that it is bounded is not, because a reply that never
//	// commits must not hang the program.
//	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
//	defer cancel()
//	for {
//		snapshot, err := client.GetSession(pollCtx, session.ID, omnigent.GetSessionOptions{
//			IncludeItems:    omnigent.Ptr(true),
//			IncludeLiveness: omnigent.Ptr(false),
//		})
//		if err != nil {
//			return err
//		}
//		if text := assistantText(snapshot.Items, responseID); text != "" {
//			fmt.Print(text)
//			return nil
//		}
//		select {
//		case <-time.After(250 * time.Millisecond):
//		case <-pollCtx.Done():
//			return pollCtx.Err()
//		}
//	}
//
// assistantText is the item read, and "Reading a conversation item" below is
// where it comes from: a [ConversationItem]'s payload has to be gated on its
// Type before it is interpreted.
//
// # Harnesses, deltas, and where the reply is
//
// A turn's authoritative output is the committed conversation item. The text
// deltas on the stream are a preview of one, and both halves of that sentence
// are load-bearing.
//
// Deltas are not promised. The platform declares a streaming flag per harness in
// the capability table in omnigent/harness_plugins.py, served over
// GET /v1/harnesses — a route this package does not call. Read the flag as a
// declaration and not a guarantee: the comment above that table records that
// only four harnesses have interrupt and streaming verified live by the harness
// bench — claude-sdk, codex, pi and openai-agents — and that the rest are
// declared from their integration mode. Three declare no streaming because a
// live run recorded zero text deltas: cursor-native, kiro-native and
// qwen-native, and on kiro-native the whole reply arrives as one
// response.output_item.done. A harness declaring true can still deliver nothing
// in a given deployment: claude-native's deltas come from a Claude Code
// MessageDisplay hook appending to a file that its forwarder tails, so a host
// where that hook has not fired streams no deltas while still declaring them.
//
// The item can arrive after the turn ends. On a harness that runs a resident
// vendor TUI — the -native family, integration mode native_tui — the reply
// reaches the session through a forwarder polling the vendor's transcript every
// 0.25s, and an assistant message is deliberately held back, for up to 2.0s,
// until its forwarded deltas have gone out ahead of it
// (omnigent/claude_native_forwarder.py). Nothing orders that post against the
// turn's terminal event. A loop that returns on [ResponseCompletedEvent] and
// reports what it accumulated therefore reports nothing at all, and cannot tell
// that from an agent that answered with silence. The quickstart's shape is the
// fix: stop reading at the terminal event, then go and read the item.
//
// Nor is the reply on the terminal event. The server builds that response object
// from id, status, model, created_at, error and usage, and does not set output,
// although [ResponseObject.Output]'s description — "empty for non-completed
// responses" — reads as a promise that a completed one is populated. Reading it
// is harmless and costs a loop over a nil slice; relying on it is not.
//
// [SessionResponse.Harness] and [AgentObject.Harness] name the harness behind a
// session or an agent, for a program that has to branch on the family. The loop
// above needs no branch, which is why it does not have one: read the deltas if
// they come, and read the item either way.
//
// # Reading a conversation item
//
// [ConversationItem.Data] is a union of eleven payloads and the spec gives it no
// discriminator. The value that says which payload it holds is the sibling
// [ConversationItem.Type], which the generated accessors cannot see, because
// each is declared on the union field alone and is a plain json.Unmarshal into
// one variant's struct.
//
// So a successful AsX is not evidence the payload was an X. [MessageData] and
// [FunctionCallData] share no field names, so AsMessageData on a function_call
// returns a zero-valued MessageData and a nil error. Gate on the type first, and
// treat the accessor as a decoder rather than as a check:
//
//	// Newest first: the agent's last message is its answer, and
//	// SessionResponse.Items is chronological.
//	func assistantText(items []omnigent.ConversationItem, responseID string) string {
//		for i := len(items) - 1; i >= 0; i-- {
//			item := items[i]
//			if item.Type != "message" || item.Status != "completed" {
//				continue
//			}
//			// A stamp is the server attesting which turn the item belongs to.
//			// An unstamped item is admitted, because a harness may leave it
//			// empty; another turn's stamp is not.
//			if item.ResponseID != "" && responseID != "" && item.ResponseID != responseID {
//				continue
//			}
//			msg, err := item.Data.AsMessageData()
//			if err != nil || !strings.EqualFold(string(msg.Role), "assistant") {
//				continue
//			}
//			if text := contentText(msg.Content); text != "" {
//				return text
//			}
//		}
//		return ""
//	}
//
// contentText joins the text blocks of [MessageData.Content], which are untyped
// maps of the shape {"type": "output_text", "text": "..."}. It allowlists the
// block types it reads rather than taking every block with a text key, because
// reasoning and refusal blocks carry one too; the README spells it out.
//
// Keying on the decoded value alone — "the role is not assistant, so skip it" —
// happens to filter a function_call out today, because the zero value of
// [MessageData.Role] matches nothing. It is an accident that stops holding the
// moment two variants share a field name, and it silently reads a payload of the
// wrong kind in the meantime.
//
// Admitting an unstamped item is what a session outliving one turn has to pay
// for: on a second invocation the loop above can return the *previous* turn's
// reply. A program that reuses a session needs the ids it had already seen before
// the turn began and has to skip those too. Three more fields are worth honouring
// before text is published anywhere — [ConversationItem.CreatedBy] is non-nil
// only for a human author, [MessageData.IsMeta] marks context meant for the agent
// and not for a transcript, and [MessageData.Interrupted] marks a partial reply
// from a turn that was cut short. Each is stamped by the server and cannot be
// forged by a client, which is what makes them worth reading.
//
// [ValidationError.Loc] is the other union here and does not have this problem:
// its two variants are a string and an int, so a failed unmarshal is a real
// answer about which one arrived.
//
// # Send-after-subscribe, and the heartbeat that looks like a signal
//
// The stream buffers nothing for absent subscribers, so input must not be posted
// until the subscription exists. The obvious-looking way to detect that is to
// wait for the first [SessionHeartbeatEvent] and send from there — and it is
// wrong. The server uses one event, with a byte-identical payload, for two
// unrelated jobs: the subscription acknowledgement it yields the moment the
// subscriber slot is registered, and the keepalive it emits every 15 seconds
// while a stream sits idle between turns. Nothing on the wire tells them apart.
// A caller that sends on every heartbeat therefore re-sends its message for as
// long as the stream stays open.
//
// [StreamOptions.OnSubscribed] is this package's answer: the iterator calls it
// once, before the first event reaches the caller, and never again for that
// stream. Where the input can be known up front,
// [SessionCreateRequest.InitialItems] is better still — the server queues it at
// create time, so there is no ordering to get right.
//
// # Generated versus hand-written
//
// models.gen.go and events.gen.go are generated from the repository's
// openapi.json by scripts/gen_go_client.py, together with the type-string
// dispatch for the 52 variants of the SSE event union. Do not edit them;
// regenerate instead.
//
// The generated surface is deliberately narrower than the spec. It is the $ref
// closure of the schemas this package's own API names — [SessionResponse],
// [ConversationDeleted], [SessionGitOptions], [ValidationError], [AgentObject],
// [SessionListItem] — plus every member of the event union. Generating the whole
// document would export types named after the server's path-mangled operationIds
// and a second public representation of what [Event] already models, neither of
// which is API this module intends to offer.
//
// Four types are hand-written in session.go because the server documents neither
// their routes nor their schemas, so openapi.json carries nothing to generate
// them from and no drift gate covers them:
// [SessionCreateRequest] (the create route takes a raw request and dispatches on
// Content-Type, so FastAPI emits no requestBody for it), and
// [SessionEventInput], [EventAccepted] and [ElicitationResult] (the send route is
// registered with include_in_schema=False, so neither its body nor its responses
// appear). A server-side change to any of the four breaks this client silently.
//
// # Listings
//
// [Client.ListAgents] and [Client.ListSessions] page by opaque cursor into a
// shared [Page]. Between them they cover the two lookups a program needs before
// it can do anything else: turning an agent name into the id a create wants, and
// finding a session it created on an earlier run rather than creating a second
// one. See [Page] for the paging loop and [ListSessionsOptions] for the filters.
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
// [ErrStreamIdle], recover by fetching the snapshot with [Client.GetSession],
// opening a fresh stream, and deduping persisted items by id. Reconnection is
// routine rather than exceptional — some deployments cap HTTP stream duration at
// a few minutes.
package omnigent

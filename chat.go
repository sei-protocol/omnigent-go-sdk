package omnigent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
)

// Chat drives one session's turns.
//
// Where [Sessions] is the session's state and [Client.Stream] is its raw events,
// Chat is the loop between them: it posts a prompt, reads the stream until the
// turn ends, runs the tools the agent asks for, and answers the approvals the
// server raises. A caller that wants the loop writes [Chat.Send]; a caller that
// wants the parts still has them.
type Chat struct {
	client    *Client
	sessionID string
	opts      ChatOptions
}

// DefaultMaxToolCalls is the tool-call budget a turn gets when
// [ChatOptions.MaxToolCalls] is zero.
//
// Chosen to sit well above a legitimate turn rather than close to one, because
// reaching it parks the agent. A session snapshot returns the newest 100 items,
// which is the server's own signal about the scale of a turn, so this leaves an
// order of magnitude of headroom. A caller doing something unusual raises it; the
// value is in the bound being finite at all, since nothing else stops a server
// that issues a fresh call id per ask.
const DefaultMaxToolCalls = 1024

// ChatOptions configures how a chat drives its turns.
type ChatOptions struct {
	// Turn decides where a turn ends. Its zero value is the stricter rule; see
	// [TurnEndsOnIdleStatus].
	Turn TurnOptions

	// Tools are the tools this client will run when the agent calls them. Nil
	// means the client runs none, and a call the agent makes is answered with
	// [ErrToolNotRegistered] rather than left parked.
	Tools *ToolRegistry

	// MaxToolCalls caps how many tool calls one turn will run. Zero means the
	// default, [DefaultMaxToolCalls]; a negative value means no cap.
	//
	// The cap exists because the call id is the server's to choose, so nothing
	// else bounds how many times a turn asks this client to run privileged code.
	// Reaching it ends the turn: the server is parked on a call this package
	// declined, so its terminal never arrives.
	MaxToolCalls int

	// Hooks observe the turn. Optional.
	Hooks StreamHooks

	// Stream configures the underlying event subscription.
	Stream StreamOptions
}

// Chat returns a chat bound to one session.
//
// Mirrors upstream's client.sessions_chat, and takes the session id rather than
// creating a session, so the session's lifecycle stays with [Sessions].
func (c *Client) Chat(sessionID string, opts ChatOptions) (*Chat, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("chat: %w: sessionID is required", ErrInvalidArgument)
	}
	if opts.Stream.OnSubscribed != nil {
		// A Chat posts its prompt from that hook, so a second one there would post
		// a second prompt for one turn and the server would answer both. Refused
		// rather than overridden: a caller who set it meant it to run.
		return nil, fmt.Errorf("chat: %w: ChatOptions.Stream.OnSubscribed is reserved, "+
			"because a turn posts its prompt there", ErrInvalidArgument)
	}
	return &Chat{client: c, sessionID: sessionID, opts: opts}, nil
}

// Turn is one prompt and the turn it produces. Single-use.
//
// Obtained from [Chat.Prompt] and read exactly once. A second read is refused
// rather than posting the prompt again, because posting twice is a second turn
// the caller did not ask for and the server would answer both.
type Turn struct {
	chat *Chat
	text string

	mu   sync.Mutex
	read bool
}

// Prompt prepares one turn without sending it.
//
// Nothing is posted until the returned turn is read, so a caller that builds a
// turn and abandons it has sent nothing.
func (c *Chat) Prompt(text string) *Turn {
	return &Turn{chat: c, text: text}
}

// Send drives one turn and yields its events until the turn ends.
//
// The loop: subscribe, post the prompt, then read. Subscribing first is what makes
// the read complete — a prompt posted before the subscription exists can be
// answered before anyone is listening, and the turn's own events are then missed.
//
// Tools run inline, before the turn's end is checked, because the server parks a
// turn on a client tool call: the terminal event only arrives after the parked
// call is answered. Approvals are answered the same way and for the same reason.
//
// Ends when the turn ends, and the subscription closes with it.
//
// Two classes of error arrive here, and they are not read the same way. A transport
// failure, a prompt that could not be posted, [ErrTurnFailed] or [ErrTurnSuperseded]
// end the sequence. A tool that failed, an approval this package could not resolve,
// a duplicated call and a hook that panicked are reported and the read continues —
// the turn is still running, and still parked on whatever comes next. Each of those
// has its own sentinel, so a caller deciding whether to stop matches rather than
// assuming.
func (c *Chat) Send(ctx context.Context, text string) iter.Seq2[Event, error] {
	return c.Prompt(text).Events(ctx)
}

// Events drives the turn. See [Chat.Send].
//
// Refuses a second read with [ErrTurnAlreadyRead].
func (t *Turn) Events(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if !t.claim() {
			yield(nil, fmt.Errorf("read turn on session %s: %w", t.chat.sessionID, ErrTurnAlreadyRead))
			return
		}
		t.chat.drive(ctx, t.text, yield)
	}
}

// claim reserves the one read this turn allows.
func (t *Turn) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.read {
		return false
	}
	t.read = true
	return true
}

// drive runs the turn loop and reports each event to the caller.
//
// The prompt is posted from [StreamOptions.OnSubscribed], which is this package's
// supported way to start a turn, and the reason is timing: the hook runs once the
// subscription is live and before the first event reaches the caller. So the turn
// cannot be answered with nobody listening, and the anchor is recorded before any
// event can be tested against it.
//
// Three properties follow from posting there rather than from a goroutine of our
// own. A stream that fails to open never runs the hook, so nothing is posted for a
// turn that never started. A caller who abandons the sequence before reading never
// runs it either, which is what makes [Chat.Prompt]'s promise true. And there is no
// readiness left to guess at: the acknowledgement frame is byte-identical to a
// keepalive, so no amount of event inspection could have recognised it.
func (c *Chat) drive(ctx context.Context, text string, yield func(Event, error) bool) {
	run := &turnRun{
		chat:       c,
		tracker:    newTurnTracker(c.opts.Turn, c.sessionID),
		dispatched: map[string]bool{},
		yield:      yield,
	}

	opts := c.opts.Stream
	opts.OnSubscribed = func(ctx context.Context, sub Subscription) error {
		accepted, err := c.client.Sessions().SendMessage(ctx, sub.SessionID, text)
		if err != nil {
			return fmt.Errorf("send prompt: %w", err)
		}
		// A refusal arrives as a 200 the caller has to read. Reported here rather
		// than ignored, because a denied prompt starts no turn: the anchor stays
		// empty, no rule can ever cross the boundary, and the read would otherwise
		// run to the caller's deadline and blame the stream.
		if accepted.Denied {
			return fmt.Errorf("send prompt: %w: %s", ErrInputDenied,
				sanitizeForError(accepted.Reason, maxErrorFieldRunes))
		}
		run.tracker.anchorOn(anchorOf(accepted))
		return nil
	}

	for event, err := range c.client.Stream(ctx, c.sessionID, opts) {
		if err != nil {
			run.emit(nil, err)
			return
		}
		run.track(event)
		if !run.emit(event, nil) {
			return
		}
		// After the event, so a caller correlating an error with its cause reads them
		// in that order. Before the end check, because the server parks the turn on a
		// tool call and an approval, and its terminal only follows once they answer.
		run.act(ctx, event)
		if run.stopped || run.unfinishable {
			return
		}
		if run.tracker.ended() {
			if run.tracker.failure != nil {
				run.emit(nil, run.tracker.failure)
			}
			return
		}
	}
	// Reported rather than treated as an end: a caller reading the sequence as
	// complete would take a partial answer for the whole one.
	if !run.tracker.ended() && !run.stopped && !run.unfinishable {
		run.emit(nil, fmt.Errorf("turn on session %s: %w", c.sessionID, ErrTurnIncomplete))
	}
}

// toolBudget resolves [ChatOptions.MaxToolCalls]: zero takes the default, and a
// negative value removes the cap.
func (c *Chat) toolBudget() int {
	switch {
	case c.opts.MaxToolCalls == 0:
		return DefaultMaxToolCalls
	case c.opts.MaxToolCalls < 0:
		return 0
	default:
		return c.opts.MaxToolCalls
	}
}

// turnRun is one turn's mutable state.
//
// A struct rather than locals in [Chat.drive], because the side effects need it:
// which calls have run, which response is live, and whether the caller has stopped
// reading. Threading four values through four methods reads worse than naming them.
type turnRun struct {
	chat    *Chat
	tracker *turnTracker
	yield   func(Event, error) bool

	// dispatched records the calls this turn has run, keyed by call id.
	//
	// The wire delivers one call twice on the MCP path — [BlockStream] folds that
	// away for a renderer, and this is the same fact on the executing side. Without
	// it a deploy, a spend or a signature runs twice for one authorisation.
	//
	// By call id alone, because that is the identity the server itself uses: the
	// output this package posts carries call_id and output, and nothing else, so a
	// call id the server issues twice concurrently is one it could not route an
	// answer to either. An earlier key added the item's agent_name, which is a
	// field the server chooses — so one call could be run again under a second
	// name, which is the opposite of what this guard is for.
	//
	// A call that failed cannot be retried under the same key. That is deliberate for
	// a tool that moves money, and it means a re-delivery after a failed post is
	// dropped rather than retried.
	dispatched map[string]bool

	// responseID and iteration say where in the turn we are, for the payloads that
	// report them.
	responseID string
	iteration  int

	// unfinishable records that the turn cannot reach its end, so reading on would
	// only wait for the caller's deadline. Set when the server refuses an answer
	// this package owes it: the server stays parked on the call, its terminal never
	// comes, and the cause is already reported.
	unfinishable bool

	// stopped records a caller that stopped reading. Nothing may yield after that:
	// calling a spent yield panics the caller's range loop.
	stopped bool
}

// emit passes one event to the caller and records a caller that stopped reading.
//
// Every yield in a turn goes through here. A side effect that ignored a false
// return and yielded again would panic in the caller's loop, so no side effect
// holds the raw yield.
// report hands an error to the caller and discards whether they are still reading.
//
// For a hook's panic, which is a step's own failure rather than the sequence's: the
// loop checks stopped straight after, so nothing yields twice.
func (r *turnRun) report(err error) { r.emit(nil, err) }

func (r *turnRun) emit(event Event, err error) bool {
	if r.stopped {
		return false
	}
	if !r.yield(event, err) {
		r.stopped = true
		return false
	}
	return true
}

// track advances the turn's own state, and nothing else.
//
// Separate from [turnRun.act] because this has to run before the end is read while
// the side effects have to run after the event they belong to. Nothing here yields,
// so it cannot be affected by a caller who stopped reading.
func (r *turnRun) track(event Event) {
	switch e := event.(type) {
	case SessionInputConsumedEvent:
		r.tracker.crossBoundary(e)
	case SessionStatusEvent:
		r.tracker.observeStatus(e)
	case SessionSupersededEvent:
		r.tracker.observeSuperseded(e)
	case ResponseCreatedEvent:
		r.responseID = e.Response.ID
		r.iteration = 0
	case ResponseCompletedEvent:
		r.tracker.observeResponseTerminal(e.Response.ID, nil)
	case ResponseCancelledEvent:
		r.tracker.observeResponseTerminal(e.Response.ID,
			fmt.Errorf("%w: the turn was cancelled", ErrTurnFailed))
	case IncompleteEvent:
		r.tracker.observeResponseTerminal(e.Response.ID,
			fmt.Errorf("%w: the response was incomplete", ErrTurnFailed))
	case ResponseFailedEvent:
		r.tracker.observeResponseTerminal(e.Response.ID,
			fmt.Errorf("%w: %s", ErrTurnFailed, responseFailure(e.Response)))
	}
}

// act runs the side effects one event calls for: the hooks, the tool this client
// owns, and the approval the server is waiting on.
func (r *turnRun) act(ctx context.Context, event Event) {
	hooks := r.chat.opts.Hooks
	switch e := event.(type) {
	case ResponseCreatedEvent:
		fire(r.report, "OnResponseStart", hooks.OnResponseStart, ResponseStartCtx{ResponseID: e.Response.ID, Model: e.Response.Model})

	case ReasoningStartedEvent:
		fire(r.report, "OnReasoningStart", hooks.OnReasoningStart, ReasoningStartCtx{ResponseID: r.responseID})

	case RetryEvent:
		fire(r.report, "OnRetry", hooks.OnRetry, RetryCtx{Source: e.Source, Attempt: e.Attempt, MaxAttempts: e.MaxAttempts})

	case ErrorEvent:
		fire(r.report, "OnServerError", hooks.OnServerError, ServerErrorCtx{
			Message: e.Error.Message, Source: e.Source, Code: e.Error.Code,
		})

	case OutputFileDoneEvent:
		filename := ""
		if e.Filename != nil {
			filename = *e.Filename
		}
		fire(r.report, "OnFileOutput", hooks.OnFileOutput, FileOutputCtx{FileID: e.FileID, Filename: filename})

	case ElicitationRequestEvent:
		r.resolveElicitation(ctx, e)

	case OutputItemDoneEvent:
		r.observeItem(ctx, e)

	case ResponseCompletedEvent:
		r.endResponse(e.Response)
	case ResponseCancelledEvent:
		r.endResponse(e.Response)
	case IncompleteEvent:
		r.endResponse(e.Response)
	case ResponseFailedEvent:
		r.endResponse(e.Response)
	}
}

func (r *turnRun) endResponse(response ResponseObject) {
	fire(r.report, "OnResponseEnd", r.chat.opts.Hooks.OnResponseEnd, ResponseEndCtx{ResponseID: response.ID, Status: response.Status})
	// Counts passes for the payloads that report one. A turn reaches one terminal
	// response, so this advances only if that response did not end the turn.
	r.iteration++
}

// observeItem reports a finished output item and runs the ones this client owns.
//
// Every function_call fires the call hooks, whichever side ran it, so a caller
// counting tool use sees all of it. Only an item awaiting this client is executed:
// one the server already ran arrives finished, and running it again would repeat
// whatever it did.
func (r *turnRun) observeItem(ctx context.Context, e OutputItemDoneEvent) {
	if itemString(e.Item, "type") != "function_call" {
		return
	}
	callID := strings.TrimSpace(itemString(e.Item, "call_id"))
	name := strings.TrimSpace(itemString(e.Item, "name"))
	if callID == "" || name == "" {
		// Trimmed first: a call the server cannot be answered about is one this
		// client must not post an output for, and whitespace is not an identifier.
		return
	}

	awaitingClient := itemString(e.Item, "status") == itemStatusActionRequired
	executedBy := "server"
	if awaitingClient {
		executedBy = "client"
	}
	info := ToolCallInfo{
		Name:       name,
		Arguments:  itemArguments(e.Item),
		CallID:     callID,
		AgentName:  itemString(e.Item, "agent_name"),
		ResponseID: r.responseID,
		Iteration:  r.iteration,
	}
	fire(r.report, "OnToolCallStart", r.chat.opts.Hooks.OnToolCallStart, ToolCallStartCtx{
		Name: info.Name, CallID: info.CallID, AgentName: info.AgentName,
		Arguments: info.Arguments, ExecutedBy: executedBy,
	})
	if !awaitingClient {
		return
	}
	if r.dispatched[callID] {
		// The same call, delivered twice. Answered once — and said so, because a
		// silent drop is indistinguishable from a call this client never saw.
		r.emit(nil, fmt.Errorf("%w: %s already ran",
			ErrToolCallDuplicated, sanitizeForError(callID, maxRequestIDRunes)))
		return
	}
	// A budget, because the call id is the server's to choose: without one, a
	// compromised relay turns a single prompt into unbounded invocations of the
	// caller's most privileged code, and only the caller's deadline stops it.
	if limit := r.chat.toolBudget(); limit > 0 && len(r.dispatched) >= limit {
		r.emit(nil, fmt.Errorf("%w: %d in one turn; raise ChatOptions.MaxToolCalls "+
			"if this turn is legitimate", ErrToolCallBudget, limit))
		// The server is parked on a call this package will not answer, so its
		// terminal never arrives.
		r.unfinishable = true
		return
	}
	r.dispatched[callID] = true
	r.runTool(ctx, info)
}

// runTool executes one call and posts its output.
//
// The output is posted whatever happened, because the server parks the turn on this
// call: a tool that fails, panics, or is not registered still has to answer, or the
// turn waits for its deadline and reads as a hung agent.
func (r *turnRun) runTool(ctx context.Context, info ToolCallInfo) {
	output, runErr := r.chat.opts.Tools.run(ctx, info)
	fire(r.report, "OnToolCallEnd", r.chat.opts.Hooks.OnToolCallEnd, ToolCallEndCtx{
		Name: info.Name, CallID: info.CallID, Output: output, Err: runErr,
	})

	accepted, err := r.chat.client.Sessions().PostEvent(ctx, r.chat.sessionID, SessionEventInput{
		Type: InputTypeFunctionCallOutput,
		Data: map[string]any{"call_id": info.CallID, "output": output},
	})
	if err != nil {
		r.emit(nil, fmt.Errorf("post output for tool %q: %w",
			sanitizeForError(info.Name, maxToolNameRunes), err))
		return
	}
	// The tool already ran. A refused output means the side effect happened and
	// the server never learned the answer, which is worth saying rather than
	// leaving the session parked on a call it thinks is outstanding.
	if accepted.Denied {
		r.emit(nil, fmt.Errorf("post output for tool %q: %w: %s",
			sanitizeForError(info.Name, maxToolNameRunes), ErrInputDenied,
			sanitizeForError(accepted.Reason, maxErrorFieldRunes)))
		// The server is parked on a call it will not accept an answer for, so its
		// terminal never arrives. Ending here reports the refusal as the cause
		// rather than the caller's deadline.
		r.unfinishable = true
		return
	}
	if runErr != nil {
		// Reported, including a tool this client does not have: a server asking for
		// a tool nobody registered is a misconfigured or hostile session, and the
		// caller is the only party who can act on that.
		r.emit(nil, runErr)
	}
}

// resolveElicitation answers an approval the server is waiting on.
//
// Declined when no hook decides, and declined when the hook panics. That is this
// package's own behaviour rather than a policy: it cannot know what a caller would
// approve, and accepting authorises the pending tool to run with the session
// owner's execution identity — see [ElicitationAccept].
//
// Answered against the session the request names, which is a sub-agent's own when
// its prompt is mirrored into an ancestor's stream.
func (r *turnRun) resolveElicitation(ctx context.Context, e ElicitationRequestEvent) {
	verdict := ElicitationDecline
	if r.decideElicitation(e) {
		verdict = ElicitationAccept
	}
	target := elicitationTarget(r.chat.sessionID, e)
	if err := r.chat.client.Sessions().ResolveElicitation(ctx, target, e.ElicitationID,
		ElicitationResult{Action: verdict}); err != nil {
		r.emit(nil, fmt.Errorf("resolve elicitation %s: %w",
			sanitizeForError(e.ElicitationID, maxRequestIDRunes), err))
	}
}

// decideElicitation asks the caller's hook, and treats a panic as a decline.
//
// A hook is caller code doing a policy lookup. A panic there must not leave the
// approval unanswered or end the turn, and the safe answer is already known, so it
// is the one given.
func (r *turnRun) decideElicitation(e ElicitationRequestEvent) (accept bool) {
	hook := r.chat.opts.Hooks.OnElicitation
	if hook == nil {
		return false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			// Declined, and reported. The verdict has to be the safe one, and the
			// caller has to hear that their policy code failed.
			accept = false
			r.report(fmt.Errorf("%w: hook OnElicitation, declined: %s", ErrHookPanicked,
				sanitizeForError(fmt.Sprint(recovered), maxErrorFieldRunes)))
		}
	}()
	return hook(elicitationCtxOf(r.chat.sessionID, r.responseID, e))
}

// elicitationTarget reports the session whose resolve route owns a request.
//
// The request names one when a sub-agent's prompt is mirrored into an ancestor's
// stream; otherwise it is the session being read. Posting a verdict to the stream's
// session in the mirrored case leaves the sub-agent parked, which reads as an agent
// that stopped rather than one waiting on an answer that went elsewhere.
func elicitationTarget(reading string, e ElicitationRequestEvent) string {
	if named := e.Params.TargetSessionID; named != nil && *named != "" {
		return *named
	}
	return reading
}

func elicitationCtxOf(fallbackSession, responseID string, e ElicitationRequestEvent) ElicitationCtx {
	ctx := ElicitationCtx{
		SessionID:       elicitationTarget(fallbackSession, e),
		ElicitationID:   e.ElicitationID,
		Message:         e.Params.Message,
		RequestedSchema: e.Params.RequestedSchema,
		ResponseID:      responseID,
	}
	if e.Params.Mode != nil {
		ctx.Mode = *e.Params.Mode
	}
	if e.Params.URL != nil {
		ctx.URL = *e.Params.URL
	}
	if e.Params.Phase != nil {
		ctx.Phase = *e.Params.Phase
	}
	if e.Params.PolicyName != nil {
		ctx.PolicyName = *e.Params.PolicyName
	}
	if e.Params.ContentPreview != nil {
		ctx.ContentPreview = *e.Params.ContentPreview
	}
	return ctx
}

// itemStatusActionRequired is the item status that means the server is waiting for
// this client.
//
// A function_call item arrives with this status when the tool is the client's to
// run, and with "completed" when the server already ran it. That difference is the
// only thing separating a call to dispatch from one to display.
const itemStatusActionRequired = "action_required"

// anchorOf reports the identifier the server will echo for a posted prompt.
//
// The item id when the prompt was persisted, and the pending id when it was parked
// instead. Whichever the post returned is what the echo carries.
func anchorOf(accepted *EventAccepted) string {
	if accepted == nil {
		return ""
	}
	if accepted.ItemID != "" {
		return accepted.ItemID
	}
	return accepted.PendingID
}

// responseFailure renders a failed response's reason.
func responseFailure(response ResponseObject) string {
	if response.Error == nil {
		return "the server reported failure, with no detail"
	}
	return fmt.Sprintf("%s (%s)",
		sanitizeForError(response.Error.Message, maxErrorFieldRunes),
		sanitizeForError(response.Error.Code, maxErrorFieldRunes))
}

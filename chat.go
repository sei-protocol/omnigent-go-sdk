package omnigent

import (
	"context"
	"fmt"
	"iter"
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

// ChatOptions configures how a chat drives its turns.
type ChatOptions struct {
	// Turn decides where a turn ends. Its zero value is the stricter rule; see
	// [TurnEndsOnIdleStatus].
	Turn TurnOptions

	// Tools are the tools this client will run when the agent calls them. Nil
	// means the client runs none, and a call the agent makes is answered with
	// [ErrToolNotRegistered] rather than left parked.
	Tools *ToolRegistry

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
// Ends when the turn ends, and the subscription closes with it. An error ends the
// sequence, and [ErrTurnFailed] means the server reported the failure rather than
// the transport.
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
		run.tracker.anchorOn(anchorOf(accepted))
		return nil
	}

	for event, err := range c.client.Stream(ctx, c.sessionID, opts) {
		if err != nil {
			run.emit(nil, err)
			return
		}
		run.observe(ctx, event)
		if run.stopped {
			return
		}
		if !run.emit(event, nil) {
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
	if !run.tracker.ended() && !run.stopped {
		run.emit(nil, fmt.Errorf("turn on session %s: %w", c.sessionID, ErrTurnIncomplete))
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
	dispatched map[string]bool

	// responseID and iteration say where in the turn we are, for the payloads that
	// report them.
	responseID string
	iteration  int

	// stopped records a caller that stopped reading. Nothing may yield after that:
	// calling a spent yield panics the caller's range loop.
	stopped bool
}

// emit passes one event to the caller and records a caller that stopped reading.
//
// Every yield in a turn goes through here. A side effect that ignored a false
// return and yielded again would panic in the caller's loop, so no side effect
// holds the raw yield.
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

// observe advances the tracker and runs the side effects one event calls for.
//
// Ordered deliberately: a tool call and an approval are answered before the turn's
// end is read, because the server parks the turn on both and the terminal event
// only follows once they are answered.
func (r *turnRun) observe(ctx context.Context, event Event) {
	hooks := r.chat.opts.Hooks
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
		fire(hooks.OnResponseStart, ResponseStartCtx{ResponseID: e.Response.ID, Model: e.Response.Model})

	case ReasoningStartedEvent:
		fire(hooks.OnReasoningStart, ReasoningStartCtx{ResponseID: r.responseID})

	case RetryEvent:
		fire(hooks.OnRetry, RetryCtx{Source: e.Source, Attempt: e.Attempt, MaxAttempts: e.MaxAttempts})

	case ErrorEvent:
		fire(hooks.OnServerError, ServerErrorCtx{
			Message: e.Error.Message, Source: e.Source, Code: e.Error.Code,
		})

	case OutputFileDoneEvent:
		filename := ""
		if e.Filename != nil {
			filename = *e.Filename
		}
		fire(hooks.OnFileOutput, FileOutputCtx{FileID: e.FileID, Filename: filename})

	case ElicitationRequestEvent:
		r.resolveElicitation(ctx, e)

	case OutputItemDoneEvent:
		r.observeItem(ctx, e)

	case ResponseCompletedEvent:
		r.endResponse(e.Response, nil)
	case ResponseCancelledEvent:
		r.endResponse(e.Response, fmt.Errorf("%w: the turn was cancelled", ErrTurnFailed))
	case IncompleteEvent:
		r.endResponse(e.Response, fmt.Errorf("%w: the response was incomplete", ErrTurnFailed))
	case ResponseFailedEvent:
		r.endResponse(e.Response, fmt.Errorf("%w: %s", ErrTurnFailed, responseFailure(e.Response)))
	}
}

func (r *turnRun) endResponse(response ResponseObject, detail error) {
	fire(r.chat.opts.Hooks.OnResponseEnd, ResponseEndCtx{ResponseID: response.ID, Status: response.Status})
	r.tracker.observeResponseTerminal(response.ID, detail)
	// A terminal response that did not end the turn was a tool-loop iteration.
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
	callID := itemString(e.Item, "call_id")
	name := itemString(e.Item, "name")
	if callID == "" || name == "" {
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
	fire(r.chat.opts.Hooks.OnToolCallStart, ToolCallStartCtx{
		Name: info.Name, CallID: info.CallID, AgentName: info.AgentName,
		Arguments: info.Arguments, ExecutedBy: executedBy,
	})
	if !awaitingClient || r.dispatched[callID] {
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
	fire(r.chat.opts.Hooks.OnToolCallEnd, ToolCallEndCtx{
		Name: info.Name, CallID: info.CallID, Output: output, Err: runErr,
	})

	if _, err := r.chat.client.Sessions().PostEvent(ctx, r.chat.sessionID, SessionEventInput{
		Type: InputTypeFunctionCallOutput,
		Data: map[string]any{"call_id": info.CallID, "output": output},
	}); err != nil {
		r.emit(nil, fmt.Errorf("post output for tool %q: %w",
			sanitizeForError(info.Name, maxToolNameRunes), err))
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
		if recover() != nil {
			accept = false
		}
	}()
	return hook(elicitationCtxOf(r.chat.sessionID, e))
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

func elicitationCtxOf(fallbackSession string, e ElicitationRequestEvent) ElicitationCtx {
	ctx := ElicitationCtx{
		SessionID:     elicitationTarget(fallbackSession, e),
		ElicitationID: e.ElicitationID,
		Message:       e.Params.Message,
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

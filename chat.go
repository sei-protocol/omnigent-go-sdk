package omnigent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"
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
			yield(nil, fmt.Errorf("%w", ErrTurnAlreadyRead))
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
// The order is what makes the read complete. The subscription has to exist before
// the prompt is posted, or the turn can be answered with nobody listening and its
// events are simply missed. [Client.Stream] returns a lazy sequence, so the
// subscription opens when this loop starts reading — which is why the prompt is
// posted from inside the loop rather than before it.
//
// The first event is the signal that the subscription is live: the server emits a
// heartbeat once the relay has registered the reader. A server that emits none
// would leave the prompt unsent, so a short wait bounds that.
func (c *Chat) drive(ctx context.Context, text string, yield func(Event, error) bool) {
	tracker := newTurnTracker(c.opts.Turn)

	ready := make(chan struct{})
	anchors := make(chan anchorResult, 1)
	var readyOnce sync.Once
	markReady := func() { readyOnce.Do(func() { close(ready) }) }

	// Posts once the subscription is live, so the loop below keeps reading while
	// the post is in flight.
	go func() {
		select {
		case <-ready:
		case <-time.After(streamReadyTimeout):
			// An older server sends no heartbeat. Posting anyway beats hanging, and
			// the window this opens is the one upstream accepts for the same reason.
		case <-ctx.Done():
			anchors <- anchorResult{err: ctx.Err()}
			return
		}
		accepted, err := c.client.Sessions().SendMessage(ctx, c.sessionID, text)
		anchors <- anchorResult{anchor: anchorOf(accepted), err: err}
	}()

	anchored := false
	// takeAnchor picks up the post's result. Blocking is safe only once an echo has
	// arrived, because an echo cannot precede the post that produced it.
	takeAnchor := func(wait bool) bool {
		if anchored {
			return true
		}
		var result anchorResult
		if wait {
			select {
			case result = <-anchors:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return false
			}
		} else {
			select {
			case result = <-anchors:
			default:
				return true
			}
		}
		anchored = true
		if result.err != nil {
			yield(nil, fmt.Errorf("send prompt: %w", result.err))
			return false
		}
		tracker.anchorOn(result.anchor)
		return true
	}

	for event, err := range c.client.Stream(ctx, c.sessionID, c.opts.Stream) {
		markReady()
		if err != nil {
			yield(nil, err)
			return
		}
		// An echo means the post has returned, so its anchor is available now and
		// the boundary check below can rely on it.
		_, isEcho := event.(SessionInputConsumedEvent)
		if !takeAnchor(isEcho) {
			return
		}
		c.observe(ctx, event, tracker, yield)
		if !yield(event, nil) {
			return
		}
		if tracker.ended() {
			if tracker.failure != nil {
				yield(nil, tracker.failure)
			}
			return
		}
	}
	// Reported rather than treated as an end: a caller reading the sequence as
	// complete would take a partial answer for the whole one.
	if !tracker.ended() {
		yield(nil, fmt.Errorf("%w", ErrTurnIncomplete))
	}
}

// anchorResult carries the posted prompt's identifier, or why it was not posted.
type anchorResult struct {
	anchor string
	err    error
}

// streamReadyTimeout bounds the wait for the subscription's first event before the
// prompt is posted anyway.
//
// The server emits a heartbeat once the relay registers a reader, so this fires
// only against a server that does not. Short, because the cost of waiting is added
// to every turn while the cost of posting early is a race that the boundary check
// already survives.
const streamReadyTimeout = time.Second

// observe advances the tracker and runs the side effects one event calls for.
//
// Ordered deliberately: the tool call and the approval are answered before the
// turn's end is read, because the server parks the turn on both and the terminal
// event only follows once they are answered.
func (c *Chat) observe(ctx context.Context, event Event, tracker *turnTracker, yield func(Event, error) bool) {
	switch e := event.(type) {
	case SessionInputConsumedEvent:
		tracker.crossBoundary(e)

	case SessionStatusEvent:
		tracker.observeStatus(e)

	case SessionSupersededEvent:
		tracker.observeSuperseded(e)

	case ResponseCreatedEvent:
		fire(c.opts.Hooks.OnResponseStart, ResponseStartCtx{ResponseID: e.Response.ID, Model: e.Response.Model})

	case ReasoningStartedEvent:
		fire(c.opts.Hooks.OnReasoningStart, ReasoningStartCtx{})

	case RetryEvent:
		fire(c.opts.Hooks.OnRetry, RetryCtx{Source: e.Source, Attempt: e.Attempt, MaxAttempts: e.MaxAttempts})

	case ErrorEvent:
		fire(c.opts.Hooks.OnServerError, ServerErrorCtx{
			Message: e.Error.Message, Source: e.Source, Code: e.Error.Code,
		})

	case OutputFileDoneEvent:
		filename := ""
		if e.Filename != nil {
			filename = *e.Filename
		}
		fire(c.opts.Hooks.OnFileOutput, FileOutputCtx{FileID: e.FileID, Filename: filename})

	case ElicitationRequestEvent:
		c.resolveElicitation(ctx, e, yield)

	case OutputItemDoneEvent:
		c.dispatchToolCall(ctx, e, yield)

	case ResponseCompletedEvent:
		c.endResponse(e.Response, nil, tracker)
	case ResponseCancelledEvent:
		c.endResponse(e.Response, fmt.Errorf("%w: the turn was cancelled", ErrTurnFailed), tracker)
	case IncompleteEvent:
		c.endResponse(e.Response, fmt.Errorf("%w: the response was incomplete", ErrTurnFailed), tracker)
	case ResponseFailedEvent:
		c.endResponse(e.Response, fmt.Errorf("%w: %s", ErrTurnFailed, responseFailure(e.Response)), tracker)
	}
}

func (c *Chat) endResponse(response ResponseObject, detail error, tracker *turnTracker) {
	fire(c.opts.Hooks.OnResponseEnd, ResponseEndCtx{ResponseID: response.ID, Status: response.Status})
	tracker.observeResponseTerminal(response.ID, detail)
}

// dispatchToolCall runs a client tool the agent called and posts its output.
//
// Only an item awaiting the client is dispatched. A server-executed call arrives
// on the same route already finished, and running it again would repeat whatever
// it did.
func (c *Chat) dispatchToolCall(ctx context.Context, e OutputItemDoneEvent, yield func(Event, error) bool) {
	if itemString(e.Item, "type") != "function_call" {
		return
	}
	if itemString(e.Item, "status") != itemStatusActionRequired {
		return
	}
	callID := itemString(e.Item, "call_id")
	name := itemString(e.Item, "name")
	if callID == "" || name == "" {
		return
	}

	info := ToolCallInfo{
		Name:      name,
		Arguments: itemArguments(e.Item),
		CallID:    callID,
		AgentName: itemString(e.Item, "agent_name"),
	}
	fire(c.opts.Hooks.OnToolCallStart, ToolCallStartCtx{
		Name: info.Name, CallID: info.CallID, AgentName: info.AgentName,
		Arguments: info.Arguments, ExecutedBy: "client",
	})

	output, runErr := c.opts.Tools.run(ctx, info)
	fire(c.opts.Hooks.OnToolCallEnd, ToolCallEndCtx{
		Name: info.Name, CallID: info.CallID, Output: output, Err: runErr,
	})

	// Posted whatever happened, because the turn is parked on this call. The
	// caller hears about the failure separately.
	if _, err := c.client.Sessions().PostEvent(ctx, c.sessionID, SessionEventInput{
		Type: InputTypeFunctionCallOutput,
		Data: map[string]any{"call_id": callID, "output": output},
	}); err != nil {
		yield(nil, fmt.Errorf("post output for tool %q: %w", name, err))
		return
	}
	if runErr != nil && !errors.Is(runErr, ErrToolNotRegistered) {
		yield(nil, runErr)
	}
}

// resolveElicitation answers an approval the server is waiting on.
//
// Declined when no hook decides, which is this package's own behaviour rather than
// a policy: approving would run a tool under the caller's identity, and a client
// that cannot ask cannot consent on their behalf.
//
// Answered against the session the request names. A sub-agent's request names its
// own session, and a verdict posted to the session being read would leave the
// asking one parked.
func (c *Chat) resolveElicitation(ctx context.Context, e ElicitationRequestEvent, yield func(Event, error) bool) {
	verdict := ElicitationDecline
	if c.opts.Hooks.OnElicitation != nil && c.opts.Hooks.OnElicitation(elicitationCtxOf(c.sessionID, e)) {
		verdict = ElicitationAccept
	}
	target := elicitationTarget(c.sessionID, e)
	if err := c.client.Sessions().ResolveElicitation(ctx, target, e.ElicitationID,
		ElicitationResult{Action: verdict}); err != nil {
		yield(nil, fmt.Errorf("resolve elicitation %s: %w", e.ElicitationID, err))
	}
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

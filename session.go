package omnigent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Discriminators for [SessionEventInput.Type]. The first names an item the
// session records; the rest are control signals that queue no item.
const (
	// InputTypeMessage is a conversation turn. Its Data is a role plus content
	// parts; build one with [UserMessage].
	InputTypeMessage = "message"

	// InputTypeFunctionCallOutput returns the result of a tool the client ran.
	// Its Data carries call_id and output.
	InputTypeFunctionCallOutput = "function_call_output"

	// InputTypeInterrupt cancels the turn in flight. This — not cancelling a
	// stream's context — is how a caller stops an agent.
	InputTypeInterrupt = "interrupt"

	// InputTypeStopSession halts the session. Owner-level access only.
	InputTypeStopSession = "stop_session"

	// InputTypeCompact asks the server to compact the conversation history.
	InputTypeCompact = "compact"

	// InputTypeApproval answers an outstanding elicitation. Its Data is an
	// elicitation_id plus the [ElicitationResult] fields; build one with
	// [ApprovalVerdict], or send it with [Client.ResolveElicitation].
	InputTypeApproval = "approval"
)

// SessionEventInput is one client-submitted input for a session: the body of a
// send, and the element type of [SessionCreateRequest.InitialItems].
//
// Hand-written, and unusually exposed: its route is registered with
// include_in_schema=False, so neither the path nor this schema appears in
// openapi.json and no drift gate covers either. It mirrors the server's
// SessionEventInput. A server-side change to it breaks this client silently.
type SessionEventInput struct {
	// Type is the input's discriminator; see the InputType constants. Required.
	Type string `json:"type"`

	// Data is the type-specific payload. The server validates its shape against
	// Type and rejects a mismatch with 422. Control inputs take an empty Data.
	Data map[string]any `json:"data,omitempty"`

	// ModelOverride substitutes for the agent spec's model on the turn this
	// input starts. Ignored when the input steers an already-running turn.
	ModelOverride string `json:"model_override,omitempty"`

	// Tools registers function tools for the turn this input starts, in
	// OpenAI's function-tool shape. Ignored when steering a running turn, whose
	// tool set is fixed at start.
	Tools []map[string]any `json:"tools,omitempty"`

	// The server's created_by is deliberately absent: it is reserved for
	// runner-originated events and a client that sets it is rejected with 403.
}

// UserMessage builds a user-role message input carrying one text part.
func UserMessage(text string) SessionEventInput {
	return SessionEventInput{
		Type: InputTypeMessage,
		Data: map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		},
	}
}

// ElicitationAction is a verdict on an outstanding elicitation. The values
// mirror the Model Context Protocol's ElicitResult.action.
type ElicitationAction string

const (
	// ElicitationAccept approves: the confirmation was given, or the form was
	// submitted. Accepting authorises the pending tool to run with the session
	// owner's execution identity, so the server requires approval access for it
	// specifically and answers [ErrForbidden] to a caller that only has edit
	// access. Declining and cancelling need only edit access, so a caller able
	// to stop an action is not necessarily able to permit one.
	ElicitationAccept ElicitationAction = "accept"

	// ElicitationDecline refuses explicitly.
	ElicitationDecline ElicitationAction = "decline"

	// ElicitationCancel dismisses without choosing. This is also the verdict the
	// server synthesises when an elicitation times out, so receiving it does not
	// prove a client sent it.
	ElicitationCancel ElicitationAction = "cancel"
)

// ElicitationResult is a verdict on one elicitation: the payload half of an
// [InputTypeApproval] input.
//
// Hand-written for the same reason as [SessionEventInput] and [EventAccepted]:
// it travels on that same include_in_schema=False route, so openapi.json carries
// no schema for it and no drift gate covers it. Its field names and semantics
// mirror MCP's ElicitResult verbatim, which is deliberate on the server's part
// so an MCP client can bridge without translating.
type ElicitationResult struct {
	// Action is the verdict. Required.
	Action ElicitationAction `json:"action"`

	// Content carries form values when Action is [ElicitationAccept] and the
	// request asked for a schema. Nil for a plain approve/reject, and for
	// decline and cancel.
	//
	// MCP restricts the values to JSON scalars and lists of strings, so a nested
	// object or a list of anything else is rejected. The Go type is wider than
	// the wire contract because map[string]any is what encoding/json gives a
	// caller building one; the server is the enforcement point.
	Content map[string]any `json:"content,omitempty"`
}

// ApprovalVerdict builds the input that answers an outstanding elicitation.
//
// The elicitation id comes from the [ElicitationRequestEvent] that asked, or
// from [SessionResponse.PendingElicitations] after a reconnect. It correlates
// the answer with the question, which is why it rides beside the verdict rather
// than inside it: [ElicitationResult] is MCP's shape, and the correlation key
// is this API's.
//
// Either source also names the session that owns the elicitation, when that is
// not the session it was read from: [ElicitationRequestParams.TargetSessionID]
// on the event, and params.target_session_id inside the snapshot's entries,
// which are those same events as raw dicts. Posting the verdict to the wrong one
// of the two is what [Client.ResolveElicitationRequest] exists to prevent.
//
// This builder validates nothing — an empty id or action is assembled as given
// and refused by the server. [Client.ResolveElicitation] checks both before
// sending, so prefer it unless you are batching inputs yourself.
func ApprovalVerdict(elicitationID string, result ElicitationResult) SessionEventInput {
	data := map[string]any{
		"elicitation_id": elicitationID,
		"action":         string(result.Action),
	}
	if result.Content != nil {
		data["content"] = result.Content
	}
	return SessionEventInput{Type: InputTypeApproval, Data: data}
}

// SessionCreateRequest is the JSON body of session create.
//
// Hand-written: the create route takes a raw request and dispatches on
// Content-Type, so FastAPI documents no requestBody for it and openapi.json
// carries no schema. It mirrors the server's SessionCreateRequest and is not
// covered by the openapi.json drift gate.
//
// This is the JSON create path, which binds an already-registered agent and
// returns the full session snapshot. The same endpoint also accepts a multipart
// bundled create that uploads an agent — a different contract returning a
// different, smaller body — which this package does not implement.
type SessionCreateRequest struct {
	// AgentID is the durable identifier of the agent to bind, e.g. "ag_abc123".
	// Required, and matched by ID rather than by name.
	AgentID string `json:"agent_id"`

	// InitialItems seeds the session's input queue, typically with a single
	// user message. Seeding the first turn here avoids a follow-up send — and
	// with it the race against opening the stream.
	InitialItems []SessionEventInput `json:"initial_items,omitempty"`

	// Title is a human-readable session title.
	Title string `json:"title,omitempty"`

	// Labels are initial guardrail labels for the session.
	Labels map[string]string `json:"labels,omitempty"`

	// ParentSessionID makes this a sub-agent session of that parent, inheriting
	// its runner affinity.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// SubAgentName selects a sub-agent type within the parent's spec tree.
	SubAgentName string `json:"sub_agent_name,omitempty"`

	// HostType is "external" (the default: the caller manages the host) or
	// "managed" (the server provisions a sandbox, in the background — HostID
	// and Workspace must be empty, and appear on the snapshot once it
	// registers).
	HostType string `json:"host_type,omitempty"`

	// HostID launches the runner on a registered host. Requires Workspace.
	HostID string `json:"host_id,omitempty"`

	// Workspace is where the session works: an absolute path on the host when
	// HostID is set, or for a managed host optionally a repository URL to clone.
	// Tilde and relative paths are rejected server-side.
	Workspace string `json:"workspace,omitempty"`

	// Git asks the server to run the session in a git worktree it creates on the
	// host, with Workspace read as the source repository. Requires HostID.
	Git *SessionGitOptions `json:"git,omitempty"`

	// TerminalLaunchArgs are pass-through CLI arguments for a native terminal
	// harness. Count and length are bounded server-side.
	TerminalLaunchArgs []string `json:"terminal_launch_args,omitempty"`

	// ModelOverride persists a per-session model override at create time, so it
	// is on the session row before the harness launches.
	ModelOverride string `json:"model_override,omitempty"`

	// ReasoningEffort persists a per-session reasoning-effort override.
	// Provider support is enforced later, at launch.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// CostControlModeOverride is "on" or "off"; empty defers to the spec.
	CostControlModeOverride string `json:"cost_control_mode_override,omitempty"`

	// HarnessOverride selects a harness other than the agent spec's. Create-time
	// only — the harness process spawns on the first turn.
	HarnessOverride string `json:"harness_override,omitempty"`
}

// EventAccepted is the acknowledgement a send returns. The server accepts input
// asynchronously: the turn has been queued, not run.
//
// Hand-written for the same reason as [SessionEventInput]: it is the response
// body of the same include_in_schema=False route, so openapi.json documents
// neither and the drift gate covers neither.
type EventAccepted struct {
	// Queued is true when the input became a conversation item, false for a
	// control input that queues nothing.
	Queued bool `json:"queued"`

	// ItemID is the queued item's identifier, present when Queued. It is the
	// same id that comes back on the stream as [SessionInputConsumedEvent],
	// which makes it the correlation key between a send and its echo.
	ItemID string `json:"item_id,omitempty"`

	// PendingID identifies a native-terminal message's optimistic placeholder,
	// which the consume event later clears. Usually empty.
	PendingID string `json:"pending_id,omitempty"`

	// Denied reports that the server handled this input synchronously by
	// refusing it, rather than queueing a turn for it. It is the difference
	// between the two reasons Queued can be false: a control input queues
	// nothing by design, while a denied one was rejected and says why in
	// [EventAccepted.Reason].
	Denied bool `json:"denied,omitempty"`

	// Reason is the server's explanation for a denial, e.g.
	// "Denied by policy". Empty unless Denied.
	//
	// Worth reading rather than discarding: without it a refused send is
	// indistinguishable from a control input, and a caller that treats every
	// unqueued send the same way reports "the server accepted this without
	// queueing it" when the server had already said exactly what was wrong.
	Reason string `json:"reason,omitempty"`
}

// GetSessionOptions tunes what a snapshot includes. A nil field lets the server
// choose; [Ptr] makes one. The zero value asks for the server's defaults, which
// is what reconciling after a dropped stream wants.
type GetSessionOptions struct {
	// IncludeItems, when false, skips the committed-items read and returns no
	// items. It is the most expensive part of building a snapshot, so set it
	// false when hydrating the transcript separately.
	//
	// Leaving it alone does not fetch the whole transcript. The server reads the
	// newest 100 items and returns them chronologically, and nothing on the
	// response separates a complete transcript from a truncated one, so a
	// session that keeps working past 100 items silently loses its oldest.
	// [Client.ListSessionItems] is the paged read that reaches them.
	IncludeItems *bool

	// IncludeLiveness, when false, skips the runner and host liveness lookup.
	IncludeLiveness *bool

	// RefreshState asks the server to re-derive the session's state before
	// answering rather than serving what it has cached.
	RefreshState *bool
}

func (o GetSessionOptions) query() url.Values {
	query := url.Values{}
	for name, value := range map[string]*bool{
		"include_items":    o.IncludeItems,
		"include_liveness": o.IncludeLiveness,
		"refresh_state":    o.RefreshState,
	} {
		if value != nil {
			query.Set(name, strconv.FormatBool(*value))
		}
	}
	return query
}

// DeleteSessionOptions tunes a delete. The zero value leaves any git worktree
// alone.
type DeleteSessionOptions struct {
	// DeleteBranch also removes the server-created git worktree and its branch.
	// Ignored for sessions without one, and best-effort: a cleanup failure does
	// not fail the delete.
	DeleteBranch bool
}

// Ptr returns a pointer to v, for the optional fields on request options where
// nil means "let the server decide".
func Ptr[T any](v T) *T { return &v }

// CreateSession creates a session bound to an existing agent and returns its
// snapshot.
func (c *Client) CreateSession(ctx context.Context, req SessionCreateRequest) (*SessionResponse, error) {
	if req.AgentID == "" {
		return nil, fmt.Errorf("create session: %w: AgentID is required", ErrInvalidArgument)
	}
	var session SessionResponse
	if err := c.doJSON(ctx, http.MethodPost, []string{"v1", "sessions"}, nil, req, &session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &session, nil
}

// GetSession returns a session's snapshot: identity, status, and committed
// items.
//
// This is the reconciliation half of the stream contract. Because the stream
// replays nothing, recovering from a drop means calling this, then opening a
// fresh stream, then deduping persisted items by id — sound because the server
// persists an item before it publishes it.
//
// What that recovers is bounded by the snapshot's item window: the newest 100,
// per [GetSessionOptions.IncludeItems]. A drop that outlasts 100 items, or a
// session already past them, needs [Client.ListSessionItems] to read the rest.
func (c *Client) GetSession(
	ctx context.Context,
	sessionID string,
	opts GetSessionOptions,
) (*SessionResponse, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("get session: %w: sessionID is required", ErrInvalidArgument)
	}
	var session SessionResponse
	segments := []string{"v1", "sessions", sessionID}
	if err := c.doJSON(ctx, http.MethodGet, segments, opts.query(), nil, &session); err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	return &session, nil
}

// DeleteSession deletes a session and the resources bound to it.
//
// It requires owner-level access, so it can fail with [ErrForbidden] on a
// session that [Client.GetSession] happily returns.
func (c *Client) DeleteSession(
	ctx context.Context,
	sessionID string,
	opts DeleteSessionOptions,
) (*ConversationDeleted, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("delete session: %w: sessionID is required", ErrInvalidArgument)
	}
	query := url.Values{}
	if opts.DeleteBranch {
		query.Set("delete_branch", "true")
	}
	var deleted ConversationDeleted
	segments := []string{"v1", "sessions", sessionID}
	if err := c.doJSON(ctx, http.MethodDelete, segments, query, nil, &deleted); err != nil {
		return nil, fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	return &deleted, nil
}

// SendInput posts one input to a session and returns the server's
// acknowledgement.
//
// The call returns as soon as the input is queued; everything the agent does
// with it arrives on the stream. Send only once the subscription is live, or the
// turn's first events are published to nobody and lost — the server buffers
// nothing. There are two ways to get that right, and watching for a
// [SessionHeartbeatEvent] is not one of them: the server sends that same event
// as its idle keepalive too, so a caller that sends on every heartbeat sends
// forever. Use [StreamOptions.OnSubscribed], which fires exactly once, or seed
// the first turn with [SessionCreateRequest.InitialItems] and skip the race.
//
// A nil error means the server accepted the input. A redirect cannot make that
// untrue: 301, 302 and 303 rewrite a POST as a GET, which would drop the body and
// then decode the GET's 200 into an acknowledgement for a turn nobody queued, so
// a method-rewriting redirect fails with [ErrUnsafeRedirect] instead.
func (c *Client) SendInput(
	ctx context.Context,
	sessionID string,
	input SessionEventInput,
) (*EventAccepted, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("send input: %w: sessionID is required", ErrInvalidArgument)
	}
	if input.Type == "" {
		return nil, fmt.Errorf("send input: %w: Type is required", ErrInvalidArgument)
	}
	var accepted EventAccepted
	segments := []string{"v1", "sessions", sessionID, "events"}
	if err := c.doJSON(ctx, http.MethodPost, segments, nil, input, &accepted); err != nil {
		return nil, fmt.Errorf("send %s to session %s: %w", input.Type, sessionID, err)
	}
	return &accepted, nil
}

// SendMessage posts a user message to a session. It is [Client.SendInput] over
// [UserMessage].
func (c *Client) SendMessage(ctx context.Context, sessionID, text string) (*EventAccepted, error) {
	return c.SendInput(ctx, sessionID, UserMessage(text))
}

// ResolveElicitation answers an outstanding elicitation on sessionID. It is
// [Client.SendInput] over [ApprovalVerdict].
//
// An agent that needs a decision before proceeding parks its turn and publishes
// an [ElicitationRequestEvent]. Nothing advances until a verdict arrives, so an
// unattended program that streams events has to answer these or its session
// stalls until the server times the elicitation out and synthesises
// [ElicitationCancel].
//
// sessionID must be the session that owns the elicitation, which is not always
// the session the prompt arrived on: a sub-agent's prompt is mirrored into its
// ancestors' streams, and [ElicitationRequestParams.TargetSessionID] then names
// the owner. The server sets a parked harness Future only for the owner, so a
// mirrored prompt answered on the stream it was read from is accepted — 202, no
// error — and can resolve nothing, leaving the sub-agent parked until the
// elicitation times out. "Can" rather than "does": the verdict is also forwarded
// to the runner bound to the session it was posted to, and a runner matches its
// own parked approvals on the elicitation id alone, so a runner-side policy
// prompt on that runner resolves anyway. That mixture is what makes getting this
// wrong quiet. [Client.ResolveElicitationRequest] routes by the event instead.
//
// The server also exposes a dedicated resolve route, which this package does not
// call. That route is registered include_in_schema=False for an internal flow
// where the prompt carries the route's own URL for a client to hit directly; its
// own documentation names this event as the equivalent path with identical
// resolution semantics, and both do reach the same server-side resolver. Sending
// the verdict as an input keeps the SDK on one write route instead of two.
func (c *Client) ResolveElicitation(
	ctx context.Context,
	sessionID, elicitationID string,
	result ElicitationResult,
) (*EventAccepted, error) {
	if elicitationID == "" {
		return nil, fmt.Errorf("resolve elicitation: %w: elicitationID is required", ErrInvalidArgument)
	}
	if result.Action == "" {
		return nil, fmt.Errorf("resolve elicitation: %w: Action is required", ErrInvalidArgument)
	}
	return c.SendInput(ctx, sessionID, ApprovalVerdict(elicitationID, result))
}

// ResolveElicitationRequest answers the elicitation one
// [ElicitationRequestEvent] asked for, posting to the session that owns it:
// [ElicitationRequestParams.TargetSessionID] when the prompt was mirrored from a
// sub-agent, and sessionID — the stream the event was read from — otherwise.
//
// This is the call to reach for when the verdict answers an event off a stream,
// because that is where the reader and the owner diverge; see
// [Client.ResolveElicitation] for what posting to the reader instead costs. A
// caller that resolves the target itself is already doing this and gains nothing
// by switching.
func (c *Client) ResolveElicitationRequest(
	ctx context.Context,
	sessionID string,
	request ElicitationRequestEvent,
	result ElicitationResult,
) (*EventAccepted, error) {
	target := sessionID
	if id := request.Params.TargetSessionID; id != nil && *id != "" {
		target = *id
	}
	return c.ResolveElicitation(ctx, target, request.ElicitationID, result)
}

// Interrupt cancels the turn in flight.
//
// This is real cancellation, unlike cancelling a stream's context: the turn runs
// server-side and does not care whether anyone is subscribed. The stream then
// carries a [SessionInterruptedEvent] and an [IncompleteEvent] whose reason
// records the interrupt.
func (c *Client) Interrupt(ctx context.Context, sessionID string) error {
	_, err := c.SendInput(ctx, sessionID, SessionEventInput{Type: InputTypeInterrupt})
	return err
}

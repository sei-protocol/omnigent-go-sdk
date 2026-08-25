package omnigent

import (
	"net/url"
	"strconv"
)

// Every type in this file reaches a route the vendored description does not
// declare, so no conformance test covers it in either direction. doc.go, under
// "Hand-authored types", names each route and what that costs.

// Discriminators for [SessionEventInput.Type]. The first names an item the
// session records; the rest are control signals that queue no item.
const (
	// InputTypeMessage is a conversation turn. Its Data is a role plus content
	// parts. [Sessions.SendMessage] builds and posts one for the plain-text case;
	// anything richer goes through [Sessions.PostEvent] directly.
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
	// elicitation_id plus the [ElicitationResult] fields.
	// [Sessions.ResolveElicitation] is the route that takes them as arguments, and
	// is what a caller answering an elicitation wants.
	InputTypeApproval = "approval"
)

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

// ElicitationAction is a verdict on an outstanding elicitation. The values
// mirror the Model Context Protocol's ElicitResult.action.
type ElicitationAction string

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

// userMessage builds the input for one text prompt.
//
// The body is a role and a list of content blocks, and one prompt is a single
// input_text block. input_text, not output_text: the block a caller sends and the
// block that comes back are different types, and [MessageData.Content] on a reply
// carries the latter.
//
// Empty text sends no block at all, which is what upstream's client does with it.
//
// A function rather than a literal inside [Sessions.SendMessage] so the shape has
// one home. Writing it inline is how it came to be wrong: nothing else in this
// module states it, so there was nothing to check against.
//
// Unexported until something outside needs it. [SessionCreateRequest.InitialItems]
// takes these directly and no caller seeds one yet, so exporting now would publish
// a symbol permanently to serve nobody. Promote it when the first one appears.
func userMessage(text string) SessionEventInput {
	content := []map[string]any{}
	if text != "" {
		content = append(content, map[string]any{"type": "input_text", "text": text})
	}
	return SessionEventInput{
		Type: InputTypeMessage,
		Data: map[string]any{
			"role":    MessageDataRoleUser,
			"content": content,
		},
	}
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
	// [Sessions.ListItems] is the paged read that reaches them.
	IncludeItems *bool

	// IncludeLiveness, when false, skips the runner and host liveness lookup.
	IncludeLiveness *bool

	// RefreshState asks the server to re-derive the session's state before
	// answering rather than serving what it has cached.
	RefreshState *bool
}

// DeleteSessionOptions tunes a delete. The zero value leaves any git worktree
// alone.
type DeleteSessionOptions struct {
	// DeleteBranch also removes the server-created git worktree and its branch.
	// Ignored for sessions without one, and best-effort: a cleanup failure does
	// not fail the delete.
	DeleteBranch bool
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

// query renders the delete options as query parameters.
func (o DeleteSessionOptions) query() url.Values {
	query := url.Values{}
	if o.DeleteBranch {
		query.Set("delete_branch", "true")
	}
	return query
}

// RunnerInfo is one runner the server knows about.
//
// Hand-written: GET /v1/runners publishes a map of arrays of untyped objects, so
// nothing in the description pins these fields. A server-side rename breaks
// [Sessions.ResolveOnlineRunner] silently.
type RunnerInfo struct {
	// RunnerID identifies the runner, e.g. "runner_abc123".
	RunnerID string `json:"runner_id"`

	// Online reports whether the runner is reachable now.
	Online bool `json:"online"`

	// Harnesses are the harnesses this runner advertises. Nil when the runner
	// reported no list, which [Sessions.ResolveOnlineRunner] treats as a fallback
	// rather than a refusal.
	Harnesses []string `json:"harnesses"`
}

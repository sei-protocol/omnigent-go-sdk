package omnigent

import (
	"encoding/json"
	"fmt"
)

// Event is one decoded frame from a session's event stream.
//
// The interface is sealed: its only implementations are the event structs in this
// file, one per member of the server's discriminated union, plus [UnknownEvent].
// Consume it with a type switch:
//
//	switch ev := event.(type) {
//	case OutputTextDeltaEvent:
//		fmt.Print(ev.Delta)
//	case ResponseCompletedEvent:
//		return nil
//	}
//
// A minimal correct consumer needs relatively few of the variants. An in-process
// agent's turn opens at [InProgressEvent] and closes at exactly one of
// [ResponseCompletedEvent], [ResponseFailedEvent], [IncompleteEvent] or
// [ResponseCancelledEvent]. Assistant text arrives as [OutputTextDeltaEvent];
// finished items as [OutputItemDoneEvent]; session-level state as
// [SessionStatusEvent]; and the echo of accepted input as
// [SessionInputConsumedEvent]. The rest — elicitation and approval flows,
// compaction, files, and session-metadata nudges — can be ignored without loss
// for that scope. That response.* lifecycle is not the whole terminal story;
// see below.
//
// Five things about the stream shape are easy to get wrong:
//
// [ResponseCreatedEvent] never arrives on a live turn. The harness emits
// "response.created" and "response.in_progress" as an inseparable pair, and the
// server drops the created half at the publish chokepoint that feeds every
// subscriber — so a subscription sees the in_progress half alone and no created
// at all. That asymmetry is the design, not a dropped frame; take in_progress,
// never created, as an in-process turn's opening event.
//
// Every stream opens with a fixed prologue, on every connect. First a
// [SessionHeartbeatEvent], which is the subscription acknowledgement — but note
// that it is indistinguishable from the keepalive of the same name that the
// server emits every 15 seconds on an idle stream, so it is a position in the
// stream and not a payload that marks "ready". Do not act on it; act on
// [StreamOptions.OnSubscribed], which fires once. Then, if a turn is already in
// flight, a REPLAY of the assistant text so far — already-emitted content, which
// double-renders if a snapshot was also fetched. Its shape depends on the
// harness: a message-scoped one (claude-native) replays [OutputTextDeltaEvent]
// only, one per in-flight message, while a response-scoped in-process agent's
// replay is prefixed with a synthesized [ResponseCreatedEvent] carrying the
// turn's response object. That prologue is the only place the type is
// observable. Then a resource snapshot of session.* events. Only then does the
// live tail begin.
//
// Nothing that arrives in-stream ends the stream. [ErrorEvent] is
// non-terminal — the turn may still complete — and [RetryEvent] is purely
// informational. A turn ending is not a transport failure, and a transport
// failure says nothing about the turn, which keeps running server-side.
//
// A turn can end with no response.* event at all, and for some harnesses always
// does. Two cases, both reached through [SessionStatusEvent]:
//
// A setup-phase failure — resolving the agent spec, or building the spawn
// environment — kills the turn before the model stream opens, so no
// [ResponseFailedEvent] is ever emitted and the only terminal edge is a Status of
// "failed". It carries the failure in Error, which the server populates on that
// status and no other, so Error.Message is the only place the reason appears.
//
// A terminal-backed harness (claude-native) emits no response.completed at all;
// its turn boundaries are session.* only. The server reads a Status of "idle" or
// "failed" as "no turn is active", but neither half of that edge resolves a turn
// on its own. ResponseID names which turn an edge describes and is set on a
// running edge too, so a turn ends on a terminal Status that also carries one.
// A running edge carrying a ResponseID is not an end: with BlockedOn set it is
// parked, and that field names what on. Resolving on ResponseID alone reports a
// session parked at a permission prompt as a finished turn, and publishes
// whatever partial reply it had written by then.
//
// In both cases nothing about the transport goes quiet while a consumer waits
// for a terminal that is not coming: the keepalive [SessionHeartbeatEvent] holds
// off [ErrStreamIdle], so watching only the response.* terminals turns a
// fail-fast carrying the server's own message into a hang to the consumer's own
// deadline. An unattended consumer wants [SessionStatusEvent] in its terminal
// switch, and wants Error.Message out of it.
//
// SequenceNumber is not a stream cursor. It is nil on every session.* event and
// at best restarts from zero each turn on the others. Order by arrival.
//
// # Naming
//
// Each variant's doc states its wire type verbatim, and where two namespaces
// publish the same trailing name both are prefixed with theirs, so no bare name
// can stand for one of a pair. [ResponseHeartbeatEvent] is "response.heartbeat"
// and [SessionHeartbeatEvent] is "session.heartbeat"; likewise
// [ResponseCreatedEvent] against [SessionCreatedEvent], and
// [ResponseCompletedEvent], [ResponseFailedEvent] and [ResponseCancelledEvent]
// against their turn.* counterparts. The unprefixed name is nobody's: reaching
// for the wrong one of a pair now takes a deliberate act rather than a guess.
type Event interface {
	isEvent()
}

// UnknownEvent carries a frame whose discriminator this build does not know.
//
// It is not an error. The server's event schemas ignore unknown fields by
// contract so a new field cannot break an older parser, and this is the same
// guarantee one level up: a client built against an older openapi.json surfaces
// a newly added event type here and keeps streaming.
type UnknownEvent struct {
	// Type is the frame's discriminator, e.g. "session.something.new".
	Type string

	// Raw is the frame's JSON payload, owned by the caller.
	Raw []byte
}

func (UnknownEvent) isEvent() {}

// DecodeEvent decodes one frame into its typed variant.
//
// A frame whose discriminator this build does not know becomes an
// [UnknownEvent] rather than an error, so an older client keeps streaming
// against a newer server. A frame that carries a known discriminator but a
// malformed body is an error, because that is the server contradicting its own
// schema rather than extending it.
func DecodeEvent(raw []byte) (Event, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("omnigent: decode event envelope: %w", err)
	}
	if envelope.Type == "" {
		return nil, fmt.Errorf("omnigent: event carries no type discriminator")
	}
	return decodeByType(envelope.Type, raw)
}

func decodeInto[T Event](raw []byte) (Event, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// eventRegistry maps each wire discriminator to a decoder for its variant.
//
// It is a map rather than a switch because two things read it: [decodeByType],
// and the conformance test that walks every variant's fields against
// spec/openapi.json. A variant the decoder knows is therefore a variant the test
// checks, and adding one to a switch while forgetting the test is not possible.
var eventRegistry = map[string]func([]byte) (Event, error){
	"browser.action_request":                decodeInto[BrowserActionRequestEvent],
	"response.client_task.cancel":           decodeInto[ClientTaskCancelEvent],
	"response.compaction.completed":         decodeInto[CompactionCompletedEvent],
	"response.compaction.failed":            decodeInto[CompactionFailedEvent],
	"response.compaction.in_progress":       decodeInto[CompactionInProgressEvent],
	"response.elicitation_request":          decodeInto[ElicitationRequestEvent],
	"response.elicitation_resolved":         decodeInto[ElicitationResolvedEvent],
	"response.error":                        decodeInto[ErrorEvent],
	"response.in_progress":                  decodeInto[InProgressEvent],
	"response.incomplete":                   decodeInto[IncompleteEvent],
	"response.output_file.done":             decodeInto[OutputFileDoneEvent],
	"response.output_item.done":             decodeInto[OutputItemDoneEvent],
	"response.output_text.delta":            decodeInto[OutputTextDeltaEvent],
	"response.policy_denied":                decodeInto[PolicyDeniedEvent],
	"response.queued":                       decodeInto[QueuedEvent],
	"response.reasoning.started":            decodeInto[ReasoningStartedEvent],
	"response.reasoning_summary_text.delta": decodeInto[ReasoningSummaryTextDeltaEvent],
	"response.reasoning_text.delta":         decodeInto[ReasoningTextDeltaEvent],
	"response.cancelled":                    decodeInto[ResponseCancelledEvent],
	"response.completed":                    decodeInto[ResponseCompletedEvent],
	"response.created":                      decodeInto[ResponseCreatedEvent],
	"response.failed":                       decodeInto[ResponseFailedEvent],
	"response.heartbeat":                    decodeInto[ResponseHeartbeatEvent],
	"response.retry":                        decodeInto[RetryEvent],
	"session.agent_changed":                 decodeInto[SessionAgentChangedEvent],
	"session.changed_files.invalidated":     decodeInto[SessionChangedFilesInvalidatedEvent],
	"session.child_session.updated":         decodeInto[SessionChildSessionUpdatedEvent],
	"session.collaboration_mode":            decodeInto[SessionCollaborationModeEvent],
	"session.created":                       decodeInto[SessionCreatedEvent],
	"session.heartbeat":                     decodeInto[SessionHeartbeatEvent],
	"session.input.consumed":                decodeInto[SessionInputConsumedEvent],
	"session.interrupted":                   decodeInto[SessionInterruptedEvent],
	"session.mcp_startup":                   decodeInto[SessionMcpStartupEvent],
	"session.model":                         decodeInto[SessionModelEvent],
	"session.model_options":                 decodeInto[SessionModelOptionsEvent],
	"session.presence":                      decodeInto[SessionPresenceEvent],
	"session.reasoning_effort":              decodeInto[SessionReasoningEffortEvent],
	"session.resource.created":              decodeInto[SessionResourceCreatedEvent],
	"session.resource.deleted":              decodeInto[SessionResourceDeletedEvent],
	"session.sandbox_status":                decodeInto[SessionSandboxStatusEvent],
	"session.skills":                        decodeInto[SessionSkillsEvent],
	"session.status":                        decodeInto[SessionStatusEvent],
	"session.superseded":                    decodeInto[SessionSupersededEvent],
	"session.terminal.activity":             decodeInto[SessionTerminalActivityEvent],
	"session.terminal_pending":              decodeInto[SessionTerminalPendingEvent],
	"session.todos":                         decodeInto[SessionTodosEvent],
	"session.usage":                         decodeInto[SessionUsageEvent],
	"response.function_call_output.delta":   decodeInto[ToolOutputDeltaEvent],
	"turn.cancelled":                        decodeInto[TurnCancelledEvent],
	"turn.completed":                        decodeInto[TurnCompletedEvent],
	"turn.failed":                           decodeInto[TurnFailedEvent],
	"turn.started":                          decodeInto[TurnStartedEvent],
}

func decodeByType(wire string, raw []byte) (Event, error) {
	decode, known := eventRegistry[wire]
	if !known {
		return UnknownEvent{Type: wire, Raw: raw}, nil
	}
	ev, err := decode(raw)
	if err != nil {
		// Name the wire type, not the Go type: a frame is what an operator has
		// in front of them, and the discriminator is how they find it.
		return nil, fmt.Errorf("omnigent: decode %s event: %w", wire, err)
	}
	return ev, nil
}

// BrowserActionRequestEvent is the wire event "browser.action_request".
//
// Request that the desktop renderer perform one browser action.
type BrowserActionRequestEvent struct {
	// The browser action to perform — the browser_ tool name with the prefix stripped, e.g.
	// "navigate", "snapshot", "click", "type", "screenshot".
	Action string `json:"action"`

	// Unique correlation id for this request, e.g. "baction_abc123". Echoed on the claim and
	// result routes.
	ActionID string `json:"action_id"`

	// Action arguments forwarded from the tool call, e.g. {"url": "https://example.com"}.
	Args           map[string]any `json:"args"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "browser.action_request".
	Type string `json:"type"`
}

func (BrowserActionRequestEvent) isEvent() {}

// ClientTaskCancelEvent is the wire event "response.client_task.cancel".
//
// Server-side request that the client cancel a tunneled tool call.
type ClientTaskCancelEvent struct {
	// Synthetic call_id the SDK uses to reconcile the local task; None when no pending tool
	// call row exists for the task.
	CallID         *string `json:"call_id,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// Identifier of the client-side task being cancelled, e.g. "resp_async_abc".
	TaskID string `json:"task_id"`

	// Type is always "response.client_task.cancel".
	Type string `json:"type"`
}

func (ClientTaskCancelEvent) isEvent() {}

// CompactionCompletedEvent is the wire event "response.compaction.completed".
//
// Conversation history compaction has finished.
type CompactionCompletedEvent struct {
	CompactedMessages []map[string]any `json:"compacted_messages,omitempty"`
	SequenceNumber    *int             `json:"sequence_number,omitempty"`

	// Text summary of the compacted conversation, or None for server-side compaction (already
	// persisted).
	Summary *string `json:"summary,omitempty"`

	// Model used for summarization, or None if truncation-based or server-side.
	SummaryModel *string `json:"summary_model,omitempty"`

	// Tiktoken estimate of the post-compaction message context size, e.g. 8421. Used by
	// clients to update the context-ring immediately without waiting for the next
	// response.completed usage report. None when token counting is unavailable.
	TotalTokens *int `json:"total_tokens,omitempty"`

	// Type is always "response.compaction.completed".
	Type string `json:"type"`
}

func (CompactionCompletedEvent) isEvent() {}

// CompactionFailedEvent is the wire event "response.compaction.failed".
//
// Conversation history compaction failed.
type CompactionFailedEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Type is always "response.compaction.failed".
	Type string `json:"type"`
}

func (CompactionFailedEvent) isEvent() {}

// CompactionInProgressEvent is the wire event "response.compaction.in_progress".
//
// Conversation history is being compacted.
type CompactionInProgressEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Type is always "response.compaction.in_progress".
	Type string `json:"type"`
}

func (CompactionInProgressEvent) isEvent() {}

// ElicitationRequestEvent is the wire event "response.elicitation_request".
//
// Synchronous request for a decision from upstream.
type ElicitationRequestEvent struct {
	// Unique correlation id for this request — appears in the consumer's approval event
	// payload, e.g. "elicit_abc123".
	ElicitationID string `json:"elicitation_id"`

	// MCP method literal — always "elicitation/create" (the value of _MCP_ELICITATION_METHOD
	// in omnigent/runtime/policies/approval.py).
	Method *string `json:"method,omitempty"`

	// The MCP-shaped params block carrying the prompt and (form-mode only) the requested
	// schema.
	Params         ElicitationRequestParams `json:"params"`
	SequenceNumber *int                     `json:"sequence_number,omitempty"`

	// Type is always "response.elicitation_request".
	Type string `json:"type"`
}

func (ElicitationRequestEvent) isEvent() {}

// ElicitationResolvedEvent is the wire event "response.elicitation_resolved".
//
// Signal that a previously-published elicitation is no longer outstanding,
// even though no UI approval verdict was delivered through POST
// /v1/sessions/{id}/events.
type ElicitationResolvedEvent struct {
	// Correlation id of the elicitation being cleared, e.g. "elicit_abc123". Must match the id
	// of a prior ElicitationRequestEvent.
	ElicitationID  string `json:"elicitation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "response.elicitation_resolved".
	Type string `json:"type"`
}

func (ElicitationResolvedEvent) isEvent() {}

// ErrorEvent is the wire event "response.error".
//
// Non-recoverable error reported during the turn.
type ErrorEvent struct {
	// Classified error description.
	Error          RetryErrorDetail `json:"error"`
	SequenceNumber *int             `json:"sequence_number,omitempty"`

	// Origin of the error — "llm" for LLM-call failures, "execution" for timeouts, "tool" for
	// tool failures (currently emitted by retry exhaustion paths).
	Source string `json:"source"`

	// Tool identifier when source == "tool"; None for the other sources.
	ToolName *string `json:"tool_name,omitempty"`

	// Type is always "response.error".
	Type string `json:"type"`
}

func (ErrorEvent) isEvent() {}

// InProgressEvent is the wire event "response.in_progress".
//
// Event emitted once the task transitions to in-progress.
type InProgressEvent struct {
	// The response object with status="in_progress".
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.in_progress".
	Type string `json:"type"`
}

func (InProgressEvent) isEvent() {}

// IncompleteEvent is the wire event "response.incomplete".
//
// Terminal event for a turn that ended without completing (e.g. hit the
// iteration cap or token budget).
type IncompleteEvent struct {
	// The final response object with status="incomplete" and incomplete_details populated
	// describing the reason.
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.incomplete".
	Type string `json:"type"`
}

func (IncompleteEvent) isEvent() {}

// OutputFileDoneEvent is the wire event "response.output_file.done".
//
// A streamed file output completed materializing.
type OutputFileDoneEvent struct {
	// MIME content type if the annotation supplied one, e.g. "application/pdf". None
	// otherwise.
	ContentType *string `json:"content_type,omitempty"`

	// Identifier of the materialized file, e.g. "file_abc123".
	FileID string `json:"file_id"`

	// Original filename if the annotation supplied one, e.g. "report.pdf". None otherwise.
	Filename       *string `json:"filename,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// Type is always "response.output_file.done".
	Type string `json:"type"`
}

func (OutputFileDoneEvent) isEvent() {}

// OutputItemDoneEvent is the wire event "response.output_item.done".
//
// A conversation output item completed during the turn.
type OutputItemDoneEvent struct {
	// The completed item dict. Heterogeneous and item-type-specific; see
	// omnigent/entities/conversation.py for the per-type *Data shapes that drive
	// serialization.
	Item           map[string]any `json:"item"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.output_item.done".
	Type string `json:"type"`
}

func (OutputItemDoneEvent) isEvent() {}

// OutputTextDeltaEvent is the wire event "response.output_text.delta".
//
// Incremental assistant-text token emitted during streaming.
type OutputTextDeltaEvent struct {
	// The text fragment for this chunk, e.g. "Hello".
	Delta string `json:"delta"`

	// Optional provider completion marker for the message.
	Final *bool `json:"final,omitempty"`

	// 0-based chunk order within the message, e.g. 3. Used to suppress repeated chunks; None
	// for in-process streaming.
	Index *int `json:"index,omitempty"`

	// For native terminal streaming, the provider's stable per-message id, e.g.
	// "2ca51d97-2f0f-493a-aed7-85a5b56c5747". None for ordinary in-process task streaming,
	// where deltas group by the active response.
	MessageID      *string `json:"message_id,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// Type is always "response.output_text.delta".
	Type string `json:"type"`
}

func (OutputTextDeltaEvent) isEvent() {}

// PolicyDeniedEvent is the wire event "response.policy_denied".
//
// Signal that a policy DENY was enforced on a native harness turn.
type PolicyDeniedEvent struct {
	// Session/conversation id the DENY applies to, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// The policy phase the DENY landed on, e.g. "tool_call".
	Phase *string `json:"phase,omitempty"`

	// Human-readable deny reason from the deciding policy, e.g. "Blocked by policy.".
	Reason         *string `json:"reason,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// Type is always "response.policy_denied".
	Type string `json:"type"`
}

func (PolicyDeniedEvent) isEvent() {}

// QueuedEvent is the wire event "response.queued".
//
// Optional event emitted between created and in_progress for background tasks
// that are queued before they start.
type QueuedEvent struct {
	// The response object with status="queued".
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.queued".
	Type string `json:"type"`
}

func (QueuedEvent) isEvent() {}

// ReasoningStartedEvent is the wire event "response.reasoning.started".
//
// Marker emitted once when a reasoning block begins.
type ReasoningStartedEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Type is always "response.reasoning.started".
	Type string `json:"type"`
}

func (ReasoningStartedEvent) isEvent() {}

// ReasoningSummaryTextDeltaEvent is the wire event "response.reasoning_summary_text.delta".
//
// Incremental reasoning-summary token.
type ReasoningSummaryTextDeltaEvent struct {
	// The summary text fragment, e.g. "Will use the search tool to gather context.".
	Delta          string `json:"delta"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "response.reasoning_summary_text.delta".
	Type string `json:"type"`
}

func (ReasoningSummaryTextDeltaEvent) isEvent() {}

// ReasoningTextDeltaEvent is the wire event "response.reasoning_text.delta".
//
// Incremental reasoning-text token (full chain-of-thought).
type ReasoningTextDeltaEvent struct {
	// The reasoning text fragment, e.g. "Considering the user's intent...".
	Delta          string `json:"delta"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "response.reasoning_text.delta".
	Type string `json:"type"`
}

func (ReasoningTextDeltaEvent) isEvent() {}

// ResponseCancelledEvent is the wire event "response.cancelled".
//
// Terminal event for a turn cancelled before completion.
type ResponseCancelledEvent struct {
	// The final response object with status="cancelled".
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.cancelled".
	Type string `json:"type"`
}

func (ResponseCancelledEvent) isEvent() {}

// ResponseCompletedEvent is the wire event "response.completed".
//
// Terminal event for a successfully completed turn.
type ResponseCompletedEvent struct {
	// The final response object with status="completed".
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.completed".
	Type string `json:"type"`
}

func (ResponseCompletedEvent) isEvent() {}

// ResponseCreatedEvent is the wire event "response.created".
//
// Initial event emitted at the start of every streaming response.
type ResponseCreatedEvent struct {
	// The newly-allocated response object.
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.created".
	Type string `json:"type"`
}

func (ResponseCreatedEvent) isEvent() {}

// ResponseFailedEvent is the wire event "response.failed".
//
// Terminal event for a turn that ended with an error.
type ResponseFailedEvent struct {
	// The final response object with status="failed" and error populated.
	Response       ResponseObject `json:"response"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "response.failed".
	Type string `json:"type"`
}

func (ResponseFailedEvent) isEvent() {}

// ResponseHeartbeatEvent is the wire event "response.heartbeat".
//
// Keepalive event emitted on a fixed cadence during streaming.
type ResponseHeartbeatEvent struct {
	// Sequence number of the last non- heartbeat event seen on the same stream, e.g. 42. None
	// before any user-visible event has fired (first heartbeat of the turn, before deltas
	// land), or when the producer chose not to populate it.
	LastEventSeq   *int `json:"last_event_seq,omitempty"`
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// ISO 8601 UTC timestamp at emission, e.g. "2026-04-27T15:30:00Z". None when the producer
	// chose not to populate it (legacy emitters).
	ServerTime *string `json:"server_time,omitempty"`

	// Type is always "response.heartbeat".
	Type string `json:"type"`
}

func (ResponseHeartbeatEvent) isEvent() {}

// RetryEvent is the wire event "response.retry".
//
// A retryable failure was caught and a retry is scheduled.
type RetryEvent struct {
	// 1-based count of the upcoming attempt (i.e. attempt that will run AFTER this delay),
	// e.g. 2 for the first retry.
	Attempt int `json:"attempt"`

	// Seconds the producer will sleep before retrying, rounded to two decimals, e.g. 1.5.
	DelaySeconds float64 `json:"delay_seconds"`

	// Classified error description for the failure being retried.
	Error RetryErrorDetail `json:"error"`

	// Total tries allowed by the retry policy, e.g. 3. Lets clients render "attempt 2 of 3".
	MaxAttempts    int  `json:"max_attempts"`
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Origin of the retried failure — "llm" for LLM-call retries, "tool" for tool-call
	// retries.
	Source string `json:"source"`

	// Tool identifier when source == "tool", e.g. "search.web". None for LLM retries.
	ToolName *string `json:"tool_name,omitempty"`

	// Type is always "response.retry".
	Type string `json:"type"`
}

func (RetryEvent) isEvent() {}

// SessionAgentChangedEvent is the wire event "session.agent_changed".
//
// Bound-agent change on a live session.
type SessionAgentChangedEvent struct {
	// The session-scoped clone now bound to the session, e.g. "ag_abc123".
	AgentID string `json:"agent_id"`

	// Display name of the agent the session now runs, e.g. "claude-native-ui". Deliberately
	// the clean target-agent name — not the clone row's "… (switch ag_…)" disambiguation name
	// — because clients render it verbatim. Category: **transient** (SSE-only).
	AgentName string `json:"agent_name"`

	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.agent_changed".
	Type string `json:"type"`
}

func (SessionAgentChangedEvent) isEvent() {}

// SessionChangedFilesInvalidatedEvent is the wire event "session.changed_files.invalidated".
//
// The session's changed-files list may have changed — refetch it.
type SessionChangedFilesInvalidatedEvent struct {
	// Environment whose changes were invalidated, e.g. "default".
	EnvironmentID  *string `json:"environment_id,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// Owning session/conversation id.
	SessionID string `json:"session_id"`

	// Type is always "session.changed_files.invalidated".
	Type string `json:"type"`
}

func (SessionChangedFilesInvalidatedEvent) isEvent() {}

// SessionChildSessionUpdatedEvent is the wire event "session.child_session.updated".
//
// A child (sub-agent) session's status changed — pushed to the PARENT.
type SessionChildSessionUpdatedEvent struct {
	// A PARTIAL ChildSessionSummary — the snapshot-on-connect sends the full summary, while
	// live runner deltas carry only the fields that changed (a status delta omits
	// last_message_preview; a preview delta carries only it).
	Child map[string]any `json:"child"`

	// The child session id, e.g. "conv_child_abc123".
	ChildSessionID string `json:"child_session_id"`

	// The PARENT (carrier) session id.
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.child_session.updated".
	Type string `json:"type"`
}

func (SessionChildSessionUpdatedEvent) isEvent() {}

// SessionCollaborationModeEvent is the wire event "session.collaboration_mode".
//
// Active collaboration-mode update from a Codex-native session.
type SessionCollaborationModeEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// The active collaboration mode string, e.g. "plan" or "default". Category: **transient**
	// (SSE-only). The server also writes omnigent.codex_native.collaboration_mode on the
	// conversation labels, so reconnect clients restore the same state from the session
	// snapshot.
	Mode           string `json:"mode"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.collaboration_mode".
	Type string `json:"type"`
}

func (SessionCollaborationModeEvent) isEvent() {}

// SessionCreatedEvent is the wire event "session.created".
//
// A child (sub-agent) session was spawned from this session.
type SessionCreatedEvent struct {
	// Registered agent id the child runs as, e.g. "agent_xyz". None is permitted only for
	// legacy spawn paths that did not record an agent id; new code MUST set it.
	AgentID *string `json:"agent_id,omitempty"`

	// The newly-created child session id, e.g. "conv_child456". Same as conversation_id on the
	// child's own stream when consumers pivot to it.
	ChildSessionID string `json:"child_session_id"`

	// The PARENT session/conversation id — this event rides the parent's stream, e.g.
	// "conv_parent123".
	ConversationID string `json:"conversation_id"`

	// Echo of conversation_id for consumers that key on a dedicated "parent" field rather than
	// the carrier conversation_id. Always equal to conversation_id; included for forward-
	// compat with clients that may relay these events across stream boundaries. Category:
	// **transient** (SSE-only).
	ParentSessionID *string `json:"parent_session_id,omitempty"`
	SequenceNumber  *int    `json:"sequence_number,omitempty"`

	// Type is always "session.created".
	Type string `json:"type"`
}

func (SessionCreatedEvent) isEvent() {}

// SessionHeartbeatEvent is the wire event "session.heartbeat".
//
// Idle-stream keepalive on GET /v1/sessions/{id}/stream.
type SessionHeartbeatEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// ISO 8601 UTC timestamp at emission, e.g. "2026-05-25T10:30:00Z". None when the producer
	// chose not to populate it.
	ServerTime *string `json:"server_time,omitempty"`

	// Type is always "session.heartbeat".
	Type string `json:"type"`
}

func (SessionHeartbeatEvent) isEvent() {}

// SessionInputConsumedEvent is the wire event "session.input.consumed".
//
// A queued input item was materialized into conversation history.
type SessionInputConsumedEvent struct {
	// The decoded queued-item payload — see SessionInputConsumedPayload.
	Data           SessionInputConsumedPayload `json:"data"`
	SequenceNumber *int                        `json:"sequence_number,omitempty"`

	// Type is always "session.input.consumed".
	Type string `json:"type"`
}

func (SessionInputConsumedEvent) isEvent() {}

// SessionInterruptedEvent is the wire event "session.interrupted".
//
// User-triggered cancel reached the loop.
type SessionInterruptedEvent struct {
	// The interrupt metadata — see SessionInterruptedPayload.
	Data           SessionInterruptedPayload `json:"data"`
	SequenceNumber *int                      `json:"sequence_number,omitempty"`

	// Type is always "session.interrupted".
	Type string `json:"type"`
}

func (SessionInterruptedEvent) isEvent() {}

// SessionMcpStartupEvent is the wire event "session.mcp_startup".
//
// Per-MCP-server startup progress for a native harness session.
type SessionMcpStartupEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Latest per-server startup map, e.g. {"safe": {"status": "starting", "error": None}}.
	// Category: **transient** (SSE + snapshot cache). Not persisted; a client connecting mid-
	// startup seeds from the session snapshot's mcp_startup field and updates live off this
	// event.
	Servers map[string]any `json:"servers"`

	// Type is always "session.mcp_startup".
	Type string `json:"type"`
}

func (SessionMcpStartupEvent) isEvent() {}

// SessionModelEvent is the wire event "session.model".
//
// Active-model update from a terminal-backed integration.
type SessionModelEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// Tier alias the session is now on, e.g. "opus" — Claude Code's version-agnostic alias,
	// matching the picker's vocabulary (not a pinned "claude-opus-4-8" id). Category:
	// **transient** (SSE-only).
	Model          string `json:"model"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.model".
	Type string `json:"type"`
}

func (SessionModelEvent) isEvent() {}

// SessionModelOptionsEvent is the wire event "session.model_options".
//
// Signal that a native session's model catalog has resolved.
type SessionModelOptionsEvent struct {
	// Session identifier, e.g. "conv_abc123". Category: **transient** (SSE-only). On
	// reconnect, clients seed Native model / effort controls from the session snapshot.
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.model_options".
	Type string `json:"type"`
}

func (SessionModelOptionsEvent) isEvent() {}

// SessionPresenceEvent is the wire event "session.presence".
//
// The session's viewer list changed — full state, not a delta.
type SessionPresenceEvent struct {
	// The conversation whose stream delivered this event — the root or a sub-agent
	// conversation, e.g. "conv_abc123". Matches the streamed conversation (not necessarily the
	// tree's root) so clients can guard events by the conversation they are viewing.
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.presence".
	Type string `json:"type"`

	// All users currently viewing any conversation in the session tree (including the
	// receiving user — the web filters self out for display), ordered by join time.
	Viewers []PresenceViewer `json:"viewers"`
}

func (SessionPresenceEvent) isEvent() {}

// SessionReasoningEffortEvent is the wire event "session.reasoning_effort".
//
// Active reasoning-effort update from a terminal-backed integration.
type SessionReasoningEffortEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// Reasoning effort now active for the session, e.g. "medium", or None when Codex cleared
	// to its default. Category: **transient** (SSE-only). The server also writes
	// reasoning_effort on the conversation, so on reconnect clients restore the selection from
	// the session snapshot rather than from a replayed event.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	SequenceNumber  *int    `json:"sequence_number,omitempty"`

	// Type is always "session.reasoning_effort".
	Type string `json:"type"`
}

func (SessionReasoningEffortEvent) isEvent() {}

// SessionResourceCreatedEvent is the wire event "session.resource.created".
//
// A session resource was created.
type SessionResourceCreatedEvent struct {
	// The newly created resource object.
	Resource       map[string]any `json:"resource"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Type is always "session.resource.created".
	Type string `json:"type"`
}

func (SessionResourceCreatedEvent) isEvent() {}

// SessionResourceDeletedEvent is the wire event "session.resource.deleted".
//
// A session resource was deleted.
type SessionResourceDeletedEvent struct {
	// Opaque id of the deleted resource.
	ResourceID string `json:"resource_id"`

	// Type of the deleted resource, e.g. "terminal", "file".
	ResourceType   string `json:"resource_type"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Owning session/conversation id.
	SessionID string `json:"session_id"`

	// Type is always "session.resource.deleted".
	Type string `json:"type"`
}

func (SessionResourceDeletedEvent) isEvent() {}

// SessionSandboxStatusEvent is the wire event "session.sandbox_status".
//
// Managed-sandbox launch progress for a host_type="managed" session.
type SessionSandboxStatusEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// Failure detail when stage == "failed", e.g. "managed sandbox launch failed: spend limit
	// reached". None otherwise. Category: **transient** (SSE-only).
	Error          *string `json:"error,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// The launch stage just entered, e.g. "provisioning" — see SandboxStatus for the full
	// pipeline order.
	Stage string `json:"stage"`

	// Type is always "session.sandbox_status".
	Type string `json:"type"`
}

func (SessionSandboxStatusEvent) isEvent() {}

// SessionSkillsEvent is the wire event "session.skills".
//
// Signal that a session's runner-owned skills have resolved.
type SessionSkillsEvent struct {
	// Session identifier, e.g. "conv_abc123". Category: **transient** (SSE-only). On
	// reconnect, clients seed the menu from the session snapshot's skills field, which is
	// populated by the runner-skills cache at snapshot build time.
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "session.skills".
	Type string `json:"type"`
}

func (SessionSkillsEvent) isEvent() {}

// SessionStatusEvent is the wire event "session.status".
//
// Session lifecycle status transition.
type SessionStatusEvent struct {
	BackgroundTaskCount *int `json:"background_task_count,omitempty"`

	// Short human phrase naming what a still-running session is parked on, e.g. "permission
	// prompt" or "dialog open". Set by terminal-backed integrations whose agent can block on a
	// dialog the web UI does not mirror, so the client can say *why* nothing is moving instead
	// of showing a bare spinner.
	BlockedOn *string `json:"blocked_on,omitempty"`

	// The conversation/session identifier whose status changed, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// Machine-readable failure detail, present only when status == "failed". Carries the
	// message the runner attached when a turn died — most importantly a SETUP-phase failure
	// (spec resolution, spawn-env build) that ends the turn before any response.failed event
	// is emitted. None for every non-failed transition.
	Error *ErrorDetail `json:"error,omitempty"`

	// Optional active response id for terminal-backed integrations, e.g. "codex_turn_abc123".
	// Clients use it to associate coarse session status edges with the assistant bubble they
	// describe. None for ordinary in-process runtime edges.
	ResponseID     *string `json:"response_id,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// New session status. "launching" (session or child task created, but no concrete harness
	// start observed), "idle" (no loop running), "running" (loop executing), "waiting" (parent
	// turn parked on the async-work drain), or "failed" (terminal failure).
	Status string `json:"status"`

	// Type is always "session.status".
	Type string `json:"type"`
}

func (SessionStatusEvent) isEvent() {}

// SessionSupersededEvent is the wire event "session.superseded".
//
// This conversation was superseded by another and clients should follow to it.
type SessionSupersededEvent struct {
	// The superseded (old) conversation id this event rides the stream of, e.g. "conv_old".
	ConversationID string `json:"conversation_id"`

	// Why the session was superseded. Currently always "clear" (a Claude Code /clear); kept as
	// a field so the client can branch on future supersession causes.
	Reason         *string `json:"reason,omitempty"`
	SequenceNumber *int    `json:"sequence_number,omitempty"`

	// The conversation to follow to, e.g. "conv_new".
	TargetConversationID string `json:"target_conversation_id"`

	// Type is always "session.superseded".
	Type string `json:"type"`
}

func (SessionSupersededEvent) isEvent() {}

// SessionTerminalActivityEvent is the wire event "session.terminal.activity".
//
// A terminal's pane produced output (runner-determined activity pulse).
type SessionTerminalActivityEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Owning session/conversation id.
	SessionID string `json:"session_id"`

	// Opaque terminal resource id, e.g. "terminal_zsh_s1".
	TerminalID string `json:"terminal_id"`

	// Type is always "session.terminal.activity".
	Type string `json:"type"`
}

func (SessionTerminalActivityEvent) isEvent() {}

// SessionTerminalPendingEvent is the wire event "session.terminal_pending".
//
// Terminal spin-up status for a terminal-first session.
type SessionTerminalPendingEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`

	// True while the terminal is being created; False once it lands or auto-create fails.
	// Category: **transient** (SSE-only). On reconnect, clients seed the spinner from the
	// session snapshot's terminal_pending field, which is populated by
	// _session_terminal_pending_cache at snapshot build time.
	Pending        bool `json:"pending"`
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Type is always "session.terminal_pending".
	Type string `json:"type"`
}

func (SessionTerminalPendingEvent) isEvent() {}

// SessionTodosEvent is the wire event "session.todos".
//
// Todo-list update from a Claude Code terminal-backed session.
type SessionTodosEvent struct {
	// Session identifier, e.g. "conv_abc123".
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Current todo items read from Claude's todo file. Each entry is a raw dict with content
	// (str), status ("pending" | "in_progress" | "completed"), and activeForm (str, the gerund
	// form) keys, e.g. [{"content": "Fix the bug", "status": "in_progress", "activeForm":
	// "Fixing the bug"}]. Category: **transient** (SSE-only).
	Todos []map[string]any `json:"todos"`

	// Type is always "session.todos".
	Type string `json:"type"`
}

func (SessionTodosEvent) isEvent() {}

// SessionUsageEvent is the wire event "session.usage".
//
// Token-usage update from a terminal-backed integration.
type SessionUsageEvent struct {
	// input + cache_creation + cache_read from the latest assistant message.usage. None on a
	// window-only broadcast.
	ContextTokens *int `json:"context_tokens,omitempty"`

	// Resolved window in tokens (e.g. 200_000 normally, 1_000_000 with opus[1m] / sonnet[1m]).
	// None on a tokens-only broadcast.
	ContextWindow *int `json:"context_window,omitempty"`

	// Session identifier.
	ConversationID string `json:"conversation_id"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Cumulative session spend in USD after this update, e.g. 0.42 — the server-computed total
	// the cost-budget policy gates on.
	TotalCostUsd *float64 `json:"total_cost_usd,omitempty"`

	// Type is always "session.usage".
	Type string `json:"type"`

	// Per-model breakdown of the same subtree usage after this update, keyed by raw harness
	// model id, e.g. {"claude-sonnet-4-6": ModelUsage(input_tokens=12000, ...)}. None
	// (stripped by exclude_none) on a broadcast that carries no per-model change, so the
	// client keeps its cached map. Category: **transient** (SSE-only).
	UsageByModel map[string]any `json:"usage_by_model,omitempty"`
}

func (SessionUsageEvent) isEvent() {}

// ToolOutputDeltaEvent is the wire event "response.function_call_output.delta".
//
// Incremental output from an in-progress function call.
type ToolOutputDeltaEvent struct {
	// Function-call correlation id.
	CallID string `json:"call_id"`

	// Command stdout/stderr fragment.
	Delta          string `json:"delta"`
	SequenceNumber *int   `json:"sequence_number,omitempty"`

	// Type is always "response.function_call_output.delta".
	Type string `json:"type"`
}

func (ToolOutputDeltaEvent) isEvent() {}

// TurnCancelledEvent is the wire event "turn.cancelled".
//
// Emitted when a turn is interrupted by the user or system.
type TurnCancelledEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Session/conversation identifier, e.g. "conv_abc123".
	SessionID string `json:"session_id"`

	// Type is always "turn.cancelled".
	Type string `json:"type"`
}

func (TurnCancelledEvent) isEvent() {}

// TurnCompletedEvent is the wire event "turn.completed".
//
// Emitted when a turn finishes successfully with no pending work.
type TurnCompletedEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Session/conversation identifier, e.g. "conv_abc123".
	SessionID string `json:"session_id"`

	// Type is always "turn.completed".
	Type string `json:"type"`
}

func (TurnCompletedEvent) isEvent() {}

// TurnFailedEvent is the wire event "turn.failed".
//
// Emitted when a turn fails due to an LLM error, timeout, or crash.
type TurnFailedEvent struct {
	// Error details, e.g. {"message": "LLM timeout", "type": "TimeoutError"}.
	Error          map[string]any `json:"error,omitempty"`
	SequenceNumber *int           `json:"sequence_number,omitempty"`

	// Session/conversation identifier, e.g. "conv_abc123".
	SessionID string `json:"session_id"`

	// Type is always "turn.failed".
	Type string `json:"type"`
}

func (TurnFailedEvent) isEvent() {}

// TurnStartedEvent is the wire event "turn.started".
//
// Emitted when the runner starts a new turn for a session.
type TurnStartedEvent struct {
	SequenceNumber *int `json:"sequence_number,omitempty"`

	// Session/conversation identifier, e.g. "conv_abc123".
	SessionID string `json:"session_id"`

	// Type is always "turn.started".
	Type string `json:"type"`
}

func (TurnStartedEvent) isEvent() {}

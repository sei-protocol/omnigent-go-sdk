package omnigent

import (
	"github.com/sei-protocol/omnigent-go-sdk/internal/api"

	"encoding/json"
	"fmt"
)

// Event is one decoded frame from a session's event stream.
//
// The implementations in this file are one per member of the server's
// discriminated union, plus [UnknownEvent]. Consume one with a type switch:
//
//	switch ev := event.(type) {
//	case OutputTextDeltaEvent:
//		fmt.Print(ev.Delta)
//	case ResponseCompletedEvent:
//		return nil
//	default:
//		log.Printf("ignoring %s", ev.EventType())
//	}
//
// Give that switch a default arm. The set grows when the server's does, and an
// [UnknownEvent] arrives for a discriminator this build predates, so a switch
// without one silently drops frames it was never written to expect.
//
// [Event.EventType] reaches the discriminator without a switch at all, which is
// what logging, metrics and routing want.
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
// see the terminal-edge note below in this comment.
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
	// EventType returns the frame's wire discriminator, e.g. "session.status".
	//
	// Logging, metrics and routing want the type and nothing else. Without this
	// each of them needs a switch over every variant to reach a field they all
	// share.
	//
	// The value is the one the frame carried, not a constant per Go type, so a
	// frame decoded by this package reports what the server actually sent. On an
	// [UnknownEvent] it is the discriminator this build did not recognise.
	EventType() string

	// isEvent seals the union, so a variant comes from this package.
	//
	// Precisely: a type declaring its own isEvent does not satisfy Event, because
	// the method is unexported and identity includes the declaring package. A type
	// embedding an exported variant does satisfy it, promoting this method with
	// the rest — so this is a strong convention, not a proof.
	//
	// The pattern is go/ast's, where Node carries the real methods and Expr, Stmt
	// and Decl each add an unexported marker — 50 implementations in one file. The
	// marker alone would be the objectionable version, because it asks every
	// variant for a method that tells a caller nothing; paired with EventType it
	// costs one line per variant and buys a closed set.
	//
	// Sealing has to happen before the first release that exports Event. Adding an
	// unexported method later breaks anyone who satisfied the interface in the
	// meantime, so this is the reversible order: seal now, unseal deliberately if
	// a caller ever needs to supply its own event.
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

func (e UnknownEvent) EventType() string { return e.Type }
func (UnknownEvent) isEvent()            {}

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
	"session.mcp_startup":                   decodeInto[SessionMCPStartupEvent],
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
type BrowserActionRequestEvent api.BrowserActionRequestEvent

func (e BrowserActionRequestEvent) EventType() string { return e.Type }
func (BrowserActionRequestEvent) isEvent()            {}

// ClientTaskCancelEvent is the wire event "response.client_task.cancel".
//
// Server-side request that the client cancel a tunneled tool call.
type ClientTaskCancelEvent api.ClientTaskCancelEvent

func (e ClientTaskCancelEvent) EventType() string { return e.Type }
func (ClientTaskCancelEvent) isEvent()            {}

// CompactionCompletedEvent is the wire event "response.compaction.completed".
//
// Conversation history compaction has finished.
type CompactionCompletedEvent api.CompactionCompletedEvent

func (e CompactionCompletedEvent) EventType() string { return e.Type }
func (CompactionCompletedEvent) isEvent()            {}

// CompactionFailedEvent is the wire event "response.compaction.failed".
//
// Conversation history compaction failed.
type CompactionFailedEvent api.CompactionFailedEvent

func (e CompactionFailedEvent) EventType() string { return e.Type }
func (CompactionFailedEvent) isEvent()            {}

// CompactionInProgressEvent is the wire event "response.compaction.in_progress".
//
// Conversation history is being compacted.
type CompactionInProgressEvent api.CompactionInProgressEvent

func (e CompactionInProgressEvent) EventType() string { return e.Type }
func (CompactionInProgressEvent) isEvent()            {}

// ElicitationRequestEvent is the wire event "response.elicitation_request".
//
// Synchronous request for a decision from upstream.
type ElicitationRequestEvent api.ElicitationRequestEvent

func (e ElicitationRequestEvent) EventType() string { return e.Type }
func (ElicitationRequestEvent) isEvent()            {}

// ElicitationResolvedEvent is the wire event "response.elicitation_resolved".
//
// Signal that a previously-published elicitation is no longer outstanding,
// even though no UI approval verdict was delivered through POST
// /v1/sessions/{id}/events.
type ElicitationResolvedEvent api.ElicitationResolvedEvent

func (e ElicitationResolvedEvent) EventType() string { return e.Type }
func (ElicitationResolvedEvent) isEvent()            {}

// ErrorEvent is the wire event "response.error".
//
// Non-recoverable error reported during the turn.
type ErrorEvent api.ErrorEvent

func (e ErrorEvent) EventType() string { return e.Type }
func (ErrorEvent) isEvent()            {}

// InProgressEvent is the wire event "response.in_progress".
//
// Event emitted once the task transitions to in-progress.
type InProgressEvent api.InProgressEvent

func (e InProgressEvent) EventType() string { return e.Type }
func (InProgressEvent) isEvent()            {}

// IncompleteEvent is the wire event "response.incomplete".
//
// Terminal event for a turn that ended without completing (e.g. hit the
// iteration cap or token budget).
type IncompleteEvent api.IncompleteEvent

func (e IncompleteEvent) EventType() string { return e.Type }
func (IncompleteEvent) isEvent()            {}

// OutputFileDoneEvent is the wire event "response.output_file.done".
//
// A streamed file output completed materializing.
type OutputFileDoneEvent api.OutputFileDoneEvent

func (e OutputFileDoneEvent) EventType() string { return e.Type }
func (OutputFileDoneEvent) isEvent()            {}

// OutputItemDoneEvent is the wire event "response.output_item.done".
//
// A conversation output item completed during the turn.
type OutputItemDoneEvent api.OutputItemDoneEvent

func (e OutputItemDoneEvent) EventType() string { return e.Type }
func (OutputItemDoneEvent) isEvent()            {}

// OutputTextDeltaEvent is the wire event "response.output_text.delta".
//
// Incremental assistant-text token emitted during streaming.
type OutputTextDeltaEvent api.OutputTextDeltaEvent

func (e OutputTextDeltaEvent) EventType() string { return e.Type }
func (OutputTextDeltaEvent) isEvent()            {}

// PolicyDeniedEvent is the wire event "response.policy_denied".
//
// Signal that a policy DENY was enforced on a native harness turn.
type PolicyDeniedEvent api.PolicyDeniedEvent

func (e PolicyDeniedEvent) EventType() string { return e.Type }
func (PolicyDeniedEvent) isEvent()            {}

// QueuedEvent is the wire event "response.queued".
//
// Optional event emitted between created and in_progress for background tasks
// that are queued before they start.
type QueuedEvent api.QueuedEvent

func (e QueuedEvent) EventType() string { return e.Type }
func (QueuedEvent) isEvent()            {}

// ReasoningStartedEvent is the wire event "response.reasoning.started".
//
// Marker emitted once when a reasoning block begins.
type ReasoningStartedEvent api.ReasoningStartedEvent

func (e ReasoningStartedEvent) EventType() string { return e.Type }
func (ReasoningStartedEvent) isEvent()            {}

// ReasoningSummaryTextDeltaEvent is the wire event "response.reasoning_summary_text.delta".
//
// Incremental reasoning-summary token.
type ReasoningSummaryTextDeltaEvent api.ReasoningSummaryTextDeltaEvent

func (e ReasoningSummaryTextDeltaEvent) EventType() string { return e.Type }
func (ReasoningSummaryTextDeltaEvent) isEvent()            {}

// ReasoningTextDeltaEvent is the wire event "response.reasoning_text.delta".
//
// Incremental reasoning-text token (full chain-of-thought).
type ReasoningTextDeltaEvent api.ReasoningTextDeltaEvent

func (e ReasoningTextDeltaEvent) EventType() string { return e.Type }
func (ReasoningTextDeltaEvent) isEvent()            {}

// ResponseCancelledEvent is the wire event "response.cancelled".
//
// Terminal event for a turn cancelled before completion.
type ResponseCancelledEvent api.CancelledEvent

func (e ResponseCancelledEvent) EventType() string { return e.Type }
func (ResponseCancelledEvent) isEvent()            {}

// ResponseCompletedEvent is the wire event "response.completed".
//
// Terminal event for a successfully completed turn.
type ResponseCompletedEvent api.CompletedEvent

func (e ResponseCompletedEvent) EventType() string { return e.Type }
func (ResponseCompletedEvent) isEvent()            {}

// ResponseCreatedEvent is the wire event "response.created".
//
// Initial event emitted at the start of every streaming response.
type ResponseCreatedEvent api.CreatedEvent

func (e ResponseCreatedEvent) EventType() string { return e.Type }
func (ResponseCreatedEvent) isEvent()            {}

// ResponseFailedEvent is the wire event "response.failed".
//
// Terminal event for a turn that ended with an error.
type ResponseFailedEvent api.FailedEvent

func (e ResponseFailedEvent) EventType() string { return e.Type }
func (ResponseFailedEvent) isEvent()            {}

// ResponseHeartbeatEvent is the wire event "response.heartbeat".
//
// Keepalive event emitted on a fixed cadence during streaming.
type ResponseHeartbeatEvent api.HeartbeatEvent

func (e ResponseHeartbeatEvent) EventType() string { return e.Type }
func (ResponseHeartbeatEvent) isEvent()            {}

// RetryEvent is the wire event "response.retry".
//
// A retryable failure was caught and a retry is scheduled.
type RetryEvent api.RetryEvent

func (e RetryEvent) EventType() string { return e.Type }
func (RetryEvent) isEvent()            {}

// SessionAgentChangedEvent is the wire event "session.agent_changed".
//
// Bound-agent change on a live session.
type SessionAgentChangedEvent api.SessionAgentChangedEvent

func (e SessionAgentChangedEvent) EventType() string { return e.Type }
func (SessionAgentChangedEvent) isEvent()            {}

// SessionChangedFilesInvalidatedEvent is the wire event "session.changed_files.invalidated".
//
// The session's changed-files list may have changed — refetch it.
type SessionChangedFilesInvalidatedEvent api.SessionChangedFilesInvalidatedEvent

func (e SessionChangedFilesInvalidatedEvent) EventType() string { return e.Type }
func (SessionChangedFilesInvalidatedEvent) isEvent()            {}

// SessionChildSessionUpdatedEvent is the wire event "session.child_session.updated".
//
// A child (sub-agent) session's status changed — pushed to the PARENT.
type SessionChildSessionUpdatedEvent api.SessionChildSessionUpdatedEvent

func (e SessionChildSessionUpdatedEvent) EventType() string { return e.Type }
func (SessionChildSessionUpdatedEvent) isEvent()            {}

// SessionCollaborationModeEvent is the wire event "session.collaboration_mode".
//
// Active collaboration-mode update from a Codex-native session.
type SessionCollaborationModeEvent api.SessionCollaborationModeEvent

func (e SessionCollaborationModeEvent) EventType() string { return e.Type }
func (SessionCollaborationModeEvent) isEvent()            {}

// SessionCreatedEvent is the wire event "session.created".
//
// A child (sub-agent) session was spawned from this session.
type SessionCreatedEvent api.SessionCreatedEvent

func (e SessionCreatedEvent) EventType() string { return e.Type }
func (SessionCreatedEvent) isEvent()            {}

// SessionHeartbeatEvent is the wire event "session.heartbeat".
//
// Idle-stream keepalive on GET /v1/sessions/{id}/stream.
type SessionHeartbeatEvent api.SessionHeartbeatEvent

func (e SessionHeartbeatEvent) EventType() string { return e.Type }
func (SessionHeartbeatEvent) isEvent()            {}

// SessionInputConsumedEvent is the wire event "session.input.consumed".
//
// A queued input item was materialized into conversation history.
type SessionInputConsumedEvent api.SessionInputConsumedEvent

func (e SessionInputConsumedEvent) EventType() string { return e.Type }
func (SessionInputConsumedEvent) isEvent()            {}

// SessionInterruptedEvent is the wire event "session.interrupted".
//
// User-triggered cancel reached the loop.
type SessionInterruptedEvent api.SessionInterruptedEvent

func (e SessionInterruptedEvent) EventType() string { return e.Type }
func (SessionInterruptedEvent) isEvent()            {}

// SessionMCPStartupEvent is the wire event "session.mcp_startup".
//
// Per-MCP-server startup progress for a native harness session.
type SessionMCPStartupEvent api.SessionMCPStartupEvent

func (e SessionMCPStartupEvent) EventType() string { return e.Type }
func (SessionMCPStartupEvent) isEvent()            {}

// SessionModelEvent is the wire event "session.model".
//
// Active-model update from a terminal-backed integration.
type SessionModelEvent api.SessionModelEvent

func (e SessionModelEvent) EventType() string { return e.Type }
func (SessionModelEvent) isEvent()            {}

// SessionModelOptionsEvent is the wire event "session.model_options".
//
// Signal that a native session's model catalog has resolved.
type SessionModelOptionsEvent api.SessionModelOptionsEvent

func (e SessionModelOptionsEvent) EventType() string { return e.Type }
func (SessionModelOptionsEvent) isEvent()            {}

// SessionPresenceEvent is the wire event "session.presence".
//
// The session's viewer list changed — full state, not a delta.
type SessionPresenceEvent api.SessionPresenceEvent

func (e SessionPresenceEvent) EventType() string { return e.Type }
func (SessionPresenceEvent) isEvent()            {}

// SessionReasoningEffortEvent is the wire event "session.reasoning_effort".
//
// Active reasoning-effort update from a terminal-backed integration.
type SessionReasoningEffortEvent api.SessionReasoningEffortEvent

func (e SessionReasoningEffortEvent) EventType() string { return e.Type }
func (SessionReasoningEffortEvent) isEvent()            {}

// SessionResourceCreatedEvent is the wire event "session.resource.created".
//
// A session resource was created.
type SessionResourceCreatedEvent api.SessionResourceCreatedEvent

func (e SessionResourceCreatedEvent) EventType() string { return e.Type }
func (SessionResourceCreatedEvent) isEvent()            {}

// SessionResourceDeletedEvent is the wire event "session.resource.deleted".
//
// A session resource was deleted.
type SessionResourceDeletedEvent api.SessionResourceDeletedEvent

func (e SessionResourceDeletedEvent) EventType() string { return e.Type }
func (SessionResourceDeletedEvent) isEvent()            {}

// SessionSandboxStatusEvent is the wire event "session.sandbox_status".
//
// Managed-sandbox launch progress for a host_type="managed" session.
type SessionSandboxStatusEvent api.SessionSandboxStatusEvent

func (e SessionSandboxStatusEvent) EventType() string { return e.Type }
func (SessionSandboxStatusEvent) isEvent()            {}

// SessionSkillsEvent is the wire event "session.skills".
//
// Signal that a session's runner-owned skills have resolved.
type SessionSkillsEvent api.SessionSkillsEvent

func (e SessionSkillsEvent) EventType() string { return e.Type }
func (SessionSkillsEvent) isEvent()            {}

// SessionStatusEvent is the wire event "session.status".
//
// Session lifecycle status transition.
type SessionStatusEvent api.SessionStatusEvent

func (e SessionStatusEvent) EventType() string { return e.Type }
func (SessionStatusEvent) isEvent()            {}

// SessionSupersededEvent is the wire event "session.superseded".
//
// This conversation was superseded by another and clients should follow to it.
type SessionSupersededEvent api.SessionSupersededEvent

func (e SessionSupersededEvent) EventType() string { return e.Type }
func (SessionSupersededEvent) isEvent()            {}

// SessionTerminalActivityEvent is the wire event "session.terminal.activity".
//
// A terminal's pane produced output (runner-determined activity pulse).
type SessionTerminalActivityEvent api.SessionTerminalActivityEvent

func (e SessionTerminalActivityEvent) EventType() string { return e.Type }
func (SessionTerminalActivityEvent) isEvent()            {}

// SessionTerminalPendingEvent is the wire event "session.terminal_pending".
//
// Terminal spin-up status for a terminal-first session.
type SessionTerminalPendingEvent api.SessionTerminalPendingEvent

func (e SessionTerminalPendingEvent) EventType() string { return e.Type }
func (SessionTerminalPendingEvent) isEvent()            {}

// SessionTodosEvent is the wire event "session.todos".
//
// Todo-list update from a Claude Code terminal-backed session.
type SessionTodosEvent api.SessionTodosEvent

func (e SessionTodosEvent) EventType() string { return e.Type }
func (SessionTodosEvent) isEvent()            {}

// SessionUsageEvent is the wire event "session.usage".
//
// Token-usage update from a terminal-backed integration.
type SessionUsageEvent api.SessionUsageEvent

func (e SessionUsageEvent) EventType() string { return e.Type }
func (SessionUsageEvent) isEvent()            {}

// ToolOutputDeltaEvent is the wire event "response.function_call_output.delta".
//
// Incremental output from an in-progress function call.
type ToolOutputDeltaEvent api.ToolOutputDeltaEvent

func (e ToolOutputDeltaEvent) EventType() string { return e.Type }
func (ToolOutputDeltaEvent) isEvent()            {}

// TurnCancelledEvent is the wire event "turn.cancelled".
//
// Emitted when a turn is interrupted by the user or system.
type TurnCancelledEvent api.TurnCancelledEvent

func (e TurnCancelledEvent) EventType() string { return e.Type }
func (TurnCancelledEvent) isEvent()            {}

// TurnCompletedEvent is the wire event "turn.completed".
//
// Emitted when a turn finishes successfully with no pending work.
type TurnCompletedEvent api.TurnCompletedEvent

func (e TurnCompletedEvent) EventType() string { return e.Type }
func (TurnCompletedEvent) isEvent()            {}

// TurnFailedEvent is the wire event "turn.failed".
//
// Emitted when a turn fails due to an LLM error, timeout, or crash.
type TurnFailedEvent api.TurnFailedEvent

func (e TurnFailedEvent) EventType() string { return e.Type }
func (TurnFailedEvent) isEvent()            {}

// TurnStartedEvent is the wire event "turn.started".
//
// Emitted when the runner starts a new turn for a session.
type TurnStartedEvent api.TurnStartedEvent

func (e TurnStartedEvent) EventType() string { return e.Type }
func (TurnStartedEvent) isEvent()            {}

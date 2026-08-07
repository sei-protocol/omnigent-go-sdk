package omnigent

// Event is one decoded frame from a session's event stream.
//
// The interface is sealed: its only implementations are the generated event
// structs in models.gen.go, one per member of the server's discriminated union,
// plus [UnknownEvent]. Consume it with a type switch:
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

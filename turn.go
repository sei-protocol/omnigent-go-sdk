package omnigent

import (
	"fmt"
	"maps"
)

// TurnEnd selects which signal ends a turn.
//
// Two harness families put the end in different places, and reading the wrong one
// is not a stylistic difference: it decides whether a caller reads a complete
// answer or a half-written one.
type TurnEnd int

const (
	// TurnEndsOnIdleStatus ends a turn on an idle status edge that names a response.
	//
	// For the harness family that drives a real terminal, where a response terminal
	// means only that the prompt reached the harness, so a terminal there is not the
	// answer.
	//
	// This is the zero value on purpose. A harness this package does not recognise
	// gets the stricter rule, because the two mistakes do not cost the same: taking
	// a lifecycle terminal early hands the caller a partial answer it believes is
	// whole, while waiting for an edge that never comes blocks the read.
	//
	// Blocks, not times out. The server's heartbeat keeps the stream's idle watchdog
	// fed, so the wrong choice here produces no error of its own while the stream
	// stays up. Give [Chat.Send] a context with a deadline, sized to the longest
	// turn this agent may legitimately take rather than to an RPC.
	//
	// Without one the read blocks until the stream itself ends, and a deployment
	// that caps stream duration then reports [ErrTurnIncomplete] — which says
	// nothing about this option being the wrong one.
	//
	// The symptom is a turn that never ends while the session's status events carry
	// no response id. That means the harness is in-process, and
	// [TurnEndsOnResponseLifecycle] is the rule it wants.
	TurnEndsOnIdleStatus TurnEnd = iota

	// TurnEndsOnResponseLifecycle ends a turn on response.completed,
	// response.failed, response.incomplete or response.cancelled.
	//
	// For an in-process harness, where the status edges carry no response id and
	// the idle edge therefore never arrives.
	TurnEndsOnResponseLifecycle
)

// TurnOptions configures how the SDK decides one turn has ended.
//
// Which harness a session runs is the caller's knowledge, not this package's, so
// [TurnOptions.End] is a value the caller supplies rather than a name this package
// maps. The rules it selects are here.
type TurnOptions struct {
	// End selects the signal that ends the turn. The zero value is the stricter
	// rule; see [TurnEndsOnIdleStatus].
	End TurnEnd

	// PriorResponseIDs are the responses already on the session before this turn's
	// prompt was posted.
	//
	// Supplying them is what separates this turn's end from an older one's. A
	// response that was live before the prompt goes on running server-side and can
	// reach a terminal inside this turn's window; its event is otherwise
	// indistinguishable from this turn's, and taken as this turn's it ends the read
	// early against another turn's reply.
	//
	// Read once, at the start. Build it from [Sessions.Get]:
	// [SessionResponse.ActiveResponseID] names the response in flight, which is the
	// one that can reach a terminal inside this turn's window. A session with
	// nothing in flight needs no entry.
	//
	// Only an entry whose value is true counts, so a map built with false values
	// gives no protection.
	PriorResponseIDs map[string]bool
}

// turnTracker decides when one turn has ended, from the events a session emits.
//
// A turn is not a stateless predicate over one event. Three facts outside any
// single event decide whether an event belongs to this turn: whether the server
// has echoed this turn's prompt yet, which responses predate it, and which signal
// this harness family puts the end on. The tracker holds exactly those.
type turnTracker struct {
	end   TurnEnd
	prior map[string]bool

	// sessionID is the session this turn is reading, so an edge belonging to
	// another session on the same stream can be told apart from this turn's.
	sessionID string

	// anchor is the identifier the send returned, and what an echo is matched
	// against. Empty until a caller anchors the turn.
	anchor string

	// anchorItem is the conversation item this turn's prompt became, learned from
	// the echo. Empty until the boundary is crossed.
	anchorItem string

	// crossed records that the server has echoed this turn's prompt. Before it, no
	// response can belong to this turn.
	crossed bool

	// id is the response this turn ended on. Empty until it ends.
	id string

	// failedTurnID is the response a failed edge named, kept apart from id because
	// a failed turn did not end with an answer. A caller salvaging a partial reply
	// reads this, and nothing else may.
	failedTurnID string

	// failure is the first fatal signal, written once, so the cause a caller
	// reports is the one that actually stopped the turn.
	failure error
}

func newTurnTracker(opts TurnOptions, sessionID string) *turnTracker {
	return &turnTracker{
		end:       opts.End,
		prior:     maps.Clone(opts.PriorResponseIDs),
		sessionID: sessionID,
	}
}

// describesThisSession reports whether a status edge is about the session this turn
// is reading.
//
// A sub-agent's events are mirrored into an ancestor's stream — which is why
// [Sessions.ResolveElicitation] has to be told which session a request names — so
// an edge on this stream is not necessarily this session's. A child's idle edge
// names the child's response, and taken as this turn's it ends the read against
// work that was never this turn's.
//
// The description marks conversation_id required and non-nullable on a status edge,
// so an omission is not a conforming server: it is a relay that dropped a field, or
// a sender hoping an omission reads as consent. Taken only on the failed branch,
// where dropping it would lose a session-level fault a caller is waiting on, and
// where ending the turn early is the safe direction anyway.
func (t *turnTracker) describesThisSession(conversationID string) bool {
	return conversationID == t.sessionID
}

// couldBeThisSession is describesThisSession relaxed for a failed edge only.
//
// A session-level fault is the one case the server reports with no session named,
// and losing it would leave a caller waiting on a turn that already failed. Ending
// early on a foreign fault is the safe direction; ignoring this session's own is
// not.
func (t *turnTracker) couldBeThisSession(conversationID string) bool {
	return conversationID == "" || conversationID == t.sessionID
}

// anchorOn names the identifier the server will echo for this turn's prompt.
//
// Either the item id the post returned, or the pending id it was parked under.
// Until this is set, no event can cross the boundary.
func (t *turnTracker) anchorOn(identifier string) { t.anchor = identifier }

// ended reports that there is nothing further to read for this turn.
func (t *turnTracker) ended() bool { return t.id != "" || t.failure != nil }

// fail records the first fatal signal and ignores the rest.
func (t *turnTracker) fail(err error) {
	if t.failure == nil {
		t.failure = err
	}
}

// crossBoundary marks the point after which an event can belong to this turn.
//
// Matched on either identifier, because the anchor is whichever the post returned:
// a prompt persisted straight away is echoed by its item id, and one parked as a
// pending input is echoed by the pending id it drains, on the same event. The item
// id is compared first because it is always populated, so a turn holding a pending
// anchor is not matched by an unrelated message's item.
func (t *turnTracker) crossBoundary(e SessionInputConsumedEvent) {
	// Nothing to compare against until a post has named an anchor. Both
	// comparisons below are between strings, so an absent item id — which decodes
	// to "" — would match an unanchored turn and unlock every rule that waits on
	// the boundary.
	if t.anchor == "" {
		return
	}
	if e.Data.ItemID == t.anchor {
		t.crossed = true
		t.anchorItem = e.Data.ItemID
		return
	}
	if e.Data.ClearedPendingID != nil && *e.Data.ClearedPendingID == t.anchor {
		t.crossed = true
		// The item the pending input drained into: always populated, and the only
		// one of the two identifiers that names a conversation item.
		t.anchorItem = e.Data.ItemID
	}
}

// observeStatus reads a coarse session status edge.
//
// An idle edge naming a response, after the boundary, is the end of the turn. A
// bare idle edge is session churn, so a missing response id makes the edge noise
// rather than a wildcard.
//
// A failed edge is the exception to that: one naming no response is how the server
// reports a session-level fault — a sandbox that never launched among them — and
// before the boundary that is precisely the failure a caller is waiting on.
func (t *turnTracker) observeStatus(e SessionStatusEvent) {
	if e.Status == SessionStatusEventStatusFailed {
		if t.couldBeThisSession(e.ConversationID) {
			t.observeFailedStatus(e)
		}
		return
	}
	if !t.describesThisSession(e.ConversationID) {
		return
	}
	if e.Status != SessionStatusEventStatusIdle || !t.crossed {
		return
	}
	if e.ResponseID == nil || *e.ResponseID == "" {
		return
	}
	if t.prior[*e.ResponseID] {
		// Live before this turn's prompt, so it cannot be the turn that answers it.
		// This is the reachable half of the overlapping-turn hazard: another turn
		// ending inside this window emits an edge otherwise identical to this
		// turn's.
		return
	}
	t.id = *e.ResponseID
}

// observeFailedStatus takes a failed edge unless it names a response that cannot
// be this turn's.
//
// A failure naming a response says which turn failed, and there are two ways it is
// not this one: the response predates this turn, or this turn has not spoken yet so
// no response can belong to it. A failure naming no response is not narrowed that
// way, and is taken.
func (t *turnTracker) observeFailedStatus(e SessionStatusEvent) {
	if e.ResponseID != nil && (!t.crossed || t.prior[*e.ResponseID]) {
		return
	}
	if t.crossed && e.ResponseID != nil {
		t.failedTurnID = *e.ResponseID
	}
	t.fail(fmt.Errorf("%w: %s", ErrTurnFailed, statusDetail(e)))
}

// observeResponseTerminal reads a response-lifecycle terminal event.
//
// Ignored under [TurnEndsOnIdleStatus], where the same event means only that the
// prompt reached the harness. The prior check is the one the idle path also makes:
// a response live before this turn's prompt can complete inside this window, and
// its event is otherwise indistinguishable from this turn's.
//
// The terminal naming this turn's response ends it. A tool loop's passes happen
// inside that one response — which is why the response id holds across them, and why
// [BlockContext.Iteration] counts passes rather than responses — so there is one
// terminal to read.
//
// The description states no status that would distinguish a pass's terminal from a
// turn's, so if a deployment ever emitted one per pass, nothing here could tell them
// apart and this would end the turn at the first. That is a contract the server
// would have to state; it is not something to guess at from this side.
func (t *turnTracker) observeResponseTerminal(responseID string, detail error) {
	if t.end != TurnEndsOnResponseLifecycle || !t.crossed || responseID == "" || t.prior[responseID] {
		return
	}
	if detail != nil {
		t.failedTurnID = responseID
		t.fail(detail)
		return
	}
	t.id = responseID
}

// observeSuperseded ends the turn, loudly, because the session it was reading has
// been replaced.
//
// The conversation this turn belongs to is retired, so an answer read here would
// land where nothing is watching, and a caller holding the old session id would go
// on addressing a dead one. Reported rather than followed: which session to
// address next is the caller's decision, not this package's.
func (t *turnTracker) observeSuperseded(e SessionSupersededEvent) {
	// Filtered like a status edge, and for a sharper reason: this error names the
	// conversation a caller should move to, so an event from another conversation on
	// this stream would redirect them to one of its choosing.
	if !t.describesThisSession(e.ConversationID) {
		return
	}
	t.fail(fmt.Errorf("%w: the session was superseded; its conversation is now %s",
		ErrTurnSuperseded, sanitizeForError(e.TargetConversationID, maxRequestIDRunes)))
}

// statusDetail renders a failed edge's reason, which is why a caller watches this
// event rather than inferring failure from silence.
func statusDetail(e SessionStatusEvent) string {
	if e.Error == nil {
		return "the session reported failure, with no detail"
	}
	code := sanitizeForError(e.Error.Code, maxErrorFieldRunes)
	message := sanitizeForError(e.Error.Message, maxErrorFieldRunes)
	if code == "" && message == "" {
		return "the session reported failure, with no detail"
	}
	return fmt.Sprintf("the session reported failure: %s (%s)", message, code)
}

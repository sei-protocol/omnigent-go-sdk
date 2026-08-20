package omnigent

import (
	"errors"
	"strings"
	"testing"
)

// The turn tracker's rules were established in the agentic driver, each by a
// failure that reached a real run. These tests carry the same assertions, so the
// driver can delete its copy and keep the guarantees.

func statusEvent(status string, responseID *string, detail *ErrorDetail) SessionStatusEvent {
	return SessionStatusEvent{
		Type:           "session.status",
		ConversationID: "conv_1",
		Status:         status,
		ResponseID:     responseID,
		Error:          detail,
	}
}

func consumed(itemID string, pendingID *string) SessionInputConsumedEvent {
	return SessionInputConsumedEvent{
		Type: "session.input_consumed",
		Data: SessionInputConsumedPayload{ItemID: itemID, ClearedPendingID: pendingID},
	}
}

// TestCrossBoundaryMatchesEitherIdentifier pins that the echo is matched on the
// item id or the pending id, because the anchor is whichever the post returned.
func TestCrossBoundaryMatchesEitherIdentifier(t *testing.T) {
	t.Parallel()

	t.Run("item id", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("item_1")
		tr.crossBoundary(consumed("item_1", nil))
		if !tr.crossed {
			t.Error("an echo naming the anchored item did not cross the boundary")
		}
		if tr.anchorItem != "item_1" {
			t.Errorf("anchorItem = %q, want item_1", tr.anchorItem)
		}
	})

	t.Run("pending id drains into an item", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("pending_1")
		pending := "pending_1"
		tr.crossBoundary(consumed("item_9", &pending))
		if !tr.crossed {
			t.Error("an echo draining the anchored pending id did not cross")
		}
		// The item id, not the pending id: it is the one that names a conversation item.
		if tr.anchorItem != "item_9" {
			t.Errorf("anchorItem = %q, want item_9", tr.anchorItem)
		}
	})

	t.Run("another message's echo does not cross", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("pending_1")
		tr.crossBoundary(consumed("item_9", nil))
		if tr.crossed {
			t.Error("an unrelated echo crossed the boundary")
		}
	})
}

// TestCrossBoundaryNeedsAnAnchorFirst pins the guard that stops an absent item id
// from matching an unanchored turn.
//
// Both comparisons are between strings, so without it an echo carrying no item id
// matches anchor "" and unlocks every rule that waits on the boundary.
func TestCrossBoundaryNeedsAnAnchorFirst(t *testing.T) {
	t.Parallel()

	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.crossBoundary(consumed("", nil))
	if tr.crossed {
		t.Error("an unanchored turn crossed the boundary on an empty echo")
	}
}

// TestObserveStatusEndsOnlyOnAnIdBearingIdleAfterTheBoundary pins the three things
// an idle edge needs before it is this turn's end.
func TestObserveStatusEndsOnlyOnAnIdBearingIdleAfterTheBoundary(t *testing.T) {
	t.Parallel()

	id := "resp_1"

	t.Run("before the boundary", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, &id, nil))
		if tr.ended() {
			t.Error("an idle edge before the boundary ended the turn")
		}
	})

	t.Run("carrying no response id", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("item_1")
		tr.crossBoundary(consumed("item_1", nil))
		tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, nil, nil))
		if tr.ended() {
			t.Error("a bare idle edge ended the turn; session churn is not an answer")
		}
	})

	t.Run("after the boundary, naming a response", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("item_1")
		tr.crossBoundary(consumed("item_1", nil))
		tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, &id, nil))
		if !tr.ended() {
			t.Fatal("an id-bearing idle edge after the boundary did not end the turn")
		}
		if tr.id != id {
			t.Errorf("id = %q, want %q", tr.id, id)
		}
	})
}

// TestObserveStatusIgnoresAnIdleEdgeForAResponseThatPredatesTheTurn pins the prior
// set. A turn already running when this one starts ends inside this window, and its
// edge is otherwise identical to this turn's.
func TestObserveStatusIgnoresAnIdleEdgeForAResponseThatPredatesTheTurn(t *testing.T) {
	t.Parallel()

	old := "resp_old"
	tr := newTurnTracker(TurnOptions{PriorResponseIDs: map[string]bool{old: true}}, "conv_1")
	tr.anchorOn("item_1")
	tr.crossBoundary(consumed("item_1", nil))
	tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, &old, nil))
	if tr.ended() {
		t.Error("an older turn's idle edge ended this turn")
	}
}

// TestObserveStatusIgnoresAFailureThatPredatesTheTurn pins the same narrowing on
// the failure path. Taken as this turn's it ends the read, and a caller salvaging a
// reply would publish another turn's.
func TestObserveStatusIgnoresAFailureThatPredatesTheTurn(t *testing.T) {
	t.Parallel()

	old := "resp_old"
	tr := newTurnTracker(TurnOptions{PriorResponseIDs: map[string]bool{old: true}}, "conv_1")
	tr.anchorOn("item_1")
	tr.crossBoundary(consumed("item_1", nil))
	tr.observeStatus(statusEvent(SessionStatusEventStatusFailed, &old, &ErrorDetail{Message: "old"}))
	if tr.ended() {
		t.Error("an older turn's failure ended this turn")
	}
}

// TestObserveStatusIgnoresAnIdBearingFailureBeforeTheBoundary pins that a failure
// naming a response cannot be this turn's before this turn has spoken.
func TestObserveStatusIgnoresAnIdBearingFailureBeforeTheBoundary(t *testing.T) {
	t.Parallel()

	id := "resp_1"
	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.observeStatus(statusEvent(SessionStatusEventStatusFailed, &id, &ErrorDetail{Message: "not ours"}))
	if tr.ended() {
		t.Error("a failure naming a response ended a turn that had not spoken")
	}
}

// TestObserveStatusTakesASessionLevelFailureBeforeTheBoundary pins the exception: a
// failure naming no response is a session-level fault, and before the boundary it
// is precisely what a caller is waiting on. A sandbox that never launched reports
// this way.
func TestObserveStatusTakesASessionLevelFailureBeforeTheBoundary(t *testing.T) {
	t.Parallel()

	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.observeStatus(statusEvent(SessionStatusEventStatusFailed, nil,
		&ErrorDetail{Code: "sandbox_launch_failed", Message: "no sandbox"}))
	if !tr.ended() {
		t.Fatal("a session-level failure did not end the turn")
	}
	if !errors.Is(tr.failure, ErrTurnFailed) {
		t.Errorf("failure = %v, want it to wrap ErrTurnFailed", tr.failure)
	}
	if !strings.Contains(tr.failure.Error(), "no sandbox") {
		t.Errorf("the server's reason was dropped: %v", tr.failure)
	}
}

// TestObserveResponseTerminalBelongsToTheLifecycleModeOnly pins the harness split.
// Under TurnEndsOnIdleStatus the same event means only that the prompt reached the
// harness, so taking it would hand back a partial answer.
func TestObserveResponseTerminalBelongsToTheLifecycleModeOnly(t *testing.T) {
	t.Parallel()

	cross := func(tr *turnTracker) {
		tr.anchorOn("item_1")
		tr.crossBoundary(consumed("item_1", nil))
	}

	t.Run("idle-status mode ignores it", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{End: TurnEndsOnIdleStatus}, "conv_1")
		cross(tr)
		tr.observeResponseTerminal("resp_1", nil)
		if tr.ended() {
			t.Error("a lifecycle terminal ended a turn whose end is the idle edge")
		}
	})

	t.Run("lifecycle mode takes it", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{End: TurnEndsOnResponseLifecycle}, "conv_1")
		cross(tr)
		tr.observeResponseTerminal("resp_1", nil)
		if tr.id != "resp_1" {
			t.Errorf("id = %q, want resp_1", tr.id)
		}
	})

	t.Run("lifecycle mode still narrows on prior", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{
			End:              TurnEndsOnResponseLifecycle,
			PriorResponseIDs: map[string]bool{"resp_old": true},
		}, "conv_1")
		cross(tr)
		tr.observeResponseTerminal("resp_old", nil)
		if tr.ended() {
			t.Error("an older response's terminal ended this turn")
		}
	})

	t.Run("lifecycle mode needs the boundary", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{End: TurnEndsOnResponseLifecycle}, "conv_1")
		tr.observeResponseTerminal("resp_1", nil)
		if tr.ended() {
			t.Error("a lifecycle terminal ended a turn that had not spoken")
		}
	})

	t.Run("a failed lifecycle terminal records the turn it names", func(t *testing.T) {
		tr := newTurnTracker(TurnOptions{End: TurnEndsOnResponseLifecycle}, "conv_1")
		cross(tr)
		tr.observeResponseTerminal("resp_1", ErrTurnFailed)
		if tr.id != "" {
			t.Error("a failed turn was recorded as ended with an answer")
		}
		if tr.failedTurnID != "resp_1" {
			t.Errorf("failedTurnID = %q, want resp_1", tr.failedTurnID)
		}
	})
}

// TestTheZeroValueTakesTheStricterRule pins the fail-closed default: an unset End
// waits for the idle edge rather than trusting a lifecycle terminal.
func TestTheZeroValueTakesTheStricterRule(t *testing.T) {
	t.Parallel()

	var opts TurnOptions
	if opts.End != TurnEndsOnIdleStatus {
		t.Fatal("the zero value is not the stricter rule")
	}
	tr := newTurnTracker(opts, "conv_1")
	tr.anchorOn("item_1")
	tr.crossBoundary(consumed("item_1", nil))
	tr.observeResponseTerminal("resp_1", nil)
	if tr.ended() {
		t.Error("the default trusted a lifecycle terminal")
	}
}

// TestFailKeepsTheFirstCause pins that the reported cause is the one that stopped
// the turn, not the last symptom to arrive.
func TestFailKeepsTheFirstCause(t *testing.T) {
	t.Parallel()

	first := errors.New("first")
	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.fail(first)
	tr.fail(errors.New("second"))
	if !errors.Is(tr.failure, first) {
		t.Errorf("failure = %v, want the first cause", tr.failure)
	}
}

// TestObserveSupersededFailsLoudly pins that a replaced session is reported rather
// than followed, and names where the conversation went.
func TestObserveSupersededFailsLoudly(t *testing.T) {
	t.Parallel()

	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.observeSuperseded(SessionSupersededEvent{
		Type:                 "session.superseded",
		ConversationID:       "conv_1",
		TargetConversationID: "conv_2",
	})
	if !errors.Is(tr.failure, ErrTurnSuperseded) {
		t.Fatalf("failure = %v, want it to wrap ErrTurnSuperseded", tr.failure)
	}
	if !strings.Contains(tr.failure.Error(), "conv_2") {
		t.Errorf("the replacement conversation was not named: %v", tr.failure)
	}
}

// TestPriorResponseIDsAreCopied pins that a caller's map cannot change the rules
// after the turn starts.
func TestPriorResponseIDsAreCopied(t *testing.T) {
	t.Parallel()

	prior := map[string]bool{"resp_old": true}
	tr := newTurnTracker(TurnOptions{PriorResponseIDs: prior}, "conv_1")
	delete(prior, "resp_old")
	prior["resp_1"] = true

	tr.anchorOn("item_1")
	tr.crossBoundary(consumed("item_1", nil))
	id := "resp_1"
	tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, &id, nil))
	if tr.id != "resp_1" {
		t.Error("a mutation after the turn started changed which responses count")
	}
}

// TestAnotherSessionsStatusEdgeDoesNotEndThisTurn pins the fourth fact a turn needs.
//
// A sub-agent's events are mirrored into an ancestor's stream — which is why
// resolving an elicitation has to be told which session named it — so an edge on
// this stream is not necessarily this session's. Measured before the filter existed:
// a child's idle edge ended the turn against work that was never this turn's, and
// the caller read half an answer with a nil error.
func TestAnotherSessionsStatusEdgeDoesNotEndThisTurn(t *testing.T) {
	t.Parallel()

	child := func(status string, responseID *string, detail *ErrorDetail) SessionStatusEvent {
		e := statusEvent(status, responseID, detail)
		e.ConversationID = "conv_child"
		return e
	}
	crossed := func() *turnTracker {
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		tr.anchorOn("item_1")
		tr.crossBoundary(consumed("item_1", nil))
		return tr
	}

	t.Run("a child's idle edge", func(t *testing.T) {
		tr := crossed()
		id := "resp_of_the_subagent"
		tr.observeStatus(child(SessionStatusEventStatusIdle, &id, nil))
		if tr.ended() {
			t.Errorf("a child session's idle edge ended this turn with id %q", tr.id)
		}
	})

	t.Run("a child's failure naming no response", func(t *testing.T) {
		tr := crossed()
		tr.observeStatus(child(SessionStatusEventStatusFailed, nil,
			&ErrorDetail{Code: "spawn_failed", Message: "the sub-agent sandbox never launched"}))
		if tr.ended() {
			t.Errorf("a child's setup failure ended this turn: %v", tr.failure)
		}
	})

	t.Run("this session's edges still count", func(t *testing.T) {
		tr := crossed()
		id := "resp_1"
		tr.observeStatus(statusEvent(SessionStatusEventStatusIdle, &id, nil))
		if tr.id != "resp_1" {
			t.Errorf("the filter rejected this session's own edge; id = %q", tr.id)
		}
	})

	t.Run("an edge naming no session is taken", func(t *testing.T) {
		// The server reports a session-level fault this way, and refusing it would
		// drop the failure a caller is waiting on.
		tr := newTurnTracker(TurnOptions{}, "conv_1")
		e := statusEvent(SessionStatusEventStatusFailed, nil, &ErrorDetail{Message: "no sandbox"})
		e.ConversationID = ""
		tr.observeStatus(e)
		if !tr.ended() {
			t.Error("a session-level failure with no session named was dropped")
		}
	})
}

// TestSupersededNamesTheReplacementSafely pins that a server-chosen id cannot forge
// a log line through this error.
func TestSupersededNamesTheReplacementSafely(t *testing.T) {
	t.Parallel()

	tr := newTurnTracker(TurnOptions{}, "conv_1")
	tr.observeSuperseded(SessionSupersededEvent{
		Type:                 "session.superseded",
		ConversationID:       "conv_1",
		TargetConversationID: "conv_2\r\nFATAL: forged" + strings.Repeat("A", 4000),
	})
	msg := tr.failure.Error()
	if strings.ContainsAny(msg, "\r\n") {
		t.Errorf("the error carries a control byte: %q", msg)
	}
	if len(msg) > 400 {
		t.Errorf("the error is %d bytes; the id is unbounded", len(msg))
	}
	if !strings.Contains(msg, "conv_2") {
		t.Errorf("the replacement conversation was lost: %q", msg)
	}
}

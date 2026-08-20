package omnigent

import (
	"context"
	"errors"
	"testing"
)

// TestGateFetchHoldsASlotForOneFetchOnly pins both halves of the contract: a
// slot is held while a request is in flight, and released before the next one.
//
// Asserted on the function rather than through a walk, because the walk's benefit
// is a scheduling property and a timing assertion on it passes on a lucky
// schedule — which is a test that cannot fail.
func TestGateFetchHoldsASlotForOneFetchOnly(t *testing.T) {
	tokens := make(chan struct{}, 1)

	free := func() bool {
		select {
		case tokens <- struct{}{}:
			<-tokens
			return true
		default:
			return false
		}
	}

	var heldDuringFetch bool
	gated := gateFetch(tokens, func(context.Context, string) (*Page[ChildSessionSummary], error) {
		heldDuringFetch = !free()
		return &Page[ChildSessionSummary]{}, nil
	})

	if !free() {
		t.Fatal("a slot was held before any fetch")
	}
	if _, err := gated(context.Background(), ""); err != nil {
		t.Fatalf("gated fetch: %v", err)
	}
	if !heldDuringFetch {
		t.Error("no slot was held while the request was in flight")
	}
	if !free() {
		t.Error("the slot was still held after the fetch returned")
	}
}

// TestGateFetchStopsWaitingOnACancelledContext pins that a cancelled walk stops
// waiting for a slot. Without it a caller's cancel is honoured only after the
// listings already holding slots drain.
func TestGateFetchStopsWaitingOnACancelledContext(t *testing.T) {
	tokens := make(chan struct{}, 1)
	tokens <- struct{}{} // the only slot is taken and never released

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var reached bool
	gated := gateFetch(tokens, func(context.Context, string) (*Page[ChildSessionSummary], error) {
		reached = true
		return &Page[ChildSessionSummary]{}, nil
	})

	_, err := gated(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if reached {
		t.Error("the fetch ran without a slot")
	}
}

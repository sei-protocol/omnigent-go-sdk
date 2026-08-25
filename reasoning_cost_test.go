package omnigent

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// TestTheReasoningFoldAllocatesLinearly pins that folding a reasoning section
// costs a small multiple of the section's own size.
//
// Three accumulators here once appended to a plain string, and the newline scan
// once walked the whole buffer per delta. Both are quadratic in the section's
// length, and both ran on the goroutine draining the socket with the idle
// monitor suspended — so a long reasoning section made the client slow and
// silent at the same time. Measured before the fix: 1.4 GB of allocation to fold
// 343 KB, and 37s of CPU at 31 MiB.
//
// Allocation rather than wall time, because a timing assertion is a flake on a
// shared machine. The ratio is what distinguishes linear from quadratic; the
// bound is loose on purpose.
//
// Not parallel, and it must not become so: runtime.ReadMemStats reads a
// process-global counter, so any test allocating alongside this one is charged to
// the fold.
func TestTheReasoningFoldAllocatesLinearly(t *testing.T) {
	const (
		delta   = 64
		deltas  = 20_000
		maxCost = 40 // allocated bytes per input byte
	)

	// No newline in the payload: the section never flushes a chunk, so the buffer
	// grows to its full length. That is the shape that paid the whole cost.
	payload := strings.Repeat("x", delta)

	events := func(yield func(Event, error) bool) {
		if !yield(ResponseCreatedEvent{Type: "response.created", Response: ResponseObject{ID: "r1"}}, nil) {
			return
		}
		for range deltas {
			e := ReasoningTextDeltaEvent{Type: "response.reasoning_text.delta", Delta: payload}
			if !yield(e, nil) {
				return
			}
		}
		yield(ResponseCompletedEvent{
			Type:     "response.completed",
			Response: ResponseObject{ID: "r1", Status: "completed"},
		}, nil)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	blocks := 0
	for range (&BlockStream{}).Blocks(events) {
		blocks++
	}

	runtime.ReadMemStats(&after)

	input := int64(delta) * deltas
	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	ratio := float64(allocated) / float64(input)
	t.Logf("folded %s of reasoning in %d blocks, allocating %s (%.1f bytes per input byte)",
		bytesHuman(input), blocks, bytesHuman(allocated), ratio)

	// A fold that yields nothing allocates nothing and would clear any bound, so
	// the ratio only means something once the reasoning reached the output.
	if blocks == 0 {
		t.Fatal("the fold produced no blocks, so the cost bound measures nothing")
	}
	if ratio > maxCost {
		t.Errorf("allocated %.1f bytes per input byte, want at most %d: the fold is "+
			"copying the accumulated section rather than appending to it", ratio, maxCost)
	}
}

func bytesHuman(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

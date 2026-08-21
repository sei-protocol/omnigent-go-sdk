package omnigent

import "testing"

// TestAttributionRecoversWhenAnOverlapEnds pins that a sub-agent's turn does not
// silence the ones after it.
//
// A text delta names no response, so the fold infers one. While two responses are
// live that inference is unsound and the fold credits nothing — correctly. It used
// to stop crediting permanently, so a single mirrored sub-agent made [OnlyAgent]
// drop every later block for the life of the subscription. The ambiguity is the
// overlap, not the stream.
func TestAttributionRecoversWhenAnOverlapEnds(t *testing.T) {
	t.Parallel()

	start := func(id, model string) Event {
		return InProgressEvent{Type: "response.in_progress",
			Response: ResponseObject{ID: id, Model: model, Status: "in_progress"}}
	}
	end := func(id string) Event {
		return ResponseCompletedEvent{Type: "response.completed",
			Response: ResponseObject{ID: id, Status: "completed"}}
	}
	delta := func(text string) Event {
		return OutputTextDeltaEvent{Type: "response.output_text.delta", Delta: text}
	}

	events := func(yield func(Event, error) bool) {
		for _, e := range []Event{
			// Turn one overlaps with a mirrored sub-agent.
			start("r1", "coder"),
			start("r2", "coder.researcher"),
			delta("ambiguous, two live. "),
			end("r2"),
			end("r1"),
			// Turn two: one response, so attribution is sound again.
			start("r3", "coder"),
			delta("turn two, one live."),
			end("r3"),
		} {
			if !yield(e, nil) {
				return
			}
		}
	}

	var agents []string
	for block, err := range (&BlockStream{}).Blocks(events) {
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		if done, ok := block.(TextDone); ok {
			agents = append(agents, done.Context().Agent)
		}
	}

	if len(agents) != 2 {
		t.Fatalf("got %d TextDone blocks (%v), want 2", len(agents), agents)
	}
	// The overlapping delta stays uncredited: honestly unknown beats confidently
	// wrong, which is what crediting the last-started response would be.
	if agents[0] != "" {
		t.Errorf("the overlapping delta was credited to %q, want no agent", agents[0])
	}
	// And the turn after it is credited again.
	if agents[1] != "coder" {
		t.Errorf("the single-response turn after the overlap was credited to %q, "+
			"want coder: attribution did not recover", agents[1])
	}
}

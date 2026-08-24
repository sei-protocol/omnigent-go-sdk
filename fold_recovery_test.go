package omnigent

import "testing"

// textDoneOf returns the finished text sections and the agent each was credited
// to, which is what a caller rendering a transcript reads.
func textDoneOf(blocks []Block) (texts, agents []string) {
	for _, block := range blocks {
		if done, ok := block.(TextDone); ok {
			texts = append(texts, done.FullText)
			agents = append(agents, done.Context().Agent)
		}
	}
	return texts, agents
}

// TestADroppedStreamStillReportsTheSecondTurnsText pins that the salvage flush is
// per response, not per subscription.
//
// A tool loop reaches several terminals on one stream, and [Client.Stream] folds
// the whole subscription in one call. A flag that only ever became true meant the
// first terminal disarmed the salvage for every turn after it: the answer a caller
// was mid-way through reading was dropped rather than delivered.
func TestADroppedStreamStillReportsTheSecondTurnsText(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.in_progress","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"first answer"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
		`{"type":"response.in_progress","response":{"id":"r2","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"second answer"}`,
	)
	texts, _ := textDoneOf(blocks)
	if len(texts) != 2 {
		t.Fatalf("the fold reported %d finished sections %q, want both: the second "+
			"turn's answer is what a dropped stream loses", len(texts), texts)
	}
	if texts[1] != "second answer" {
		t.Errorf("the second section is %q, want %q", texts[1], "second answer")
	}
}

// TestAReannouncementWithoutAModelKeepsTheAgentName pins that a response keeps the
// name it was announced under.
//
// A server announces one response on created, queued and in_progress, and the
// later frames need not repeat the model. Overwriting on every announcement wrote
// the empty string over a known name, and the fold then credited the turn's own
// words to nobody — the ambiguity the attribution rule exists to report honestly,
// raised where there is no ambiguity at all.
func TestAReannouncementWithoutAModelKeepsTheAgentName(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.in_progress","response":{"id":"r1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"still coder speaking"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	_, agents := textDoneOf(blocks)
	if len(agents) != 1 || agents[0] != "coder" {
		t.Fatalf("the fold credited %q, want one section credited to coder", agents)
	}
}

// TestAQueuedResponseIsNotASecondSpeaker pins that queued input does not blank
// attribution.
//
// A second prompt typed while the agent is still working is queued, and the
// session API names queued input one of its headline properties. A queued response
// has not begun speaking, so counting it as live made the ordinary case — type
// ahead, then read the answer — report an uncredited turn.
func TestAQueuedResponseIsNotASecondSpeaker(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.in_progress","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.queued","response":{"id":"r2","model":"coder","status":"queued"}}`,
		`{"type":"response.output_text.delta","delta":"r1 is still the only one talking."}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	_, agents := textDoneOf(blocks)
	if len(agents) != 1 || agents[0] != "coder" {
		t.Fatalf("the fold credited %q, want one section credited to coder", agents)
	}
	// The queued response is still announced, because a caller counting responses
	// wants to know one is waiting.
	starts := 0
	for _, block := range blocks {
		if _, ok := block.(ResponseStartBlock); ok {
			starts++
		}
	}
	if starts != 2 {
		t.Errorf("the fold drew %d start blocks, want 2: a queued response is "+
			"announced even though it is not yet speaking", starts)
	}
}

package omnigent

import (
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"
)

// eventsFrom decodes wire frames the way the stream does, so a block test is
// driven by the same bytes a server would send.
func eventsFrom(t *testing.T, frames ...string) iter.Seq2[Event, error] {
	t.Helper()
	assertKnownFrames(t, frames)
	decoded := make([]Event, 0, len(frames))
	for _, frame := range frames {
		event, err := DecodeEvent([]byte(frame))
		if err != nil {
			t.Fatalf("DecodeEvent(%s): %v", frame, err)
		}
		decoded = append(decoded, event)
	}
	return func(yield func(Event, error) bool) {
		for _, event := range decoded {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func foldBlocks(t *testing.T, threshold int, frames ...string) []Block {
	t.Helper()
	stream := &BlockStream{TextFlushThreshold: threshold}
	var blocks []Block
	for block, err := range stream.Blocks(eventsFrom(t, frames...)) {
		if err != nil {
			t.Fatalf("Blocks: %v", err)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func kindsOf(blocks []Block) string {
	var out []string
	for _, b := range blocks {
		out = append(out, strings.TrimPrefix(fmt.Sprintf("%T", b), "omnigent."))
	}
	return strings.Join(out, ",")
}

// TestBlocksReportAResponseOnce pins that created, queued and in_progress produce
// one start. The server announces a response on all three.
func TestBlocksReportAResponseOnce(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.queued","response":{"id":"r1","model":"coder","status":"queued"}}`,
		`{"type":"response.in_progress","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
	)
	if got := kindsOf(blocks); got != "ResponseStartBlock" {
		t.Fatalf("got %s, want one ResponseStartBlock", got)
	}
	start := blocks[0].(ResponseStartBlock)
	if start.ResponseID != "r1" || start.Model != "coder" {
		t.Errorf("start = %+v", start)
	}
}

// TestTextFlushesOnWordBoundaries pins that a chunk never ends mid-word, which a
// renderer cannot un-draw once shown.
func TestTextFlushesOnWordBoundaries(t *testing.T) {
	t.Parallel()

	// The first word is longer than the threshold, deliberately: with short words a
	// cut at a fixed offset lands on a space by luck and proves nothing.
	blocks := foldBlocks(t, 10,
		`{"type":"response.output_text.delta","delta":"internationalization is "}`,
		`{"type":"response.output_text.delta","delta":"a long word "}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	var chunks []string
	var done *TextDone
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextChunk:
			chunks = append(chunks, typed.Text)
		case TextDone:
			copied := typed
			done = &copied
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks; the fixture must flush more than once to test a boundary", len(chunks))
	}
	// Every chunk but the last is cut at a boundary, so it ends with whitespace.
	// The last is the remainder flushed when the section closes, which need not.
	for _, chunk := range chunks[:len(chunks)-1] {
		if strings.TrimRight(chunk, " \n\t") == chunk {
			t.Errorf("chunk %q ends mid-word", chunk)
		}
	}
	if joined := strings.Join(chunks, ""); joined != "internationalization is a long word " {
		t.Errorf("the chunks do not reconstruct the text: %q", joined)
	}
	if done == nil {
		t.Fatal("no TextDone at the end of the section")
	}
	if done.FullText != "internationalization is a long word " {
		t.Errorf("FullText = %q", done.FullText)
	}
}

// TestAnUnbrokenRunIsStillFlushed pins that text with no word boundary is emitted
// rather than held. Waiting for a boundary that never comes holds the whole answer.
func TestAnUnbrokenRunIsStillFlushed(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 50)
	blocks := foldBlocks(t, 10,
		fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, long),
	)
	if kindsOf(blocks) == "" {
		t.Fatal("an unbroken run produced nothing")
	}
	if !strings.Contains(kindsOf(blocks), "TextChunk") {
		t.Errorf("got %s, want a TextChunk", kindsOf(blocks))
	}
}

// TestADoubledToolReportIsOneBlock pins FR-024. Under the MCP path a call and its
// result each surface twice with the same call id, and a caller sees one block.
func TestADoubledToolReportIsOneBlock(t *testing.T) {
	t.Parallel()

	call := `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1",` +
		`"name":"Read","arguments":"{\"file_path\":\"/a/b/test.py\"}","agent_name":"coder"}}`
	result := `{"type":"response.output_item.done","item":{"type":"function_call_output",` +
		`"call_id":"c1","output":"file contents"}}`

	blocks := foldBlocks(t, 0, call, call, result, result)
	if got := kindsOf(blocks); got != "ToolGroup,ToolResultBlock" {
		t.Fatalf("got %s, want one ToolGroup and one ToolResultBlock", got)
	}
	group := blocks[0].(ToolGroup)
	if len(group.Executions) != 1 {
		t.Fatalf("the group holds %d executions", len(group.Executions))
	}
	// The summary shows the base name, which is what a reader recognises.
	if group.Executions[0].ArgsSummary != "test.py" {
		t.Errorf("ArgsSummary = %q, want test.py", group.Executions[0].ArgsSummary)
	}
	// The result carries the call's metadata, so a result-only renderer has it.
	got := blocks[1].(ToolResultBlock)
	if got.Name != "Read" || got.ArgsSummary != "test.py" || got.Output != "file contents" {
		t.Errorf("result = %+v", got)
	}
}

// TestAResultWithNoCallStillRenders pins the case where the call was reported
// before this stream was read. Dropping it loses the tool's output entirely.
func TestAResultWithNoCallStillRenders(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.output_item.done","item":{"type":"function_call_output",`+
			`"call_id":"c9","name":"Bash","output":"done"}}`,
	)
	if got := kindsOf(blocks); got != "ToolResultBlock" {
		t.Fatalf("got %s", got)
	}
	if out := blocks[0].(ToolResultBlock).Output; out != "done" {
		t.Errorf("Output = %q", out)
	}
}

// TestReasoningChunksSuppressTheSummaryBlock pins that a renderer showing both does
// not draw the same reasoning twice.
func TestReasoningChunksSuppressTheSummaryBlock(t *testing.T) {
	t.Parallel()

	withChunks := foldBlocks(t, 0,
		`{"type":"response.reasoning.started"}`,
		`{"type":"response.reasoning_text.delta","delta":"first line\nsecond line\n"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	if strings.Contains(kindsOf(withChunks), "ReasoningBlock") {
		t.Errorf("a section that streamed chunks also emitted a summary: %s", kindsOf(withChunks))
	}
	if !strings.Contains(kindsOf(withChunks), "ReasoningChunk") {
		t.Errorf("no chunks were emitted: %s", kindsOf(withChunks))
	}

	// No newline, so nothing streamed: the summary is what carries the reasoning.
	withoutChunks := foldBlocks(t, 0,
		`{"type":"response.reasoning.started"}`,
		`{"type":"response.reasoning_text.delta","delta":"unterminated thought"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	if !strings.Contains(kindsOf(withoutChunks), "ReasoningChunk") &&
		!strings.Contains(kindsOf(withoutChunks), "ReasoningBlock") {
		t.Errorf("the reasoning was lost: %s", kindsOf(withoutChunks))
	}
}

// TestTextEndsAnOpenReasoningSection pins that the model moving from thinking to
// answering closes the section, which no event states on its own.
func TestTextEndsAnOpenReasoningSection(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.reasoning.started"}`,
		// No trailing newline, deliberately: this reasoning is flushed only when
		// the section closes, so the order proves what closed it.
		`{"type":"response.reasoning_text.delta","delta":"thinking"}`,
		`{"type":"response.output_text.delta","delta":"the answer is 42"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	got := kindsOf(blocks)
	// Closed by the text arriving, so it lands before it. Either shape carries the
	// reasoning; what matters is that it precedes the answer.
	reasoningAt := strings.IndexAny(got, "R")
	for _, kind := range []string{"ReasoningChunk", "ReasoningBlock"} {
		if at := strings.Index(got, kind); at >= 0 {
			reasoningAt = at
			break
		}
	}
	textAt := strings.Index(got, "TextChunk")
	if textAt < 0 {
		textAt = strings.Index(got, "TextDone")
	}
	if reasoningAt < 0 || textAt < 0 {
		t.Fatalf("expected both reasoning and text: %s", got)
	}
	if reasoningAt > textAt {
		t.Errorf("reasoning did not close before the answer: %s", got)
	}
}

// TestATerminalResponseClosesWhatIsOpen pins the order a caller reads: the answer,
// then the turn closing.
func TestATerminalResponseClosesWhatIsOpen(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 1000,
		`{"type":"response.output_text.delta","delta":"held back"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	)
	got := kindsOf(blocks)
	if !strings.HasSuffix(got, "ResponseEndBlock") {
		t.Fatalf("got %s, want the end last", got)
	}
	if !strings.Contains(got, "TextDone") {
		t.Errorf("the held text was never reported: %s", got)
	}
}

// TestADroppedStreamStillReportsItsText pins that a stream ending without a
// terminal response does not lose the answer it had gathered.
func TestADroppedStreamStillReportsItsText(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 1000, `{"type":"response.output_text.delta","delta":"partial"}`)
	if !strings.Contains(kindsOf(blocks), "TextDone") {
		t.Fatalf("got %s, want the text flushed", kindsOf(blocks))
	}
}

// TestBlocksPassAnErrorThrough pins that a failing stream is visible to a caller
// reading blocks, not silently truncated.
func TestBlocksPassAnErrorThrough(t *testing.T) {
	t.Parallel()

	boom := fmt.Errorf("transport died")
	failing := func(yield func(Event, error) bool) {
		yield(nil, boom)
	}
	stream := &BlockStream{}
	var errs []error
	for _, err := range stream.Blocks(failing) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want the one the stream reported", len(errs))
	}
}

// TestNativeToolAndFileAndRetryFold pins the remaining single-event branches.
func TestNativeToolAndFileAndRetryFold(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.output_item.done","item":{"type":"web_search_call",`+
			`"data":{"action":{"type":"search","query":"go iterators"}}}}`,
		`{"type":"response.output_file.done","file_id":"file_1","filename":"out.csv"}`,
		`{"type":"response.retry","source":"tool","attempt":2,"max_attempts":3,"delay_seconds":1.5,`+
			`"error":{"code":"timeout","message":"slow"}}`,
		`{"type":"response.compaction.in_progress"}`,
		`{"type":"response.error","source":"llm","error":{"code":"llm_auth_failed","message":"bad key"}}`,
	)
	if got := kindsOf(blocks); got != "NativeToolBlock,FileBlock,RetryBlock,CompactionBlock,ErrorBlock" {
		t.Fatalf("got %s", got)
	}
	if label := blocks[0].(NativeToolBlock).Label; label != "web search: go iterators" {
		t.Errorf("Label = %q", label)
	}
	if file := blocks[1].(FileBlock); file.Filename == nil || *file.Filename != "out.csv" {
		t.Errorf("file = %+v", file)
	}
	if retry := blocks[2].(RetryBlock); retry.Delay.Milliseconds() != 1500 {
		t.Errorf("Delay = %v, want 1.5s", retry.Delay)
	}
	if e := blocks[4].(ErrorBlock); e.Code != "llm_auth_failed" || e.Message != "bad key" {
		t.Errorf("error = %+v", e)
	}
}

// TestMalformedToolArgumentsStillRenderTheCall pins that a model emitting bad JSON
// costs the arguments, not the turn.
func TestMalformedToolArgumentsStillRenderTheCall(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1",`+
			`"name":"Bash","arguments":"{not json"}}`,
	)
	if got := kindsOf(blocks); got != "ToolGroup" {
		t.Fatalf("got %s", got)
	}
	execution := blocks[0].(ToolGroup).Executions[0]
	if execution.Name != "Bash" {
		t.Errorf("the call lost its name: %+v", execution)
	}
	if execution.Arguments != nil {
		t.Errorf("malformed arguments decoded to %v", execution.Arguments)
	}
}

// TestFormatToolArgsBriefIsStableAndBounded pins the summary a caller re-rendering
// a recorded turn has to reproduce.
func TestFormatToolArgsBriefIsStableAndBounded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"Read", map[string]any{"file_path": "/long/path/to/file.go"}, "file.go"},
		{"Bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"Grep", map[string]any{"pattern": "func main"}, "func main"},
		{"Unlisted", map[string]any{"k": "v"}, `{"k":"v"}`},
		{"Read", nil, ""},
	}
	for _, tc := range tests {
		if got := FormatToolArgsBrief(tc.name, tc.args); got != tc.want {
			t.Errorf("FormatToolArgsBrief(%s, %v) = %q, want %q", tc.name, tc.args, got, tc.want)
		}
	}

	long := FormatToolArgsBrief("Bash", map[string]any{"command": strings.Repeat("é", 200)})
	if runes := len([]rune(long)); runes > maxArgsSummaryRunes+1 {
		t.Errorf("summary is %d runes, want it bounded", runes)
	}
	if !json.Valid([]byte(`"` + strings.ReplaceAll(long, `"`, `\"`) + `"`)) {
		t.Errorf("truncation produced invalid UTF-8: %q", long)
	}
}

// TestBlocksCarryTheAgentTheyCameFrom pins the context the fold writes.
//
// Driven through the fold rather than through a constructed block: a block built in
// the package proves nothing about the context a stream actually produces.
func TestBlocksCarryTheAgentTheyCameFrom(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.created","response":{"id":"r1","model":"coder.researcher","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"found it"}`,
		`{"type":"response.completed","response":{"id":"r1","model":"coder.researcher","status":"completed"}}`,
	)
	if len(blocks) == 0 {
		t.Fatal("no blocks")
	}
	for _, block := range blocks {
		ctx := block.Context()
		if ctx.Agent != "coder.researcher" {
			t.Errorf("%T reports agent %q, want coder.researcher", block, ctx.Agent)
		}
		if ctx.Depth != 1 {
			t.Errorf("%T reports depth %d, want 1 for a dotted agent", block, ctx.Depth)
		}
	}
}

// TestOnlyAgentFiltersARealFold pins the transform against data the fold produces.
//
// A synthetic block cannot show the transform matches what a stream produces, which
// is the only thing that makes the transform useful.
func TestOnlyAgentFiltersARealFold(t *testing.T) {
	t.Parallel()

	stream := &BlockStream{}
	events := eventsFrom(t,
		`{"type":"response.created","response":{"id":"r1","model":"coder.researcher","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"from the sub-agent"}`,
		`{"type":"response.completed","response":{"id":"r1","model":"coder.researcher","status":"completed"}}`,
	)

	kept := 0
	for block, err := range Pipe(stream.Blocks(events), OnlyAgent("coder.researcher")) {
		if err != nil {
			t.Fatalf("Blocks: %v", err)
		}
		kept++
		_ = block
	}
	if kept == 0 {
		t.Fatal("OnlyAgent dropped every block of the agent it names")
	}

	// And it still excludes another agent.
	other := 0
	for range Pipe(stream.Blocks(eventsFrom(t,
		`{"type":"response.created","response":{"id":"r1","model":"coder.researcher","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"x"}`,
	)), OnlyAgent("someone.else")) {
		other++
	}
	if other != 0 {
		t.Errorf("OnlyAgent kept %d blocks from another agent", other)
	}
}

// TestABlockCannotBeRewrittenFromOutside pins that Context reports what the fold
// recorded.
//
// A block's context is reachable only through the accessor, and is not marshalled:
// an exported embedded field would be promoted to every variant and appear as an
// untagged "Ctx" key in a caller's persisted transcript.
func TestABlockDoesNotMarshalItsContext(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(TextChunk{Text: "hi"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "Ctx") {
		t.Errorf("a block marshals an untagged context key: %s", encoded)
	}
}

// TestAShortAnswerStillStreams pins that text below the flush threshold reaches a
// live reader.
//
// A message item is the server's complete statement of a section, so it is the
// authoritative TextDone. Replacing the buffer with it discarded the deltas, and a
// caller streaming an answer shorter than the threshold saw nothing at all.
func TestAShortAnswerStillStreams(t *testing.T) {
	t.Parallel()

	blocks := foldBlocks(t, 0,
		`{"type":"response.output_text.delta","delta":"short"}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":`+
			`[{"type":"output_text","text":"short"}]}}`,
	)
	var chunked, done bool
	for _, block := range blocks {
		switch block.(type) {
		case TextChunk:
			chunked = true
		case TextDone:
			done = true
		}
	}
	if !chunked {
		t.Errorf("a short answer produced no TextChunk: %s", kindsOf(blocks))
	}
	if !done {
		t.Errorf("a short answer produced no TextDone: %s", kindsOf(blocks))
	}
}

// TestAttributionStopsRatherThanGuessingWhenResponsesInterleave pins the direction
// this fails in.
//
// A text delta names no response, so the fold can only credit it to whichever
// response started last. That is right while one is live and wrong the moment two
// are: a mirrored sub-agent's start would otherwise re-credit the parent's own words
// to the child, and OnlyAgent would drop the parent's answer rather than merely fail
// to find it. Crediting nothing is recoverable; crediting wrongly is not.
func TestAttributionStopsRatherThanGuessingWhenResponsesInterleave(t *testing.T) {
	t.Parallel()

	t.Run("one response at a time credits every block", func(t *testing.T) {
		blocks := foldBlocks(t, 0,
			`{"type":"response.created","response":{"id":"r1","model":"coder.researcher","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"found it"}`,
			`{"type":"response.completed","response":{"id":"r1","model":"coder.researcher","status":"completed"}}`,
		)
		for _, block := range blocks {
			if got := block.Context().Agent; got != "coder.researcher" {
				t.Errorf("%T reports agent %q", block, got)
			}
		}
	})

	t.Run("two live responses credit nothing", func(t *testing.T) {
		blocks := foldBlocks(t, 0,
			`{"type":"response.created","response":{"id":"parent","model":"coder","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"PARENT-1 "}`,
			`{"type":"response.created","response":{"id":"child","model":"coder.researcher","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"CHILD-1 "}`,
			`{"type":"response.output_item.done","item":{"type":"message","content":`+
				`[{"type":"output_text","text":"mixed"}]}}`,
		)
		for _, block := range blocks {
			if _, isStart := block.(ResponseStartBlock); isStart {
				// A start block names its own response, so it keeps its model.
				continue
			}
			if got := block.Context().Agent; got != "" {
				t.Errorf("%T credited agent %q while two responses were live", block, got)
			}
		}
	})
}

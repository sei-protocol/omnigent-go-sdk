package omnigent

import (
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
)

// blockSeq replays a fixed list, so a transform's behaviour is decided by the
// transform and not by a server's timing.
func blockSeq(blocks ...Block) iter.Seq2[Block, error] {
	return func(yield func(Block, error) bool) {
		for _, b := range blocks {
			if !yield(b, nil) {
				return
			}
		}
	}
}

func collectBlocks(t *testing.T, seq iter.Seq2[Block, error]) ([]Block, []error) {
	t.Helper()
	var blocks []Block
	var errs []error
	for block, err := range seq {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks, errs
}

func names(blocks []Block) string {
	var out []string
	for _, b := range blocks {
		out = append(out, fmt.Sprintf("%T", b))
	}
	return strings.Join(out, ",")
}

// tag appends a marker to every TextChunk, so composing two tags records the
// order they ran in. Two filtering transforms would commute and prove nothing.
func tag(marker string) Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			for block, err := range seq {
				if chunk, ok := block.(TextChunk); ok && err == nil {
					chunk.Text += marker
					block = chunk
				}
				if !yield(block, err) {
					return
				}
			}
		}
	}
}

func TestPipeAppliesTransformsInTheOrderWritten(t *testing.T) {
	t.Parallel()

	blocks, errs := collectBlocks(t, Pipe(blockSeq(TextChunk{Text: "start"}), tag("-a"), tag("-b")))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// "a" ran first, so its marker sits closer to the original text.
	if got := blocks[0].(TextChunk).Text; got != "start-a-b" {
		t.Errorf("Text = %q, want start-a-b; Pipe ran the transforms out of order", got)
	}
}

func TestSkipBlocksDropsOnlyWhatItSelects(t *testing.T) {
	t.Parallel()

	blocks, _ := collectBlocks(t, SkipBlocks(IsBlock[ReasoningBlock], IsBlock[RetryBlock])(
		blockSeq(
			ReasoningBlock{ReasoningText: "why"},
			TextChunk{Text: "a"},
			RetryBlock{Source: "tool"},
			CompactionBlock{},
		)))
	if got := names(blocks); got != "omnigent.TextChunk,omnigent.CompactionBlock" {
		t.Errorf("got %s", got)
	}
}

// TestTransformsNeverSwallowAnError pins the property every transform shares. A
// dropped error leaves a caller reading a truncated turn as a complete one.
func TestTransformsNeverSwallowAnError(t *testing.T) {
	t.Parallel()

	boom := errors.New("stream died")
	failing := func(yield func(Block, error) bool) {
		yield(TextChunk{Text: "partial"}, nil)
		yield(nil, boom)
	}

	for name, transform := range map[string]Transform{
		"SkipBlocks":                SkipBlocks(IsBlock[TextChunk]),
		"OnlyBlocks":                OnlyBlocks(IsBlock[ResponseEndBlock]),
		"OnlyAgent":                 OnlyAgent("nobody"),
		"SkipIntermediateEnds":      SkipIntermediateEnds(),
		"MergeTextAcrossIterations": MergeTextAcrossIterations(),
	} {
		t.Run(name, func(t *testing.T) {
			_, errs := collectBlocks(t, transform(failing))
			if len(errs) != 1 || !errors.Is(errs[0], boom) {
				t.Errorf("%s dropped the error: got %v", name, errs)
			}
		})
	}
}

func TestOnlyAgentKeepsOneAgentAndEmptyKeepsAll(t *testing.T) {
	t.Parallel()

	from := func(agent, text string) Block {
		return TextChunk{blockCtx: blockCtx{Ctx: BlockContext{Agent: agent}}, Text: text}
	}
	seq := func() iter.Seq2[Block, error] {
		return blockSeq(from("", "root"), from("sub", "child"), from("", "root2"))
	}

	blocks, _ := collectBlocks(t, OnlyAgent("sub")(seq()))
	if len(blocks) != 1 || blocks[0].(TextChunk).Text != "child" {
		t.Errorf("OnlyAgent(sub) kept %d blocks", len(blocks))
	}

	all, _ := collectBlocks(t, OnlyAgent("")(seq()))
	if len(all) != 3 {
		t.Errorf("OnlyAgent(\"\") kept %d blocks, want all 3", len(all))
	}
}

// TestSkipIntermediateEndsKeepsTheLastEndOnly pins the tool-loop case: a turn that
// looped three times must read as finishing once.
func TestSkipIntermediateEndsKeepsTheLastEndOnly(t *testing.T) {
	t.Parallel()

	blocks, _ := collectBlocks(t, SkipIntermediateEnds()(blockSeq(
		TextChunk{Text: "a"},
		ResponseEndBlock{Status: "completed"},
		ToolGroup{Iteration: 1},
		ResponseEndBlock{Status: "completed"},
		TextChunk{Text: "b"},
		// Consecutive, deliberately: with a block between every end, holding the
		// first and holding the last cannot be told apart.
		ResponseEndBlock{Status: "completed"},
		ResponseEndBlock{Status: "failed"},
	)))
	if got := names(blocks); got != "omnigent.TextChunk,omnigent.ToolGroup,omnigent.TextChunk,omnigent.ResponseEndBlock" {
		t.Fatalf("got %s", got)
	}
	// The one kept is the last, not the first.
	if last := blocks[len(blocks)-1].(ResponseEndBlock); last.Status != "failed" {
		t.Errorf("kept the end with status %q, want the last one", last.Status)
	}
}

func TestSkipIntermediateEndsEmitsNothingExtraWhenThereIsNoEnd(t *testing.T) {
	t.Parallel()

	blocks, _ := collectBlocks(t, SkipIntermediateEnds()(blockSeq(TextChunk{Text: "a"})))
	if got := names(blocks); got != "omnigent.TextChunk" {
		t.Errorf("got %s", got)
	}
}

// TestMergeTextAcrossIterationsReportsOneAnswer pins that a looped turn renders as
// one answer, and that the merged block lands before the end.
func TestMergeTextAcrossIterationsReportsOneAnswer(t *testing.T) {
	t.Parallel()

	ctx := BlockContext{Agent: "coder", Turn: 2}
	blocks, _ := collectBlocks(t, MergeTextAcrossIterations()(blockSeq(
		TextChunk{Text: "one "},
		TextDone{FullText: "one "},
		ToolGroup{Iteration: 1},
		TextDone{blockCtx: blockCtx{Ctx: ctx}, FullText: "two"},
		ResponseEndBlock{blockCtx: blockCtx{Ctx: ctx}, Status: "completed"},
	)))
	if got := names(blocks); got != "omnigent.TextChunk,omnigent.ToolGroup,omnigent.TextDone,omnigent.ResponseEndBlock" {
		t.Fatalf("got %s", got)
	}
	merged := blocks[2].(TextDone)
	if merged.FullText != "one two" {
		t.Errorf("FullText = %q, want %q", merged.FullText, "one two")
	}
	if merged.Context().Agent != "coder" {
		t.Errorf("the merged block lost its context: %+v", merged.Context())
	}
}

// TestMergeTextAcrossIterationsFlushesADroppedStream pins that a stream ending
// without a terminal response still reports the text it gathered.
func TestMergeTextAcrossIterationsFlushesADroppedStream(t *testing.T) {
	t.Parallel()

	ctx := BlockContext{Agent: "coder", Turn: 3}
	blocks, _ := collectBlocks(t, MergeTextAcrossIterations()(blockSeq(
		TextDone{blockCtx: blockCtx{Ctx: ctx}, FullText: "partial answer"},
	)))
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want the flushed TextDone", len(blocks))
	}
	flushed := blocks[0].(TextDone)
	if flushed.FullText != "partial answer" {
		t.Errorf("FullText = %q", flushed.FullText)
	}
	// No terminal response arrived, so the context can only come from the text the
	// transform accumulated.
	if flushed.Context().Agent != "coder" || flushed.Context().Turn != 3 {
		t.Errorf("the flushed block lost its context: %+v", flushed.Context())
	}
}

func TestMergeTextAcrossIterationsMarksCodeBlocks(t *testing.T) {
	t.Parallel()

	blocks, _ := collectBlocks(t, MergeTextAcrossIterations()(blockSeq(
		TextDone{FullText: "see ```go\nx := 1\n```"},
		ResponseEndBlock{Status: "completed"},
	)))
	if !blocks[0].(TextDone).HasCodeBlocks {
		t.Error("a fenced block was not reported")
	}
}

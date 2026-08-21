package omnigent

import (
	"iter"
	"strings"
)

// Transform wraps a block sequence, so a caller changes what it renders without
// changing how it reads.
//
// A transform is a function rather than an interface because every one of them is
// one operation over a sequence, and a caller composes them with [Pipe].
type Transform func(iter.Seq2[Block, error]) iter.Seq2[Block, error]

// Pipe composes transforms left to right, so Pipe(seq, a, b) reads as "a, then b".
//
// Written this way round because it matches the order a caller says it: drop the
// reasoning, then merge the text. The nesting that produces, b(a(seq)), is the
// reverse, which is why this exists rather than leaving callers to nest by hand.
func Pipe(seq iter.Seq2[Block, error], transforms ...Transform) iter.Seq2[Block, error] {
	for _, transform := range transforms {
		seq = transform(seq)
	}
	return seq
}

// SkipBlocks drops the blocks a predicate selects.
//
// A predicate rather than a list of types, because Go has no variadic type
// parameter and a list of reflect.Type would move the mistake from compile time to
// run time. [IsBlock] builds the common case:
//
//	Pipe(seq, SkipBlocks(IsBlock[ReasoningBlock], IsBlock[ReasoningChunk]))
//
// An error is never dropped. A transform that swallowed one would leave a caller
// reading a truncated turn as a complete one.
func SkipBlocks(drop ...func(Block) bool) Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			for block, err := range seq {
				if err != nil {
					if !yield(block, err) {
						return
					}
					continue
				}
				if anyMatch(drop, block) {
					continue
				}
				if !yield(block, nil) {
					return
				}
			}
		}
	}
}

// IsBlock reports whether a block is of one variant, for [SkipBlocks] and
// [OnlyBlocks].
//
// Instantiated per variant — IsBlock[ReasoningBlock] — so a caller names the type
// and the compiler checks it belongs to the union.
func IsBlock[T Block](block Block) bool {
	_, is := block.(T)
	return is
}

// OnlyBlocks keeps the blocks a predicate selects and drops the rest.
//
// The complement of [SkipBlocks], for the case where the wanted set is smaller
// than the unwanted one. Errors pass through for the same reason.
func OnlyBlocks(keep ...func(Block) bool) Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			for block, err := range seq {
				if err != nil {
					if !yield(block, err) {
						return
					}
					continue
				}
				if !anyMatch(keep, block) {
					continue
				}
				if !yield(block, nil) {
					return
				}
			}
		}
	}
}

func anyMatch(predicates []func(Block) bool, block Block) bool {
	for _, matches := range predicates {
		if matches(block) {
			return true
		}
	}
	return false
}

// OnlyAgent keeps the blocks one agent produced.
//
// An empty name keeps every block, so a caller can pass a configured value
// through without branching on whether it was set.
func OnlyAgent(agent string) Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			for block, err := range seq {
				if err != nil {
					if !yield(block, err) {
						return
					}
					continue
				}
				// A nil block with no error is not a shape the fold produces, but
				// Transform is exported and takes any sequence. The other four
				// transforms pass it through rather than reading it, so this one does
				// too: a filter is not the place to decide a caller's input is wrong.
				if agent != "" && block != nil && block.Context().Agent != agent {
					continue
				}
				if !yield(block, nil) {
					return
				}
			}
		}
	}
}

// SkipIntermediateEnds keeps only the last [ResponseEndBlock] of a sequence.
//
// One turn reaches one terminal response, so this is a no-op on a single turn. It
// is for a sequence that carries several — a [Client.Stream] read across more than
// one turn, or a replayed transcript — where a caller rendering every end draws the
// work as finished several times.
//
// Each end is held back until something follows it: a block after an end proves
// that end was intermediate, and the end still held when the sequence stops is the
// real one.
//
// The cost is that the final end arrives only when the sequence ends, which for a
// live stream is the same moment.
func SkipIntermediateEnds() Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			var held Block
			for block, err := range seq {
				if err != nil {
					// Errors are not held back: a caller waiting on an end must not
					// wait on a sequence that has already failed.
					if !yield(block, err) {
						return
					}
					continue
				}
				if _, isEnd := block.(ResponseEndBlock); isEnd {
					held = block
					continue
				}
				// A non-end block means the buffered end was intermediate, so it is
				// dropped. That is only sound while a terminal response is the last
				// block a turn produces, which is [BlockStream]'s job rather than
				// this one's: upstream's fold flushes text inside its terminal
				// handler and emits nothing after the end, and so does ours.
				held = nil
				if !yield(block, nil) {
					return
				}
			}
			if held != nil {
				yield(held, nil)
			}
		}
	}
}

// MergeTextAcrossIterations joins the [TextDone] blocks of one response into one.
//
// A response finishes a text section per message item, and a tool loop produces
// several, so a caller rendering a finished transcript otherwise draws the answer in
// as many pieces as the agent spoke. This reports it once.
//
// It flushes at each [ResponseEndBlock], which is one per turn. On a sequence
// spanning several turns that is one answer per turn, which is usually what a
// caller wants; [SkipIntermediateEnds] first would join them into one.
//
// [TextChunk] passes through untouched, so live rendering is unaffected. A sequence
// that ends with no terminal response still reports what it gathered, using the
// context of the text rather than of an end that never came.
func MergeTextAcrossIterations() Transform {
	return func(seq iter.Seq2[Block, error]) iter.Seq2[Block, error] {
		return func(yield func(Block, error) bool) {
			var accumulated strings.Builder
			var ctx BlockContext

			flush := func(at BlockContext) bool {
				if accumulated.Len() == 0 {
					return true
				}
				text := accumulated.String()
				accumulated.Reset()
				return yield(TextDone{
					blockCtx:      blockAt(at),
					FullText:      text,
					HasCodeBlocks: strings.Contains(text, "```"),
				}, nil)
			}

			for block, err := range seq {
				if err != nil {
					if !yield(block, err) {
						return
					}
					continue
				}
				switch typed := block.(type) {
				case TextDone:
					// Held rather than yielded. Its context is kept for the case
					// where no terminal response arrives, which is the only path
					// that has no better one to use.
					accumulated.WriteString(typed.FullText)
					ctx = typed.Context()
				case ResponseEndBlock:
					// The response is over, so whatever text accumulated is complete.
					// Emitted before the end, so a caller reading in order sees the
					// answer and then the turn closing.
					if !flush(typed.Context()) {
						return
					}
					if !yield(block, nil) {
						return
					}
				default:
					if !yield(block, nil) {
						return
					}
				}
			}
			// A sequence that stops without a terminal response still has to report
			// what it accumulated, or a caller loses the answer to a dropped stream.
			flush(ctx)
		}
	}
}

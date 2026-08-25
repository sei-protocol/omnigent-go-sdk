package omnigent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
)

// QueryResult is one turn folded into what a caller usually wants: the answer, and
// the files the agent produced.
//
// The whole turn in two fields, for the caller who does not want a sequence at all.
// A caller who does wants [Chat.Send] with [BlockStream].
type QueryResult struct {
	// Text is the assistant's answer, joined across the turn's tool-loop passes.
	// Empty when the agent produced no text.
	Text string

	// Files are the artifacts the turn produced, in the order they arrived. Empty
	// when it produced none.
	//
	// Each carries the id and, when the server reported one, the name. The bytes
	// are fetched with [SessionFiles.Download]: an artifact can be any size, and a
	// fold that pulled every file's content would decide that for the caller.
	Files []FileBlock
}

// Query drives one turn and folds it into its answer.
//
// The composition this performs is the one a caller would otherwise write:
// [BlockStream] over [Chat.Send], then [SkipIntermediateEnds] so a tool loop reports
// one ending, then [MergeTextAcrossIterations] so it reports one answer.
//
// Returns what it gathered alongside any error, because a turn that fails partway
// still produced whatever it produced, and discarding that would leave a caller
// with less than the stream gave.
func (c *Chat) Query(ctx context.Context, text string) (*QueryResult, error) {
	result := &QueryResult{}
	var answer strings.Builder

	blocks := c.queryBlocks(ctx, text)
	for block, err := range blocks {
		if err != nil {
			return result, err
		}
		switch typed := block.(type) {
		case TextDone:
			answer.WriteString(typed.FullText)
		case FileBlock:
			result.Files = append(result.Files, typed)
		}
	}
	result.Text = answer.String()
	return result, nil
}

// QueryStream is one turn's text, streamed.
//
// Single-use, like the [Turn] beneath it: reading twice would post the prompt twice
// and the server would answer both.
type QueryStream struct {
	chat *Chat
	turn *Turn

	mu    sync.Mutex
	files []FileBlock
	read  bool
}

// QueryStream prepares one turn whose text is read as it arrives.
//
// Nothing is posted until [QueryStream.Text] is read.
func (c *Chat) QueryStream(text string) *QueryStream {
	return &QueryStream{chat: c, turn: c.Prompt(text)}
}

// Text yields the answer in the chunks the agent produced, so a caller can render
// as the turn runs.
//
// The context bounds the turn, as it does for [Chat.Send]. [QueryStream.Files] is
// complete once this sequence ends; reading it during the sequence reports the
// artifacts that have arrived so far.
func (q *QueryStream) Text(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if !q.claim() {
			yield("", fmt.Errorf("read query stream on session %s: %w",
				q.chat.sessionID, ErrTurnAlreadyRead))
			return
		}
		stream := &BlockStream{}
		for block, err := range stream.Blocks(q.turn.Events(ctx)) {
			if err != nil {
				if !yield("", err) {
					return
				}
				continue
			}
			switch typed := block.(type) {
			case TextChunk:
				if !yield(typed.Text, nil) {
					return
				}
			case FileBlock:
				q.addFile(typed)
			}
		}
	}
}

// Files reports the artifacts the turn produced.
//
// Complete once [QueryStream.Text] has ended. Safe to call while it runs, where it
// reports what has arrived.
func (q *QueryStream) Files() []FileBlock {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]FileBlock(nil), q.files...)
}

func (q *QueryStream) addFile(file FileBlock) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.files = append(q.files, file)
}

func (q *QueryStream) claim() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.read {
		return false
	}
	q.read = true
	return true
}

// queryBlocks folds one turn into the block sequence a query reads.
func (c *Chat) queryBlocks(ctx context.Context, text string) iter.Seq2[Block, error] {
	stream := &BlockStream{}
	return Pipe(stream.Blocks(c.Send(ctx, text)),
		SkipIntermediateEnds(), MergeTextAcrossIterations())
}

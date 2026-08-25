package omnigent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A turn with two message items, a file artifact, and one terminal response.
//
// One terminal, because that is what a turn produces: a tool loop's passes happen
// inside one response, which is why the response id holds across them.
func queryScript() []string {
	return []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"Let me look. "}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Let me look. "}]}}`,
		`{"type":"response.output_file.done","file_id":"file_1","filename":"chart.png"}`,
		`{"type":"response.output_text.delta","delta":"The answer is 42."}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"The answer is 42."}]}}`,
		`{"type":"response.completed","response":{"id":"r1","model":"coder","status":"completed"}}`,
	}
}

// TestQueryFoldsATurnIntoItsAnswer pins the composition Query performs, which is the
// one a caller would otherwise write by hand.
// contextWithTimeout keeps these tests to one bounded shape.
func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, func()) {
	t.Helper()
	return context.WithTimeout(t.Context(), d)
}

func TestQueryFoldsATurnIntoItsAnswer(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, queryScript())
	chat, err := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	ctx, cancel := contextWithTimeout(t, 10*time.Second)
	defer cancel()

	result, err := chat.Query(ctx, "how many?")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// One answer, not one per tool-loop pass: that is what the composition is for.
	if !strings.Contains(result.Text, "42") {
		t.Errorf("Text = %q, want the final answer", result.Text)
	}
	if strings.Count(result.Text, "Let me look.") > 1 {
		t.Errorf("Text repeats a pass: %q", result.Text)
	}
	if len(result.Files) != 1 || result.Files[0].FileID != "file_1" {
		t.Fatalf("Files = %+v, want the one artifact", result.Files)
	}
	if name := result.Files[0].Filename; name == nil || *name != "chart.png" {
		t.Errorf("the artifact lost its name: %+v", result.Files[0])
	}
}

// TestQueryReturnsWhatItGatheredOnFailure pins that a turn failing partway does not
// discard what it produced.
func TestQueryReturnsWhatItGatheredOnFailure(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_file.done","file_id":"file_1","filename":"partial.csv"}`,
		`{"type":"response.failed","response":{"id":"r1","model":"coder","status":"failed",` +
			`"error":{"code":"llm_timeout","message":"upstream timed out"}}}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	ctx, cancel := contextWithTimeout(t, 10*time.Second)
	defer cancel()

	result, err := chat.Query(ctx, "how many?")
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("got %v, want ErrTurnFailed", err)
	}
	if !strings.Contains(err.Error(), "upstream timed out") {
		t.Errorf("the server's reason was dropped: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("a failing turn discarded the artifact it had produced: %+v", result.Files)
	}
}

// TestQueryStreamYieldsTextAsItArrives pins the streaming half, and that Files is
// complete once the sequence ends.
func TestQueryStreamYieldsTextAsItArrives(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, queryScript())
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	ctx, cancel := contextWithTimeout(t, 10*time.Second)
	defer cancel()

	stream := chat.QueryStream("how many?")
	var chunks []string
	for chunk, err := range stream.Text(ctx) {
		if err != nil {
			t.Fatalf("Text: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatal("no text chunks")
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "42") {
		t.Errorf("streamed text = %q", joined)
	}
	if files := stream.Files(); len(files) != 1 {
		t.Errorf("Files() = %+v after the sequence ended, want the one artifact", files)
	}
}

// TestQueryStreamIsSingleUse pins the same guard a Turn carries: a second read would
// post the prompt again.
func TestQueryStreamIsSingleUse(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{echoFrame,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	ctx, cancel := contextWithTimeout(t, 10*time.Second)
	defer cancel()

	stream := chat.QueryStream("hi")
	for range stream.Text(ctx) {
	}

	var second error
	for _, err := range stream.Text(ctx) {
		if err != nil {
			second = err
		}
	}
	if !errors.Is(second, ErrTurnAlreadyRead) {
		t.Fatalf("second read returned %v, want ErrTurnAlreadyRead", second)
	}
	if got := server.postedTypes(); len(got) != 1 {
		t.Errorf("the prompt was posted %d times", len(got))
	}
}

// TestQueryStreamPostsNothingUntilRead pins that building one sends nothing.
func TestQueryStreamPostsNothingUntilRead(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{echoFrame,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})

	_ = chat.QueryStream("never read")
	time.Sleep(250 * time.Millisecond)
	if got := server.postedTypes(); len(got) != 0 {
		t.Errorf("an unread query posted %v", got)
	}
}

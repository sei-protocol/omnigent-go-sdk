package omnigent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestARedeliveredCallDoesNotEndTheTurn pins that the ordinary MCP double
// delivery costs nothing.
//
// The wire delivers one call twice on that path, which is why the dispatch guard
// exists. The first delivery runs the tool and posts its output, so the second is
// redundant rather than unanswered — treating it as fatal ends a turn that is
// working, and the caller loses everything the agent said afterwards.
func TestARedeliveredCallDoesNotEndTheTurn(t *testing.T) {
	t.Parallel()

	call := `{"type":"response.output_item.done","item":{"type":"function_call",` +
		`"status":"action_required","call_id":"call_1","name":"Echo","arguments":"{}"}}`
	_, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.in_progress","response":{"id":"r1","status":"in_progress"}}`,
		call,
		call, // the same call again, which is the documented MCP shape
		`{"type":"response.output_text.delta","delta":"the answer after the tool"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})

	var mu sync.Mutex
	ran := 0
	tools := NewToolRegistry()
	if err := tools.Register("Echo", nil, func(context.Context, ToolCallInfo) (string, error) {
		mu.Lock()
		ran++
		mu.Unlock()
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	chat, err := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var text strings.Builder
	for event := range chat.Send(t.Context(), "hi") {
		if delta, ok := event.(OutputTextDeltaEvent); ok {
			text.WriteString(delta.Delta)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if ran != 1 {
		t.Errorf("the tool ran %d times, want 1", ran)
	}
	// The symptom: the turn stopped at the duplicate, so everything the agent said
	// after it never reached the caller.
	if got := text.String(); got != "the answer after the tool" {
		t.Errorf("the caller read %q after the redelivery, want the whole answer: "+
			"the turn ended on a duplicate that cost nothing", got)
	}
}

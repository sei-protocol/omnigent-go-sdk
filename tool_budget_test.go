package omnigent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestATurnWillNotRunToolsWithoutBound pins the tool-call budget.
//
// The call id is the server's to choose, so the dispatch guard cannot bound how
// many calls a turn makes — a fresh id per ask is a fresh call. Before the budget
// only the caller's deadline stopped it, which turned one prompt into unbounded
// invocations of whatever the caller registered.
func TestATurnWillNotRunToolsWithoutBound(t *testing.T) {
	t.Parallel()

	asks := func(n int) []string {
		frames := []string{echoFrame}
		for i := range n {
			frames = append(frames, fmt.Sprintf(
				`{"type":"response.output_item.done","item":{"type":"function_call",`+
					`"status":"action_required","call_id":"call_%d","name":"Echo"}}`, i))
		}
		return append(frames,
			`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`)
	}

	run := func(t *testing.T, budget, offered int) (ran int, err error) {
		t.Helper()

		var mu sync.Mutex
		tools := &ToolRegistry{}
		if regErr := tools.Register("Echo", nil, func(context.Context, ToolCallInfo) (string, error) {
			mu.Lock()
			ran++
			mu.Unlock()
			return "ok", nil
		}); regErr != nil {
			t.Fatalf("Register: %v", regErr)
		}

		_, client := newChatServer(t, nil, asks(offered))
		chat, chatErr := client.Chat("conv_1", ChatOptions{
			Turn:         TurnOptions{End: TurnEndsOnResponseLifecycle},
			Tools:        tools,
			MaxToolCalls: budget,
		})
		if chatErr != nil {
			t.Fatalf("Chat: %v", chatErr)
		}
		for _, e := range chat.Send(t.Context(), "hi") {
			if e != nil {
				err = e
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return ran, err
	}

	t.Run("the budget stops the turn and names itself", func(t *testing.T) {
		t.Parallel()

		ran, err := run(t, 3, 20)
		if ran != 3 {
			t.Errorf("ran %d tools against a budget of 3", ran)
		}
		if !errors.Is(err, ErrToolCallBudget) {
			t.Fatalf("reported %v, want ErrToolCallBudget", err)
		}
		// The caller has to be able to act on it, and the only action is raising it.
		if !strings.Contains(err.Error(), "MaxToolCalls") {
			t.Errorf("the error does not name the option to raise: %v", err)
		}
	})

	t.Run("a negative budget removes the cap", func(t *testing.T) {
		t.Parallel()

		ran, err := run(t, -1, 12)
		if ran != 12 {
			t.Errorf("ran %d of 12 offered with the cap removed", ran)
		}
		if errors.Is(err, ErrToolCallBudget) {
			t.Errorf("a negative budget still enforced a cap: %v", err)
		}
	})

	t.Run("zero takes the default", func(t *testing.T) {
		t.Parallel()

		chat, err := (&Client{}).Chat("conv_1", ChatOptions{})
		if err == nil && chat.toolBudget() != DefaultMaxToolCalls {
			t.Errorf("toolBudget() = %d for a zero option, want %d",
				chat.toolBudget(), DefaultMaxToolCalls)
		}
	})
}

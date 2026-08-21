package omnigent

import (
	"context"
	"testing"
	"time"
)

// TestAResponseStartsOnWhicheverEventAnnouncesIt pins that the turn reads all
// three announcing events, and reports one start per response.
//
// The server emits response.created and response.in_progress as an inseparable
// pair and drops the created half at the publish chokepoint that feeds every
// subscriber. A reader keyed on created therefore never fires on a live turn:
// OnResponseStart was dead in production and every response id the turn reported
// was empty, while the test suite passed because its fixture sent created.
func TestAResponseStartsOnWhicheverEventAnnouncesIt(t *testing.T) {
	t.Parallel()

	for _, announce := range []struct {
		name  string
		frame string
	}{
		{"in_progress, which is what a subscription sees", `response.in_progress`},
		{"queued", `response.queued`},
		{"created, which only a replay carries", `response.created`},
	} {
		t.Run(announce.name, func(t *testing.T) {
			t.Parallel()

			_, client := newChatServer(t, nil, []string{
				echoFrame,
				`{"type":"` + announce.frame + `","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
				`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
			})

			var starts []string
			chat, err := client.Chat("conv_1", ChatOptions{
				Turn: TurnOptions{End: TurnEndsOnResponseLifecycle},
				Hooks: StreamHooks{OnResponseStart: func(c ResponseStartCtx) {
					starts = append(starts, c.ResponseID)
				}},
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for range chat.Send(ctx, "hi") {
			}

			if len(starts) != 1 || starts[0] != "r1" {
				t.Errorf("OnResponseStart fired %v, want exactly [r1]", starts)
			}
		})
	}

	t.Run("all three for one response still start it once", func(t *testing.T) {
		t.Parallel()

		_, client := newChatServer(t, nil, []string{
			echoFrame,
			`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
			`{"type":"response.queued","response":{"id":"r1","model":"coder","status":"queued"}}`,
			`{"type":"response.in_progress","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
		})

		var starts []string
		chat, err := client.Chat("conv_1", ChatOptions{
			Turn: TurnOptions{End: TurnEndsOnResponseLifecycle},
			Hooks: StreamHooks{OnResponseStart: func(c ResponseStartCtx) {
				starts = append(starts, c.ResponseID)
			}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		for range chat.Send(ctx, "hi") {
		}

		if len(starts) != 1 {
			t.Errorf("OnResponseStart fired %d times for one response (%v), want 1",
				len(starts), starts)
		}
	})
}

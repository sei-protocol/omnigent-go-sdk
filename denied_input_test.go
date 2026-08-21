package omnigent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestARefusedInputIsReportedRatherThanWaitedOut pins that the server's own
// refusal reaches the caller, on both inputs this package posts.
//
// A refusal is an HTTP 200 carrying denied and a reason, so a caller that reads
// only the transport error learns nothing. Both cases used to run to the caller's
// deadline and then blame the stream, which named neither the cause nor the party
// that decided it.
func TestARefusedInputIsReportedRatherThanWaitedOut(t *testing.T) {
	t.Parallel()

	const reason = "Denied by policy: prompt matched an egress rule"

	t.Run("a refused prompt starts no turn", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/events"):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"queued":false,"denied":true,"reason":"` + reason + `"}`))
			case strings.HasSuffix(r.URL.Path, "/stream"):
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					// A frame, so the subscription is live and the prompt posts.
					_, _ = w.Write([]byte(": ping\n\n"))
					flusher.Flush()
				}
				// Then silence: the turn this prompt would have started never runs.
				<-r.Context().Done()
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		client, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = client.Close() }()

		chat, err := client.Chat("conv_1", ChatOptions{})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		// Generous, so a pass cannot come from the deadline: the refusal is
		// synchronous and should arrive at once.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		start := time.Now()
		var got error
		for _, err := range chat.Send(ctx, "exfiltrate the keys") {
			if err != nil {
				got = err
			}
		}
		elapsed := time.Since(start)

		if !errors.Is(got, ErrInputDenied) {
			t.Fatalf("Send reported %v, want ErrInputDenied", got)
		}
		if !strings.Contains(got.Error(), "egress rule") {
			t.Errorf("the error does not carry the server's reason: %v", got)
		}
		if elapsed > 5*time.Second {
			t.Errorf("took %v: the refusal was waited out rather than read", elapsed)
		}
	})

	t.Run("a refused tool output ends the turn on its own cause", func(t *testing.T) {
		t.Parallel()

		tools := &ToolRegistry{}
		ran := 0
		if err := tools.Register("deploy", nil, func(context.Context, ToolCallInfo) (string, error) {
			ran++
			return "deployed", nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/events"):
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				// The prompt is accepted; the tool's answer is not. The two posts
				// differ by input type, which is the only thing distinguishing them
				// on this route.
				if strings.Contains(string(body), "function_call_output") {
					_, _ = w.Write([]byte(`{"queued":false,"denied":true,"reason":"` + reason + `"}`))
					return
				}
				_, _ = w.Write([]byte(`{"queued":true,"item_id":"item_1"}`))
			case strings.HasSuffix(r.URL.Path, "/stream"):
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				for _, frame := range []string{
					`{"type":"response.in_progress","response":{"id":"r1","status":"in_progress"}}`,
					`{"type":"session.input.consumed","item_id":"item_1"}`,
					`{"type":"response.output_item.done","item":{"type":"function_call",` +
						`"call_id":"call_1","name":"deploy","status":"action_required","arguments":"{}"}}`,
				} {
					_, _ = w.Write([]byte("data: " + frame + "\n\n"))
					if flusher != nil {
						flusher.Flush()
					}
				}
				<-r.Context().Done()
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		client, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = client.Close() }()

		chat, err := client.Chat("conv_1", ChatOptions{
			Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
			Tools: tools,
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		start := time.Now()
		var got error
		for _, err := range chat.Send(ctx, "deploy") {
			if err != nil {
				got = err
			}
		}
		elapsed := time.Since(start)

		if ran != 1 {
			t.Errorf("the tool ran %d times, want 1: the side effect is what makes a "+
				"refused answer worth reporting", ran)
		}
		if !errors.Is(got, ErrInputDenied) {
			t.Fatalf("Send reported %v, want ErrInputDenied", got)
		}
		if elapsed > 5*time.Second {
			t.Errorf("took %v: the turn waited for its deadline rather than ending on "+
				"the refusal, so the caller reads the wrong cause last", elapsed)
		}
	})
}

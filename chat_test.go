package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// chatServer is a session server that emits a scripted event stream and records
// what the client posted back.
type chatServer struct {
	mu       sync.Mutex
	posts    []map[string]any
	resolved []string

	// script is written to the stream after the client subscribes. Each entry is
	// one SSE frame's JSON.
	script []string

	// afterPost, when set, is appended to the script once the client posts its
	// prompt — which is how a turn that depends on the prompt is modelled.
	afterPost []string

	posted chan struct{}
}

func (s *chatServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stream"):
			s.serveStream(w, r)
		case strings.HasSuffix(r.URL.Path, "/events"):
			s.serveEvents(w, r)
		case strings.Contains(r.URL.Path, "/elicitations/"):
			s.serveResolve(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	})
}

func (s *chatServer) serveStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)

	// The heartbeat the relay sends once a reader is registered. It is what tells
	// the client the subscription is live.
	_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"session.heartbeat"}`)
	flusher.Flush()

	// Wait for the prompt, so the scripted turn cannot be answered before it.
	select {
	case <-s.posted:
	case <-time.After(3 * time.Second):
	case <-r.Context().Done():
		return
	}

	for _, frame := range append(s.script, s.afterPost...) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		flusher.Flush()
	}
	<-r.Context().Done()
}

func (s *chatServer) serveEvents(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.posts = append(s.posts, body)
	first := len(s.posts) == 1
	s.mu.Unlock()
	if first {
		close(s.posted)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"queued","item_id":"item_1"}`))
}

func (s *chatServer) serveResolve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	// Records which session owned the resolve and what verdict was posted.
	s.resolved = append(s.resolved, fmt.Sprintf("%s:%v", parts[2], body["action"]))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *chatServer) postedTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.posts {
		out = append(out, fmt.Sprint(p["type"]))
	}
	return out
}

// assertKnownFrames fails a fixture whose discriminator this build does not know.
//
// An unknown type decodes to [UnknownEvent], which is the right behaviour for a
// newer server and the wrong behaviour for a typo: the frame arrives, matches no
// branch, and the test hangs waiting for a turn that can never end. One
// mis-spelled discriminator cost exactly that.
func assertKnownFrames(t *testing.T, frames []string) {
	t.Helper()
	for _, frame := range frames {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(frame), &envelope); err != nil {
			t.Fatalf("fixture is not JSON: %s", frame)
		}
		if envelope.Type == "" {
			t.Fatalf("fixture carries no type: %s", frame)
		}
		if _, known := eventRegistry[envelope.Type]; !known {
			t.Fatalf("fixture type %q is in no decoder; it would arrive as UnknownEvent", envelope.Type)
		}
	}
}

func newChatServer(t *testing.T, script, afterPost []string) (*chatServer, *Client) {
	t.Helper()
	assertKnownFrames(t, script)
	assertKnownFrames(t, afterPost)
	server := &chatServer{script: script, afterPost: afterPost, posted: make(chan struct{})}
	httpServer := httptest.NewServer(server.handler(t))
	t.Cleanup(httpServer.Close)

	client, err := New(httpServer.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

// echoFrame is the server's echo of a posted prompt, which is what lets a turn
// tell its own events from an older turn's. The discriminator is taken from the
// vendored description, not from memory: "session.input_consumed" decodes as an
// UnknownEvent and the boundary silently never crosses.
const echoFrame = `{"type":"session.input.consumed","data":{"type":"message","item_id":"item_1","data":{}}}`

// TestSendDrivesOneTurnToItsAnswer pins FR-017: a caller sends a prompt and reads
// the answer without writing the loop.
func TestSendDrivesOneTurnToItsAnswer(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"resp_1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"hello there"}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"coder","status":"completed"}}`,
		`{"type":"session.status","conversation_id":"conv_1","status":"idle","response_id":"resp_1"}`,
	})

	chat, err := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var kinds []string
	for event, err := range chat.Send(ctx, "hi") {
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		kinds = append(kinds, event.EventType())
	}
	if got := strings.Join(kinds, ","); !strings.Contains(got, "response.completed") {
		t.Fatalf("the turn did not reach its terminal event: %s", got)
	}
	// The turn ended at the terminal event, so the trailing idle edge is not read.
	if last := kinds[len(kinds)-1]; last != "response.completed" {
		t.Errorf("read past the turn's end: last event was %s", last)
	}
	if got := server.postedTypes(); len(got) != 1 || got[0] != "message" {
		t.Errorf("posted %v, want one message", got)
	}
}

// TestSendRefusesASecondRead pins FR-020. Reading again would post the prompt
// twice and the server would answer both.
func TestSendRefusesASecondRead(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	turn := chat.Prompt("hi")
	for range turn.Events(ctx) {
	}

	var second error
	for _, err := range turn.Events(ctx) {
		if err != nil {
			second = err
		}
	}
	if !errors.Is(second, ErrTurnAlreadyRead) {
		t.Fatalf("second read returned %v, want ErrTurnAlreadyRead", second)
	}
	if got := server.postedTypes(); len(got) != 1 {
		t.Errorf("the prompt was posted %d times, want once", len(got))
	}
}

// TestAnUnregisteredToolIsAnsweredRatherThanLeftParked pins FR-027. The server
// parks the turn on a client tool call, so reporting to the caller is not enough —
// the call has to be answered or the turn hangs to its deadline.
func TestAnUnregisteredToolIsAnsweredRatherThanLeftParked(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.output_item.done","item":{"type":"function_call","status":"action_required",` +
			`"call_id":"call_1","name":"Absent","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Tools: NewToolRegistry(),
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var reported error
	for _, err := range chat.Send(ctx, "hi") {
		if err != nil && !errors.Is(err, ErrTurnIncomplete) {
			reported = err
		}
	}
	// Answered: the output post is what unparks the turn.
	posts := server.postedTypes()
	if len(posts) != 2 || posts[1] != "function_call_output" {
		t.Fatalf("posted %v, want a message then a function_call_output", posts)
	}
	server.mu.Lock()
	output := fmt.Sprint(server.posts[1]["data"])
	server.mu.Unlock()
	if !strings.Contains(output, "Absent") {
		t.Errorf("the posted output does not name the missing tool: %s", output)
	}
	if reported != nil && !errors.Is(reported, ErrToolNotRegistered) {
		t.Errorf("reported %v", reported)
	}
}

// TestARegisteredToolRunsAndItsOutputIsPosted pins FR-026.
func TestARegisteredToolRunsAndItsOutputIsPosted(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.output_item.done","item":{"type":"function_call","status":"action_required",` +
			`"call_id":"call_1","name":"Echo","arguments":"{\"text\":\"pong\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})

	tools := NewToolRegistry()
	var sawArgs map[string]any
	if err := tools.Register("Echo", map[string]any{"name": "Echo"},
		func(_ context.Context, info ToolCallInfo) (string, error) {
			sawArgs = info.Arguments
			return "ran " + fmt.Sprint(info.Arguments["text"]), nil
		}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, err := range chat.Send(ctx, "hi") {
		if err != nil && !errors.Is(err, ErrTurnIncomplete) {
			t.Fatalf("Send: %v", err)
		}
	}

	if sawArgs["text"] != "pong" {
		t.Errorf("the tool saw arguments %v", sawArgs)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.posts) != 2 {
		t.Fatalf("posted %v", server.postedTypes())
	}
	data := fmt.Sprint(server.posts[1]["data"])
	if !strings.Contains(data, "ran pong") {
		t.Errorf("the tool's output was not posted: %s", data)
	}
}

// TestAnApprovalIsDeclinedWhenNoHookDecides pins FR-021 and FR-031: the fail-closed
// default is this package's own behaviour, and it holds no policy.
func TestAnApprovalIsDeclinedWhenNoHookDecides(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.elicitation_request","elicitation_id":"el_1",` +
			`"params":{"message":"run rm -rf /?"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.resolved) != 1 || server.resolved[0] != "conv_1:decline" {
		t.Fatalf("resolved %v, want one decline against conv_1", server.resolved)
	}
}

// TestAnApprovalGoesToTheSessionTheRequestNames pins FR-022. A sub-agent's request
// is mirrored into the ancestor's stream and names its own session; a verdict sent
// to the stream's session leaves the sub-agent parked.
func TestAnApprovalGoesToTheSessionTheRequestNames(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.elicitation_request","elicitation_id":"el_1",` +
			`"params":{"message":"child asks","target_session_id":"conv_child"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})

	var sawSession string
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle},
		Hooks: StreamHooks{OnElicitation: func(c ElicitationCtx) bool {
			sawSession = c.SessionID
			return true
		}},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	if sawSession != "conv_child" {
		t.Errorf("the hook was told session %q, want conv_child", sawSession)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.resolved) != 1 || server.resolved[0] != "conv_child:accept" {
		t.Fatalf("resolved %v, want one accept against conv_child", server.resolved)
	}
}

// TestTheSubscriptionExistsBeforeThePromptIsPosted pins the ordering the whole read
// depends on. A prompt posted before the subscription can be answered with nobody
// listening, and the turn's events are then missed.
func TestTheSubscriptionExistsBeforeThePromptIsPosted(t *testing.T) {
	t.Parallel()

	var order []string
	var mu sync.Mutex
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		if len(order) == 0 || order[len(order)-1] != what {
			order = append(order, what)
		}
	}

	posted := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stream"):
			record("subscribe")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"session.heartbeat"}`)
			flusher.Flush()
			select {
			case <-posted:
			case <-time.After(3 * time.Second):
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", echoFrame)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.completed","response":{"id":"r","status":"completed"}}`)
			flusher.Flush()
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/events"):
			record("post")
			close(posted)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"queued","item_id":"item_1"}`))
		}
	}))
	defer httpServer.Close()

	client, err := New(httpServer.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "subscribe" || order[1] != "post" {
		t.Fatalf("order = %v, want subscribe then post", order)
	}
}

// TestAServerRunToolIsNotRunAgain pins the one thing that separates a call to
// dispatch from a call to display.
//
// A function_call item arrives with status action_required when the tool is the
// client's to run, and completed when the server already ran it. Dispatching the
// second would repeat whatever it did — a write, a spend, a deploy.
func TestAServerRunToolIsNotRunAgain(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.output_item.done","item":{"type":"function_call","status":"completed",` +
			`"call_id":"c1","name":"Deploy","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	})

	tools := NewToolRegistry()
	ran := 0
	if err := tools.Register("Deploy", nil, func(context.Context, ToolCallInfo) (string, error) {
		ran++
		return "deployed", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	if ran != 0 {
		t.Errorf("a server-executed tool was run again %d times", ran)
	}
	if got := server.postedTypes(); len(got) != 1 || got[0] != "message" {
		t.Errorf("posted %v, want only the prompt", got)
	}
}

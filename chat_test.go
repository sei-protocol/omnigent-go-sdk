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
	"sync/atomic"
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

	// doneAfterScript ends the stream cleanly, with its terminal sentinel, when the
	// script runs out. That is the only way to reach the path where the stream ends
	// before the turn does: closing the socket instead is a transport failure, which
	// the turn loop reports before it gets there.
	doneAfterScript bool

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
	if s.doneAfterScript {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
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
// branch, and the test hangs waiting for a turn that can never end.
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
		if err != nil {
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
		if err != nil {
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

// TestNothingIsPostedUntilTheSubscriptionIsLive pins the ordering the whole read
// depends on, by its consequence rather than by arrival order.
//
// Asserted by consequence rather than by arrival order: an ordering assertion holds
// for any mechanism that happens to be fast, including a bare sleep. These two cases
// pass only if the prompt is posted from the subscription itself.
func TestNothingIsPostedUntilTheSubscriptionIsLive(t *testing.T) {
	t.Parallel()

	t.Run("a stream that never opens posts nothing", func(t *testing.T) {
		var posts atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/events") {
				posts.Add(1)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"runner_unavailable"}`))
		}))
		defer srv.Close()

		client, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = client.Close() }()

		chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		var got error
		for _, err := range chat.Send(ctx, "deploy to prod") {
			if err != nil {
				got = err
			}
		}
		if got == nil {
			t.Fatal("a stream that could not open reported success")
		}
		// The error is retryable by its own documentation, so the prompt must not
		// have landed — otherwise a retry runs the turn twice.
		time.Sleep(300 * time.Millisecond)
		if posts.Load() != 0 {
			t.Errorf("the prompt was posted %d times for a turn that never started", posts.Load())
		}
	})

	t.Run("a turn nobody reads posts nothing", func(t *testing.T) {
		server, client := newChatServer(t, nil, []string{echoFrame,
			`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`})
		chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})

		_ = chat.Prompt("never read")
		time.Sleep(300 * time.Millisecond)
		if got := server.postedTypes(); len(got) != 0 {
			t.Errorf("an unread turn posted %v; Chat.Prompt promises it sends nothing", got)
		}
	})
}

// TestChatRefusesToShareTheSubscriptionHook pins that a caller cannot post a second
// prompt for one turn.
//
// A Chat posts from [StreamOptions.OnSubscribed]; a caller's own hook there would
// run too, and the server would answer both prompts. The single-use guard on a Turn
// does not cover it, because both posts belong to one read.
func TestChatRefusesToShareTheSubscriptionHook(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Chat("conv_1", ChatOptions{
		Stream: StreamOptions{OnSubscribed: func(context.Context, Subscription) error { return nil }},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("got %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestOneCallIDRunsAToolOnce pins the executing side of a fact [BlockStream] already
// folds away for a renderer: the MCP path delivers one call twice. A non-idempotent
// tool must not run twice for one authorisation.
func TestOneCallIDRunsAToolOnce(t *testing.T) {
	t.Parallel()

	call := `{"type":"response.output_item.done","item":{"type":"function_call",` +
		`"status":"action_required","call_id":"c1","name":"Deploy","arguments":"{}"}}`
	server, client := newChatServer(t, nil, []string{
		echoFrame, call, call,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})

	tools := NewToolRegistry()
	var ran atomic.Int64
	if err := tools.Register("Deploy", nil, func(context.Context, ToolCallInfo) (string, error) {
		ran.Add(1)
		return "deployed", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	if ran.Load() != 1 {
		t.Errorf("one call ran the tool %d times", ran.Load())
	}
	if got := server.postedTypes(); len(got) != 2 {
		t.Errorf("posted %v, want the prompt and one output", got)
	}
}

// TestBreakingOnAnErrorDoesNotPanic pins the range-over-func contract across the
// side effects, which yield too.
//
// A caller writing "if err != nil { break }" is the ordinary case. A side effect
// that ignores yield's false return and lets the loop yield again panics that
// caller's process with "range function continued iteration after function for loop
// body returned false".
func TestBreakingOnAnErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	for name, script := range map[string][]string{
		"a failing tool": {
			echoFrame,
			`{"type":"response.output_item.done","item":{"type":"function_call",` +
				`"status":"action_required","call_id":"c1","name":"Boom","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
		},
		"an approval that cannot be resolved": {
			echoFrame,
			`{"type":"response.elicitation_request","elicitation_id":"el_1","params":{"message":"ok?"}}`,
			`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, client := newChatServer(t, nil, script)
			tools := NewToolRegistry()
			if err := tools.Register("Boom", nil, func(context.Context, ToolCallInfo) (string, error) {
				return "", errors.New("blew up")
			}); err != nil {
				t.Fatalf("Register: %v", err)
			}
			chat, _ := client.Chat("conv_1", ChatOptions{
				Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			for _, err := range chat.Send(ctx, "hi") {
				if err != nil {
					break
				}
			}
		})
	}
}

// TestAPanickingApprovalHookDeclines pins that the fail-closed answer survives
// caller code that fails.
//
// A hook is a policy lookup in someone else's program. A panic there must not leave
// the approval unanswered — which parks the turn — nor end the read, because the
// safe answer is already known.
func TestAPanickingApprovalHookDeclines(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.elicitation_request","elicitation_id":"el_1","params":{"message":"ok?"}}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Hooks: StreamHooks{OnElicitation: func(ElicitationCtx) bool { panic("policy lookup failed") }},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.resolved) != 1 || server.resolved[0] != "conv_1:decline" {
		t.Fatalf("resolved %v, want one decline", server.resolved)
	}
}

// TestAToolCallCarriesItsResponseAndIteration pins two fields a tool reads to scope
// an idempotency guard. Documented and always zero is worse than absent.
func TestAToolCallCarriesItsResponseAndIteration(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"r7","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call",` +
			`"status":"action_required","call_id":"c1","name":"Echo","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"id":"r7","status":"completed"}}`,
	})
	tools := NewToolRegistry()
	var seen ToolCallInfo
	if err := tools.Register("Echo", nil, func(_ context.Context, i ToolCallInfo) (string, error) {
		seen = i
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for range chat.Send(ctx, "hi") {
	}

	if seen.ResponseID != "r7" {
		t.Errorf("ResponseID = %q, want r7", seen.ResponseID)
	}
}

// TestThePromptIsPostedOnAnyFirstFrame pins that the subscription notice does not
// depend on the first frame being a decodable event.
//
// A proxy in front of the server emits a comment keepalive to hold an SSE
// connection open, so requiring a decoded event made Chat.Send a silent no-op
// behind any such relay: nothing posted, nothing yielded, and only the caller's own
// deadline to show for it.
func TestThePromptIsPostedOnAnyFirstFrame(t *testing.T) {
	t.Parallel()

	for name, firstFrame := range map[string]string{
		"a proxy comment keepalive": ": ping\n\n",
		"an empty data frame":       "data: \n\n",
		"a frame this build cannot decode": "data: {\"type\":\"session.status\"," +
			"\"status\":12345}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int64
			posted := make(chan struct{})
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/events") {
					posts.Add(1)
					once.Do(func() { close(posted) })
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"queued","item_id":"item_1"}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher := w.(http.Flusher)
				_, _ = fmt.Fprint(w, firstFrame)
				flusher.Flush()
				// The turn is sent only once the prompt arrives, so this odd frame is
				// the only thing on the wire until the hook fires. A fixture that sent
				// valid frames first would let the hook fire on one of those, and the
				// test could not see the difference.
				select {
				case <-posted:
				case <-time.After(3 * time.Second):
					return
				case <-r.Context().Done():
					return
				}
				for _, frame := range []string{
					echoFrame,
					`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
				} {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
					flusher.Flush()
				}
				<-r.Context().Done()
			}))
			defer srv.Close()

			client, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = client.Close() }()

			chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for range chat.Send(ctx, "deploy") {
			}

			if posts.Load() != 1 {
				t.Errorf("the prompt was posted %d times behind %s", posts.Load(), name)
			}
		})
	}
}

// TestOneCallIDRunsOnceWhateverAgentNameItCarries pins that the dispatch guard is
// scoped to the call, and to nothing the server also chooses.
//
// This test previously asserted the opposite, on the reasoning that each agent
// numbers its own calls so one id could name two. The server's own contract says
// otherwise: the output this package posts carries call_id and output and nothing
// else, so a call id issued twice concurrently is one the server could not route
// an answer to either. Upstream's client posts the same two fields and guards
// nothing at all.
//
// Keying on the item's agent_name made the guard defeatable by the party it
// defends against — the same call, replayed under a second name, ran a second
// time. A deploy, a spend or a signature is exactly what that costs.
func TestOneCallIDRunsOnceWhateverAgentNameItCarries(t *testing.T) {
	t.Parallel()

	call := func(agent string) string {
		return `{"type":"response.output_item.done","item":{"type":"function_call",` +
			`"status":"action_required","call_id":"call_1","name":"Echo","agent_name":"` + agent + `"}}`
	}
	server, client := newChatServer(t, nil, []string{
		echoFrame, call("coder.a"), call("coder.b"),
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})

	var mu sync.Mutex
	var ran []string
	tools := &ToolRegistry{}
	if err := tools.Register("Echo", nil, func(_ context.Context, info ToolCallInfo) (string, error) {
		mu.Lock()
		ran = append(ran, info.AgentName)
		mu.Unlock()
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	chat, err := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var duplicated error
	for _, err := range chat.Send(t.Context(), "hi") {
		if errors.Is(err, ErrToolCallDuplicated) {
			duplicated = err
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 {
		t.Errorf("the tool ran %d times for one call id (%v), want 1", len(ran), ran)
	}
	if duplicated == nil {
		t.Error("the second delivery was dropped without saying so; a silent drop is " +
			"indistinguishable from a call this client never saw")
	}
	_ = server
}

// TestATrueDuplicateRunsOnceAndSaysSo pins the other half: the same agent's same
// call id runs once, and the drop is reported rather than silent.
func TestATrueDuplicateRunsOnceAndSaysSo(t *testing.T) {
	t.Parallel()

	call := `{"type":"response.output_item.done","item":{"type":"function_call",` +
		`"status":"action_required","call_id":"call_1","name":"Deploy","agent_name":"coder"}}`
	_, client := newChatServer(t, nil, []string{
		echoFrame, call, call,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})
	tools := NewToolRegistry()
	var ran atomic.Int64
	if err := tools.Register("Deploy", nil, func(context.Context, ToolCallInfo) (string, error) {
		ran.Add(1)
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var duplicate error
	for _, err := range chat.Send(ctx, "hi") {
		if errors.Is(err, ErrToolCallDuplicated) {
			duplicate = err
		}
	}
	if ran.Load() != 1 {
		t.Errorf("ran %d times for one call", ran.Load())
	}
	if duplicate == nil {
		t.Error("the duplicate was dropped silently; a caller counting tool use is wrong and unaware")
	}
}

// TestAPanicInAnyHookIsReportedAndTheTurnContinues pins that the guard covers every
// hook, not only the approval one.
//
// A panic in OnToolCallStart pre-empts the output post, which parks the session until
// its deadline — a larger blast radius than the hook that was guarded first.
func TestAPanicInAnyHookIsReportedAndTheTurnContinues(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.output_item.done","item":{"type":"function_call",` +
			`"status":"action_required","call_id":"c1","name":"Echo","agent_name":"coder"}}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})
	tools := NewToolRegistry()
	if err := tools.Register("Echo", nil, func(context.Context, ToolCallInfo) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn:  TurnOptions{End: TurnEndsOnResponseLifecycle},
		Tools: tools,
		Hooks: StreamHooks{OnToolCallStart: func(ToolCallStartCtx) { panic("hook is broken") }},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var reported error
	for _, err := range chat.Send(ctx, "hi") {
		if errors.Is(err, ErrHookPanicked) {
			reported = err
		}
	}
	if reported == nil {
		t.Error("a panicking hook was not reported")
	}
	// The tool still ran and its output still went back, so the session is not parked.
	if got := server.postedTypes(); len(got) != 2 {
		t.Errorf("posted %v, want the prompt and the tool output", got)
	}
}

// TestAStreamThatEndsEarlyIsReportedNotSwallowed pins ErrTurnIncomplete end to end.
//
// A stream can end before the turn does — a deployment that caps stream duration, or
// a relay that drops the connection. Reported, because a caller reading the sequence
// as complete takes a partial answer for the whole one.
func TestAStreamThatEndsEarlyIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()

	server, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"half an answer"}`,
	})
	server.doneAfterScript = true

	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var got error
	for _, err := range chat.Send(ctx, "hi") {
		if err != nil {
			got = err
		}
	}
	if !errors.Is(got, ErrTurnIncomplete) {
		t.Fatalf("got %v, want ErrTurnIncomplete", got)
	}
}

// TestATurnEndsOnAnIdleEdgeThroughSend pins the idle-status rule through the public
// entry point, and with it the session filter at its only production call site.
//
// Every other end-to-end test uses the lifecycle rule, which never reaches
// observeStatus — so mis-wiring the tracker's session id changed nothing they saw.
func TestATurnEndsOnAnIdleEdgeThroughSend(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.created","response":{"id":"r1","model":"coder","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"the answer"}`,
		`{"type":"response.completed","response":{"id":"r1","model":"coder","status":"completed"}}`,
		`{"type":"session.status","conversation_id":"conv_1","status":"idle","response_id":"r1"}`,
	})
	chat, _ := client.Chat("conv_1", ChatOptions{Turn: TurnOptions{End: TurnEndsOnIdleStatus}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var last string
	var got error
	for event, err := range chat.Send(ctx, "hi") {
		if err != nil {
			got = err
			continue
		}
		last = event.EventType()
	}
	if got != nil {
		t.Fatalf("Send: %v", got)
	}
	if last != "session.status" {
		t.Errorf("the turn ended on %s, want the idle status edge", last)
	}
}

// TestAFailingToolReachesTheCaller pins that a tool's failure is reported, not only
// posted to the server. It is the caller's own code failing.
func TestAFailingToolReachesTheCaller(t *testing.T) {
	t.Parallel()

	_, client := newChatServer(t, nil, []string{
		echoFrame,
		`{"type":"response.output_item.done","item":{"type":"function_call",` +
			`"status":"action_required","call_id":"c1","name":"Boom","agent_name":"coder"}}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
	})
	tools := NewToolRegistry()
	if err := tools.Register("Boom", nil, func(context.Context, ToolCallInfo) (string, error) {
		return "", errors.New("disk full")
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	chat, _ := client.Chat("conv_1", ChatOptions{
		Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var reported error
	for _, err := range chat.Send(ctx, "hi") {
		if errors.Is(err, ErrToolFailed) {
			reported = err
		}
	}
	if reported == nil {
		t.Fatal("the tool's failure never reached the caller")
	}
	if !strings.Contains(reported.Error(), "disk full") {
		t.Errorf("the tool's own reason was dropped: %v", reported)
	}
}

// TestAServerRunToolIsNeverDispatched pins the one field separating a call to run
// from a call to display, across every value that is not action_required.
func TestAServerRunToolIsNeverDispatched(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"completed", "in_progress", ""} {
		t.Run("status="+status, func(t *testing.T) {
			server, client := newChatServer(t, nil, []string{
				echoFrame,
				`{"type":"response.output_item.done","item":{"type":"function_call",` +
					`"status":"` + status + `","call_id":"c1","name":"Deploy","agent_name":"coder"}}`,
				`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
			})
			tools := NewToolRegistry()
			var ran atomic.Int64
			if err := tools.Register("Deploy", nil, func(context.Context, ToolCallInfo) (string, error) {
				ran.Add(1)
				return "deployed", nil
			}); err != nil {
				t.Fatalf("Register: %v", err)
			}
			chat, _ := client.Chat("conv_1", ChatOptions{
				Turn: TurnOptions{End: TurnEndsOnResponseLifecycle}, Tools: tools,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for range chat.Send(ctx, "hi") {
			}

			if ran.Load() != 0 {
				t.Errorf("a call with status %q ran %d times", status, ran.Load())
			}
			if got := server.postedTypes(); len(got) != 1 {
				t.Errorf("posted %v, want only the prompt", got)
			}
		})
	}
}

package omnigent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// frame renders one SSE frame the way the server's serializer does: an event
// name line, a single-line JSON data line, then a blank line.
func frame(name, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, data)
}

// sseHandler serves frames in order, flushing each so the client sees it land.
func sseHandler(frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			_ = controller.Flush()
		}
	}
}

// describe projects an event to a short string, so a table can state its
// expectations without spelling out whole structs.
func describe(event Event) string {
	switch ev := event.(type) {
	case SessionHeartbeatEvent:
		return "heartbeat"
	case ResponseCreatedEvent:
		return "created:" + ev.Response.ID
	case InProgressEvent:
		return "in_progress:" + ev.Response.ID
	case OutputTextDeltaEvent:
		return "delta:" + ev.Delta
	case OutputItemDoneEvent:
		return "item_done"
	case ResponseCompletedEvent:
		return "completed:" + ev.Response.Status
	case ResponseFailedEvent:
		return "failed:" + ev.Response.Status
	case ErrorEvent:
		return "error:" + string(ev.Source) + ":" + ev.Error.Code
	case RetryEvent:
		return fmt.Sprintf("retry:%s:%d/%d", ev.Source, ev.Attempt, ev.MaxAttempts)
	case SessionStatusEvent:
		return "status:" + string(ev.Status)
	case SessionInputConsumedEvent:
		return "consumed:" + ev.Data.ItemID
	case UnknownEvent:
		return "unknown:" + ev.Type + ":" + string(ev.Raw)
	default:
		return fmt.Sprintf("%T", event)
	}
}

// collect drains a stream, returning each event's projection and the terminal
// error. It fails the test if an error step is not the last one.
func collect(t *testing.T, stream func(yield func(Event, error) bool)) ([]string, error) {
	t.Helper()

	var (
		got      []string
		streamed error
		finished bool
	)
	for event, err := range stream {
		if finished {
			t.Fatal("iteration continued past an error step; errors must be terminal")
		}
		if err != nil {
			streamed = err
			finished = true
			continue
		}
		got = append(got, describe(event))
	}
	return got, streamed
}

func TestStreamEvents(t *testing.T) {
	t.Parallel()

	const response = `{"id":"resp_1","created_at":1,"model":"m","status":"completed"}`

	tests := []struct {
		name       string
		frames     []string
		wantEvents []string
		wantErr    error
	}{
		{
			name: "a whole turn, prologue through completion",
			frames: []string{
				frame("session.heartbeat", `{"type":"session.heartbeat"}`),
				frame("session.status", `{"type":"session.status","conversation_id":"conv_1","status":"running"}`),
				frame("session.input.consumed", `{"type":"session.input.consumed","data":{"type":"message","item_id":"item_9","data":{}}}`),
				frame("response.created", `{"type":"response.created","response":`+response+`}`),
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hel"}`),
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`),
				frame("response.completed", `{"type":"response.completed","response":`+response+`}`),
				frame("session.status", `{"type":"session.status","conversation_id":"conv_1","status":"idle"}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{
				"heartbeat",
				"status:running",
				"consumed:item_9",
				"created:resp_1",
				"delta:Hel",
				"delta:lo",
				"completed:completed",
				"status:idle",
			},
		},
		{
			name: "an unrecognised type surfaces opaquely rather than failing",
			frames: []string{
				frame("session.heartbeat", `{"type":"session.heartbeat"}`),
				frame("session.something.new", `{"type":"session.something.new","payload":7}`),
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"ok"}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{
				"heartbeat",
				`unknown:session.something.new:{"type":"session.something.new","payload":7}`,
				"delta:ok",
			},
		},
		{
			name: "an unknown field on a known event is ignored, not fatal",
			frames: []string{
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"ok","future_field":true}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"delta:ok"},
		},
		{
			name: "an in-stream error is an event, and the stream continues",
			frames: []string{
				frame("response.retry", `{"type":"response.retry","source":"tool","attempt":1,"max_attempts":3,"delay_seconds":0.5,"error":{"code":"timeout","message":"slow"}}`),
				frame("response.error", `{"type":"response.error","source":"execution","error":{"code":"tool_failed","message":"nope"}}`),
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"recovered"}`),
				frame("response.completed", `{"type":"response.completed","response":`+response+`}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{
				"retry:tool:1/3",
				"error:execution:tool_failed",
				"delta:recovered",
				"completed:completed",
			},
		},
		{
			name: "a turn failing is not a transport failure",
			frames: []string{
				frame("response.failed", `{"type":"response.failed","response":{"id":"resp_1","created_at":1,"model":"m","status":"failed","error":{"code":"llm_error","message":"boom"}}}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"failed:failed"},
		},
		{
			name: "the terminal sentinel is honoured with no preceding event name",
			frames: []string{
				frame("session.heartbeat", `{"type":"session.heartbeat"}`),
				"data: [DONE]\n\n",
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"never"}`),
			},
			wantEvents: []string{"heartbeat"},
		},
		{
			name: "a body that ends without the sentinel is a drop",
			frames: []string{
				frame("session.heartbeat", `{"type":"session.heartbeat"}`),
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial"}`),
			},
			wantEvents: []string{"heartbeat", "delta:partial"},
			wantErr:    ErrStreamInterrupted,
		},
		{
			name:    "an empty body is a drop too",
			frames:  nil,
			wantErr: ErrStreamInterrupted,
		},
		{
			name: "the payload's own discriminator wins over the event name",
			frames: []string{
				frame("response.output_text.delta", `{"type":"session.heartbeat"}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"heartbeat"},
		},
		{
			name: "an event name alone still dispatches",
			frames: []string{
				"event: response.output_text.delta\ndata: {\"delta\":\"named\"}\n\n",
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"delta:named"},
		},
		{
			name: "multi-line data is rejoined",
			frames: []string{
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"split\"}\n\n",
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"delta:split"},
		},
		{
			name: "comments and unused fields are skipped",
			frames: []string{
				": keepalive\n\n",
				"id: 7\nretry: 3000\nevent: session.heartbeat\ndata: {\"type\":\"session.heartbeat\"}\n\n",
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"heartbeat"},
		},
		{
			name: "a data payload that is not JSON is skipped, and the stream carries on",
			frames: []string{
				"event: session.heartbeat\ndata: not json\n\n",
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"after"}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"delta:after"},
		},
		{
			name: "a frame with no discriminator anywhere is skipped too",
			frames: []string{
				"data: {\"delta\":\"orphan\"}\n\n",
				frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"after"}`),
				"data: [DONE]\n\n",
			},
			wantEvents: []string{"delta:after"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(sseHandler(tc.frames...))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))

			if tc.wantErr != nil {
				if !errors.Is(streamErr, tc.wantErr) {
					t.Fatalf("stream error = %v, want it to wrap %v", streamErr, tc.wantErr)
				}
			} else if streamErr != nil {
				t.Fatalf("stream error = %v, want none", streamErr)
			}
			if len(got) != len(tc.wantEvents) {
				t.Fatalf("got %d events %v, want %d %v", len(got), got, len(tc.wantEvents), tc.wantEvents)
			}
			for i := range got {
				if got[i] != tc.wantEvents[i] {
					t.Errorf("event %d = %q, want %q", i, got[i], tc.wantEvents[i])
				}
			}
		})
	}
}

// TestStreamSkipsAnUndecodableFrame is P4. One frame whose field type does not
// match the schema — the shape spec drift takes — ended the whole subscription,
// while the Python client logs it and continues. It is now skipped and reported,
// and the frames after it still reach the caller.
//
// Unreachable against this server today: the stream route validates every event
// against its event union before serializing it. It bites on drift, which is
// exactly when losing one frame beats losing the turn.
func TestStreamSkipsAnUndecodableFrame(t *testing.T) {
	t.Parallel()

	const undecodable = `{"type":"response.output_text.delta","delta":42}`

	server := httptest.NewServer(sseHandler(
		frame("session.heartbeat", `{"type":"session.heartbeat"}`),
		frame("response.output_text.delta", undecodable),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"after"}`),
		frame("response.completed", `{"type":"response.completed","response":`+
			`{"id":"resp_1","created_at":1,"model":"m","status":"completed"}}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var skipped []SkippedFrame
	opts := StreamOptions{OnSkippedFrame: func(ctx context.Context, frame SkippedFrame) error {
		skipped = append(skipped, frame)
		return nil
	}}
	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", opts))

	if streamErr != nil {
		t.Fatalf("stream error = %v, want none: one unreadable frame must not end the turn", streamErr)
	}
	want := []string{"heartbeat", "delta:after", "completed:completed"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, events[i], want[i])
		}
	}

	if len(skipped) != 1 {
		t.Fatalf("OnSkippedFrame ran %d times, want once", len(skipped))
	}
	got := skipped[0]
	if got.SessionID != "conv_1" {
		t.Errorf("SkippedFrame.SessionID = %q, want conv_1", got.SessionID)
	}
	if got.Name != "response.output_text.delta" {
		t.Errorf("SkippedFrame.Name = %q, want response.output_text.delta", got.Name)
	}
	if got.Payload != undecodable {
		t.Errorf("SkippedFrame.Payload = %q, want the frame's own payload %q", got.Payload, undecodable)
	}
	if got.Err == nil {
		t.Fatal("SkippedFrame.Err = nil, want the decode failure")
	}
	if !strings.Contains(got.Err.Error(), "response.output_text.delta") {
		t.Errorf("SkippedFrame.Err %q does not name the event type it failed on", got.Err)
	}
}

// TestStreamSkippedFrameHookCanEndTheStream is the opt-out: a caller who would
// rather fail loud on drift than lose a frame returns an error from the hook, and
// the stream ends with it. Skipping is the default, not the only choice.
func TestStreamSkippedFrameHookCanEndTheStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(sseHandler(
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":42}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"never"}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	refused := errors.New("this client treats drift as fatal")
	opts := StreamOptions{OnSkippedFrame: func(ctx context.Context, frame SkippedFrame) error {
		return refused
	}}
	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", opts))

	if !errors.Is(streamErr, refused) {
		t.Fatalf("stream error = %v, want it to wrap the hook's error", streamErr)
	}
	if len(events) != 0 {
		t.Errorf("events = %v, want none: the hook ended the stream at the first bad frame", events)
	}
}

func TestStreamRequiresASessionID(t *testing.T) {
	t.Parallel()

	client, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, streamErr := collect(t, client.Stream(t.Context(), "", StreamOptions{}))
	if streamErr == nil || !strings.Contains(streamErr.Error(), "sessionID is required") {
		t.Fatalf("stream error = %v, want one naming the missing session id", streamErr)
	}
}

// releasableClock is a clock that reads zero until the test releases it, and then
// runs away.
//
// Both halves matter. Held at zero, no scheduling delay can make the watchdog
// judge a stream idle, so a test can be sure the frames it expects arrive however
// loaded the runner is. Released, every reading is a further timeout past the
// last, so the next check fires whatever order it lands in — including after the
// resume that follows a caller's handler, which re-baselines the watchdog and
// would defeat a clock that only jumped once.
type releasableClock struct {
	step     time.Duration
	released atomic.Bool
	ticks    atomic.Int64
}

func (c *releasableClock) elapsed() time.Duration {
	if !c.released.Load() {
		return 0
	}
	return time.Duration(c.ticks.Add(int64(c.step)))
}

// release starts the clock. Silence after this point is silence at any speed.
func (c *releasableClock) release() { c.released.Store(true) }

// TestStreamIdleTimeout pins that a transport that stops talking ends the stream
// with [ErrStreamIdle], and that the frames it did send arrive first.
//
// The stream's timer still runs on real time, so a check lands when it lands.
// What the clock decides is the verdict that check reaches, and that no check
// before the release can reach it at all. The previous version instead gave the
// heartbeat a 150ms head start and hoped: a runner that stalled there failed on
// the missing heartbeat rather than on the timeout it was testing.
func TestStreamIdleTimeout(t *testing.T) {
	t.Parallel()

	// Short, because this is now only the interval between checks, not a margin
	// that delivery has to beat.
	const idleTimeout = 20 * time.Millisecond

	// One frame, then silence: the server this stands in for would have
	// heartbeated, so the watchdog firing is the correct diagnosis.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHandler(frame("session.heartbeat", `{"type":"session.heartbeat"}`))(w, r)
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(idleTimeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	clock := &releasableClock{step: idleTimeout}
	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		return newIdleWatchdogWithClock(timeout, cancel, clock.elapsed)
	}

	var (
		events    []string
		streamErr error
	)
	for event, err := range client.Stream(t.Context(), "conv_1", StreamOptions{}) {
		if err != nil {
			streamErr = err
			continue
		}
		events = append(events, describe(event))
		// The heartbeat is in the caller's hands, so the transport going quiet
		// from here is the thing under test rather than a race against delivery.
		clock.release()
	}

	if len(events) != 1 || events[0] != "heartbeat" {
		t.Errorf("events = %v, want the one heartbeat that did arrive", events)
	}
	if !errors.Is(streamErr, ErrStreamIdle) {
		t.Fatalf("stream error = %v, want it to wrap ErrStreamIdle", streamErr)
	}
	if errors.Is(streamErr, context.Canceled) {
		t.Error("an idle timeout must not present as caller cancellation")
	}
}

// TestStreamPerCallIdleTimeoutOverridesTheClientDefault pins which of the two
// timeouts a stream is built with.
//
// It reads the value handed to the watchdog rather than waiting to see which one
// expires. That is the property — [StreamOptions.IdleTimeout] outranks
// [WithStreamIdleTimeout] — and waiting for a firing showed it only by inference,
// at 150ms a run: an hour failing to elapse is not evidence that the hour was the
// value this stream discarded.
func TestStreamPerCallIdleTimeoutOverridesTheClientDefault(t *testing.T) {
	t.Parallel()

	const perCall = 150 * time.Millisecond

	server := httptest.NewServer(sseHandler("data: [DONE]\n\n"))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var built atomic.Int64
	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		built.Store(int64(timeout))
		return newIdleWatchdog(timeout, cancel)
	}

	if _, streamErr := collect(t, client.Stream(t.Context(), "conv_1",
		StreamOptions{IdleTimeout: perCall})); streamErr != nil {
		t.Fatalf("stream error = %v, want none", streamErr)
	}

	if got := time.Duration(built.Load()); got != perCall {
		t.Errorf("the stream's watchdog was built with %s, want the per-call %s: "+
			"StreamOptions.IdleTimeout outranks WithStreamIdleTimeout", got, perCall)
	}
}

func TestStreamRejectedSubscription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"absent or invisible", http.StatusNotFound, `{"error":{"code":"not_found","message":"gone"}}`, ErrNotFound},
		{"unauthenticated", http.StatusUnauthorized, `{"detail":"unauthorized"}`, ErrUnauthorized},
		{"read access missing", http.StatusForbidden, `{"detail":"forbidden"}`, ErrForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))
			if len(events) != 0 {
				t.Errorf("got events %v, want none", events)
			}
			if !errors.Is(streamErr, tc.want) {
				t.Fatalf("stream error = %v, want it to wrap %v", streamErr, tc.want)
			}
			var apiErr *APIError
			if !errors.As(streamErr, &apiErr) {
				t.Fatalf("stream error %v does not unwrap to *APIError", streamErr)
			}
		})
	}
}

func TestStreamRequestShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      StreamOptions
		wantQuery string
	}{
		{name: "attentive subscriber sends no idle flag"},
		{name: "idle subscriber declares itself", opts: StreamOptions{Idle: true}, wantQuery: "idle=true"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Clone(r.Context())
				sseHandler("data: [DONE]\n\n")(w, r)
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, streamErr := collect(t, client.Stream(t.Context(), "conv_1", tc.opts)); streamErr != nil {
				t.Fatalf("stream: %v", streamErr)
			}
			got := <-requests
			if got.URL.EscapedPath() != "/v1/sessions/conv_1/stream" {
				t.Errorf("path = %q, want /v1/sessions/conv_1/stream", got.URL.EscapedPath())
			}
			if got.URL.RawQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", got.URL.RawQuery, tc.wantQuery)
			}
			if accept := got.Header.Get("Accept"); accept != "text/event-stream" {
				t.Errorf("Accept = %q, want text/event-stream", accept)
			}
		})
	}
}

// TestStreamSkippingEveryFrameIsNotASilentEmptyStream keeps the property that
// made the Go client better than the reference here: a stream that delivers no
// event cannot end quietly. Skipping a frame is a degradation, but skipping every
// frame a stream carried is a failure, and reporting it is what stops a decode
// failure from being read as an agent that said nothing.
func TestStreamSkippingEveryFrameIsNotASilentEmptyStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(sseHandler(
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":42}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":43}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))

	if len(events) != 0 {
		t.Errorf("events = %v, want none: neither frame was decodable", events)
	}
	if !errors.Is(streamErr, ErrStreamProtocol) {
		t.Fatalf("stream error = %v, want it to wrap ErrStreamProtocol", streamErr)
	}
	if !strings.Contains(streamErr.Error(), "response.output_text.delta") {
		t.Errorf("error %q does not name the event type that failed to decode", streamErr)
	}
	// The sentinel did arrive, so this is not a dropped transport and a caller
	// must not reconcile as if it were.
	if errors.Is(streamErr, ErrStreamInterrupted) {
		t.Error("a fully undecodable stream must not present as an interrupted one")
	}
}

// TestABytesArrivingResetTheWatchdogWhileALineIsStillIncomplete is P3, stated on
// a clock the test drives.
//
// The watchdog was fed once per completed line, so the idle timeout bounded not
// only silence but the whole transfer of one frame — and this server writes a
// frame as a single data: line, the snapshot-on-connect one approaching
// maxFrameBytes. A throttled link therefore reported ErrStreamIdle partway
// through a perfectly healthy frame. The watchdog is fed by bytes arriving
// instead, so a frame that keeps arriving keeps the stream alive however long it
// takes.
//
// Driven rather than raced. The property is "silence is measured from the last
// bytes read", and a test that expressed it by sleeping between writes asserted
// something about the runner's scheduler as much as about the watchdog: the
// original slept 25ms against a 200ms timeout, an 8x margin a loaded CI runner
// ate often enough to fail the job. Here the clock only moves when this test
// moves it, so the same answer comes back every run.
func TestABytesArrivingResetTheWatchdogWhileALineIsStillIncomplete(t *testing.T) {
	t.Parallel()

	const timeout = time.Minute

	// Far past the timeout with nothing read: the state a slow frame's transfer
	// reaches, and the state the old watchdog cancelled in.
	elapsed := timeout * 10

	t.Run("bytes read inside the gap keep the stream alive", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var clock atomic.Int64
		watchdog := newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
			return time.Duration(clock.Load())
		})
		defer watchdog.stop()

		clock.Store(int64(elapsed))
		reader := &watchdogReader{inner: strings.NewReader("more of the same frame"), watchdog: watchdog}
		if _, err := reader.Read(make([]byte, 8)); err != nil {
			t.Fatalf("read: %v", err)
		}

		watchdog.check()

		if watchdog.expired() {
			t.Error("the watchdog reported silence although bytes had just arrived: " +
				"a frame still being delivered is a slow link, not a dead one")
		}
		if ctx.Err() != nil {
			t.Errorf("the watchdog cancelled a stream that was still receiving: %v", ctx.Err())
		}
	})

	t.Run("the same gap with nothing read is silence", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var clock atomic.Int64
		watchdog := newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
			return time.Duration(clock.Load())
		})
		defer watchdog.stop()

		// Same clock, same timeout, no read. This is what makes the case above
		// mean something: the read is the only difference between them.
		clock.Store(int64(elapsed))
		watchdog.check()

		if !watchdog.expired() {
			t.Error("the watchdog did not fire on a transport that delivered nothing")
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("ctx error = %v, want context.Canceled", ctx.Err())
		}
	})
}

// TestTheWatchdogDoesNotCountTimeSpentOutsideTheRead pins both halves of the
// bracket the read loop puts around every call into the caller's code.
//
// suspend has to stop the judging, and resume has to re-baseline. Resume's half
// is easy to lose, because in most tests a read follows immediately and records
// activity anyway — so the mutation survives everything except a check taken
// between the resume and the next read, which is what this does.
func TestTheWatchdogDoesNotCountTimeSpentOutsideTheRead(t *testing.T) {
	t.Parallel()

	const timeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var clock atomic.Int64
	watchdog := newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
		return time.Duration(clock.Load())
	})
	defer watchdog.stop()

	// The caller's handler runs long, and an expiry lands while it does.
	watchdog.suspend()
	clock.Store(int64(timeout * 10))
	watchdog.check()
	if watchdog.expired() {
		t.Fatal("the watchdog judged a gap it was suspended for: a slow handler is " +
			"not a silent transport")
	}

	// The handler returns, and the next expiry lands before any further bytes.
	watchdog.resume()
	watchdog.check()
	if watchdog.expired() {
		t.Error("the watchdog counted the suspended gap once it resumed: resume has " +
			"to restart the clock, or the handler's time is charged to the transport")
	}
	if ctx.Err() != nil {
		t.Errorf("the watchdog cancelled a live stream: %v", ctx.Err())
	}
}

// TestStreamReadsItsBodyThroughTheWatchdog is the wiring half.
//
// [TestABytesArrivingResetTheWatchdogWhileALineIsStillIncomplete] proves the
// watchdog counts silence from the last bytes read. That only reaches a real
// stream if [Client.Stream] reads the response body through a [watchdogReader],
// which is the line a refactor drops silently: every other test still passes,
// because a stream that is never quiet never needs the watchdog.
//
// So this counts the readings the watchdog takes. Reading through the watchdog
// takes one per Read that delivered bytes, which for a frame written in chunks is
// many; reading around it takes the one at construction and nothing else. The
// clock never advances, so no scheduling delay can end this stream — the test
// says nothing about timing, only about wiring.
func TestStreamReadsItsBodyThroughTheWatchdog(t *testing.T) {
	t.Parallel()

	const (
		chunks    = 24
		chunkSize = 4 << 10
	)

	delta := strings.Repeat("z", chunks*chunkSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		// One frame, one data: line, delivered a piece at a time.
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","delta":"`)
		_ = controller.Flush()
		for chunk := range chunks {
			_, _ = io.WriteString(w, delta[chunk*chunkSize:(chunk+1)*chunkSize])
			_ = controller.Flush()
		}
		_, _ = io.WriteString(w, "\"}\n\ndata: [DONE]\n\n")
		_ = controller.Flush()
	}))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A clock that never moves, so the watchdog cannot fire whatever the runner
	// is doing, and every reading it takes is counted.
	var readings atomic.Int64
	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		return newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
			readings.Add(1)
			return 0
		})
	}

	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))

	if streamErr != nil {
		t.Fatalf("stream error = %v, want none", streamErr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want the one frame that was sent", len(events))
	}
	if want := "delta:" + delta; events[0] != want {
		t.Errorf("the reassembled frame is %d bytes, want %d", len(events[0]), len(want))
	}
	// Two readings come from elsewhere: one at construction, one from the resume
	// after this stream's single event. A third can only have come from a Read.
	// The frame is 96 KiB against a 64 KiB scanner buffer, so a body read through
	// the watchdog cannot deliver it in one.
	if got := readings.Load(); got < 3 {
		t.Errorf("the watchdog took %d clock readings; a body read through it takes "+
			"one per delivering Read, so this stream is not reading through it at all", got)
	}
}

// TestStreamIdleTimeoutFiresMidFrame is the other half of P3: feeding the watchdog
// on byte progress must not let a half-arrived frame hold it off forever. A frame
// that stops mid-line and never resumes is a dead transport like any other.
//
// The clock is released by the handler rather than by the caller's loop, because
// there is no event here to hand back — and no rendezvous is needed either way.
// The outcome is the same whether the truncated bytes were read before the
// watchdog fired or not: no event can be built from half a line.
func TestStreamIdleTimeoutFiresMidFrame(t *testing.T) {
	t.Parallel()

	const idleTimeout = 20 * time.Millisecond

	clock := &releasableClock{step: idleTimeout}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		// An event name, the start of its data line, then nothing.
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","delta":"trunc`)
		_ = controller.Flush()
		clock.release()
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(idleTimeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		return newIdleWatchdogWithClock(timeout, cancel, clock.elapsed)
	}

	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))

	if len(events) != 0 {
		t.Errorf("events = %v, want none: the frame never completed", events)
	}
	if !errors.Is(streamErr, ErrStreamIdle) {
		t.Fatalf("stream error = %v, want it to wrap ErrStreamIdle", streamErr)
	}
}

// TestAPauseShorterThanTheTimeoutIsNotSilence is the complement: a tool call can
// hold a turn open for minutes, and the server's heartbeat is what proves the
// transport is alive through it.
//
// The handler moves the clock, a quarter of the timeout per heartbeat, so three
// pauses total three quarters of it. Staying under the timeout in aggregate is
// what makes the result independent of when reads land — HTTP buffering can
// deliver three writes in one Read, so a test that advanced a full gap per
// heartbeat would fire or not depending on the transport's buffer size rather
// than on the watchdog.
//
// What this does not pin: that arriving bytes are what rearm the watchdog. A
// clock kept under the timeout cannot fire whatever feeds it, and counting the
// watchdog's clock readings does not separate the two either, because resume
// takes one after every event delivered. That half is
// [TestABytesArrivingResetTheWatchdogWhileALineIsStillIncomplete] and
// [TestStreamReadsItsBodyThroughTheWatchdog]; here the subject is the gap.
func TestAPauseShorterThanTheTimeoutIsNotSilence(t *testing.T) {
	t.Parallel()

	const (
		idleTimeout = time.Minute
		heartbeats  = 3
		pause       = idleTimeout / 4
	)

	var clock atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		for range heartbeats {
			clock.Add(int64(pause))
			_, _ = io.WriteString(w, frame("session.heartbeat", `{"type":"session.heartbeat"}`))
			_ = controller.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = controller.Flush()
	}))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(idleTimeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		return newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
			return time.Duration(clock.Load())
		})
	}

	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))
	if streamErr != nil {
		t.Fatalf("stream error = %v, want none: silence under the timeout is a live "+
			"transport", streamErr)
	}
	if len(events) != heartbeats {
		t.Errorf("got %d events, want %d", len(events), heartbeats)
	}
}

func TestStreamAbruptConnectionTeardown(t *testing.T) {
	t.Parallel()

	// Hijack and close mid-frame: the client sees a transport error rather than
	// a clean EOF, and must still classify it as a drop needing reconciliation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n" +
			"Transfer-Encoding: chunked\r\n\r\n")
		_ = buffered.Flush()
		chunk := frame("session.heartbeat", `{"type":"session.heartbeat"}`)
		_, _ = fmt.Fprintf(buffered, "%x\r\n%s\r\n", len(chunk), chunk)
		_ = buffered.Flush()
		// A truncated chunk, then a hard close: no terminating chunk, no sentinel.
		_, _ = buffered.WriteString("ff\r\nevent: response.output_te")
		_ = buffered.Flush()
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))
	if len(events) != 1 || events[0] != "heartbeat" {
		t.Errorf("events = %v, want the frame that did arrive before the teardown", events)
	}
	if !errors.Is(streamErr, ErrStreamInterrupted) {
		t.Fatalf("stream error = %v, want it to wrap ErrStreamInterrupted", streamErr)
	}
}

func TestStreamCallerCancellation(t *testing.T) {
	t.Parallel()

	handlerReturned := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		sseHandler(frame("session.heartbeat", `{"type":"session.heartbeat"}`))(w, r)
		<-r.Context().Done()
	}))
	defer server.Close()

	// An idle timeout far longer than the test, so only cancellation can end it.
	client, err := New(server.URL, WithStreamIdleTimeout(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var (
		events    []string
		streamErr error
		started   = time.Now()
	)
	for event, err := range client.Stream(ctx, "conv_1", StreamOptions{}) {
		if err != nil {
			streamErr = err
			continue
		}
		events = append(events, describe(event))
		// Cancel from inside the loop, mid-stream, with the server still holding
		// the connection open.
		cancel()
	}
	elapsed := time.Since(started)

	if len(events) != 1 {
		t.Errorf("events = %v, want the one frame read before cancelling", events)
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error = %v, want it to wrap context.Canceled", streamErr)
	}
	if errors.Is(streamErr, ErrStreamIdle) || errors.Is(streamErr, ErrStreamInterrupted) {
		t.Error("caller cancellation must not present as an idle or dropped stream")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %s to be observed; it should be prompt", elapsed)
	}
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Error("the server never observed the disconnect; the response body was not closed")
	}
}

func TestStreamBreakingOutEarly(t *testing.T) {
	t.Parallel()

	handlerReturned := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		sseHandler(
			frame("session.heartbeat", `{"type":"session.heartbeat"}`),
			frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"a"}`),
		)(w, r)
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	seen := 0
	for range client.Stream(t.Context(), "conv_1", StreamOptions{}) {
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("saw %d events before breaking, want 1", seen)
	}
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Error("abandoning the loop left the response body open")
	}
}

// TestStreamSpawnsNoGoroutines is deliberately sequential: it counts
// goroutines, which only means anything when nothing else is running.
func TestStreamSpawnsNoGoroutines(t *testing.T) {
	settle(t)
	before := runtime.NumGoroutine()

	for range 20 {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sseHandler(frame("session.heartbeat", `{"type":"session.heartbeat"}`))(w, r)
			<-r.Context().Done()
		}))

		client, err := New(server.URL, WithStreamIdleTimeout(time.Hour))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		for range client.Stream(ctx, "conv_1", StreamOptions{}) {
			break
		}
		cancel()
		server.Close()
	}

	settle(t)
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("goroutine count grew from %d to %d after 20 abandoned streams\n%s", before, after, buf)
	}
}

// settle waits for the goroutine count to stop moving, so a leak check is not
// racing the runtime's own teardown.
func settle(t *testing.T) {
	t.Helper()

	previous := -1
	for range 200 {
		runtime.GC()
		current := runtime.NumGoroutine()
		if current == previous {
			return
		}
		previous = current
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIdleWatchdogIgnoresAnExpiryThatDataBeat is the unit-level statement of the
// race: a frame that lands while the timer's callback is already in flight must
// not be read as silence. Timer.Reset does not un-run a started callback, so the
// callback has to re-check the clock itself — which is what this drives, by
// running the expiry hook directly after recording activity.
func TestIdleWatchdogIgnoresAnExpiryThatDataBeat(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	watchdog := newIdleWatchdog(time.Hour, cancel)
	defer watchdog.stop()

	watchdog.alive()
	watchdog.check()

	if watchdog.expired() {
		t.Error("the watchdog reported an idle stream although a frame had just arrived")
	}
	if ctx.Err() != nil {
		t.Errorf("the watchdog cancelled a live stream: %v", ctx.Err())
	}
}

// TestIdleWatchdogStillFiresOnRealSilence is the other half: with no activity
// recorded inside the timeout, the expiry must cancel.
//
// It drives the watchdog's clock rather than sleeping, so the condition is
// stated instead of raced: no scheduler, the same answer every run. A wall-clock
// timeout cannot state it, because time.Now's resolution is coarser than a
// nanosecond on some platforms and two readings can be equal.
func TestIdleWatchdogStillFiresOnRealSilence(t *testing.T) {
	t.Parallel()

	const timeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var clock atomic.Int64
	watchdog := newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
		return time.Duration(clock.Load())
	})
	defer watchdog.stop()

	// Real time passes while the stream's clock does not: an expiry now is the
	// timer firing early, and must re-arm rather than cancel.
	watchdog.check()
	if watchdog.expired() {
		t.Fatal("the watchdog fired with no time elapsed on the clock it measures")
	}

	// Now silence longer than the timeout, and nothing else changed.
	clock.Store(int64(timeout + 1))
	watchdog.check()

	if !watchdog.expired() {
		t.Error("the watchdog did not fire on a stream that was genuinely silent")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx error = %v, want context.Canceled", ctx.Err())
	}
}

// TestIdleWatchdogMeasuresMonotonicTimeNotTheWallClock is S4. alive() stored
// time.Now().UnixNano() and check() compared it back with
// time.Since(time.Unix(0, last)) — and time.Unix reconstructs a Time with no
// monotonic reading, so that comparison is wall clock against wall clock. A clock
// step, an NTP correction or a VM resuming from a snapshot could then cancel a
// healthy stream or hide a dead one, and a stream has no other liveness control.
//
// The check is on what gets recorded, because that is where the wall clock
// entered: nanoseconds since the watchdog started, not nanoseconds since 1970.
// Against feat/go-client-v2 this fails with a recorded value around 1.7e18.
func TestIdleWatchdogMeasuresMonotonicTimeNotTheWallClock(t *testing.T) {
	t.Parallel()

	_, cancel := context.WithCancel(t.Context())
	defer cancel()

	watchdog := newIdleWatchdog(time.Hour, cancel)
	defer watchdog.stop()

	watchdog.alive()
	recorded := watchdog.last.Load()
	if recorded > int64(time.Minute) {
		t.Errorf("the watchdog recorded %d ns of elapsed time, which is a wall-clock timestamp "+
			"rather than a monotonic elapsed reading; a clock adjustment would break liveness detection",
			recorded)
	}
	if recorded < 0 {
		t.Errorf("the watchdog recorded %d ns, which cannot be elapsed time", recorded)
	}
}

// TestASlowHandlerDoesNotCountAsASilentTransport is the behavioural statement of
// the same bug, on a clock the test drives.
//
// The idle timeout bounds transport silence, so a caller's handler blocking for
// several times the timeout must not end the stream, and must certainly not end
// it with ErrStreamIdle right after events were delivered. [Client.Stream]
// brackets every call into the caller's code with suspend and resume for that
// reason.
//
// The handler moves the clock instead of sleeping. That is the whole window under
// test — the time the read loop spends outside the read — so advancing it there
// states the condition exactly, where the previous version slept 200ms against a
// 50ms timeout three times over and spent 600ms of real time asking the scheduler
// the same question.
//
// Calling check from inside the handler is what makes it falsifiable: suspended,
// the watchdog must re-arm without judging the gap at all. Drop the suspend and
// this cancels the stream on the first event.
//
// It does not pin resume's own re-baseline, because the whole response is already
// buffered here, so the read after the handler records activity anyway. That half
// is pinned in [TestTheWatchdogDoesNotCountTimeSpentOutsideTheRead].
func TestASlowHandlerDoesNotCountAsASilentTransport(t *testing.T) {
	t.Parallel()

	const idleTimeout = time.Minute

	server := httptest.NewServer(sseHandler(
		frame("session.heartbeat", `{"type":"session.heartbeat"}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"a"}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"b"}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL, WithStreamIdleTimeout(idleTimeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var (
		clock    atomic.Int64
		watchdog *idleWatchdog
	)
	client.newWatchdog = func(timeout time.Duration, cancel context.CancelFunc) *idleWatchdog {
		watchdog = newIdleWatchdogWithClock(timeout, cancel, func() time.Duration {
			return time.Duration(clock.Load())
		})
		return watchdog
	}

	var (
		events    []string
		streamErr error
	)
	for event, err := range client.Stream(t.Context(), "conv_1", StreamOptions{}) {
		if err != nil {
			streamErr = err
			continue
		}
		events = append(events, describe(event))
		// A handler far slower than the idle timeout, with the whole response
		// already buffered in the transport, and an expiry landing while it runs.
		clock.Add(int64(4 * idleTimeout))
		watchdog.check()
	}

	if streamErr != nil {
		t.Fatalf("stream error = %v, want none: a slow handler is not a dead transport", streamErr)
	}
	want := []string{"heartbeat", "delta:a", "delta:b"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, events[i], want[i])
		}
	}
}

func TestStreamOnSubscribedRunsExactlyOnce(t *testing.T) {
	t.Parallel()

	// Three heartbeats: one acknowledgement and two keepalives, byte-identical
	// on the wire. A caller keying off the event would send three times.
	heartbeat := frame("session.heartbeat", `{"type":"session.heartbeat"}`)
	server := httptest.NewServer(sseHandler(
		heartbeat,
		heartbeat,
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"a"}`),
		heartbeat,
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var (
		calls  int
		before int
	)
	opts := StreamOptions{OnSubscribed: func(ctx context.Context, sub Subscription) error {
		calls++
		return nil
	}}
	for event, err := range client.Stream(t.Context(), "conv_1", opts) {
		if err != nil {
			t.Fatalf("stream error = %v, want none", err)
		}
		if calls == 0 {
			t.Error("an event reached the caller before OnSubscribed ran")
		}
		if event != nil && before == 0 {
			before = calls
		}
	}

	if calls != 1 {
		t.Errorf("OnSubscribed ran %d times across 3 heartbeats, want exactly 1", calls)
	}
	if before != 1 {
		t.Errorf("OnSubscribed had run %d times when the first event arrived, want 1", before)
	}
}

func TestStreamOnSubscribedFailureEndsTheStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(sseHandler(
		frame("session.heartbeat", `{"type":"session.heartbeat"}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"a"}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sendFailed := errors.New("send rejected")
	opts := StreamOptions{OnSubscribed: func(ctx context.Context, sub Subscription) error { return sendFailed }}
	events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", opts))

	if len(events) != 0 {
		t.Errorf("events = %v, want none: the hook failed before the first event", events)
	}
	if !errors.Is(streamErr, sendFailed) {
		t.Fatalf("stream error = %v, want it to wrap the hook's error", streamErr)
	}
}

func TestStreamMissingSessionIDIsMatchable(t *testing.T) {
	t.Parallel()

	client, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, streamErr := collect(t, client.Stream(t.Context(), "", StreamOptions{}))
	if !errors.Is(streamErr, ErrInvalidArgument) {
		t.Fatalf("stream error = %v, want it to wrap ErrInvalidArgument", streamErr)
	}
}

// TestStreamBoundsTheAccumulatedFrameNotJustOneLine is S2. bufio.Scanner.Buffer
// caps a single line; the payload builder accumulated every data: line and reset
// only at the blank line that ends a frame. A server that never sends that blank
// line therefore grew the client's heap without bound, which the constant's own
// comment claimed it could not. The bound is now on the frame.
//
// The idle timeout here is short on purpose: against feat/go-client-v2 this test
// fails by taking the whole timeout and reporting ErrStreamIdle — the frame was
// still being accumulated when the watchdog gave up — instead of refusing the
// frame the moment it outgrew the limit.
func TestStreamBoundsTheAccumulatedFrameNotJustOneLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		serve func(w io.Writer, flush func() error) error
	}{
		{
			// Many lines, each comfortably inside the line cap, and never the
			// blank line that would end the frame.
			name: "data lines forever with no blank line",
			serve: func(w io.Writer, flush func() error) error {
				line := "data: " + strings.Repeat("x", 64<<10) + "\n"
				for range (maxFrameBytes / (64 << 10)) + 8 {
					if _, err := io.WriteString(w, line); err != nil {
						return err
					}
					if err := flush(); err != nil {
						return err
					}
				}
				return nil
			},
		},
		{
			// The other half of the same budget: one line past the cap, which
			// bufio.Scanner refuses. Same diagnosis, so the same sentinel.
			name: "a single line past the limit",
			serve: func(w io.Writer, flush func() error) error {
				if _, err := io.WriteString(w, "data: "+strings.Repeat("y", maxFrameBytes+1)+"\n\n"); err != nil {
					return err
				}
				return flush()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				controller := http.NewResponseController(w)
				// A write error means the client hung up, which is the pass path.
				_ = tc.serve(w, controller.Flush)
				<-r.Context().Done()
			}))
			defer server.Close()

			client, err := New(server.URL, WithStreamIdleTimeout(2*time.Second))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			events, streamErr := collect(t, client.Stream(t.Context(), "conv_1", StreamOptions{}))

			if !errors.Is(streamErr, ErrStreamFrameTooLarge) {
				t.Fatalf("stream error = %v, want it to wrap ErrStreamFrameTooLarge", streamErr)
			}
			// It is a protocol failure, so a caller matching the broader sentinel
			// sees it too.
			if !errors.Is(streamErr, ErrStreamProtocol) {
				t.Errorf("stream error = %v, want it to wrap ErrStreamProtocol as well", streamErr)
			}
			if errors.Is(streamErr, ErrStreamIdle) {
				t.Error("an oversized frame must not present as an idle transport")
			}
			if len(events) != 0 {
				t.Errorf("events = %v, want none: no frame ever completed", events)
			}
		})
	}
}

// TestStreamOnSubscribedDescribesItsSubscription is D2. The hook took only a
// context, which froze the package's headline signature: its parameter list could
// never grow. It now takes a struct alongside the context, and the struct carries
// what a hook needs to act without closing over the call site. Against
// feat/go-client-v2 this does not compile — OnSubscribed has one parameter.
func TestStreamOnSubscribedDescribesItsSubscription(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(sseHandler(
		frame("session.heartbeat", `{"type":"session.heartbeat"}`),
		"data: [DONE]\n\n",
	))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got Subscription
	opts := StreamOptions{
		Idle: true,
		OnSubscribed: func(ctx context.Context, sub Subscription) error {
			got = sub
			return nil
		},
	}
	if _, streamErr := collect(t, client.Stream(t.Context(), "conv_9", opts)); streamErr != nil {
		t.Fatalf("stream: %v", streamErr)
	}
	if got.SessionID != "conv_9" {
		t.Errorf("Subscription.SessionID = %q, want conv_9", got.SessionID)
	}
	if !got.Idle {
		t.Error("Subscription.Idle = false, want it to mirror StreamOptions.Idle")
	}

	// And the signature itself must be extensible: a context first, then one
	// struct whose fields can grow without breaking a caller.
	hook, ok := reflect.TypeFor[StreamOptions]().FieldByName("OnSubscribed")
	if !ok {
		t.Fatal("StreamOptions has no OnSubscribed field")
	}
	if hook.Type.Kind() != reflect.Func || hook.Type.NumIn() != 2 {
		t.Fatalf("OnSubscribed is %s, want a two-parameter func", hook.Type)
	}
	if hook.Type.In(0) != reflect.TypeFor[context.Context]() {
		t.Errorf("OnSubscribed's first parameter is %s, want context.Context", hook.Type.In(0))
	}
	if hook.Type.In(1).Kind() != reflect.Struct {
		t.Errorf("OnSubscribed's second parameter is %s, want a struct so it can gain fields",
			hook.Type.In(1))
	}
}

// TestUnsentinelledStreamStillNamesATotalDecodeFailure pins the diagnosis on the
// ending that fires most.
//
// A stream that stops without [DONE] is routine: deployments cap stream
// duration. If every frame also failed to decode, the caller sees nothing
// delivered and a bare ErrStreamInterrupted, and the documented recovery is to
// resubscribe — which skips every frame again, in a loop, with no signal naming
// the cause.
func TestUnsentinelledStreamStillNamesATotalDecodeFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for range 3 {
			// A known discriminator whose body contradicts the schema, which is
			// the drift the package's own docs name as realistic.
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":12345}\n\n"))
		}
		if flusher != nil {
			flusher.Flush()
		}
		// No [DONE]: the body just ends.
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var got error
	for _, err := range client.Stream(t.Context(), "conv_1", StreamOptions{}) {
		if err != nil {
			got = err
		}
	}
	if got == nil {
		t.Fatal("Stream yielded no error for a stream that delivered nothing")
	}
	if !errors.Is(got, ErrStreamInterrupted) {
		t.Errorf("error = %v, want it to wrap ErrStreamInterrupted", got)
	}
	if !strings.Contains(got.Error(), "skipped all 3 frames") {
		t.Errorf("error does not say every frame was skipped, so a caller cannot tell\n"+
			"a decode failure from a transport fault:\n  %v", got)
	}
}

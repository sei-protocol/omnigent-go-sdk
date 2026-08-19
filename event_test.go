package omnigent

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDecodeEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		typ     string
		payload string
		want    Event
		wantErr bool
	}{
		{
			name:    "text delta",
			typ:     "response.output_text.delta",
			payload: `{"type":"response.output_text.delta","delta":"hi","index":3,"message_id":"m1"}`,
			want: OutputTextDeltaEvent{
				Type:      "response.output_text.delta",
				Delta:     "hi",
				Index:     Ptr(3),
				MessageID: Ptr("m1"),
			},
		},
		{
			name:    "session status carries a typed enum",
			typ:     "session.status",
			payload: `{"type":"session.status","conversation_id":"conv_1","status":"waiting"}`,
			want: SessionStatusEvent{
				Type:           "session.status",
				ConversationID: "conv_1",
				Status:         "waiting",
			},
		},
		{
			name:    "an execution-sourced error is accepted, not just llm and tool",
			typ:     "response.error",
			payload: `{"type":"response.error","source":"execution","error":{"code":"c","message":"m"}}`,
			want: ErrorEvent{
				Type:   "response.error",
				Source: "execution",
				Error:  RetryErrorDetail{Code: "c", Message: "m"},
			},
		},
		{
			name:    "sequence_number stays absent rather than defaulting to zero",
			typ:     "session.heartbeat",
			payload: `{"type":"session.heartbeat"}`,
			want:    SessionHeartbeatEvent{Type: "session.heartbeat"},
		},
		{
			name:    "an unmapped type becomes an opaque event",
			typ:     "session.invented",
			payload: `{"type":"session.invented","x":1}`,
			want:    UnknownEvent{Type: "session.invented", Raw: []byte(`{"type":"session.invented","x":1}`)},
		},
		{
			name:    "a payload that contradicts its own schema fails",
			typ:     "response.output_text.delta",
			payload: `{"type":"response.output_text.delta","delta":{"not":"a string"}}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeByType(tc.typ, []byte(tc.payload))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeByType(%q) = %#v, want an error", tc.typ, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeByType(%q): %v", tc.typ, err)
			}
			// Compare through JSON so pointer fields compare by value.
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal got: %v", err)
			}
			wantJSON, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("decodeByType(%q) = %s, want %s", tc.typ, gotJSON, wantJSON)
			}
		})
	}
}

// TestEventCoversEveryUnionMember guards the generated dispatch: every member of
// the server's union must decode to a distinct typed event rather than silently
// falling through to UnknownEvent.
func TestEventCoversEveryUnionMember(t *testing.T) {
	t.Parallel()

	// One representative per family; a member missing from events.gen.go would
	// come back as UnknownEvent instead of its own type.
	types := []string{
		"response.created", "response.in_progress", "response.completed",
		"response.failed", "response.incomplete", "response.cancelled",
		"response.queued", "response.output_text.delta", "response.output_item.done",
		"response.error", "response.retry", "response.heartbeat",
		"response.reasoning.started", "response.reasoning_text.delta",
		"response.reasoning_summary_text.delta", "response.function_call_output.delta",
		"response.compaction.in_progress", "response.compaction.completed",
		"response.compaction.failed", "response.elicitation_request",
		"response.elicitation_resolved", "response.policy_denied",
		"response.client_task.cancel", "response.output_file.done",
		"browser.action_request",
		"session.status", "session.input.consumed", "session.heartbeat",
		"session.interrupted", "session.usage", "session.model",
		"session.reasoning_effort", "session.collaboration_mode",
		"session.agent_changed", "session.todos", "session.terminal_pending",
		"session.sandbox_status", "session.mcp_startup", "session.skills",
		"session.model_options", "session.created", "session.superseded",
		"session.presence", "session.resource.created", "session.resource.deleted",
		"session.child_session.updated", "session.changed_files.invalidated",
		"session.terminal.activity",
		"turn.started", "turn.completed", "turn.failed", "turn.cancelled",
	}
	if len(types) != 52 {
		t.Fatalf("the union has 52 members; this test lists %d", len(types))
	}

	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()

			// A payload carrying only the discriminator. Required fields are
			// absent, which is fine: encoding/json leaves them zero rather than
			// failing, so this exercises dispatch alone.
			event, err := decodeByType(typ, []byte(`{"type":`+quote(typ)+`}`))
			if err != nil {
				t.Fatalf("decodeByType(%q): %v", typ, err)
			}
			if unknown, ok := event.(UnknownEvent); ok {
				t.Fatalf("%q decoded to UnknownEvent(%q); the generated dispatch is missing it",
					typ, unknown.Type)
			}
		})
	}
}

// TestDocumentedTerminalSet pins the terminal edges the [Event] doc tells a
// consumer to switch on: an in-process turn's four response.* terminals, plus
// the session.status edge that carries a turn-end no response.* event describes.
// A rename or a drop in the generated dispatch would leave a consumer's terminal
// branch silently unreachable, and a new edge on the server has to reach the
// doc, not just this list.
func TestDocumentedTerminalSet(t *testing.T) {
	t.Parallel()

	terminals := []struct {
		typ     string
		payload string
		want    Event
	}{
		{"response.completed", `{"type":"response.completed"}`, ResponseCompletedEvent{}},
		{"response.failed", `{"type":"response.failed"}`, ResponseFailedEvent{}},
		{"response.incomplete", `{"type":"response.incomplete"}`, IncompleteEvent{}},
		{"response.cancelled", `{"type":"response.cancelled"}`, ResponseCancelledEvent{}},
		{"session.status", `{"type":"session.status","status":"failed"}`, SessionStatusEvent{}},
	}
	if len(terminals) != 5 {
		t.Fatalf("the doc names 5 terminal edges; this test lists %d", len(terminals))
	}

	for _, tc := range terminals {
		t.Run(tc.typ, func(t *testing.T) {
			t.Parallel()

			got, err := decodeByType(tc.typ, []byte(tc.payload))
			if err != nil {
				t.Fatalf("decodeByType(%q): %v", tc.typ, err)
			}
			if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", tc.want) {
				t.Errorf("decodeByType(%q) = %T, want %T", tc.typ, got, tc.want)
			}
		})
	}
}

// TestSessionStatusFailedCarriesTheFailure covers the fifth terminal edge's
// payload. Reporting the server's message is the whole reason to treat the
// status as terminal, so the error has to survive decoding.
//
// The wire shape is the server's: SessionStatusEvent in
// omnigent/server/schemas.py documents `error` as populated only when status is
// "failed", and names a setup-phase failure — spec resolution, spawn-env
// build — as the case that ends a turn before any response.failed exists.
func TestSessionStatusFailedCarriesTheFailure(t *testing.T) {
	t.Parallel()

	const frame = `{"type":"session.status","conversation_id":"conv_1","status":"failed",` +
		`"error":{"code":"spec_resolution_failed","message":"agent spec not found"}}`

	event, err := decodeByType("session.status", []byte(frame))
	if err != nil {
		t.Fatalf("decodeByType(session.status): %v", err)
	}
	status, ok := event.(SessionStatusEvent)
	if !ok {
		t.Fatalf("decodeByType(session.status) = %T, want SessionStatusEvent", event)
	}
	if status.Status != "failed" {
		t.Errorf("Status = %q, want %q", status.Status, "failed")
	}
	if status.Error == nil {
		t.Fatalf("Error = nil; a failed status must carry the failure to report")
	}
	if status.Error.Message != "agent spec not found" {
		t.Errorf("Error.Message = %q, want %q", status.Error.Message, "agent spec not found")
	}
	if status.Error.Code != "spec_resolution_failed" {
		t.Errorf("Error.Code = %q, want %q", status.Error.Code, "spec_resolution_failed")
	}
}

// TestLiveTurnOpensAtInProgress holds the [Event] doc's lifecycle claim to a
// checked-in transcript, so putting response.created back at the head of a live
// turn contradicts a test rather than only a comment.
//
// These frames are the server's shape rather than a capture. The harness emits
// response.created and response.in_progress as one pair
// (_initial_envelope_events in omnigent/runtime/harnesses/_scaffold.py), and the
// runner drops the created half on the session event queue that the server's
// relay reads and republishes to subscribers (the `!= "response.created"` guard
// in omnigent/runner/app.py). in_progress is what is left.
func TestLiveTurnOpensAtInProgress(t *testing.T) {
	t.Parallel()

	// One live turn end to end: the acknowledgement heartbeat, the turn
	// opening, its text, and one terminal.
	live := []struct{ typ, payload string }{
		{"session.heartbeat", `{"type":"session.heartbeat"}`},
		{"response.in_progress", `{"type":"response.in_progress"}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`},
		{"response.completed", `{"type":"response.completed"}`},
	}

	var created, inProgress int
	for _, frame := range live {
		event, err := decodeByType(frame.typ, []byte(frame.payload))
		if err != nil {
			t.Fatalf("decodeByType(%q): %v", frame.typ, err)
		}
		switch event.(type) {
		case ResponseCreatedEvent:
			created++
		case InProgressEvent:
			inProgress++
		}
	}
	// A consumer keyed on created to mean "a turn started" never fires.
	if created != 0 {
		t.Errorf("a live turn yielded %d ResponseCreatedEvent, want 0", created)
	}
	if inProgress != 1 {
		t.Errorf("a live turn yielded %d InProgressEvent, want 1", inProgress)
	}
}

// TestReplayPrologueShape covers the other half of that claim: the mid-turn
// replay is the one place response.created is observable, so it must stay
// decodable even though no live turn carries it.
//
// Both shapes come from snapshot_for in omnigent/runtime/inflight_text.py. A
// response-scoped in-process turn replays its captured response object as a
// synthesized response.created ahead of the accumulated text; a message-scoped
// (claude-native) turn replays one delta per in-flight message and no envelope.
func TestReplayPrologueShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		frames []struct{ typ, payload string }
		want   []Event
	}{
		{
			name: "a response-scoped replay is prefixed with response.created",
			frames: []struct{ typ, payload string }{
				{"response.created", `{"type":"response.created"}`},
				{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"so far"}`},
			},
			want: []Event{ResponseCreatedEvent{}, OutputTextDeltaEvent{}},
		},
		{
			name: "a message-scoped replay carries deltas only",
			frames: []struct{ typ, payload string }{
				{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"so far","message_id":"m1","index":4}`},
			},
			want: []Event{OutputTextDeltaEvent{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if len(tc.frames) != len(tc.want) {
				t.Fatalf("the fixture has %d frames and %d expectations", len(tc.frames), len(tc.want))
			}
			for i, frame := range tc.frames {
				got, err := decodeByType(frame.typ, []byte(frame.payload))
				if err != nil {
					t.Fatalf("decodeByType(%q): %v", frame.typ, err)
				}
				if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", tc.want[i]) {
					t.Errorf("frame %d decoded to %T, want %T", i, got, tc.want[i])
				}
			}
		})
	}
}

func quote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

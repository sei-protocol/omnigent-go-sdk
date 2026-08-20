package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorded is what one call actually put on the wire.
type recorded struct {
	method string
	path   string
	query  string
	body   map[string]any
}

// routeRecorder answers every request with reply and remembers the last one.
//
// A hand-written client's most likely defect is a wrong route or a wrong verb —
// nothing in the conformance gate can see either, because neither is a type. This
// is the test that can.
func routeRecorder(t *testing.T, reply string) (*Client, *recorded) {
	t.Helper()

	got := &recorded{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &got.body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, got
}

func TestEachSessionCallReachesItsRoute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		reply      string
		call       func(*Sessions) error
		wantMethod string
		wantPath   string
		wantBody   map[string]any
	}{
		{
			name:  "create",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.Create(context.Background(), SessionCreateRequest{AgentID: "ag_1"})
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions",
			wantBody:   map[string]any{"agent_id": "ag_1"},
		},
		{
			name:  "get",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.Get(context.Background(), "conv_1", GetSessionOptions{})
				return err
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/sessions/conv_1",
		},
		{
			name:  "delete",
			reply: `{"id":"conv_1","deleted":true}`,
			call: func(s *Sessions) error {
				_, err := s.Delete(context.Background(), "conv_1", DeleteSessionOptions{})
				return err
			},
			wantMethod: http.MethodDelete,
			wantPath:   "/v1/sessions/conv_1",
		},
		{
			name:  "fork",
			reply: `{"id":"conv_2"}`,
			call: func(s *Sessions) error {
				_, err := s.Fork(context.Background(), "conv_1", SessionForkRequest{})
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions/conv_1/fork",
		},
		{
			name:  "bind runner patches, it does not post",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.BindRunner(context.Background(), "conv_1", "runner_1")
				return err
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/v1/sessions/conv_1",
			wantBody:   map[string]any{"runner_id": "runner_1"},
		},
		{
			name:  "set archived",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.SetArchived(context.Background(), "conv_1", true)
				return err
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/v1/sessions/conv_1",
			wantBody:   map[string]any{"archived": true},
		},
		{
			name:  "set external id",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.SetExternalID(context.Background(), "conv_1", "ext_1")
				return err
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/v1/sessions/conv_1",
			wantBody:   map[string]any{"external_session_id": "ext_1"},
		},
		{
			name:  "set reasoning effort",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.SetReasoningEffort(context.Background(), "conv_1", "high")
				return err
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/v1/sessions/conv_1",
			wantBody:   map[string]any{"reasoning_effort": "high"},
		},
		{
			name:  "clear reasoning effort sends the alias",
			reply: `{"id":"conv_1"}`,
			call: func(s *Sessions) error {
				_, err := s.ClearReasoningEffort(context.Background(), "conv_1")
				return err
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/v1/sessions/conv_1",
			wantBody:   map[string]any{"reasoning_effort": clearOverrideAlias},
		},
		{
			name:  "send message posts to the events route",
			reply: `{"queued":true,"item_id":"item_1"}`,
			call: func(s *Sessions) error {
				_, err := s.SendMessage(context.Background(), "conv_1", "hello")
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions/conv_1/events",
			wantBody:   map[string]any{"type": InputTypeMessage},
		},
		{
			name:       "interrupt is an input, not its own route",
			reply:      `{"queued":false}`,
			call:       func(s *Sessions) error { return s.Interrupt(context.Background(), "conv_1") },
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions/conv_1/events",
			wantBody:   map[string]any{"type": InputTypeInterrupt},
		},
		{
			name:       "compact likewise",
			reply:      `{"queued":false}`,
			call:       func(s *Sessions) error { return s.Compact(context.Background(), "conv_1") },
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions/conv_1/events",
			wantBody:   map[string]any{"type": InputTypeCompact},
		},
		{
			name:  "resolve elicitation posts to the session the caller named",
			reply: `{}`,
			call: func(s *Sessions) error {
				return s.ResolveElicitation(context.Background(), "conv_child", "eli_1", ElicitationResult{})
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sessions/conv_child/elicitations/eli_1/resolve",
		},
		{
			name:  "list agents",
			reply: `{"data":[],"has_more":false}`,
			call: func(s *Sessions) error {
				for _, err := range s.ListAgents(context.Background(), ListAgentsOptions{}) {
					return err
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/agents",
		},
		{
			name:  "list items",
			reply: `{"data":[],"has_more":false}`,
			call: func(s *Sessions) error {
				for _, err := range s.ListItems(context.Background(), "conv_1", SessionItemsOptions{}) {
					return err
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/sessions/conv_1/items",
		},
		{
			name:  "session file list",
			reply: `{"data":[],"has_more":false}`,
			call: func(s *Sessions) error {
				for _, err := range s.client.Files().ForSession("conv_1").List(context.Background(), ListFilesOptions{}) {
					return err
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/sessions/conv_1/resources/files",
		},
		{
			name:  "session file get",
			reply: `{"id":"file_1"}`,
			call: func(s *Sessions) error {
				_, err := s.client.Files().ForSession("conv_1").Get(context.Background(), "file_1")
				return err
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/sessions/conv_1/resources/files/file_1",
		},
		{
			name:  "session file delete",
			reply: `{}`,
			call: func(s *Sessions) error {
				return s.client.Files().ForSession("conv_1").Delete(context.Background(), "file_1")
			},
			wantMethod: http.MethodDelete,
			wantPath:   "/v1/sessions/conv_1/resources/files/file_1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, got := routeRecorder(t, tc.reply)
			if err := tc.call(client.Sessions()); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got.method != tc.wantMethod {
				t.Errorf("method = %s, want %s", got.method, tc.wantMethod)
			}
			if got.path != tc.wantPath {
				t.Errorf("path = %s, want %s", got.path, tc.wantPath)
			}
			for key, want := range tc.wantBody {
				if have, ok := got.body[key]; !ok || have != want {
					t.Errorf("body[%q] = %v, want %v", key, have, want)
				}
			}
		})
	}
}

func TestResolveAgentFollowsTheCursorAndNamesWhatItSaw(t *testing.T) {
	t.Parallel()

	t.Run("resolves past the first page", func(t *testing.T) {
		t.Parallel()
		page := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page++
			w.Header().Set("Content-Type", "application/json")
			if page == 1 {
				_, _ = w.Write([]byte(`{"data":[{"id":"ag_a","name":"alpha"}],"last_id":"ag_a","has_more":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"ag_b","name":"beta"}],"last_id":"ag_b","has_more":false}`))
		}))
		defer server.Close()

		client, err := New(server.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		agent, err := client.Sessions().ResolveAgent(context.Background(), "beta")
		if err != nil {
			t.Fatalf("ResolveAgent: %v", err)
		}
		if agent.ID != "ag_b" {
			t.Errorf("id = %q, want ag_b; the walk stopped at page one", agent.ID)
		}
	})

	t.Run("a miss wraps ErrNotFound and names the alternatives", func(t *testing.T) {
		t.Parallel()
		client, _ := routeRecorder(t, `{"data":[{"id":"ag_a","name":"alpha"}],"has_more":false}`)
		_, err := client.Sessions().ResolveAgent(context.Background(), "absent")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), "alpha") {
			t.Errorf("error does not name what it did see, so a caller cannot self-correct: %v", err)
		}
	})

	t.Run("an empty name is rejected before any request", func(t *testing.T) {
		t.Parallel()
		client, got := routeRecorder(t, `{}`)
		if _, err := client.Sessions().ResolveAgent(context.Background(), ""); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("error = %v, want ErrInvalidArgument", err)
		}
		if got.method != "" {
			t.Errorf("issued a %s request for a rejected argument", got.method)
		}
	})
}

func TestResolveOnlineRunnerPrefersAnAdvertisedHarness(t *testing.T) {
	t.Parallel()

	const listing = `{"data":[
		{"runner_id":"offline_1","online":false,"harnesses":["claude-sdk"]},
		{"runner_id":"silent_1","online":true},
		{"runner_id":"match_1","online":true,"harnesses":["openai-agents","claude-sdk"]}
	]}`

	cases := []struct {
		name string
		opts ResolveOnlineRunnerOptions
		want string
	}{
		{"an advertised harness wins over a silent runner", ResolveOnlineRunnerOptions{Harness: "openai-agents"}, "match_1"},
		{"no harness asked for takes the first usable one", ResolveOnlineRunnerOptions{}, "match_1"},
		{
			name: "an alias matches through Canonicalize",
			opts: ResolveOnlineRunnerOptions{
				Harness:      "claude",
				Canonicalize: func(s string) string { return strings.TrimSuffix(s, "-sdk") },
			},
			want: "match_1",
		},
		{"an unadvertised harness falls back to the silent runner", ResolveOnlineRunnerOptions{Harness: "nothing-advertises-this"}, "silent_1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := routeRecorder(t, listing)
			got, err := client.Sessions().ResolveOnlineRunner(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("ResolveOnlineRunner: %v", err)
			}
			if got != tc.want {
				t.Errorf("runner = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("no match is an empty id and no error", func(t *testing.T) {
		t.Parallel()
		client, _ := routeRecorder(t, `{"data":[{"runner_id":"a","online":false,"harnesses":["x"]}]}`)
		got, err := client.Sessions().ResolveOnlineRunner(context.Background(), ResolveOnlineRunnerOptions{Harness: "x"})
		if err != nil {
			t.Fatalf("ResolveOnlineRunner: %v", err)
		}
		if got != "" {
			t.Errorf("runner = %q, want empty: no online runner matches", got)
		}
	})
}

func TestOfflineRunnerIsNeverChosen(t *testing.T) {
	t.Parallel()

	client, _ := routeRecorder(t, `{"data":[{"runner_id":"offline_only","online":false,"harnesses":["h"]}]}`)
	got, err := client.Sessions().ResolveOnlineRunner(context.Background(), ResolveOnlineRunnerOptions{})
	if err != nil {
		t.Fatalf("ResolveOnlineRunner: %v", err)
	}
	if got != "" {
		t.Errorf("chose %q, which the server reported offline", got)
	}
}

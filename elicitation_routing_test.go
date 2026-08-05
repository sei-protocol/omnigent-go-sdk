package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestResolveElicitationRequestPostsToTheOwningSession pins the routing. A
// sub-agent's prompt is mirrored into its ancestors' streams, so the session an
// event was read from is not always the session that owns it, and the server
// resolves the parked harness Future only for the owner. Posting a mirrored
// verdict to the stream instead is accepted and resolves nothing, which is the
// failure this routing exists to avoid.
func TestResolveElicitationRequestPostsToTheOwningSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   *string
		wantPath string
	}{
		{
			name:     "a mirrored prompt goes to its target",
			target:   Ptr("conv_child"),
			wantPath: "/v1/sessions/conv_child/events",
		},
		{
			name:     "an unmirrored prompt goes to the stream it came from",
			wantPath: "/v1/sessions/conv_stream/events",
		},
		{
			name: "an empty target is not a session id",
			// The field is optional on the wire and its own documentation says
			// absent means "resolve against the current session"; a present but
			// empty value cannot mean anything else, and posting it would address
			// the sessions collection rather than a session.
			target:   Ptr(""),
			wantPath: "/v1/sessions/conv_stream/events",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			type captured struct {
				path string
				body []byte
			}
			seen := make(chan captured, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seen <- captured{path: r.URL.EscapedPath(), body: body}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"queued":false}`))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			request := ElicitationRequestEvent{
				ElicitationID: "elicit_1",
				Params: ElicitationRequestParams{
					Message:         "Approve running 'rm -rf /tmp/cache'?",
					TargetSessionID: tc.target,
				},
			}
			if _, err := client.ResolveElicitationRequest(context.Background(), "conv_stream",
				request, ElicitationResult{Action: ElicitationAccept}); err != nil {
				t.Fatalf("ResolveElicitationRequest: %v", err)
			}

			got := <-seen
			if got.path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.path, tc.wantPath)
			}
			// The verdict itself is unchanged by the routing: same approval input,
			// same correlation key, only a different session in the path.
			var body struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(got.body, &body); err != nil {
				t.Fatalf("Unmarshal request body: %v", err)
			}
			want := map[string]any{"elicitation_id": "elicit_1", "action": "accept"}
			if body.Type != InputTypeApproval || !reflect.DeepEqual(body.Data, want) {
				t.Errorf("body = {type:%q data:%#v}, want {type:%q data:%#v}",
					body.Type, body.Data, InputTypeApproval, want)
			}
		})
	}
}

// TestResolveElicitationRequestKeepsTheArgumentChecks checks the routing is a
// wrapper and not a bypass: an event carrying no elicitation id still cannot
// produce a request, whichever session it would have been addressed to.
func TestResolveElicitationRequestKeepsTheArgumentChecks(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request reached the server; the argument should have been refused")
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	request := ElicitationRequestEvent{
		Params: ElicitationRequestParams{TargetSessionID: Ptr("conv_child")},
	}
	_, err = client.ResolveElicitationRequest(context.Background(), "conv_stream",
		request, ElicitationResult{Action: ElicitationAccept})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want one matching ErrInvalidArgument", err)
	}
	if got := err.Error(); !strings.Contains(got, "elicitationID is required") {
		t.Errorf("error = %q, want it to mention the missing argument", got)
	}
}

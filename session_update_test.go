package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestUpdateSessionSendsOnlyTheFieldsSet pins the property that makes a PATCH
// safe to build from a partly-filled struct: an unset field is absent from the
// body, not present and empty.
//
// The distinction is the whole point of the call. A body carrying every field
// would send "" for a title the caller never named and false for an archived
// flag they never set, so a caller relabelling a session would silently rename
// it too. Absence is what tells the server to leave a field alone.
func TestUpdateSessionSendsOnlyTheFieldsSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      UpdateSessionRequest
		wantKeys []string
	}{
		{
			name: "a zero request sends an empty body, changing nothing",
		},
		{
			name:     "archiving sends the flag alone",
			req:      UpdateSessionRequest{Archived: Ptr(true)},
			wantKeys: []string{"archived"},
		},
		{
			name:     "a false flag is still sent, since unset and false differ",
			req:      UpdateSessionRequest{Archived: Ptr(false)},
			wantKeys: []string{"archived"},
		},
		{
			name:     "labels travel without dragging the title along",
			req:      UpdateSessionRequest{Labels: map[string]string{"pr": "3870"}},
			wantKeys: []string{"labels"},
		},
		{
			name: "several fields serialise together",
			req: UpdateSessionRequest{
				Archived: Ptr(true),
				Title:    Ptr("reviewed"),
				Labels:   map[string]string{"pr": "3870"},
			},
			wantKeys: []string{"archived", "labels", "title"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			requests := make(chan *http.Request, 1)
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies <- body
				requests <- r.Clone(r.Context())
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"conv_1","object":"session"}`))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := client.UpdateSession(context.Background(), "conv_1", tc.req); err != nil {
				t.Fatalf("UpdateSession: %v", err)
			}

			got := <-requests
			if got.Method != http.MethodPatch {
				t.Errorf("method = %s, want PATCH", got.Method)
			}
			if want := "/v1/sessions/conv_1"; got.URL.EscapedPath() != want {
				t.Errorf("path = %q, want %q", got.URL.EscapedPath(), want)
			}

			var sent map[string]any
			if err := json.Unmarshal(<-bodies, &sent); err != nil {
				t.Fatalf("the body did not decode as an object: %v", err)
			}
			keys := make([]string, 0, len(sent))
			for key := range sent {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tc.wantKeys) {
				t.Errorf("body carried %v, want exactly %v", keys, tc.wantKeys)
			}
		})
	}
}

// TestUpdateSessionRequiresASessionID checks the call that cannot produce a
// meaningful request is refused before one is sent. An empty id would otherwise
// PATCH the sessions collection.
func TestUpdateSessionRequiresASessionID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request reached the server; the argument should have been refused")
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.UpdateSession(context.Background(), "", UpdateSessionRequest{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want one matching ErrInvalidArgument", err)
	}
	if got := err.Error(); !strings.Contains(got, "sessionID is required") {
		t.Errorf("error = %q, want it to mention the missing argument", got)
	}
}

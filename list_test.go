package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestListQueryEncoding pins what each option set puts on the wire. The
// zero-value cases carry the weight: the server validates order, limit, sort_by
// and kind against patterns and bounds, so a Go zero value forwarded as "" or 0
// would 422 a caller who expressed no preference.
func TestListQueryEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		call      func(context.Context, *Client) error
		wantPath  string
		wantQuery string
	}{
		{
			name: "zero agent options send no parameters at all",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListAgents(ctx, ListAgentsOptions{})
				return err
			},
			wantPath: "/v1/agents",
		},
		{
			name: "zero session options send no parameters at all",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListSessions(ctx, ListSessionsOptions{})
				return err
			},
			wantPath: "/v1/sessions",
		},
		{
			name: "agent options serialise in full",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListAgents(ctx, ListAgentsOptions{
					Limit: 50, After: "ag_9", Before: "ag_1", Order: SortAscending,
				})
				return err
			},
			wantPath:  "/v1/agents",
			wantQuery: "after=ag_9&before=ag_1&limit=50&order=asc",
		},
		{
			name: "session options serialise in full",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListSessions(ctx, ListSessionsOptions{
					Limit: 100, After: "conv_9", Before: "conv_1",
					AgentID: "ag_1", AgentName: "reviewer",
					Order: SortAscending, SortBy: SessionSortByUpdatedAt,
					SearchQuery: "flaky test", IncludeArchived: true,
					Kind: SessionKindAny, Project: "proj_1", Pinned: true,
				})
				return err
			},
			wantPath: "/v1/sessions",
			wantQuery: "after=conv_9&agent_id=ag_1&agent_name=reviewer&before=conv_1" +
				"&include_archived=true&kind=any&limit=100&order=asc&pinned=true" +
				"&project=proj_1&search_query=flaky+test&sort_by=updated_at",
		},
		{
			name: "false booleans are omitted rather than sent as false",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListSessions(ctx, ListSessionsOptions{
					AgentID: "ag_1", IncludeArchived: false, Pinned: false,
				})
				return err
			},
			wantPath:  "/v1/sessions",
			wantQuery: "agent_id=ag_1",
		},
		{
			name: "a search query is escaped, not concatenated",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListSessions(ctx, ListSessionsOptions{SearchQuery: "a&b=c d"})
				return err
			},
			wantPath:  "/v1/sessions",
			wantQuery: "search_query=a%26b%3Dc+d",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Clone(r.Context())
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[],"has_more":false}`))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tc.call(context.Background(), client); err != nil {
				t.Fatalf("call: %v", err)
			}
			got := <-requests
			if got.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", got.Method)
			}
			if got.URL.EscapedPath() != tc.wantPath {
				t.Errorf("path = %q, want %q", got.URL.EscapedPath(), tc.wantPath)
			}
			if got.URL.RawQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", got.URL.RawQuery, tc.wantQuery)
			}
		})
	}
}

// TestListDecodesPages checks the shared envelope decodes into Page[T] for both
// element types, including the typed item fields a caller filters on.
func TestListDecodesPages(t *testing.T) {
	t.Parallel()

	t.Run("agents", func(t *testing.T) {
		t.Parallel()

		body := `{"object":"list","data":[
			{"id":"ag_1","name":"reviewer","builtin":false,"harness":"claude"},
			{"id":"ag_2","name":"polly","builtin":true}
		],"first_id":"ag_1","last_id":"ag_2","has_more":true}`
		client := clientFor(t, body)

		page, err := client.ListAgents(context.Background(), ListAgentsOptions{})
		if err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		if len(page.Data) != 2 {
			t.Fatalf("len(Data) = %d, want 2", len(page.Data))
		}
		if page.Data[0].Name != "reviewer" {
			t.Errorf("Data[0].Name = %q, want reviewer", page.Data[0].Name)
		}
		// The flag that distinguishes an operator-installed agent from one that
		// ships with the server. The web UI partitions on a hardcoded name list
		// instead; this is the field that actually carries the answer.
		if page.Data[0].Builtin == nil || *page.Data[0].Builtin {
			t.Errorf("Data[0].Builtin = %v, want false", page.Data[0].Builtin)
		}
		if page.Data[1].Builtin == nil || !*page.Data[1].Builtin {
			t.Errorf("Data[1].Builtin = %v, want true", page.Data[1].Builtin)
		}
		if page.FirstID != "ag_1" || page.LastID != "ag_2" || !page.HasMore {
			t.Errorf("envelope = (%q, %q, %v), want (ag_1, ag_2, true)",
				page.FirstID, page.LastID, page.HasMore)
		}
	})

	t.Run("sessions carry the labels a reconcile matches on", func(t *testing.T) {
		t.Parallel()

		body := `{"object":"list","data":[
			{"id":"conv_1","agent_id":"ag_1","agent_name":"reviewer","status":"idle",
			 "labels":{"run-key":"pr-42"},"archived":false}
		],"last_id":"conv_1","has_more":false}`
		client := clientFor(t, body)

		page, err := client.ListSessions(context.Background(), ListSessionsOptions{AgentID: "ag_1"})
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(page.Data) != 1 {
			t.Fatalf("len(Data) = %d, want 1", len(page.Data))
		}
		item := page.Data[0]
		if item.ID != "conv_1" {
			t.Errorf("ID = %q, want conv_1", item.ID)
		}
		if item.AgentID != "ag_1" {
			t.Errorf("AgentID = %q, want ag_1", item.AgentID)
		}
		if got := item.Labels["run-key"]; got != "pr-42" {
			t.Errorf("Labels[run-key] = %q, want pr-42", got)
		}
		if page.HasMore {
			t.Error("HasMore = true, want false")
		}
	})

	t.Run("an empty page decodes without error", func(t *testing.T) {
		t.Parallel()

		client := clientFor(t, `{"object":"list","data":[],"has_more":false}`)
		page, err := client.ListSessions(context.Background(), ListSessionsOptions{})
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(page.Data) != 0 || page.HasMore || page.FirstID != "" || page.LastID != "" {
			t.Errorf("page = %+v, want an empty page", page)
		}
	})
}

// TestPaginationWalkUsesTheServersCursors runs the loop the Page doc prescribes
// against a two-page listing, so the documented recipe is executed rather than
// only described.
func TestPaginationWalkUsesTheServersCursors(t *testing.T) {
	t.Parallel()

	// Guarded rather than bare: the client drives these requests one at a time,
	// so the appends happen to be ordered today, but that is a property of the
	// caller and not of this slice.
	var mu sync.Mutex
	var sawAfter []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		mu.Lock()
		sawAfter = append(sawAfter, after)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch after {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"conv_1"},{"id":"conv_2"}],` +
				`"first_id":"conv_1","last_id":"conv_2","has_more":true}`))
		case "conv_2":
			_, _ = w.Write([]byte(`{"data":[{"id":"conv_3"}],` +
				`"first_id":"conv_3","last_id":"conv_3","has_more":false}`))
		default:
			t.Errorf("unexpected cursor %q", after)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var ids []string
	opts := ListSessionsOptions{}
	for {
		page, err := client.ListSessions(context.Background(), opts)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		for _, s := range page.Data {
			ids = append(ids, s.ID)
		}
		if !page.HasMore {
			break
		}
		opts.After = page.LastID
	}

	if want := []string{"conv_1", "conv_2", "conv_3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	mu.Lock()
	cursors := append([]string(nil), sawAfter...)
	mu.Unlock()
	if want := []string{"", "conv_2"}; !reflect.DeepEqual(cursors, want) {
		t.Errorf("cursors sent = %v, want %v", cursors, want)
	}
}

// TestApprovalVerdictShape pins the payload the server reads. It parses
// data.elicitation_id and data.action out of the input body, so both belong in
// data and not beside it, and content is absent rather than null when unset.
func TestApprovalVerdictShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result ElicitationResult
		want   map[string]any
	}{
		{
			name:   "accept without form content",
			result: ElicitationResult{Action: ElicitationAccept},
			want:   map[string]any{"elicitation_id": "elicit_1", "action": "accept"},
		},
		{
			name:   "decline",
			result: ElicitationResult{Action: ElicitationDecline},
			want:   map[string]any{"elicitation_id": "elicit_1", "action": "decline"},
		},
		{
			name: "accept with form content",
			result: ElicitationResult{
				Action:  ElicitationAccept,
				Content: map[string]any{"reason": "looks fine", "count": float64(2)},
			},
			want: map[string]any{
				"elicitation_id": "elicit_1",
				"action":         "accept",
				"content":        map[string]any{"reason": "looks fine", "count": float64(2)},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input := ApprovalVerdict("elicit_1", tc.result)
			if input.Type != InputTypeApproval {
				t.Errorf("Type = %q, want %q", input.Type, InputTypeApproval)
			}

			// Through JSON, because what matters is the wire shape rather than
			// the Go map: an omitted content must not become "content": null.
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var decoded struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(decoded.Data, tc.want) {
				t.Errorf("data = %#v, want %#v", decoded.Data, tc.want)
			}
		})
	}
}

// TestResolveElicitationRejectsIncompleteVerdicts checks the arguments that
// cannot produce a meaningful request are refused before one is sent. An empty
// action would reach the server as a verdict of no kind.
func TestResolveElicitationRejectsIncompleteVerdicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sessionID   string
		elicitation string
		result      ElicitationResult
		wantIn      string
	}{
		{
			name:        "no session id",
			elicitation: "elicit_1",
			result:      ElicitationResult{Action: ElicitationAccept},
			wantIn:      "sessionID is required",
		},
		{
			name:      "no elicitation id",
			sessionID: "conv_1",
			result:    ElicitationResult{Action: ElicitationAccept},
			wantIn:    "elicitationID is required",
		},
		{
			name:        "no action",
			sessionID:   "conv_1",
			elicitation: "elicit_1",
			wantIn:      "Action is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("a request reached the server; the argument should have been refused")
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = client.ResolveElicitation(
				context.Background(), tc.sessionID, tc.elicitation, tc.result)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want one matching ErrInvalidArgument", err)
			}
			if got := err.Error(); !strings.Contains(got, tc.wantIn) {
				t.Errorf("error = %q, want it to mention %q", got, tc.wantIn)
			}
		})
	}
}

// TestResolveElicitationPostsToTheEventsRoute checks the verdict travels as an
// input on the one write route rather than to the server's separate resolve
// route, which is registered include_in_schema=False.
func TestResolveElicitationPostsToTheEventsRoute(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"queued":false,"elicitation_id":"elicit_1"}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	accepted, err := client.ResolveElicitation(context.Background(), "conv_1", "elicit_1",
		ElicitationResult{Action: ElicitationDecline})
	if err != nil {
		t.Fatalf("ResolveElicitation: %v", err)
	}
	if accepted.Queued {
		t.Error("Queued = true; resolving an elicitation queues no conversation item")
	}

	got := <-seen
	if want := "/v1/sessions/conv_1/events"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	var body struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("Unmarshal request body: %v", err)
	}
	want := map[string]any{"elicitation_id": "elicit_1", "action": "decline"}
	if body.Type != InputTypeApproval || !reflect.DeepEqual(body.Data, want) {
		t.Errorf("body = {type:%q data:%#v}, want {type:%q data:%#v}",
			body.Type, body.Data, InputTypeApproval, want)
	}
}

// TestListSurfacesAPIErrors checks a non-2xx on a listing arrives as *APIError
// rather than a decode failure over an error envelope.
func TestListSurfacesAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"limit must be between 1 and 1000"}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.ListAgents(context.Background(), ListAgentsOptions{Limit: 5000})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "list agents") {
		t.Errorf("error = %q, want it to name the operation", err.Error())
	}
}

// clientFor starts a server answering every request with body, and returns a
// client pointed at it.
func clientFor(t *testing.T, body string) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// TestDocumentedWalkTerminatesOnAnEmptyPage runs the loop from [Page]'s doc
// comment against a server that answers has_more:true with no items — the shape
// this server does not produce, but a proxy or a later version could. An empty
// page carries an empty LastID, so a walk that trusts has_more alone rewinds to
// the first page and never finishes. The doc prescribes two break conditions
// precisely so termination is the caller's property; this executes that.
func TestDocumentedWalkTerminatesOnAnEmptyPage(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":true}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Verbatim from the Page doc's loop.
	opts := ListSessionsOptions{}
	for range 50 {
		page, err := client.ListSessions(context.Background(), opts)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if !page.HasMore || len(page.Data) == 0 {
			goto done
		}
		opts.After = page.LastID
	}
	t.Fatal("the documented walk did not terminate on an empty page")
done:
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("sent %d requests, want 1: the empty page is terminal", requests)
	}
}

// TestAdoptRecipeFindsASessionBeyondTheFirstPage runs the README's adopt recipe
// against a server holding more of the agent's sessions than one page, with the
// labelled one last. There is no server-side label filter and a page defaults to
// 20, so a single-page scan reports "no existing session" and its caller creates
// the duplicate the label exists to prevent.
func TestAdoptRecipeFindsASessionBeyondTheFirstPage(t *testing.T) {
	t.Parallel()

	const target, runKey = "conv_20", "pr-42"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			items := make([]string, 0, 20)
			for i := range 20 {
				items = append(items,
					fmt.Sprintf(`{"id":"conv_%02d","agent_id":"ag_1","labels":{}}`, i))
			}
			_, _ = fmt.Fprintf(w, `{"data":[%s],"first_id":"conv_00","last_id":"conv_19","has_more":true}`,
				strings.Join(items, ","))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"agent_id":"ag_1","labels":{"run-key":%q}}],`+
			`"first_id":%q,"last_id":%q,"has_more":false}`, target, runKey, target, target)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Verbatim from the README's adopt recipe.
	found := ""
	opts := ListSessionsOptions{AgentID: "ag_1"}
walk:
	for {
		page, err := client.ListSessions(context.Background(), opts)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		for _, s := range page.Data {
			if s.Labels["run-key"] == runKey {
				found = s.ID
				break walk
			}
		}
		if !page.HasMore || len(page.Data) == 0 {
			break
		}
		opts.After = page.LastID
	}

	if found != target {
		t.Errorf("adopted %q, want %q: the recipe must page or it duplicates the session",
			found, target)
	}
}

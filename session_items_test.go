package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestListSessionItemsQueryEncoding pins what the items listing puts on the
// wire. The zero-value case carries the weight for the same reason it does on
// the other two listings: the route validates order against ^(asc|desc)$ and
// limit against 1..1000, so a Go zero value forwarded as "" or 0 would 422 a
// caller who expressed no preference.
func TestListSessionItemsQueryEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      SessionItemsOptions
		wantQuery string
	}{
		{
			name: "zero options send no parameters at all",
		},
		{
			name: "options serialise in full",
			opts: SessionItemsOptions{
				Limit: 500, After: "msg_9", Before: "msg_1", Order: SortDescending,
			},
			wantQuery: "after=msg_9&before=msg_1&limit=500&order=desc",
		},
		{
			name:      "a window is both cursors together, not one replacing the other",
			opts:      SessionItemsOptions{After: "msg_1", Before: "msg_9"},
			wantQuery: "after=msg_1&before=msg_9",
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
			if _, err := client.ListSessionItems(context.Background(), "conv_1", tc.opts); err != nil {
				t.Fatalf("ListSessionItems: %v", err)
			}
			got := <-requests
			if got.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", got.Method)
			}
			if want := "/v1/sessions/conv_1/items"; got.URL.EscapedPath() != want {
				t.Errorf("path = %q, want %q", got.URL.EscapedPath(), want)
			}
			if got.URL.RawQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", got.URL.RawQuery, tc.wantQuery)
			}
		})
	}
}

// TestListSessionItemsRequiresASessionID checks the call that cannot produce a
// meaningful request is refused before one is sent. An empty id would otherwise
// resolve to the sessions collection with a trailing /items.
func TestListSessionItemsRequiresASessionID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request reached the server; the argument should have been refused")
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.ListSessionItems(context.Background(), "", SessionItemsOptions{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want one matching ErrInvalidArgument", err)
	}
	if got := err.Error(); !strings.Contains(got, "sessionID is required") {
		t.Errorf("error = %q, want it to mention the missing argument", got)
	}
}

// TestListSessionItemsDecodesTheFlatItemShape pins the shape this route sends,
// which is not the shape the snapshot sends. The server renders each item with
// its flatten-for-API form: the payload's own fields sit on the top level beside
// the common ones, there is no nested data object, and created_at is absent. A
// caller that reused the snapshot's typed reader over this page would read zero
// values, so the flatness is the contract worth pinning.
func TestListSessionItemsDecodesTheFlatItemShape(t *testing.T) {
	t.Parallel()

	body := `{"object":"list","data":[
		{"id":"msg_1","response_id":"resp_1","type":"message","status":"completed",
		 "role":"assistant","content":[{"type":"output_text","text":"hi"}]}
	],"first_id":"msg_1","last_id":"msg_1","has_more":true}`
	client := clientFor(t, body)

	page, err := client.ListSessionItems(context.Background(), "conv_1", SessionItemsOptions{})
	if err != nil {
		t.Fatalf("ListSessionItems: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(page.Data))
	}
	item := page.Data[0]
	if item["id"] != "msg_1" {
		t.Errorf(`item["id"] = %v, want msg_1`, item["id"])
	}
	if item["role"] != "assistant" {
		t.Errorf(`item["role"] = %v, want assistant on the top level, not under "data"`, item["role"])
	}
	if _, nested := item["data"]; nested {
		t.Error(`item has a "data" key; this route flattens the payload instead`)
	}
	if _, ok := item["created_at"]; ok {
		t.Error(`item has a "created_at" key; this route omits it`)
	}
	if page.FirstID != "msg_1" || page.LastID != "msg_1" || !page.HasMore {
		t.Errorf("envelope = (%q, %q, %v), want (msg_1, msg_1, true)",
			page.FirstID, page.LastID, page.HasMore)
	}
}

// TestTheSnapshotItemTypeCannotReadAListedItem is why [SessionItem] is untyped.
// Decoding the listing's flat item into the snapshot's [ConversationItem]
// succeeds and yields nothing usable — the payload it wants under data is spread
// across the top level — so typing the page that way would have handed callers a
// silent misread rather than an error.
func TestTheSnapshotItemTypeCannotReadAListedItem(t *testing.T) {
	t.Parallel()

	listed := []byte(`{"id":"msg_1","response_id":"resp_1","type":"message","status":"completed",
		"role":"assistant","content":[{"type":"output_text","text":"hi"}]}`)

	var item ConversationItem
	if err := json.Unmarshal(listed, &item); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, err := item.Data.AsMessageData(); err == nil {
		t.Error("Data.AsMessageData succeeded; the flat item carries no data object to decode")
	}
	if item.CreatedAt != 0 {
		t.Errorf("CreatedAt = %d, want 0: the listing does not send it", item.CreatedAt)
	}
}

// TestPagedItemsReachWhatTheSnapshotTruncates is the reason this operation
// exists. The snapshot is built from the newest 100 items, so on a session that
// has committed more than that the oldest are simply not in it — and nothing on
// the response says so, which is what makes reading a reply off the snapshot
// look like it works right up until it doesn't. The paged read finds the same
// item the snapshot cannot see.
func TestPagedItemsReachWhatTheSnapshotTruncates(t *testing.T) {
	t.Parallel()

	// 150 committed items, so the snapshot's window starts at msg_050.
	ids := make([]string, 0, 150)
	for i := range 150 {
		ids = append(ids, fmt.Sprintf("msg_%03d", i))
	}
	// The newest item below the snapshot's floor: unreachable from the snapshot,
	// and far enough in that finding it takes more than the first page.
	const target = "msg_049"

	var mu sync.Mutex
	pages := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		newest := ids[len(ids)-100:]
		encoded := make([]string, 0, len(newest))
		for _, id := range newest {
			encoded = append(encoded, fmt.Sprintf(
				`{"id":%q,"response_id":"resp_1","type":"message","status":"completed",`+
					`"created_at":1,"data":{"role":"assistant","content":[]}}`, id))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"conv_1","agent_id":"ag_1","status":"idle",`+
			`"created_at":1,"items":[%s]}`, strings.Join(encoded, ","))
	})
	mux.HandleFunc("GET /v1/sessions/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pages++
		mu.Unlock()
		query := r.URL.Query()
		limit := 100
		if raw := query.Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				t.Errorf("limit = %q, want an integer", raw)
			}
			limit = parsed
		}
		start := 0
		if after := query.Get("after"); after != "" {
			index := slices.Index(ids, after)
			if index < 0 {
				t.Errorf("cursor %q is not an id this server handed out", after)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			start = index + 1
		}
		end := min(start+limit, len(ids))
		page := ids[start:end]
		encoded := make([]string, 0, len(page))
		for _, id := range page {
			encoded = append(encoded, fmt.Sprintf(
				`{"id":%q,"response_id":"resp_1","type":"message","status":"completed",`+
					`"role":"assistant","content":[]}`, id))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"object":"list","data":[%s],"first_id":%q,"last_id":%q,"has_more":%t}`,
			strings.Join(encoded, ","), page[0], page[len(page)-1], end < len(ids))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The path GetSession's doc prescribes for recovery, which the cap truncates.
	session, err := client.GetSession(context.Background(), "conv_1",
		GetSessionOptions{IncludeItems: Ptr(true)})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(session.Items) != 100 {
		t.Fatalf("snapshot carried %d items, want the server's 100", len(session.Items))
	}
	if slices.ContainsFunc(session.Items, func(item ConversationItem) bool {
		return item.ID == target
	}) {
		t.Fatal("the snapshot carried the oldest item; this test no longer covers the cap")
	}

	// The paged read, walked as Page documents. The iteration bound is generous
	// against the eight pages this takes and is only there so a cursor that stops
	// advancing fails the test rather than spinning inside it.
	var found string
	opts := SessionItemsOptions{Limit: 20}
walk:
	for range 20 {
		page, err := client.ListSessionItems(context.Background(), "conv_1", opts)
		if err != nil {
			t.Fatalf("ListSessionItems: %v", err)
		}
		for _, item := range page.Data {
			if item["id"] == target {
				found, _ = item["id"].(string)
				break walk
			}
		}
		if !page.HasMore || len(page.Data) == 0 {
			break
		}
		opts.After = page.LastID
	}

	if found != target {
		t.Errorf("paged read found %q, want %q: the items cursor is what reaches past the cap",
			found, target)
	}
	mu.Lock()
	defer mu.Unlock()
	if pages < 2 {
		t.Errorf("read %d pages, want more than one: the cursor has to advance to get there", pages)
	}
}

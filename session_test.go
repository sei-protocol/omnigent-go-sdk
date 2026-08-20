package omnigent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// childPage is one page of a child listing, keyed by the session it belongs to.
type childPage struct {
	ids     []string
	hasMore bool
}

// treeServer serves a child-session topology, page by page, and counts requests.
//
// It pages deliberately: a parent with more children than one page is where
// upstream's own walk stops short, so a test that never pages cannot see the bug
// this walk exists to avoid.
func treeServer(t *testing.T, topology map[string][]childPage) (*Client, *atomic.Int64) {
	t.Helper()

	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[3] != "child_sessions" {
			http.NotFound(w, r)
			return
		}
		pages := topology[parts[2]]
		index := 0
		if after := r.URL.Query().Get("after"); after != "" {
			for i, page := range pages {
				if len(page.ids) > 0 && page.ids[len(page.ids)-1] == after {
					index = i + 1
					break
				}
			}
		}
		page := Page[ChildSessionSummary]{Data: []ChildSessionSummary{}}
		if index < len(pages) {
			for _, id := range pages[index].ids {
				page.Data = append(page.Data, ChildSessionSummary{ID: id})
			}
			page.HasMore = pages[index].hasMore
			if len(page.Data) > 0 {
				page.LastID = page.Data[len(page.Data)-1].ID
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, &requests
}

func TestChildrenTreeEndsOnACycle(t *testing.T) {
	t.Parallel()

	// root -> a -> b -> root. An unguarded walk never returns.
	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}}},
		"a":    {{ids: []string{"b"}}},
		"b":    {{ids: []string{"root"}}},
	})

	done := make(chan struct{})
	var root *TreeNode
	var err error
	go func() {
		root, err = client.Sessions().ChildrenTree(context.Background(), "root", TreeOptions{MaxDepth: 10})
		close(done)
	}()
	select {
	case <-done:
	case <-testTimeout(t):
		t.Fatal("ChildrenTree did not return; the cycle guard is not holding")
	}
	if err != nil {
		t.Fatalf("ChildrenTree: %v", err)
	}

	// A session reachable two ways genuinely appears twice in the tree, so the
	// invariant is not "each id once" — it is that the walk descends into each
	// session once, and records the repeat as a boundary instead of following it.
	descended := map[string]int{}
	var repeats []*TreeNode
	var count func(*TreeNode)
	count = func(n *TreeNode) {
		if len(n.Children) > 0 {
			descended[n.ID]++
		}
		if n.ID == "root" && n.Depth > 0 {
			repeats = append(repeats, n)
		}
		for _, c := range n.Children {
			count(c)
		}
	}
	count(root)
	for id, times := range descended {
		if times > 1 {
			t.Errorf("descended into %s %d times; the visited set is not holding", id, times)
		}
	}
	if len(repeats) != 1 {
		t.Fatalf("the cycle back to root appears %d times, want 1", len(repeats))
	}
	if !repeats[0].Truncated {
		t.Error("the cycle edge is not marked Truncated, so a caller cannot tell it from a leaf")
	}
	if len(repeats[0].Children) != 0 {
		t.Error("the walk followed the cycle edge")
	}
}

func TestChildrenTreePagesEveryChild(t *testing.T) {
	t.Parallel()

	// Two pages under one parent. A walk that reads only the first loses "b".
	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}, hasMore: true}, {ids: []string{"b"}}},
	})

	root, err := client.Sessions().ChildrenTree(context.Background(), "root", TreeOptions{})
	if err != nil {
		t.Fatalf("ChildrenTree: %v", err)
	}
	var got []string
	for _, c := range root.Children {
		got = append(got, c.ID)
	}
	if len(got) != 2 {
		t.Fatalf("children = %v, want both pages: [a b]", got)
	}
}

func TestChildrenTreeStopsAtTheDepthCapAndSaysSo(t *testing.T) {
	t.Parallel()

	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}}},
		"a":    {{ids: []string{"b"}}},
		"b":    {{ids: []string{"c"}}},
	})

	root, err := client.Sessions().ChildrenTree(context.Background(), "root", TreeOptions{MaxDepth: 1})
	if err != nil {
		t.Fatalf("ChildrenTree: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(root.Children))
	}
	child := root.Children[0]
	if len(child.Children) != 0 {
		t.Errorf("descended past MaxDepth 1")
	}
	if !child.Truncated {
		t.Error("node at the cap has children and does not report Truncated, so a caller cannot tell it from a leaf")
	}
}

func TestSubtreeBusyRefusesAFalseNegativeOnATruncatedWalk(t *testing.T) {
	t.Parallel()

	// The busy child sits below the cap, so a quiet answer would be wrong.
	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}}},
		"a":    {{ids: []string{"b"}}},
	})

	busy, err := client.Sessions().SubtreeBusy(context.Background(), "root", TreeOptions{MaxDepth: 1})
	if busy {
		t.Fatal("SubtreeBusy = true; the fixture has no busy child")
	}
	if err == nil {
		t.Error("SubtreeBusy reported a quiet subtree with no error, but the walk was truncated")
	}
}

func TestChildSessionBusyPrefersTheLiveSignal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		child ChildSessionSummary
		want  bool
	}{
		{"live busy wins", ChildSessionSummary{Busy: Ptr(true), CurrentTaskStatus: Ptr("completed")}, true},
		{"live idle wins", ChildSessionSummary{Busy: Ptr(false), CurrentTaskStatus: Ptr("running")}, false},
		{"falls back to a non-terminal task", ChildSessionSummary{CurrentTaskStatus: Ptr("running")}, true},
		{"a terminal task is idle", ChildSessionSummary{CurrentTaskStatus: Ptr("failed")}, false},
		{"no signal at all is idle", ChildSessionSummary{}, false},
		{"an unknown status counts as work", ChildSessionSummary{CurrentTaskStatus: Ptr("a_status_this_build_never_saw")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ChildSessionBusy(tc.child); got != tc.want {
				t.Errorf("ChildSessionBusy = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListStopsWhenTheCallerStopsRanging(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// Always another page, so only the caller can end this.
		page := Page[SessionListItem]{
			Data:    []SessionListItem{{ID: fmt.Sprintf("conv_%d", requests.Load())}},
			LastID:  fmt.Sprintf("conv_%d", requests.Load()),
			HasMore: true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := 0
	for _, err := range client.Sessions().List(context.Background(), ListSessionsOptions{}) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen++
		if seen == 3 {
			break
		}
	}
	if got := requests.Load(); got > 4 {
		t.Errorf("issued %d requests after the caller stopped at 3 items; the walk kept paging", got)
	}
}

func TestListTerminatesOnAnEmptyCursorEvenWhenTheServerSaysMore(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// A proxy or a future server that reports more while returning nothing.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":true}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, err := range client.Sessions().List(context.Background(), ListSessionsOptions{}) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("issued %d requests against an empty page reporting has_more; want 1", got)
	}
}

func TestRejectedArgumentReachesTheCallerThroughTheSequence(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := 0
	for _, err := range client.Sessions().ListItems(context.Background(), "", SessionItemsOptions{}) {
		got++
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("error = %v, want ErrInvalidArgument", err)
		}
	}
	if got != 1 {
		t.Errorf("yielded %d times for a rejected argument, want exactly 1; a nil sequence would look like an empty listing", got)
	}
}

func TestClearingAnOverrideSendsTheServersAlias(t *testing.T) {
	t.Parallel()

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"conv_1"}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Sessions().ClearModelOverride(context.Background(), "conv_1"); err != nil {
		t.Fatalf("ClearModelOverride: %v", err)
	}
	// The description clears on an alias, not on an empty value. An empty string
	// would be forwarded to the executor as a model name.
	if got := body["model_override"]; got != clearOverrideAlias {
		t.Errorf("model_override = %v, want %q", got, clearOverrideAlias)
	}
}

func TestSetModelOverrideRefusesTheEmptyString(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Sessions().SetModelOverride(context.Background(), "conv_1", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("error = %v, want ErrInvalidArgument: the empty string is not the clear", err)
	}
}

// testTimeout gives a blocking test a bound without importing a clock.
func testTimeout(t *testing.T) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	timer := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			close(timer)
		}
	}()
	return timer
}

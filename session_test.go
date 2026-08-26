package omnigent

import (
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
		root, err = client.Sessions().ChildrenTree(t.Context(), "root", TreeOptions{MaxDepth: 10})
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
	if !repeats[0].Repeated {
		t.Error("the cycle edge is not marked Repeated, so a caller cannot tell it from a leaf")
	}
	if repeats[0].Truncated {
		t.Error("the cycle edge reports Truncated; that reason is the depth cap, not a cycle")
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

	root, err := client.Sessions().ChildrenTree(t.Context(), "root", TreeOptions{})
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

	root, err := client.Sessions().ChildrenTree(t.Context(), "root", TreeOptions{MaxDepth: 1})
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

func TestSubtreeBusyReportsAnIncompleteWalkRatherThanAFalseNegative(t *testing.T) {
	t.Parallel()

	// The busy child sits below the cap, so a quiet answer would be wrong.
	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}}},
		"a":    {{ids: []string{"b"}}},
	})

	busy, complete, err := client.Sessions().SubtreeBusy(t.Context(), "root", TreeOptions{MaxDepth: 1})
	if err != nil {
		t.Fatalf("SubtreeBusy: %v", err)
	}
	if busy {
		t.Fatal("SubtreeBusy = true; the fixture has no busy child")
	}
	// The busy child sits below the cap, so a quiet answer is inconclusive and the
	// caller has to be told that rather than left to assume the subtree is idle.
	if complete {
		t.Error("reported a complete walk, but it stopped at MaxDepth 1 with children below")
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
	for _, err := range client.Sessions().List(t.Context(), ListSessionsOptions{}) {
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
	for _, err := range client.Sessions().List(t.Context(), ListSessionsOptions{}) {
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
	for _, err := range client.Sessions().ListItems(t.Context(), "", SessionItemsOptions{}) {
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
	if _, err := client.Sessions().ClearModelOverride(t.Context(), "conv_1"); err != nil {
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
	if _, err := client.Sessions().SetModelOverride(t.Context(), "conv_1", ""); !errors.Is(err, ErrInvalidArgument) {
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

// TestSubtreeBusyReportsACompleteWalk pins the other half: when the walk reached
// the whole subtree, a quiet answer is conclusive and the caller should not be
// nudged into raising the depth.
func TestSubtreeBusyReportsACompleteWalk(t *testing.T) {
	t.Parallel()

	// One level, and the child has no children of its own.
	client, _ := treeServer(t, map[string][]childPage{
		"root": {{ids: []string{"a"}}},
		"a":    {{ids: nil}},
	})

	busy, complete, err := client.Sessions().SubtreeBusy(t.Context(), "root", TreeOptions{MaxDepth: 3})
	if err != nil {
		t.Fatalf("SubtreeBusy: %v", err)
	}
	if busy {
		t.Error("busy = true; the fixture has no busy child")
	}
	if !complete {
		t.Error("complete = false for a walk that reached every node")
	}
}

// TestListingThatNeverAdvancesEnds pins the bound that makes a cursor walk the
// caller's to end rather than the server's to grant.
//
// A server that returns a cursor it already returned — through a bug, or a proxy
// rewriting cursors — otherwise pages forever in the caller's process, buffering
// every row. Measured before this bound existed: 26,094 requests in two seconds.
func TestListingThatNeverAdvancesEnds(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		page := Page[SessionListItem]{
			Data:    []SessionListItem{{ID: fmt.Sprintf("conv_%d", n)}},
			LastID:  fmt.Sprintf("cur_%d", n%2), // alternates, never advances
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
	var got error
	for _, err := range client.Sessions().List(t.Context(), ListSessionsOptions{}) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil {
		t.Fatal("the walk reported no error against a listing that never advanced")
	}
	if !errors.Is(got, ErrListingUnbounded) {
		t.Errorf("error = %v, want it to wrap ErrListingUnbounded", got)
	}
	if requests.Load() > 10 {
		t.Errorf("issued %d requests before stopping", requests.Load())
	}
}

// TestAnEmptyPageRefusesTheListing pins the stop [Page.HasMore] documents and the
// walk did not implement, and pins it as a refusal rather than a quiet end.
//
// A page with no rows that still claims more clears both other guards: HasMore is
// true, LastID is non-empty, and no cursor repeats. Left alone the walk runs to the
// page cap. Stopped quietly it is worse than that: the caller cannot tell a
// truncated listing from a complete one, so a reclaim sweep would report success
// over the sessions it never saw. So it yields [ErrListingUnbounded] instead, like
// the repeated-cursor and page-cap guards it belongs with.
func TestAnEmptyPageRefusesTheListing(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		page := Page[SessionListItem]{
			Data:    nil,                      // nothing, but
			HasMore: true,                     // always more,
			LastID:  fmt.Sprintf("cur_%d", n), // on a cursor that advances
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var got error
	var rows int
	for _, err := range client.Sessions().List(t.Context(), ListSessionsOptions{}) {
		if err != nil {
			got = err
			break
		}
		rows++
	}
	if got == nil {
		t.Fatal("the walk ended quietly on a page that claimed more while returning " +
			"nothing; a caller cannot tell that from a complete listing")
	}
	if !errors.Is(got, ErrListingUnbounded) {
		t.Errorf("error = %v, want it to wrap ErrListingUnbounded", got)
	}
	if rows != 0 {
		t.Errorf("yielded %d rows from empty pages", rows)
	}
	// One request: the first empty page settles it, so this costs no amplification.
	if n := requests.Load(); n != 1 {
		t.Errorf("issued %d requests, want 1", n)
	}
}

// TestChildrenTreeBoundsEveryLevel pins the concurrency bound at the level that
// has the most requests.
//
// The depth-cap probe is one listing per child at the widest level of the walk.
// Before it took a token, a bound of 4 admitted 28 in flight.
func TestChildrenTreeBoundsEveryLevel(t *testing.T) {
	t.Parallel()

	var inFlight, peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)

		page := Page[ChildSessionSummary]{}
		for i := range 6 {
			page.Data = append(page.Data, ChildSessionSummary{ID: fmt.Sprintf("%s-%d", r.URL.Path, i)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Sessions().ChildrenTree(t.Context(), "root",
		TreeOptions{MaxDepth: 3, Concurrency: 4}); err != nil {
		t.Fatalf("ChildrenTree: %v", err)
	}
	if got := peak.Load(); got > 4 {
		t.Errorf("peak %d concurrent listings against a bound of 4", got)
	}
}

// TestSubtreeBusyIsAFunctionOfServerStateNotResponseOrder pins the property a
// shared visited set broke.
//
// The fixture is asymmetric on purpose: "shared" is reachable at depth 1 through
// "near" and at depth 2 through "far". With one visited set, whichever goroutine
// won the race claimed it, and the claiming node's depth then decided whether the
// busy leaf below it fell inside MaxDepth. Same server state, different answer,
// chosen by which HTTP response landed first.
func TestSubtreeBusyIsAFunctionOfServerStateNotResponseOrder(t *testing.T) {
	t.Parallel()

	// root -> near -> shared -> leaf(busy)      (leaf at depth 3)
	// root -> mid  -> far -> shared -> leaf     (leaf at depth 4, past MaxDepth 3)
	//
	// Whichever path claims "shared" first, the walk must still find the busy leaf
	// through the shallow one.
	for _, slow := range []string{"near", "mid"} {
		t.Run("slow_"+slow, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
				id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[2]
				if id == slow {
					time.Sleep(60 * time.Millisecond)
				}
				page := Page[ChildSessionSummary]{Data: []ChildSessionSummary{}}
				switch id {
				case "root":
					page.Data = []ChildSessionSummary{{ID: "near"}, {ID: "mid"}}
				case "near":
					page.Data = []ChildSessionSummary{{ID: "shared"}}
				case "mid":
					page.Data = []ChildSessionSummary{{ID: "far"}}
				case "far":
					page.Data = []ChildSessionSummary{{ID: "shared"}}
				case "shared":
					page.Data = []ChildSessionSummary{{ID: "leaf", Busy: Ptr(true)}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(page)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			busy, _, err := client.Sessions().SubtreeBusy(
				t.Context(), "root", TreeOptions{MaxDepth: 3})
			if err != nil {
				t.Fatalf("SubtreeBusy: %v", err)
			}
			if !busy {
				t.Error("busy = false; the busy leaf is reachable at depth 3 through near->shared, " +
					"so the answer must not depend on which parent claimed shared")
			}
		})
	}
}

// TestChildrenTreeNamesWhyANodeHasNoChildren pins the three reasons apart.
func TestChildrenTreeNamesWhyANodeHasNoChildren(t *testing.T) {
	t.Parallel()

	client, _ := treeServer(t, map[string][]childPage{
		"root":  {{ids: []string{"cap", "cycle"}}},
		"cap":   {{ids: []string{"below_cap"}}},
		"cycle": {{ids: []string{"root"}}},
	})

	root, err := client.Sessions().ChildrenTree(t.Context(), "root", TreeOptions{MaxDepth: 1})
	if err != nil {
		t.Fatalf("ChildrenTree: %v", err)
	}
	byID := map[string]*TreeNode{}
	for _, c := range root.Children {
		byID[c.ID] = c
	}
	if node := byID["cap"]; node == nil || !node.Truncated || node.Repeated {
		t.Errorf("the depth-capped node reports Truncated=%v Repeated=%v, want true/false",
			node != nil && node.Truncated, node != nil && node.Repeated)
	}
	// "cycle" is at the cap too, so its own child is never fetched — the cap wins
	// over the cycle because the walk stops before it looks.
	if node := byID["cycle"]; node == nil || !node.Truncated {
		t.Errorf("the second node at the cap does not report Truncated")
	}
}

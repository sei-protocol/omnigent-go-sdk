package omnigent

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// TerminalTaskStatuses are the task statuses that mean no work is outstanding.
//
// A status this build has never seen counts as non-terminal, which is the safe
// direction: treating unknown work as finished reports a busy subtree as idle.
var TerminalTaskStatuses = []string{"completed", "failed", "cancelled"}

// IsTerminalTaskStatus reports whether a task status means the work has stopped.
func IsTerminalTaskStatus(status string) bool {
	return slices.Contains(TerminalTaskStatuses, status)
}

// ChildSessionBusy reports whether a child session has work outstanding.
//
// Two signals, in the order the server itself resolves them. Busy is the live
// answer from the session loop, so it wins when the server sends it. Otherwise
// fall back to the current task's status, where a non-terminal status means work
// is outstanding and a missing status means none is.
//
// Reading both matters: Busy is nil on a summary the server built without the
// live cache, and treating nil as idle reports a working subtree as finished.
func ChildSessionBusy(child ChildSessionSummary) bool {
	if child.Busy != nil {
		return *child.Busy
	}
	if child.CurrentTaskStatus == nil {
		return false
	}
	return !IsTerminalTaskStatus(*child.CurrentTaskStatus)
}

// TreeOptions bounds a subtree walk. The zero value uses the defaults below.
type TreeOptions struct {
	// MaxDepth caps how far the walk descends. Zero means three levels.
	//
	// A cap rather than an option to omit: a session tree is server-shaped, so a
	// caller cannot know its depth before walking it, and an unbounded walk on a
	// deep tree is a request storm the caller did not ask for.
	MaxDepth int

	// Concurrency caps how many child listings run at once. Zero means four.
	Concurrency int

	// MaxNodes caps how many sessions the walk will visit. Zero means one
	// thousand.
	//
	// The walk spawns one goroutine per node, so without this the tree's shape
	// decides the process's memory. It also bounds the one cost of per-path cycle
	// detection: a session reachable through two parents is expanded under both,
	// which is true to the topology and doubles the work for that subtree.
	MaxNodes int
}

func (o TreeOptions) withDefaults() TreeOptions {
	if o.MaxDepth <= 0 {
		o.MaxDepth = 3
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.MaxNodes <= 0 {
		o.MaxNodes = 1000
	}
	return o
}

// TreeNode is one session in a subtree walk, with the children the walk reached.
//
// A node with no children has one of four reasons, and a caller usually needs to
// know which: it has none, the walk stopped at the depth cap, the session already
// appears on the path above it, or its listing failed. The three fields below name
// the last three; all false with no children means it genuinely has none.
type TreeNode struct {
	// Session is the summary the parent's listing carried. Zero for the root,
	// which no listing describes.
	Session ChildSessionSummary

	// ID is the session this node describes.
	ID string

	// Depth is how far below the root this node sits. Zero for the root.
	Depth int

	// Children are the nodes below this one.
	Children []*TreeNode

	// Truncated reports that this node has children the walk did not fetch,
	// because it reached [TreeOptions.MaxDepth] or the node ceiling. Raise the
	// bound and walk again to see them.
	Truncated bool

	// Repeated reports that this session already appears between here and the
	// root, so the walk stopped rather than following a cycle. Its children are
	// whatever they are at the shallower position.
	Repeated bool

	// Err is this node's own listing failure, so a caller can tell a subtree that
	// is empty from one that could not be read. The walk continues past it, and
	// [Sessions.ChildrenTree] also returns the first such error.
	Err error
}

// ChildrenTree walks a session's subtree, bounded.
//
// Three bounds, each for a failure this walk would otherwise hit. Depth, because
// a caller cannot know a server-shaped tree's depth in advance. Concurrency,
// because one listing per child at every level is a request storm. And a visited
// set, because a tree that carries a cycle would otherwise never end — the walk
// visits each session once and records the repeat as a non-descent rather than
// following it.
//
// Every child pages. A parent with more children than one page holds still
// reports all of them, which is where upstream's own walk stops short.
//
// On error it returns the partial tree alongside it. An operator inspecting a
// broken tree needs the part that resolved.
func (s *Sessions) ChildrenTree(ctx context.Context, sessionID string, opts TreeOptions) (*TreeNode, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("walk child sessions: %w: sessionID is required", ErrInvalidArgument)
	}
	opts = opts.withDefaults()

	root := &TreeNode{ID: sessionID}
	tokens := make(chan struct{}, opts.Concurrency)

	var mu sync.Mutex
	var firstErr error
	visited := 1

	// recordErr keeps the first failure and drops the rest: a walk that fails in
	// four places has one cause worth reporting and three symptoms.
	recordErr := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	// admit reserves a slot under the node ceiling. It is the only shared counter
	// the walk keeps, because a per-path visited set cannot bound total work.
	admit := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if visited >= opts.MaxNodes {
			return false
		}
		visited++
		return true
	}

	// listChildren drains one node's listing, holding a token for the whole walk
	// of it. The depth-cap probe holds one too: that level has more requests than
	// every other combined, so leaving it unbounded would make Concurrency a
	// number that describes nothing.
	listChildren := func(node *TreeNode) ([]ChildSessionSummary, error) {
		tokens <- struct{}{}
		defer func() { <-tokens }()

		var children []ChildSessionSummary
		for child, err := range s.Children(ctx, node.ID) {
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}
		return children, nil
	}

	var wg sync.WaitGroup
	var walk func(node *TreeNode, ancestors map[string]bool)

	walk = func(node *TreeNode, ancestors map[string]bool) {
		defer wg.Done()

		children, err := listChildren(node)
		if err != nil {
			// On the node, so a caller can tell an unreadable subtree from an empty
			// one, and on the walk, so one call site sees that something failed.
			node.Err = err
			recordErr(err)
			return
		}
		if node.Depth >= opts.MaxDepth {
			node.Truncated = len(children) > 0
			return
		}

		// One set per path, copied on descent. A path cannot race with itself, so
		// which node owns a shared subtree no longer depends on which HTTP response
		// lands first. The cost is that a session reachable two ways is expanded
		// twice, which MaxNodes bounds.
		for _, child := range children {
			next := &TreeNode{Session: child, ID: child.ID, Depth: node.Depth + 1}
			node.Children = append(node.Children, next)

			switch {
			case ancestors[child.ID]:
				next.Repeated = true
			case !admit():
				next.Truncated = true
			default:
				descend := make(map[string]bool, len(ancestors)+1)
				for id := range ancestors {
					descend[id] = true
				}
				descend[child.ID] = true

				wg.Add(1)
				go walk(next, descend)
			}
		}
	}

	wg.Add(1)
	walk(root, map[string]bool{sessionID: true})
	wg.Wait()

	return root, firstErr
}

// SubtreeBusy reports whether any session *below* the named one has work
// outstanding, and whether the walk saw the whole subtree.
//
// It does not read the named session's own state: no listing describes it, so
// there is no summary to read. Use [Sessions.Get] for that.
//
// complete is false when the walk did not see the whole subtree — it stopped at
// [TreeOptions.MaxDepth] or the node ceiling, or a listing failed. That makes a
// false busy inconclusive rather than wrong. A true busy is sound either way, so
// complete is true whenever busy is.
//
// A cycle does not make the answer incomplete: a repeated session's subtree was
// already walked where it first appeared.
//
// Truncation is deliberately not an error. It is a property of the answer, and the
// caller decides what to do about it: raise [TreeOptions.MaxDepth] and ask again,
// or accept the bound. Reporting it as an error would either force a caller to
// string-match, or make this package invent a sentinel for a case that is not a
// failure. An error here means the walk itself failed.
//
//	busy, complete, err := sessions.SubtreeBusy(ctx, id, omnigent.TreeOptions{})
//	switch {
//	case err != nil:
//		return err
//	case busy:
//		// work outstanding, whatever the walk missed
//	case !complete:
//		// nothing found, but the walk did not reach the whole subtree
//	}
func (s *Sessions) SubtreeBusy(ctx context.Context, sessionID string, opts TreeOptions) (busy, complete bool, err error) {
	root, err := s.ChildrenTree(ctx, sessionID, opts)
	if root == nil {
		return false, false, err
	}

	// Three of the four reasons a node has no children leave the answer sound. A
	// Repeated node's subtree was already walked at its shallower position, so
	// nothing below it is unseen. Truncated and Err are the two that hide work.
	incomplete := false
	var visit func(*TreeNode)
	visit = func(node *TreeNode) {
		if node.Truncated || node.Err != nil {
			incomplete = true
		}
		// Depth 0 is the named session, which no listing describes, so there is no
		// summary to read. A caller asking about that session's own turn wants
		// Sessions.Get; this answers about the subtree below it, and says so.
		if node.Depth > 0 && ChildSessionBusy(node.Session) {
			busy = true
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root)

	// A positive answer needs no completeness caveat: work found is work found.
	if busy {
		return true, true, err
	}
	return false, !incomplete, err
}

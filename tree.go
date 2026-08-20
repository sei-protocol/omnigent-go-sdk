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
}

func (o TreeOptions) withDefaults() TreeOptions {
	if o.MaxDepth <= 0 {
		o.MaxDepth = 3
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	return o
}

// TreeNode is one session in a subtree walk, with the children the walk reached.
type TreeNode struct {
	// Session is the summary the parent's listing carried. Zero for the root,
	// which no listing describes.
	Session ChildSessionSummary

	// ID is the session this node describes.
	ID string

	// Depth is how far below the root this node sits. Zero for the root.
	Depth int

	// Children are the nodes below this one, empty at the depth cap.
	Children []*TreeNode

	// Truncated reports that this node has children the walk did not descend
	// into, because it reached the depth cap. Without it a caller cannot tell a
	// leaf from a boundary.
	Truncated bool
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
	seen := map[string]bool{sessionID: true}
	var mu sync.Mutex
	tokens := make(chan struct{}, opts.Concurrency)

	var firstErr error
	var walk func(node *TreeNode)
	var wg sync.WaitGroup

	walk = func(node *TreeNode) {
		defer wg.Done()

		if node.Depth >= opts.MaxDepth {
			// Ask whether children exist without descending, so Truncated is an
			// answer rather than a guess.
			for child, err := range s.Children(ctx, node.ID) {
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				_ = child
				node.Truncated = true
				return
			}
			return
		}

		tokens <- struct{}{}
		var children []ChildSessionSummary
		var listErr error
		for child, err := range s.Children(ctx, node.ID) {
			if err != nil {
				listErr = err
				break
			}
			children = append(children, child)
		}
		<-tokens

		if listErr != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = listErr
			}
			mu.Unlock()
			return
		}

		for _, child := range children {
			mu.Lock()
			repeat := seen[child.ID]
			if !repeat {
				seen[child.ID] = true
			}
			mu.Unlock()
			if repeat {
				// A cycle, or the same session reachable two ways. Record it as a
				// leaf rather than following it again.
				node.Children = append(node.Children, &TreeNode{
					Session: child, ID: child.ID, Depth: node.Depth + 1, Truncated: true,
				})
				continue
			}
			next := &TreeNode{Session: child, ID: child.ID, Depth: node.Depth + 1}
			node.Children = append(node.Children, next)
			wg.Add(1)
			go walk(next)
		}
	}

	wg.Add(1)
	walk(root)
	wg.Wait()

	return root, firstErr
}

// SubtreeBusy reports whether any session in the subtree has work outstanding.
//
// It walks the same bounded tree, so the same caveats apply: a subtree deeper than
// the cap answers about the part it reached. A truncated walk that found no work
// is not proof there is none, so this reports the truncation as an error rather
// than a false negative.
func (s *Sessions) SubtreeBusy(ctx context.Context, sessionID string, opts TreeOptions) (bool, error) {
	root, err := s.ChildrenTree(ctx, sessionID, opts)
	if root == nil {
		return false, err
	}

	busy := false
	truncated := false
	var visit func(*TreeNode)
	visit = func(node *TreeNode) {
		if node.Truncated {
			truncated = true
		}
		if node.Depth > 0 && ChildSessionBusy(node.Session) {
			busy = true
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root)

	if busy {
		// A positive answer is sound whatever the walk missed.
		return true, err
	}
	if err != nil {
		return false, err
	}
	if truncated {
		return false, fmt.Errorf("subtree busy for %s: %w: the walk stopped at depth %d, so a quiet result is not proof",
			sessionID, ErrInvalidArgument, opts.withDefaults().MaxDepth)
	}
	return false, nil
}

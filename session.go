package omnigent

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/url"
)

// Sessions reaches the session surface.
//
// It exists as its own type rather than as methods on [Client] because the
// surface is twenty-odd calls, and upstream's Python client draws the same line
// with its sessions namespace. Reach it through [Client.Sessions].
type Sessions struct {
	client *Client
}

// Sessions returns the session surface, bound to this client.
func (c *Client) Sessions() *Sessions { return &Sessions{client: c} }

// Create creates a session bound to an existing agent and returns its snapshot.
func (s *Sessions) Create(ctx context.Context, req SessionCreateRequest) (*SessionResponse, error) {
	if req.AgentID == "" {
		return nil, fmt.Errorf("create session: %w: AgentID is required", ErrInvalidArgument)
	}
	var session SessionResponse
	if err := s.client.doJSON(ctx, http.MethodPost, []string{"v1", "sessions"}, nil, req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Get returns a session's snapshot.
//
// A 404 here does not prove the session is absent: the server answers 404 for a
// session the caller cannot see, so as not to leak its existence. See
// [ErrNotFound].
func (s *Sessions) Get(ctx context.Context, sessionID string, opts GetSessionOptions) (*SessionResponse, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("get session: %w: sessionID is required", ErrInvalidArgument)
	}
	var session SessionResponse
	if err := s.client.doJSON(ctx, http.MethodGet,
		[]string{"v1", "sessions", sessionID}, opts.query(), nil, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Delete deletes a session.
func (s *Sessions) Delete(ctx context.Context, sessionID string, opts DeleteSessionOptions) (*ConversationDeleted, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("delete session: %w: sessionID is required", ErrInvalidArgument)
	}
	var deleted ConversationDeleted
	if err := s.client.doJSON(ctx, http.MethodDelete,
		[]string{"v1", "sessions", sessionID}, opts.query(), nil, &deleted); err != nil {
		return nil, err
	}
	return &deleted, nil
}

// Fork branches a session at its current state and returns the new one.
func (s *Sessions) Fork(ctx context.Context, sessionID string, req SessionForkRequest) (*SessionResponse, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("fork session: %w: sessionID is required", ErrInvalidArgument)
	}
	var forked SessionResponse
	if err := s.client.doJSON(ctx, http.MethodPost,
		[]string{"v1", "sessions", sessionID, "fork"}, nil, req, &forked); err != nil {
		return nil, err
	}
	return &forked, nil
}

// List walks every session the caller can see.
//
// The walk pages internally, so a caller ranges once. Order on creation time
// unless recency is what matters: activity reorders an updated-at listing
// underneath the cursor, so a long walk under it can revisit or skip an item.
// See [SessionSortBy].
func (s *Sessions) List(ctx context.Context, opts ListSessionsOptions) iter.Seq2[SessionListItem, error] {
	return pageSeq(ctx, func(ctx context.Context, cursor string) (*Page[SessionListItem], error) {
		query := opts.query()
		if cursor != "" {
			query.Set("after", cursor)
		}
		var page Page[SessionListItem]
		if err := s.client.doJSON(ctx, http.MethodGet,
			[]string{"v1", "sessions"}, query, nil, &page); err != nil {
			return nil, err
		}
		return &page, nil
	})
}

// ListItems walks a session's conversation items in transcript order.
//
// The description types this listing's payload as heterogeneous, so each item's
// own payload arrives undecoded. Switch on [ConversationItem.Type], then
// unmarshal [ConversationItem.Data] into the matching variant.
func (s *Sessions) ListItems(ctx context.Context, sessionID string, opts SessionItemsOptions) iter.Seq2[ConversationItem, error] {
	if sessionID == "" {
		return errSeq[ConversationItem](fmt.Errorf("list session items: %w: sessionID is required", ErrInvalidArgument))
	}
	return pageSeq(ctx, func(ctx context.Context, cursor string) (*Page[ConversationItem], error) {
		query := opts.query()
		if cursor != "" {
			query.Set("after", cursor)
		}
		var page Page[ConversationItem]
		if err := s.client.doJSON(ctx, http.MethodGet,
			[]string{"v1", "sessions", sessionID, "items"}, query, nil, &page); err != nil {
			return nil, err
		}
		return &page, nil
	})
}

// Children walks a session's immediate child sessions.
//
// Immediate, not recursive: [Sessions.ChildrenTree] walks the subtree, and it
// bounds itself because a tree can carry a cycle.
func (s *Sessions) Children(ctx context.Context, sessionID string) iter.Seq2[ChildSessionSummary, error] {
	if sessionID == "" {
		return errSeq[ChildSessionSummary](fmt.Errorf("list child sessions: %w: sessionID is required", ErrInvalidArgument))
	}
	return pageSeq(ctx, func(ctx context.Context, cursor string) (*Page[ChildSessionSummary], error) {
		query := url.Values{}
		if cursor != "" {
			query.Set("after", cursor)
		}
		var page Page[ChildSessionSummary]
		if err := s.client.doJSON(ctx, http.MethodGet,
			[]string{"v1", "sessions", sessionID, "child_sessions"}, query, nil, &page); err != nil {
			return nil, err
		}
		return &page, nil
	})
}

// ListAgents walks the registered agents.
//
// The description types this listing's payload as heterogeneous, so the agent
// shape here is a contract this package states rather than one the document
// declares. See [AgentObject].
func (s *Sessions) ListAgents(ctx context.Context, opts ListAgentsOptions) iter.Seq2[AgentObject, error] {
	return pageSeq(ctx, func(ctx context.Context, cursor string) (*Page[AgentObject], error) {
		query := opts.query()
		if cursor != "" {
			query.Set("after", cursor)
		}
		var page Page[AgentObject]
		if err := s.client.doJSON(ctx, http.MethodGet,
			[]string{"v1", "agents"}, query, nil, &page); err != nil {
			return nil, err
		}
		return &page, nil
	})
}

// errSeq yields one error and stops.
//
// An argument this package rejects before sending anything still has to reach the
// caller, and a sequence has no other channel. Returning nil instead would make a
// rejected call look like an empty listing.
func errSeq[T any](err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		yield(zero, err)
	}
}

// Six of the session's state changes are one PATCH each, and every field already
// exists on the update request. Naming them is the point: SetModelOverride states
// an intent that a generic patch call does not, and the difference between
// clearing an override and unbinding a runner is legible at the call site rather
// than in the payload.

// Update applies an arbitrary patch and returns the new snapshot.
//
// Prefer one of the named wrappers below. This is here for a field they do not
// cover yet.
func (s *Sessions) Update(ctx context.Context, sessionID string, req UpdateSessionRequest) (*SessionResponse, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("update session: %w: sessionID is required", ErrInvalidArgument)
	}
	var session SessionResponse
	if err := s.client.doJSON(ctx, http.MethodPatch,
		[]string{"v1", "sessions", sessionID}, nil, req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// clearOverrideAlias is the value the server reads as "remove the override".
//
// The description names "default", "off" and "reset" for the model override and
// "default" for reasoning effort, so "default" is the one both accept.
const clearOverrideAlias = "default"

// There is no UnbindRunner. The description says only that a nil runner_id leaves
// the binding unchanged; it defines no value that releases one. Add the method
// when the description names the release, not before.

// BindRunner binds the session to a runner.
func (s *Sessions) BindRunner(ctx context.Context, sessionID, runnerID string) (*SessionResponse, error) {
	if runnerID == "" {
		return nil, fmt.Errorf("bind runner: %w: runnerID is required", ErrInvalidArgument)
	}
	return s.Update(ctx, sessionID, UpdateSessionRequest{RunnerID: Ptr(runnerID)})
}

// SetModelOverride substitutes a model for the agent spec's own.
//
// The value is forwarded to the executor as-is; the server does not enumerate
// valid models, so a bad one fails at turn start rather than here. Use
// [Sessions.ClearModelOverride] to remove it — the empty string is not the clear.
func (s *Sessions) SetModelOverride(ctx context.Context, sessionID, model string) (*SessionResponse, error) {
	if model == "" {
		return nil, fmt.Errorf("set model override: %w: model is empty; use ClearModelOverride", ErrInvalidArgument)
	}
	return s.Update(ctx, sessionID, UpdateSessionRequest{ModelOverride: Ptr(model)})
}

// ClearModelOverride returns the session to the model its agent spec names.
//
// The server clears on an alias rather than on an empty value, matching its own
// /model semantics. This sends one of those aliases.
func (s *Sessions) ClearModelOverride(ctx context.Context, sessionID string) (*SessionResponse, error) {
	return s.Update(ctx, sessionID, UpdateSessionRequest{ModelOverride: Ptr(clearOverrideAlias)})
}

// SetReasoningEffort sets the reasoning effort for later turns.
//
// Provider support is validated when a turn executes, not here. Use
// [Sessions.ClearReasoningEffort] to remove the override.
func (s *Sessions) SetReasoningEffort(ctx context.Context, sessionID, effort string) (*SessionResponse, error) {
	if effort == "" {
		return nil, fmt.Errorf("set reasoning effort: %w: effort is empty; use ClearReasoningEffort", ErrInvalidArgument)
	}
	return s.Update(ctx, sessionID, UpdateSessionRequest{ReasoningEffort: Ptr(effort)})
}

// ClearReasoningEffort returns the session to its spec's reasoning effort.
func (s *Sessions) ClearReasoningEffort(ctx context.Context, sessionID string) (*SessionResponse, error) {
	return s.Update(ctx, sessionID, UpdateSessionRequest{ReasoningEffort: Ptr(clearOverrideAlias)})
}

// SetArchived archives or restores the session.
func (s *Sessions) SetArchived(ctx context.Context, sessionID string, archived bool) (*SessionResponse, error) {
	return s.Update(ctx, sessionID, UpdateSessionRequest{Archived: Ptr(archived)})
}

// SetExternalID records a caller's own identifier on the session.
//
// Idempotent on the same value. A different value is rejected, because the
// mapping is meant to be stable once made.
func (s *Sessions) SetExternalID(ctx context.Context, sessionID, externalID string) (*SessionResponse, error) {
	return s.Update(ctx, sessionID, UpdateSessionRequest{ExternalSessionID: Ptr(externalID)})
}

// PostEvent sends one input to a session.
//
// This route is registered include_in_schema=False, so neither its body nor its
// responses appear in the vendored description. [SessionEventInput] and
// [EventAccepted] are contracts this package states.
//
// Read the result rather than discarding it: an input the server refuses comes
// back with Queued false and Denied true, and a caller that treats every
// unqueued send alike reports "accepted without queueing" when the server had
// already said what was wrong.
func (s *Sessions) PostEvent(ctx context.Context, sessionID string, input SessionEventInput) (*EventAccepted, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("post event: %w: sessionID is required", ErrInvalidArgument)
	}
	if input.Type == "" {
		return nil, fmt.Errorf("post event: %w: Type is required", ErrInvalidArgument)
	}
	var accepted EventAccepted
	if err := s.client.doJSON(ctx, http.MethodPost,
		[]string{"v1", "sessions", sessionID, "events"}, nil, input, &accepted); err != nil {
		return nil, err
	}
	return &accepted, nil
}

// SendMessage posts a user message and starts a turn.
func (s *Sessions) SendMessage(ctx context.Context, sessionID, text string) (*EventAccepted, error) {
	return s.PostEvent(ctx, sessionID, SessionEventInput{
		Type: InputTypeMessage,
		Data: map[string]any{"text": text},
	})
}

// Interrupt stops the turn in flight, leaving the session usable.
func (s *Sessions) Interrupt(ctx context.Context, sessionID string) error {
	_, err := s.PostEvent(ctx, sessionID, SessionEventInput{Type: InputTypeInterrupt})
	return err
}

// Compact asks the session to compact its context.
func (s *Sessions) Compact(ctx context.Context, sessionID string) error {
	_, err := s.PostEvent(ctx, sessionID, SessionEventInput{Type: InputTypeCompact})
	return err
}

// ResolveElicitation answers an approval request the agent raised.
//
// The request names the session its resolve endpoint belongs to, which is not
// always the session whose stream carried it: a child's prompt is mirrored into
// an ancestor's stream. Post to the session the request names, or the resolve
// lands nowhere and the agent stays parked. See
// [ElicitationRequestParams.TargetSessionID].
func (s *Sessions) ResolveElicitation(ctx context.Context, sessionID, elicitationID string, result ElicitationResult) error {
	if sessionID == "" || elicitationID == "" {
		return fmt.Errorf("resolve elicitation: %w: sessionID and elicitationID are required", ErrInvalidArgument)
	}
	return s.client.doJSON(ctx, http.MethodPost,
		[]string{"v1", "sessions", sessionID, "elicitations", elicitationID, "resolve"},
		nil, result, nil)
}

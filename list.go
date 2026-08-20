package omnigent

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strconv"
)

// Page is one page of a cursor-paginated listing.
//
// Every listing shares this envelope because the server does: GET /v1/agents,
// GET /v1/sessions and GET /v1/sessions/{id}/items return the same four fields
// around a differently-typed [Page.Data]. The server also sends a constant
// "object": "list" alongside them, which the Go type already conveys, so it is
// not carried here.
//
// Paging is by opaque cursor, not offset. To walk a listing, pass the previous
// page's [Page.LastID] as the next request's After while [Page.HasMore] is true:
//
//	opts := omnigent.ListSessionsOptions{AgentID: agentID}
//	for {
//		page, err := client.ListSessions(ctx, opts)
//		if err != nil {
//			return err
//		}
//		for _, s := range page.Data {
//			// ...
//		}
//		// Two conditions, not one. HasMore is the server's answer; the empty
//		// check is what makes the loop terminate on its own rather than on the
//		// server's good behaviour. An empty page yields an empty LastID, so
//		// continuing would re-request the first page forever.
//		if !page.HasMore || len(page.Data) == 0 {
//			break
//		}
//		opts.After = page.LastID
//	}
//
// Reversing that walk means Before and [Page.FirstID] instead. Do not derive a
// cursor from an item yourself: the ids are the server's and only the ones it
// hands back are guaranteed to page correctly under a given Order.
type Page[T any] struct {
	// Data is this page's items, in the requested order. Empty on the last page
	// of an exhausted listing, and empty rather than nil when the server sends
	// an empty array.
	Data []T `json:"data"`

	// FirstID is the id of Data's first item, the cursor for paging backwards.
	// Empty when Data is.
	FirstID string `json:"first_id,omitempty"`

	// LastID is the id of Data's last item, the cursor for paging forwards.
	// Empty when Data is.
	LastID string `json:"last_id,omitempty"`

	// HasMore reports whether more items exist beyond this page in the requested
	// direction — the direction is baked in, because the server applies the
	// cursor comparison and the ordering together.
	//
	// It is the loop condition, and a full page does not imply another one: the
	// server asks for one row more than the limit and reports whether it got it.
	// That also means a true HasMore beside an empty Data is not something this
	// server produces. Terminate on both anyway, per the loop above — an empty
	// page carries an empty LastID, so a caller that trusts HasMore alone re-reads
	// the first page for as long as a proxy or a future server keeps saying yes.
	HasMore bool `json:"has_more"`
}

// SortOrder is the direction a listing returns its items in.
type SortOrder string

const (
	// SortDescending returns newest first. The server's default on the agent and
	// session listings.
	SortDescending SortOrder = "desc"

	// SortAscending returns oldest first. The server's default on the items
	// listing, whose chronological order is the transcript's own.
	SortAscending SortOrder = "asc"
)

// SessionSortBy is the timestamp a session listing orders on.
type SessionSortBy string

const (
	// SessionSortByCreatedAt orders on creation time. The server's default, and
	// the stable choice for paging: an update mid-walk cannot reorder items
	// underneath the cursor.
	SessionSortByCreatedAt SessionSortBy = "created_at"

	// SessionSortByUpdatedAt orders on last-activity time, which is what a
	// recency-ordered list wants. Because activity reorders items, a long walk
	// under this can revisit or skip one.
	SessionSortByUpdatedAt SessionSortBy = "updated_at"
)

// SessionKind selects which sessions a listing includes.
type SessionKind string

const (
	// SessionKindDefault includes only top-level sessions. The server's
	// default: sub-agent sessions are an implementation detail of the parent
	// turn, so they stay out unless asked for.
	SessionKindDefault SessionKind = "default"

	// SessionKindSubAgent includes only sub-agent sessions.
	SessionKindSubAgent SessionKind = "sub_agent"

	// SessionKindAny includes both.
	SessionKindAny SessionKind = "any"
)

// ListAgentsOptions tunes an agent listing. The zero value asks for the
// server's defaults: the 20 most recently created agents.
type ListAgentsOptions struct {
	// Limit caps the page size. The server accepts 1 to 1000 and defaults to
	// 20; zero here sends nothing and takes that default.
	Limit int

	// After returns the agents following this cursor. Use a previous page's
	// [Page.LastID].
	After string

	// Before returns the agents preceding this cursor. Use a previous page's
	// [Page.FirstID].
	Before string

	// Order is the sort direction. Empty takes the server's default,
	// [SortDescending].
	Order SortOrder
}

func (o ListAgentsOptions) query() url.Values {
	query := url.Values{}
	setPageQuery(query, o.Limit, o.After, o.Before, o.Order)
	return query
}

// ListSessionsOptions tunes a session listing. The zero value asks for the
// server's defaults: the 20 most recently created top-level sessions the caller
// can see, excluding archived ones.
//
// The filters combine with AND. AgentID and AgentName both select by agent and
// are not alternatives to each other: setting both narrows to sessions matching
// both, which is empty unless they name the same agent.
type ListSessionsOptions struct {
	// Limit caps the page size. The server accepts 1 to 1000 and defaults to
	// 20; zero here sends nothing and takes that default.
	Limit int

	// After returns the sessions following this cursor. Use a previous page's
	// [Page.LastID].
	After string

	// Before returns the sessions preceding this cursor. Use a previous page's
	// [Page.FirstID].
	Before string

	// AgentID restricts the listing to one agent by id. This is the filter to
	// reach for when reconciling against sessions a program created earlier: it
	// is exact, where AgentName resolves through a mutable label.
	AgentID string

	// AgentName restricts the listing to one agent by name. An agent can be
	// renamed, so a program that stores a name rather than an id can start
	// matching a different agent, or none.
	AgentName string

	// Order is the sort direction. Empty takes the server's default,
	// [SortDescending].
	Order SortOrder

	// SortBy is the timestamp to order on. Empty takes the server's default,
	// [SessionSortByCreatedAt].
	SortBy SessionSortBy

	// SearchQuery restricts the listing to sessions matching a free-text
	// search. A match populates [SessionListItem.SearchSnippet].
	//
	// It travels as a query parameter, so it lands in the access log of the
	// server and of anything in front of it. Search for a title, not for
	// something that would be a problem to find written down.
	SearchQuery string

	// IncludeArchived also returns archived sessions, which are otherwise
	// omitted. [SessionListItem.Archived] tells them apart.
	IncludeArchived bool

	// Kind selects top-level, sub-agent, or all sessions. Empty takes the
	// server's default, [SessionKindDefault].
	Kind SessionKind

	// Project restricts the listing to one project by id.
	Project string

	// Pinned restricts the listing to pinned sessions.
	Pinned bool
}

func (o ListSessionsOptions) query() url.Values {
	query := url.Values{}
	setPageQuery(query, o.Limit, o.After, o.Before, o.Order)
	for name, value := range map[string]string{
		"agent_id":     o.AgentID,
		"agent_name":   o.AgentName,
		"sort_by":      string(o.SortBy),
		"search_query": o.SearchQuery,
		"kind":         string(o.Kind),
		"project":      o.Project,
	} {
		if value != "" {
			query.Set(name, value)
		}
	}
	for name, value := range map[string]bool{
		"include_archived": o.IncludeArchived,
		"pinned":           o.Pinned,
	} {
		if value {
			query.Set(name, "true")
		}
	}
	return query
}

// SessionItem is one item from [Client.ListSessionItems].
//
// It is untyped because the route sends the server's flatten-for-API shape
// rather than the [ConversationItem] a snapshot carries: id, response_id, type
// and status sit beside the typed payload's own fields — role and content on a
// message, name and arguments on a function call — spread onto the same object,
// with absent optional fields left out and no created_at at all. openapi.json
// declares this page's elements untyped, so there is nothing generated to decode
// them into either.
//
// An alias rather than a defined type, so it interchanges with the same flat
// shape on the stream, [OutputItemDoneEvent.Item].
type SessionItem = map[string]any

// SessionItemsOptions tunes a session-items listing. The zero value asks for the
// server's defaults: the session's oldest 100 items, chronologically.
type SessionItemsOptions struct {
	// Limit caps the page size. The server accepts 1 to 1000 and defaults to
	// 100; zero here sends nothing and takes that default.
	Limit int

	// After returns the items following this cursor. Use a previous page's
	// [Page.LastID].
	After string

	// Before returns the items preceding this cursor. Use a previous page's
	// [Page.FirstID]. Set beside After it narrows to the window between the two
	// rather than replacing it, because the server applies both comparisons.
	Before string

	// Order is the sort direction, and both cursors are read relative to it.
	// Empty takes this route's default, [SortAscending] — chronological, and
	// not the [SortDescending] the agent and session listings default to.
	Order SortOrder
}

func (o SessionItemsOptions) query() url.Values {
	query := url.Values{}
	setPageQuery(query, o.Limit, o.After, o.Before, o.Order)
	return query
}

// setPageQuery writes the four cursor-pagination parameters the listings share.
//
// Every one is omitted when unset rather than sent as a zero value. That is not
// only tidiness: the server validates order against ^(asc|desc)$ and limit
// against 1..1000, so sending Go's "" or 0 would earn a 422 for a caller who
// simply did not choose.
func setPageQuery(query url.Values, limit int, after, before string, order SortOrder) {
	if limit != 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	for name, value := range map[string]string{
		"after":  after,
		"before": before,
		"order":  string(order),
	} {
		if value != "" {
			query.Set(name, value)
		}
	}
}

// ListAgents returns one page of the agents this caller may start a session
// with, newest first unless [ListAgentsOptions.Order] says otherwise.
//
// The listing is not only the built-in agents, despite the route's name. It is
// every agent not scoped to a single session, which covers the ones an operator
// installed alongside the ones that ship with the server;
// [AgentObject.Builtin] tells them apart. Agents created for one session are
// never listed.
//
// This is the supported way to turn an agent name into the id
// [SessionCreateRequest.AgentID] wants. There is no lookup-by-name route, so
// that means paging until the name matches — worth caching, since an id is
// stable and a listing is not free.

// maxListingPages bounds one listing walk.
//
// A cursor walk's termination is the server's to grant, and a server that keeps
// answering "more" — through a bug, a proxy rewriting cursors, or malice — turns a
// listing into an unbounded loop in the caller's process. The number is generous
// against any real listing and finite against that.
const maxListingPages = 10_000

// pageSeq walks a cursor-paged listing as one sequence.
//
// A caller almost always wants every item, and the loop that gets there is the
// same four lines every time: read a page, yield its items, stop, carry the cursor
// forward. Getting it wrong is quiet, so this owns it.
//
// Three stops, because the server decides two of them and cannot be trusted with
// the third. It stops when the server says there is no more; when the cursor comes
// back empty, which a listing that reports more while returning nothing would
// otherwise loop on; and when a cursor repeats or the page count reaches
// [maxListingPages], which is what makes the walk end whatever the server does.
//
// The sequence starts no goroutine, so abandoning the range stops the walk and
// issues no further request. cursor is declared inside the closure, so a second
// range restarts from the first page rather than resuming a half-consumed walk.
func pageSeq[T any](ctx context.Context, fetch func(context.Context, string) (*Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var cursor string
		seen := make(map[string]bool)

		for pages := 0; ; pages++ {
			if pages >= maxListingPages {
				var zero T
				yield(zero, fmt.Errorf("%w: stopped after %d pages",
					ErrListingUnbounded, maxListingPages))
				return
			}

			page, err := fetch(ctx, cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Data {
				if !yield(item, nil) {
					return
				}
			}
			if !page.HasMore || page.LastID == "" {
				return
			}
			if seen[page.LastID] {
				// The server advanced nothing. Following it again reads the same
				// page forever, so stop and say which cursor repeated.
				var zero T
				yield(zero, fmt.Errorf("%w: cursor %q repeated after %d pages",
					ErrListingUnbounded, page.LastID, pages+1))
				return
			}
			seen[page.LastID] = true
			cursor = page.LastID
		}
	}
}

// gateFetch bounds the requests a paged listing issues, not the listing itself.
//
// The token is held for one fetch and released before the next. Holding it across
// a whole drain makes a concurrency bound describe listings, and a listing that
// pages many times then keeps a slot the rest of the work needs: a branch whose
// levels are sequential round trips waits for an unrelated listing to finish
// before it issues its first request.
//
// Acquisition honours ctx, so a cancelled walk stops waiting for a slot rather
// than for the walk to drain.
func gateFetch[T any](
	tokens chan struct{},
	fetch func(context.Context, string) (*Page[T], error),
) func(context.Context, string) (*Page[T], error) {
	return func(ctx context.Context, cursor string) (*Page[T], error) {
		select {
		case tokens <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-tokens }()
		return fetch(ctx, cursor)
	}
}

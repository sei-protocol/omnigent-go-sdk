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
// Paging is by opaque cursor, not offset: the next request carries the previous
// page's [Page.LastID] as its After while [Page.HasMore] is true.
//
// No exported call returns a single Page, so this is not a loop a caller writes.
// Every listing on this client is an [iter.Seq2] that runs it — Sessions().List and
// its siblings — and the shape is here because the stop conditions are the part that
// is easy to get wrong, and worth reading before trusting a listing:
//
//	for pages := 0; ; pages++ {
//		page, err := fetch(ctx, cursor) // one request, one Page
//		if err != nil {
//			return err
//		}
//		for _, item := range page.Data {
//			// ...
//		}
//		// Only the server saying there is no more is a finished listing. A page
//		// that claims more while returning nothing, or with no cursor to follow,
//		// is a listing cut short, and breaking quietly on either makes a
//		// truncated answer look like the whole set.
//		if !page.HasMore {
//			break
//		}
//		if len(page.Data) == 0 || page.LastID == "" {
//			return fmt.Errorf("listing cut short after %d pages", pages+1)
//		}
//		cursor = page.LastID
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
	// A false value is the only finished listing. A full page does not imply
	// another one: the server asks for one row more than the limit and reports
	// whether it got it, so a true value beside an empty Data is not something this
	// server produces.
	//
	// Do not treat that shape as an end anyway. It means the listing was cut short,
	// and a caller that breaks quietly on it cannot tell a truncated answer from a
	// complete one — while a caller that trusts this field alone re-reads the first
	// page for as long as a proxy or a future server keeps saying yes. [pageSeq]
	// yields [ErrListingUnbounded] instead, and so should a hand-rolled loop.
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

// SessionItem is one item from [Sessions.ListItems].
//
// It is untyped because the route sends the server's flatten-for-API shape rather
// than the [ConversationItem] a snapshot carries. Both carry id, response_id,
// type, status and created_at; the difference is the payload. A snapshot nests it
// under data, while this route spreads the payload's own fields — role and content
// on a message, name and arguments on a function call — onto the same object,
// leaving absent optional fields out. openapi.json declares this page's elements
// untyped, so there is nothing generated to decode them into either.
//
// A defined type rather than an alias, so the common fields are methods instead of
// six package-level names. It still assigns both ways with a plain map[string]any,
// including the same flat shape on the stream, [OutputItemDoneEvent.Item], because
// that side is unnamed. The one thing it does not do is match a type switch: boxed
// in an any, a SessionItem falls through case map[string]any. Convert explicitly
// where that matters.
//
// [Sessions.ListItems] yields these. The methods read the fields every item carries;
// the payload's own fields are read directly, keyed by what the item's type declares.
//
// The methods exist because the alternative at each call site is a double assertion —
// fetch, then assert — repeated per field, where a missing key and a key holding the
// wrong type both have to end up as the same zero value anyway. The five string ones
// read through the same helper the stream's item handling uses.
//
// Reading a payload field means knowing how encoding/json stored it, and the trap is
// numbers: every JSON number in a map[string]any is a float64, so an assertion to
// int or int64 does not fail loudly, it yields zero. [SessionItem.CreatedAt] exists because
// created_at is the common field that trap applies to.
//
// float64 is also why this shape is approximate above 2^53: an integer larger than
// that is rounded on decode, and re-marshalling one writes the rounded value back.
// The payload fields this API is known to carry do not reach that magnitude —
// arguments and output ride as JSON strings — but nothing in the type enforces it,
// so a caller re-marshalling an item, or reading a field that carries an exact large
// integer, should decode that field itself from the raw response.
type SessionItem map[string]any

// ID reports the item's own identifier. Empty when the field is absent or holds
// anything but a string.
func (i SessionItem) ID() string { return itemString(i, "id") }

// ResponseID reports which response this item belongs to. Empty when the field
// is absent, null, or holds anything but a string -- this route's elements are
// declared untyped, so which of those the server sends is not something the SDK can
// promise.
func (i SessionItem) ResponseID() string { return itemString(i, "response_id") }

// Type reports the item's kind, e.g. "message" or "function_call". It decides which
// payload fields the item carries.
func (i SessionItem) Type() string { return itemString(i, "type") }

// Status reports the item's lifecycle state, e.g. "completed".
func (i SessionItem) Status() string { return itemString(i, "status") }

// CreatedAt reports when the server recorded the item, in Unix seconds. Zero
// when the field is absent or holds anything but a number.
//
// Reads through float64 because that is what encoding/json stores every JSON number
// as in a map[string]any. Asserting to int64 directly compiles, never panics on the
// comma-ok form, and yields zero for every item — a silent wrong answer, which is
// the shape this listing's accessors exist to prevent.
func (i SessionItem) CreatedAt() int64 {
	seconds, ok := i["created_at"].(float64)
	if !ok {
		return 0
	}
	return int64(seconds)
}

// CreatedBy reports the human actor who authored the item, and is empty for one
// the agent, a tool, or the system produced.
//
// Empty covers absent, null, and a non-string alike. The vendored description marks
// created_by optional and nullable, so a non-human item may arrive either way and
// the difference carries no meaning; what separates a client-authored item from the
// agent's own is whether this answers empty, which is a reason to reject one as a
// turn's reply.
func (i SessionItem) CreatedBy() string { return itemString(i, "created_by") }

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
// One stop ends the walk and four refuse it. It ends when the server says there is
// no more. It refuses — yielding [ErrListingUnbounded] — when a page claims more
// while returning nothing, when a page claims more with no cursor to follow, and
// when a cursor repeats or the page count reaches [maxListingPages].
//
// The split is the point. A stop the caller cannot see is indistinguishable from a
// complete listing, so anything the walk did not reach reads as absent rather than
// unseen. Every refusal is a shape a correct server does not produce, so a caller
// reaching one has been handed a partial answer and needs to know.
//
// The empty-page refusal is checked before the cursor on purpose, because the two
// arrive together: [Page.LastID] is empty when Data is, so a cursor check placed
// first would absorb the empty page and end the walk quietly. A proxy rewriting
// cursors produces the same emptiness with a fresh cursor each time, which clears
// the repeat guard too and would otherwise leave only the page cap.
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
			// The server saying there is no more is the only quiet end. Every other
			// reason to stop means the listing was cut short, and a quiet stop is
			// indistinguishable from a complete one -- a caller acting on the
			// difference, reclaiming every session it can find say, would treat a
			// truncated answer as the whole set and report success over what it
			// never saw.
			if !page.HasMore {
				return
			}
			if len(page.Data) == 0 {
				// More promised, nothing delivered. This is the shape [Page.LastID]
				// describes -- an empty page carries an empty cursor -- so it is
				// checked before the cursor, which would otherwise absorb it.
				var zero T
				yield(zero, fmt.Errorf("%w: page %d claimed more while returning nothing",
					ErrListingUnbounded, pages+1))
				return
			}
			if page.LastID == "" {
				// Rows, more promised, and nothing to ask for them with.
				var zero T
				yield(zero, fmt.Errorf("%w: page %d claimed more with no cursor to follow",
					ErrListingUnbounded, pages+1))
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

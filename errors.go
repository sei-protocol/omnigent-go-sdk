package omnigent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sentinels for the failure classes the server actually distinguishes. Match
// them with errors.Is against any error this package returns; reach the details
// with errors.As and [APIError].
//
// The set mirrors the server's own error-code table rather than inventing
// per-resource variants: a 404 is a 404 whatever endpoint produced it.
var (
	// ErrInvalidArgument is the one sentinel here that is not a server
	// response: this package rejected an argument before sending anything. An
	// empty session id, a create with no agent id, an input with no type, an
	// option handed a nil or empty value. Retrying cannot help — fix the call.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrInvalidInput is a 400: the request was understood and rejected.
	ErrInvalidInput = errors.New("invalid input")

	// ErrUnauthorized is a 401: no identity was presented, or the one presented
	// was not accepted. Supply credentials with one of the auth options.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrForbidden is a 403: the caller is known but lacks the required access
	// level on the session. Note the asymmetry — reading a session needs less
	// than sending to it, which needs less than deleting it.
	ErrForbidden = errors.New("forbidden")

	// ErrNotFound is a 404, which does NOT prove the session is absent. The
	// server also answers 404 when the caller has no access at all, so as not
	// to leak session existence. Treat it as "absent or invisible to you"; do
	// not paper over a permissions misconfiguration by retrying.
	ErrNotFound = errors.New("not found")

	// ErrConflict is a 409: the resource already exists, or the request
	// conflicts with current state.
	ErrConflict = errors.New("conflict")

	// ErrHarnessNotConfigured is a 412: the session's agent has no usable
	// harness configured server-side. Retrying will not help.
	ErrHarnessNotConfigured = errors.New("harness not configured")

	// ErrValidation is a 422: the request body did not match the schema. The
	// per-field detail is available from [APIError.ValidationErrors].
	ErrValidation = errors.New("request validation failed")

	// ErrUnavailable is a 503: no runner is available, or the one bound cannot
	// serve the request. Usually transient.
	ErrUnavailable = errors.New("service unavailable")

	// ErrServer matches any 5xx, including the ones with their own sentinel.
	ErrServer = errors.New("server error")

	// ErrStreamInterrupted reports that a stream's body ended without the
	// server's terminal sentinel. This is the normal failure mode, not an edge
	// case: it covers a dropped transport, a subscriber-queue overflow (which
	// deliberately omits the sentinel to signal loss), and deployment-level
	// caps on stream duration. Events published while nobody was subscribed are
	// gone, so recover with a snapshot rather than by retrying blind.
	ErrStreamInterrupted = errors.New("session stream ended without its terminal sentinel")

	// ErrStreamIdle reports that no frame arrived within the idle timeout. The
	// server keeps a 15-second heartbeat precisely so this means a dead
	// transport rather than a slow agent — a turn may legitimately produce no
	// output for minutes while a tool runs, and the heartbeat continues.
	// Recover exactly as for [ErrStreamInterrupted].
	ErrStreamIdle = errors.New("session stream idle past its timeout")

	// ErrStreamProtocol reports a frame this package could not parse: a data
	// payload that is not a JSON object, or one carrying no discriminator.
	ErrStreamProtocol = errors.New("malformed session stream frame")

	// ErrStreamFrameTooLarge reports a frame past the size this package will
	// accumulate. It is a kind of [ErrStreamProtocol] and matches that too: a
	// well-formed frame ends, and one that does not is either a broken server or
	// a hostile one. Recover exactly as for [ErrStreamInterrupted].
	ErrStreamFrameTooLarge = fmt.Errorf("%w: frame exceeds the client's size limit", ErrStreamProtocol)

	// ErrListingUnbounded reports a paged listing that never reached an end: it
	// returned a cursor it had already returned, or kept reporting more past the
	// page ceiling.
	//
	// The server decides when a listing ends, so a walk cannot rely on it. This is
	// what the walk raises instead of continuing, and retrying will not help until
	// the server's paging is fixed.
	ErrListingUnbounded = errors.New("listing did not reach an end")

	// ErrTruncated reports that a response was larger than the bound the caller
	// supplied, so what was written is a prefix rather than the whole thing.
	//
	// Not [ErrInvalidArgument]: the caller's arguments were accepted and the
	// request was made. The server chose the length. Retry with a larger bound, or
	// treat the prefix as all that was wanted.
	ErrTruncated = errors.New("response exceeds the caller's bound")

	// ErrUnsafeRedirect reports a redirect this package would not follow: one
	// leaving the host the base URL named, one stepping down from https to
	// plain http, one rewriting a write as a read, or a chain that never
	// arrived anywhere within the hop limit.
	//
	// It is not a server error and retrying will not help. For the first two it
	// means the server — or something in front of it — answered with a location
	// this client will not carry the caller's credentials to; for the third, that
	// following it would have reported a dropped write as a success; for the
	// fourth, that the location chain is looping. Fix the base URL, or the proxy
	// in front of the server. The package overview, under Redirects, has the
	// reasoning and what Go's own rule does instead.
	ErrUnsafeRedirect = errors.New("refused to follow an unsafe redirect")

	// ErrToolCallDuplicated reports a call the wire delivered twice, which this
	// client ran once.
	//
	// Not a failure: the answer went back for the first delivery. Reported because a
	// silent drop is indistinguishable from a call this client never saw, and a
	// caller counting tool use would be quietly wrong.
	ErrToolCallDuplicated = errors.New("the call was already run")

	// ErrToolCallBudget means one turn asked this client to run more tools than
	// [ChatOptions.MaxToolCalls] allows.
	//
	// A legitimate turn does not reach it. A server that issues a fresh call id
	// per ask does, which is the case the budget exists for.
	ErrToolCallBudget = errors.New("the turn exceeded its tool-call budget")

	// ErrInputDenied means the server refused an input synchronously, saying why.
	//
	// Distinct from a transport failure: the send reached the server and the
	// server answered. A denied prompt is the case worth naming, because the
	// turn it would have started never begins, so nothing on the stream can end
	// it and the read otherwise runs to the caller's deadline.
	ErrInputDenied = errors.New("the server denied the input")

	// ErrHookPanicked reports that a caller-supplied hook panicked.
	//
	// The turn continues, and an approval hook that panics declines. Reported
	// because the server chooses the fields a hook reads, so it chooses the input
	// that trips one — and an unreported panic is a denial of service with no signal
	// where a caller looks.
	ErrHookPanicked = errors.New("a hook panicked")

	// ErrTurnAlreadyRead reports a second attempt to read one turn.
	//
	// Refused rather than repeated: reading again would post the prompt a second
	// time, and the server would answer both. A caller wanting two turns asks for
	// two.
	ErrTurnAlreadyRead = errors.New("this turn was already read")

	// ErrTurnIncomplete reports that the event stream ended before the turn did.
	//
	// The turn may still be running server-side. Reported rather than treated as an
	// end, because a caller reading the sequence as complete would take a partial
	// answer for the whole one.
	ErrTurnIncomplete = errors.New("the stream ended before the turn did")

	// ErrToolNotRegistered reports that the agent called a tool this client does
	// not have.
	//
	// The turn is not left parked on it: an output naming the mismatch is posted
	// anyway, because a server waiting for a call it will never receive reads as a
	// hung agent rather than as a missing tool.
	ErrToolNotRegistered = errors.New("no such tool is registered")

	// ErrToolFailed reports that a registered tool returned an error or panicked.
	//
	// Its output is posted as the error text, for the same reason: the turn is
	// parked on the call, so a failing tool still has to answer.
	ErrToolFailed = errors.New("the tool failed")

	// ErrTurnFailed reports that the server ended a turn without an answer.
	//
	// The turn reached the server and the server reported a failure, so the wrapped
	// message is the server's own reason. Distinct from a transport failure, which
	// says nothing about whether the turn ran.
	ErrTurnFailed = errors.New("the turn failed")

	// ErrTurnSuperseded reports that the session a turn was reading has been
	// replaced, and names the conversation that replaced it.
	//
	// Not followed automatically: a caller holding the old session id would keep
	// addressing a retired conversation, and which session to address next is the
	// caller's decision.
	ErrTurnSuperseded = errors.New("the session was superseded")

	// ErrRedirectNotFollowed reports a redirect this package could not follow,
	// as distinct from one it refused. An upload streams its body, so there is
	// nothing to replay at the new location and net/http hands the response back
	// rather than consulting the redirect policy.
	//
	// The location was on the server the base URL names, so nothing was sent
	// anywhere else and this is a configuration to fix rather than an attempt to
	// divert a credential: point the base URL at the route that serves the
	// upload. A location naming another server is [ErrUnsafeRedirect] instead.
	ErrRedirectNotFollowed = errors.New("could not follow a redirect")

	// ErrResponseTooLarge means a successful response's body exceeded what this
	// package will decode.
	//
	// The size is not a caller's choice, because the caller does not choose the
	// body. A server the caller cannot see is the only party writing it, and a
	// decode is the one place this package holds a whole response in memory at
	// once. See maxResponseBytes.
	ErrResponseTooLarge = errors.New("the response is too large to decode")
)

// sanitizeForError makes a server-chosen string safe to render into an error.
//
// An error string is the one thing a caller reliably logs, and a log line is a
// record other tools parse. Raw control bytes let the server forge a second line
// or drive a terminal; an unbounded string lets it flood the log. Both are the
// server's choice, not the caller's, so neither reaches the rendered message.
//
// Replaces every C0 and C1 control with a space, collapses runs, and caps the
// result. Invalid UTF-8 becomes the replacement rune rather than raw bytes.
func sanitizeForError(s string, max int) string {
	var b strings.Builder
	b.Grow(min(len(s), max))
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			r = '\uFFFD'
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// One space for any run of controls, so a forged newline cannot start
			// a line and a stripped run cannot join two words.
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		space = false
		if b.Len() >= max {
			return strings.TrimSpace(b.String()) + "..."
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// maxErrorFieldRunes bounds one server-supplied field in a rendered error. The
// body cap is 64 KiB, so without this a single title fills a log line with it.
const maxErrorFieldRunes = 200

// maxRequestIDRunes is tighter than [maxErrorFieldRunes] because a request id is
// an opaque handle an operator pastes into a query, not prose. The server's own
// ids are short, so a long one means something other than this API answered.
const maxRequestIDRunes = 80

// maxAgentNameRunes bounds one agent name inside a resolve failure. The listing
// route is unbounded and heterogeneous, so both the number of names and the
// length of each are the server's choice; [Sessions.ResolveAgent] caps the count
// and this caps each entry.
const maxAgentNameRunes = 60

// maxToolNameRunes bounds a tool name inside an error. The name comes from the
// model by way of the server, so its length is not the caller's choice.
const maxToolNameRunes = 60

// Error is the interface every server-response error in this package satisfies.
//
// It exists so a caller can branch on the status without naming a concrete type:
//
//	var apiErr omnigent.Error
//	if errors.As(err, &apiErr) && apiErr.Status() == http.StatusTooManyRequests {
//		// back off
//	}
//
// The sentinels in this file remain the better match for a class of failure this API
// actually distinguishes. Reach for Error when the status itself is what matters,
// and for [APIError] when the server's own code, title or remediation is.
type Error interface {
	error

	// Status returns the HTTP status the server answered with.
	Status() int
}

// Compile-time proof that the concrete error satisfies the interface, so a
// change to either one fails here rather than at a caller.
var _ Error = (*APIError)(nil)

// maxErrorBodyBytes caps how much of a failed response this package retains or
// drains.
const maxErrorBodyBytes = 64 << 10

// maxResponseBytes caps a 2xx body this package decodes into a value.
//
// maxErrorBodyBytes covers the failure path and the pooling drain, and neither
// reaches this one: a decode reads until the JSON value ends, so without a bound
// the ceiling is whatever the unary timeout allows a server to send. Some types
// this package decodes carry an open-ended map, which turns a large body into a
// larger heap.
//
// The number is the largest legitimate response with room to spare, not a tuned
// limit. A session snapshot is the biggest one: the server returns its newest
// 100 items, so a transcript of large messages is the shape that grows. Files go
// through Download, which takes its own bound from the caller.
const maxResponseBytes = 32 << 20

// APIError is a non-2xx response from the server.
//
// The server speaks two error envelopes and neither is guaranteed, so read the
// fields defensively: Code and Message come from the structured envelope,
// Detail from FastAPI's own, and Body always holds what actually arrived.
//
// # What this does not put in a log line
//
// [APIError.Error] renders the status, the error code and message, the title
// when the server supplied one, and RequestID — what a bug report needs. It
// never renders Body. A non-2xx body need not come from this API at all: an auth
// proxy answering 302-to-login or 502 can put a CSRF token, a signed state
// parameter or an echoed request header in it, and Error's return value is the
// one string that reliably reaches a log aggregator. Body is still here for the
// caller who needs to see it, and callers who log it choose to.
//
// Code, Message, Title, Cause and Remediation are parsed out of that same body,
// so they carry the same caveat one step weaker: they are populated only when the
// body matched an envelope this API defines, which a foreign responder usually
// does not produce, and what reaches the log is then named strings rather than
// opaque bytes. A proxy that happens to answer in the same shape is rendered —
// bounded to those fields.
//
// Header is the response's headers with the ones that carry a credential by
// specification removed: Set-Cookie and Set-Cookie2, which mint a client
// credential, and the Authorization pair, which has no business in a response.
// A deployment's own bespoke secret header is not something this package can
// enumerate, so treat Header as untrusted for logging too.
type APIError struct {
	// StatusCode is the HTTP status. It, not the message text, is what the
	// sentinels are matched on.
	StatusCode int

	// Code is the server's own error code — "not_found", "runner_unavailable",
	// and so on — from a {"error": {"code", "message"}} envelope. Empty when
	// the response used FastAPI's {"detail": ...} shape or was not JSON.
	Code string

	// Message is the human-readable message from either envelope. May be empty.
	Message string

	// Title is a short headline naming what went wrong, e.g. "Claude Code can't
	// run as root". The server sets it when it recognised the failure, so a
	// caller can show it instead of the raw Code. Empty when absent.
	Title string

	// Cause is a sentence or two explaining why the request failed. It is paired
	// with Title. Empty when absent.
	Cause string

	// Remediation is a concrete next step, sometimes a command to run. Empty
	// when the server has no single clear fix to name.
	Remediation string

	// Detail is the raw value of a {"detail": ...} envelope. For a 422 it is a
	// list; see [APIError.ValidationErrors]. Nil when absent.
	Detail json.RawMessage

	// Body is the response body, truncated to a bounded size. Retained because
	// an auth proxy or gateway ahead of the server may answer with something
	// that is not JSON at all, and that is exactly why [APIError.Error] does not
	// render it: what a proxy puts in a body is not this API's, and may be a
	// credential. Treat it as untrusted bytes to be looked at, not logged.
	Body []byte

	// ContentType is the response's Content-Type header, useful for telling a
	// server error apart from a proxy's HTML interstitial. Shorthand for
	// Header.Get("Content-Type").
	ContentType string

	// RequestID is the server's X-Request-Id, which its middleware stamps on
	// every response and logs alongside the failure. Quote it when reporting a
	// server-side problem: it is the only handle that ties this error to the
	// server's own record of it. Empty if the response never reached the
	// server's middleware — a proxy or gateway error, say.
	RequestID string

	// Header is the response's headers, less the ones that carry a credential:
	// see this type's doc. It is here for the correlation and rate-limit headers
	// a deployment may add beyond the two this type documents. Nil is possible; use
	// Header.Get, which tolerates it.
	Header http.Header
}

// Status returns the HTTP status, so an *APIError satisfies [Error].
func (e *APIError) Status() int { return e.StatusCode }

// Error implements error. It appends the request id when the server supplied
// one, so a copied-out error message is enough to find the server-side record.
//
// Every field it renders is the server's choice, so every one goes through
// [sanitizeForError]. A field added here needs the same treatment.
//
// It renders no part of Body. When the response carried no structured message —
// the proxy-interstitial case — it says how much body there was and what type
// it claimed, which is enough to tell "the API rejected this" from "something
// else answered" without copying bytes of unknown provenance into a log.
func (e *APIError) Error() string {
	label := strconv.Itoa(e.StatusCode)
	if text := http.StatusText(e.StatusCode); text != "" {
		label += " " + text
	}
	// Title is the server's own headline for a failure it recognised, so it
	// leads when present: it says more than the code and is shorter than Cause.
	headline := sanitizeForError(e.Code, maxErrorFieldRunes)
	if e.Title != "" {
		headline = sanitizeForError(e.Title, maxErrorFieldRunes)
	}
	message := sanitizeForError(e.Message, maxErrorFieldRunes)
	var detail string
	switch {
	case headline != "" && message != "":
		detail = fmt.Sprintf("%s: %s: %s", label, headline, message)
	case message != "":
		detail = fmt.Sprintf("%s: %s", label, message)
	case headline != "":
		detail = fmt.Sprintf("%s: %s", label, headline)
	case len(e.Body) > 0:
		kind := e.ContentType
		if kind == "" {
			kind = "untyped"
		}
		detail = fmt.Sprintf("%s: no error envelope, %d-byte %s body withheld (see APIError.Body)",
			label, len(e.Body), kind)
	default:
		detail = label
	}
	if e.RequestID != "" {
		detail += fmt.Sprintf(" (request id %s)", sanitizeForError(e.RequestID, maxRequestIDRunes))
	}
	return "omnigent: " + detail
}

// Is reports whether this error matches one of the package sentinels, so that
// errors.Is(err, ErrNotFound) works on a wrapped *APIError. A 503 matches both
// [ErrUnavailable] and the broader [ErrServer].
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrInvalidInput:
		return e.StatusCode == http.StatusBadRequest
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrForbidden:
		return e.StatusCode == http.StatusForbidden
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	case ErrHarnessNotConfigured:
		return e.StatusCode == http.StatusPreconditionFailed
	case ErrValidation:
		return e.StatusCode == http.StatusUnprocessableEntity
	case ErrUnavailable:
		return e.StatusCode == http.StatusServiceUnavailable
	case ErrServer:
		return e.StatusCode >= 500
	}
	return false
}

// ValidationErrors decodes a 422's per-field detail. It returns nil, nil when
// the error carries no detail list, so a caller can ask unconditionally.
func (e *APIError) ValidationErrors() ([]ValidationError, error) {
	if len(e.Detail) == 0 {
		return nil, nil
	}
	var details []ValidationError
	if err := json.Unmarshal(e.Detail, &details); err != nil {
		return nil, fmt.Errorf("decode validation detail: %w", err)
	}
	return details, nil
}

// credentialHeaders are the response headers that carry a credential by
// specification. Set-Cookie and Set-Cookie2 mint one for the client's next
// request; the Authorization pair belongs on a request and never on a response,
// so a response carrying one is either a misconfigured proxy or a reflection
// attack, and either way it is not something to retain.
var credentialHeaders = []string{
	"Set-Cookie",
	"Set-Cookie2",
	"Authorization",
	"Proxy-Authorization",
}

// newAPIError reads resp's body and classifies it into an *APIError.
func newAPIError(resp *http.Response) *APIError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	header := resp.Header.Clone()
	for _, name := range credentialHeaders {
		header.Del(name)
	}
	apiErr := &APIError{
		StatusCode:  resp.StatusCode,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		RequestID:   resp.Header.Get("X-Request-Id"),
		Header:      header,
	}

	// Two envelopes reach a client. The server's own errors are converted to
	// {"error": {"code", "message"}}; FastAPI's HTTPException and validation
	// failures pass through as {"detail": ...} because no handler is registered
	// for them. A response may also be neither, so failing to decode is fine.
	var envelope struct {
		Error *struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			Title       string `json:"title"`
			Cause       string `json:"cause"`
			Remediation string `json:"remediation"`
		} `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return apiErr
	}
	if envelope.Error != nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.Title = envelope.Error.Title
		apiErr.Cause = envelope.Error.Cause
		apiErr.Remediation = envelope.Error.Remediation
	}
	apiErr.Detail = envelope.Detail
	if apiErr.Message == "" {
		// A plain-string detail is the common HTTPException shape; a list one
		// (422) stays in Detail for ValidationErrors to interpret.
		var text string
		if json.Unmarshal(envelope.Detail, &text) == nil {
			apiErr.Message = text
		}
	}
	return apiErr
}

// ValidationError is one entry from a 422's per-field detail list.
type ValidationError struct {
	// Loc is the path to the offending field. Its elements are strings and
	// integers mixed, because a path crosses object keys and array indices, so
	// it is []any rather than a generated wrapper type per position.
	Loc []any `json:"loc"`

	// Msg is the human-readable problem with that field.
	Msg string `json:"msg"`

	// Type is the validator's own name for the failure, e.g. "string_type".
	Type string `json:"type"`

	// Ctx carries validator-specific context. Nil when absent.
	Ctx map[string]any `json:"ctx,omitempty"`

	// Input is the value that failed validation. Nil when absent.
	Input any `json:"input,omitempty"`
}

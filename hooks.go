package omnigent

import "fmt"

// StreamHooks observes a turn's lifecycle without reading raw events.
//
// Every field is optional; a nil hook is skipped. Hooks run on the caller's own
// goroutine, in event order — this package starts none — so a hook that blocks
// holds the turn up. That is what makes them suitable for a progress display and
// not for work of their own.
//
// Every hook but one only observes. [StreamHooks.OnElicitation] is the exception:
// its answer authorises a pending tool to run. The rest cannot change the turn —
// blocks are what a caller renders and [ToolRegistry] is what a caller runs, so
// these are for the side effects that belong to neither, such as a spinner or a
// metric.
type StreamHooks struct {
	// OnResponseStart fires once per response, when the server announces it.
	OnResponseStart func(ResponseStartCtx)

	// OnResponseEnd fires when a response reaches a terminal state, which is once
	// per turn: a tool loop's passes all report under the one response.
	OnResponseEnd func(ResponseEndCtx)

	// OnToolCallStart fires when the agent calls a tool, server-run or client-run.
	OnToolCallStart func(ToolCallStartCtx)

	// OnToolCallEnd fires when that call's output arrives.
	OnToolCallEnd func(ToolCallEndCtx)

	// OnReasoningStart fires when the server reports reasoning beginning.
	//
	// There is deliberately no matching end hook. A section closes when text starts
	// or the response ends, and its accumulated text lives in [BlockStream]'s fold;
	// a second copy of that state here would drift from the first. Reasoning
	// completion arrives on the block sequence as [ReasoningBlock] or the last
	// [ReasoningChunk], which is where a caller that wants the text reads it.
	//
	// A caller driving a spinner keys the stop on the next block of another kind
	// rather than on a hook that would have to guess the same thing.
	OnReasoningStart func(ReasoningStartCtx)

	// OnRetry fires when the server reports it is retrying.
	OnRetry func(RetryCtx)

	// OnServerError fires when the server reports an error mid-response. It is not
	// the end of the turn; a terminal response is.
	OnServerError func(ServerErrorCtx)

	// OnFileOutput fires when the agent produces a file.
	OnFileOutput func(FileOutputCtx)

	// OnElicitation decides an approval request the server raised.
	//
	// Return true to accept and false to decline. A nil hook declines, and so does
	// a hook that panics. That is this package's own behaviour and not a policy: it
	// cannot know what a caller would approve, and accepting authorises the pending
	// tool to run with the session owner's execution identity — which is not
	// necessarily the approver's. See [ElicitationAccept] for what the server
	// requires of an accept, and [ElicitationCtx] for what the request carries.
	OnElicitation func(ElicitationCtx) bool
}

// ResponseStartCtx describes a response beginning.
type ResponseStartCtx struct {
	ResponseID string
	Model      string
}

// ResponseEndCtx describes a response reaching a terminal state.
type ResponseEndCtx struct {
	ResponseID string
	Status     string
}

// ToolCallStartCtx describes a tool call the agent made.
type ToolCallStartCtx struct {
	Name      string
	CallID    string
	AgentName string
	Arguments map[string]any

	// ExecutedBy is "server" or "client".
	ExecutedBy string
}

// ToolCallEndCtx describes a tool call's output arriving.
type ToolCallEndCtx struct {
	Name   string
	CallID string
	Output string

	// Err is set when this client ran the tool and it failed. Nil for a
	// server-executed call, whose failure arrives as its output.
	Err error
}

// ReasoningStartCtx describes a reasoning section beginning.
type ReasoningStartCtx struct {
	ResponseID string
}

// RetryCtx describes a retry the server reported.
type RetryCtx struct {
	Source      string
	Attempt     int
	MaxAttempts int
}

// ServerErrorCtx describes an error the server reported mid-response.
type ServerErrorCtx struct {
	Message string
	Source  string
	Code    string
}

// FileOutputCtx describes a file the agent produced.
type FileOutputCtx struct {
	FileID   string
	Filename string
}

// ElicitationCtx describes an approval the server is waiting on.
//
// SessionID is the session the request names, not the session the caller is
// reading. They are normally the same; when a sub-agent raises the request they
// are not, and the verdict has to go to the one that asked.
type ElicitationCtx struct {
	SessionID     string
	ElicitationID string

	// Message is what the server is asking, e.g. a command awaiting approval.
	Message string

	// Phase and PolicyName are the server's own classification of the request,
	// empty when it sent none. A caller with a policy keys on these. Unlike
	// [ElicitationCtx.Extra] their type is enforced by the decoder, so a server
	// that sends the wrong one loses the whole event rather than the field.
	Phase      string
	PolicyName string

	// Extra carries the request's undeclared parameters, nil when it had none.
	//
	// The schema allows them and the server uses them. tool_name — the gated
	// tool's registered name, e.g. "Bash" — arrives here and nowhere else, and it
	// is the finest-grained thing the server attests about an approval, which
	// makes it what a policy allowlist keys on: a policy name covers every tool
	// that policy gates, so allowing one tool by policy name allows the rest.
	//
	// Read a value in two steps, because absent and present-but-not-a-string are
	// different answers and only one of them means the server said nothing:
	//
	//	raw, present := ctx.Extra["tool_name"]
	//	name, valid := raw.(string)
	//
	// A single assertion reports both as false and cannot tell them apart, which
	// is the whole reason this is a map rather than a named field.
	//
	// Treat either miss as unknown and fail closed. A declared field's type is
	// enforced by the decoder and a violation rejects the whole event; nothing
	// enforces a type here, so a policy that cannot see the tool it is gating
	// should decline rather than fall through to a broader rule. Present but not
	// a string is worth logging on its own: the server does not send that shape,
	// so something else did.
	//
	// A map rather than named fields because the set is the server's to change.
	// Naming one here would make the SDK's opinion about which extra matters
	// permanent, and would collapse those three answers into an empty string.
	Extra map[string]any

	// ContentPreview is a preview of what would run, empty when the server sent
	// none.
	ContentPreview string

	// Mode is how the server expects this to be answered: "form" for a schema the
	// caller fills in, "url" for an out-of-band flow at [ElicitationCtx.URL].
	// Empty when the server does not say.
	//
	// A decision made without it is a decision made blind, which is why upstream's
	// own client passes it. Note that a "form" answer needs values this package
	// cannot yet send: see [StreamHooks.OnElicitation].
	Mode string

	// URL is where a "url" mode flow happens, for example an OAuth authorize
	// endpoint. Empty in any other mode.
	//
	// Show it before approving. This package refuses an off-host redirect on the
	// unary path for the same reason a destination matters, and an approval that
	// hides the destination gives that up.
	URL string

	// RequestedSchema is the shape a "form" mode answer should take. Nil when the
	// server does not send one.
	RequestedSchema map[string]any

	// ResponseID is the response in flight when the server raised this, or empty
	// when none is. It is what correlates an approval with the work it gates.
	ResponseID string
}

// fire calls a hook when one is set, and turns a panic in it into an error.
//
// Written once so no call site repeats either the nil check or the recovery, and so
// adding a hook cannot forget them. A hook is caller code on the goroutine draining
// the turn: a panic escaping it ends the read, and one escaping OnToolCallStart
// pre-empts the output post and parks the session until its deadline.
//
// The panic is reported rather than swallowed. A server chooses the fields a hook
// reads, so it chooses the input that trips one, and an unreported panic is a denial
// of service with no signal where a caller looks.
func fire[T any](report func(error), name string, hook func(T), payload T) {
	if hook == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			report(fmt.Errorf("%w: hook %s: %s", ErrHookPanicked, name,
				sanitizeForError(fmt.Sprint(recovered), maxErrorFieldRunes)))
		}
	}()
	hook(payload)
}

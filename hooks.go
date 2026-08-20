package omnigent

// StreamHooks observes a turn's lifecycle without reading raw events.
//
// Every field is optional; a nil hook is skipped. Hooks are called from the
// goroutine draining the turn, in event order, so a hook that blocks holds the
// turn up — which is what makes them suitable for a progress display and not for
// work of their own.
//
// A hook cannot change the turn. Blocks are what a caller renders and
// [ToolRegistry] is what a caller runs; these are for the side effects that
// belong to neither, such as a spinner or a metric.
type StreamHooks struct {
	// OnResponseStart fires once per response, when the server announces it.
	OnResponseStart func(ResponseStartCtx)

	// OnResponseEnd fires when a response reaches a terminal state. A tool loop
	// reaches one per iteration, so this fires more than once per turn.
	OnResponseEnd func(ResponseEndCtx)

	// OnToolCallStart fires when the agent calls a tool, server-run or client-run.
	OnToolCallStart func(ToolCallStartCtx)

	// OnToolCallEnd fires when that call's output arrives.
	OnToolCallEnd func(ToolCallEndCtx)

	// OnReasoningStart and OnReasoningEnd bracket a reasoning section.
	OnReasoningStart func(ReasoningStartCtx)
	OnReasoningEnd   func(ReasoningEndCtx)

	// OnRetry fires when the server reports it is retrying.
	OnRetry func(RetryCtx)

	// OnServerError fires when the server reports an error mid-response. It is not
	// the end of the turn; a terminal response is.
	OnServerError func(ServerErrorCtx)

	// OnFileOutput fires when the agent produces a file.
	OnFileOutput func(FileOutputCtx)

	// OnElicitation decides an approval request the server raised.
	//
	// Return true to accept and false to decline. A nil hook declines, which is
	// this package's own default and not a policy: the SDK cannot know what a
	// caller would approve, and approving on a caller's behalf runs a tool under
	// their identity. See [ElicitationCtx].
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

// ReasoningEndCtx describes a reasoning section ending.
type ReasoningEndCtx struct {
	ResponseID string
	Text       string
	Summary    string
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
	// empty when it sent none. A caller with a policy keys on these.
	Phase      string
	PolicyName string

	// ContentPreview is a preview of what would run, empty when the server sent
	// none.
	ContentPreview string
}

// fire calls a hook when one is set. Written once so no caller repeats the nil
// check, and so adding a hook cannot forget it.
func fire[T any](hook func(T), ctx T) {
	if hook != nil {
		hook(ctx)
	}
}

package omnigent

import "time"

// Block is one rendered piece of a turn.
//
// Where an [Event] is what the server sent, a Block is what a caller draws. The
// stream carries deltas, duplicate reports and lifecycle noise that no renderer
// wants; [BlockStream] folds those into this smaller set, so a caller writes a
// switch over what it displays rather than over the wire protocol.
//
// Sealed like [Event], by a different mechanism: [Event]'s variants each declare
// their own methods, while these embed one unexported struct that supplies both the
// context and the marker. The effect is the same — an unexported method keeps the
// variants this package's. That stops
// an independent implementation, not a type embedding an exported variant, which
// promotes the marker with everything else — so the seal is a strong convention
// rather than a proof. Give a switch over the variants a default arm regardless:
// the set grows when the rendering does.
type Block interface {
	// Context reports which agent produced this block, how deep that agent sits, and
	// which tool-loop iteration the block came from.
	//
	// A single-agent caller ignores it. One rendering a sub-agent tree routes on it,
	// which is why it is on the interface rather than on each variant.
	Context() BlockContext

	isBlock()
}

// BlockContext is the metadata every [Block] carries.
type BlockContext struct {
	// Agent names the agent that produced the block, e.g. "coder.researcher". It is
	// the response's model, which the description defines as the agent that produced
	// it — so a root agent reports its own name rather than an empty one.
	//
	// Empty only before any response has been announced, which is where an
	// [ErrorBlock] or [RetryBlock] can arrive.
	Agent string

	// Depth is how deep that agent sits in the sub-agent tree, counted from the dots
	// in [BlockContext.Agent]: upstream derives it the same way.
	//
	// A convention rather than a declared field. The description does not say a dot
	// in an agent name means nesting, so a name that carries one for another reason
	// reports a depth it does not have. Route on Agent when that matters.
	Depth int

	// Iteration counts tool-loop passes within the turn. A loop that runs three
	// times produces blocks at iterations 0, 1 and 2.
	//
	// Named for the pass rather than the turn, because [Turn] is one prompt and
	// everything it produces — a different thing, and one word cannot be both.
	Iteration int

	// At is when the block was made, from a monotonic clock, so a renderer can
	// measure elapsed time without a wall-clock jump changing the answer.
	At time.Time
}

// blockCtx embeds into every variant, so each carries the context and satisfies
// the seal without restating either.
//
// Its field is unexported, so [Block.Context] is the only way to read a block's
// context and nothing can write one. An exported field would promote into every
// variant — settable from outside the package, and marshalled as an untagged "Ctx"
// key into a persisted transcript — neither of which go doc would show.
type blockCtx struct {
	ctx BlockContext
}

func (b blockCtx) Context() BlockContext { return b.ctx }
func (blockCtx) isBlock()                {}

// blockAt stamps a context with the current time, and is how this package builds
// every block. A caller reads a block; it does not construct one.
func blockAt(ctx BlockContext) blockCtx {
	ctx.At = time.Now()
	return blockCtx{ctx: ctx}
}

// ResponseStartBlock reports that a response has begun.
type ResponseStartBlock struct {
	blockCtx

	// Model is the agent model answering, e.g. "coder".
	Model string

	// ResponseID identifies the response this turn is reading.
	ResponseID string
}

// ToolExecution is one tool call paired with its result.
//
// Not a [Block]: it is what a [ToolGroup] holds. A batch of calls from one
// iteration renders as one group, so a caller draws the batch rather than
// discovering its shape from a run of separate blocks.
type ToolExecution struct {
	// Name is the tool called, e.g. "Read".
	Name string

	// Arguments are the call's decoded arguments. Empty when the server sent none
	// or when they did not decode.
	Arguments map[string]any

	// ArgsSummary is a one-line rendering of Arguments for a compact display, e.g.
	// "test.py". See [FormatToolArgsBrief].
	ArgsSummary string

	// CallID identifies the call, and is what pairs a result to it.
	CallID string

	// AgentName is the agent that invoked the tool.
	AgentName string

	// ExecutedBy is "server" or "client". A client-executed call is one this SDK
	// ran on the caller's behalf; see [ToolRegistry].
	ExecutedBy string

	// Output is the tool's output, or nil while the call is still outstanding. A
	// pointer rather than an empty string, because a tool that legitimately returns
	// nothing is not a tool still running.
	Output *string
}

// ToolGroup is a tool call, with room for the batch the wire does not yet group.
//
// The fold reports one call per group, because the server delivers one item per
// call and nothing in the item says which calls were issued together. A renderer
// should therefore expect a run of single-execution groups rather than one group
// per iteration, and range Executions rather than indexing it.
type ToolGroup struct {
	blockCtx

	// Executions are the calls in this group, in the order the server reported them.
	// One, until the wire distinguishes a batch.
	Executions []ToolExecution
}

// ToolResultBlock is one tool's result, emitted when the tool finishes.
//
// Carries the call's arguments as well as its output, so a caller rendering
// results alone still has the call's metadata and does not have to correlate
// against an earlier [ToolGroup].
type ToolResultBlock struct {
	blockCtx

	Name        string
	CallID      string
	AgentName   string
	Output      string
	Arguments   map[string]any
	ArgsSummary string
}

// NativeToolBlock is output from a provider-native tool, such as a web search or
// an MCP call, which the server runs and reports whole.
type NativeToolBlock struct {
	blockCtx

	// ToolType is the provider's own type, e.g. "web_search_call".
	ToolType string

	// Label is a short display name, e.g. "search".
	Label string

	// Data is the provider's payload, undecoded, because its shape is the
	// provider's and this package does not declare it.
	Data map[string]any
}

// TextChunk is a flushed piece of streamed assistant text.
type TextChunk struct {
	blockCtx

	Text string
}

// TextDone is the complete text of one text section.
//
// A caller rendering live output reads [TextChunk] and ignores this; one rendering
// a finished transcript reads this and ignores the chunks. Both arrive, so neither
// has to accumulate.
type TextDone struct {
	blockCtx

	// FullText is the accumulated text of the section.
	FullText string

	// HasCodeBlocks reports a fenced code block in FullText, so a renderer can
	// choose a monospace treatment without scanning again.
	HasCodeBlocks bool
}

// ReasoningStartBlock reports that reasoning has begun, which is a caller's cue to
// show a thinking indicator.
type ReasoningStartBlock struct {
	blockCtx
}

// ReasoningChunk is an incremental piece of reasoning text.
//
// Emitted while reasoning is still running, so a caller can show progress during
// a long tool-call window.
type ReasoningChunk struct {
	blockCtx

	Text string
}

// ReasoningBlock is one completed reasoning section.
//
// Emitted only when no [ReasoningChunk] was sent for that section, so a caller
// that renders both does not draw the same reasoning twice — once as progress and
// again as a summary card.
type ReasoningBlock struct {
	blockCtx

	ReasoningText string
	SummaryText   string
}

// ErrorBlock is an error the server reported during the response.
type ErrorBlock struct {
	blockCtx

	// Message is the server's free-form reason. Empty when the server sent
	// response.error without one, which is why Code is carried separately.
	Message string

	// Source is where the error came from, e.g. "llm".
	Source string

	// Code is the machine-readable classification, e.g. "llm_auth_failed". A
	// renderer falls back to it so a blank Message still shows the caller
	// something.
	Code string
}

// RetryBlock reports that the server is retrying.
type RetryBlock struct {
	blockCtx

	// Source is what is being retried, e.g. "tool".
	Source string

	Attempt     int
	MaxAttempts int
	Delay       time.Duration
}

// CompactionBlock reports that the conversation is being compacted.
type CompactionBlock struct {
	blockCtx
}

// FileBlock is a file the agent produced.
type FileBlock struct {
	blockCtx

	FileID string

	// Filename is the name the server recorded, or nil when it recorded none.
	Filename *string
}

// ResponseEndBlock reports that the response reached a terminal state.
//
// One arrives per response. A sequence spanning several turns therefore carries
// several, and a caller
// that wants the last one alone applies [SkipIntermediateEnds].
type ResponseEndBlock struct {
	blockCtx

	// Status is the terminal status, e.g. "completed" or "failed".
	Status string

	// Response is the response snapshot the terminal event carried.
	//
	// A pointer because the struct is large, not because it is optional: the four
	// terminal events declare it as a required value, so a block built from one
	// always has it. Nil only on a block a caller constructed itself.
	Response *ResponseObject
}

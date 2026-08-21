package omnigent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultTextFlushThreshold is how many buffered characters make a text chunk
// worth emitting.
//
// Small enough that a reader sees progress on a slow model, large enough that a
// renderer is not redrawing on every token. Flushing also waits for a word
// boundary, so the number is a floor rather than a target.
const defaultTextFlushThreshold = 30

// BlockStream folds a session's events into the blocks a caller renders.
//
// The wire is not a rendering model. Text arrives as deltas, reasoning as a
// second delta channel, a tool call and its result as two heterogeneous items,
// and a tool loop's passes all report under one response. This
// turns that into [Block] values a switch is worth writing over.
//
// A BlockStream holds configuration only. Every call to [BlockStream.Blocks]
// keeps its own state, so one value serves any number of concurrent turns.
type BlockStream struct {
	// TextFlushThreshold is the minimum buffered characters before a [TextChunk]
	// is emitted on a word boundary. Zero means [defaultTextFlushThreshold].
	//
	// Raise it for a renderer that redraws expensively; set it to 1 to forward
	// every delta.
	TextFlushThreshold int
}

// blockState is one turn's folding state.
//
// Held in a struct rather than in the closure's locals because the folding is
// several named steps that each touch a few fields, and a closure over twelve
// locals reads as one long function whatever its indentation.
type blockState struct {
	threshold int
	yield     func(Block, error) bool
	stopped   bool

	// ctx is the context stamped onto every block, carrying the agent and the
	// tool-loop iteration the blocks belong to.
	ctx BlockContext

	// startedResponses records which responses already produced a
	// [ResponseStartBlock]. The server announces a response on created, queued and
	// in_progress, and a caller wants one start.
	startedResponses map[string]bool

	// liveResponses are the responses announced and not yet terminal. More than one
	// means the stream is carrying interleaved responses, which is what
	// [blockState.attributable] answers.
	liveResponses map[string]bool

	// attributable records that every block so far can be credited to one agent.
	//
	// A text delta names no response, so the fold can only credit it to whichever
	// response started last. That is right while one response is live and wrong the
	// moment two are: a mirrored sub-agent's start would otherwise re-credit the
	// parent's own words to the child, and [OnlyAgent] would drop the parent's
	// answer rather than merely fail to find it. Once two are live the fold stops
	// crediting anything, because it cannot recover which is which.
	attributable bool

	inText string // accumulated, not yet flushed

	// fullText is everything this section has produced, in a builder rather than a
	// string: appending to a string copies the whole answer per delta, which is
	// quadratic in the answer's length and runs on the goroutine draining the
	// stream.
	fullText    strings.Builder
	inReasoning bool
	// The same reason fullText is a Builder: appending to a string copies the
	// whole section per delta. Measured before this shape, at 31 MiB of reasoning
	// across four frames: 37s of CPU against 27ms for the text path, and 1.4 GB of
	// allocation to fold 343 KB. reasoning is a byte slice rather than a Builder
	// because the line flush below slices it, which a Builder cannot do.
	reasoning     []byte          // accumulated reasoning, not yet flushed
	reasoningText strings.Builder // the section's full reasoning
	summaryText   strings.Builder
	// reasoningChunked records that this section streamed chunks, which suppresses
	// the closing [ReasoningBlock] so a renderer showing both does not draw the
	// same reasoning twice.
	reasoningChunked bool

	// executions keeps every call's metadata for the whole turn, because a result can
	// arrive long after its call and rendering it needs the original name and
	// arguments.
	executions map[string]*ToolExecution

	// seenCalls and seenResults suppress the second report of one call.
	//
	// Under the MCP path a call surfaces twice with the same call id — once as the
	// harness parses the tool-use block, once when it invokes the server handler —
	// and its result likewise. Both are the same call, so a caller sees one block.
	seenCalls   map[string]bool
	seenResults map[string]bool
}

// Blocks folds one event sequence into blocks.
//
// The sequence is the caller's: pair it with [Client.Stream] to render a live
// turn, or with a replayed slice to render a recorded one. An error passes
// through in place, so a caller reading blocks still sees a stream that failed.
func (bs *BlockStream) Blocks(events iter.Seq2[Event, error]) iter.Seq2[Block, error] {
	threshold := bs.TextFlushThreshold
	if threshold <= 0 {
		threshold = defaultTextFlushThreshold
	}
	return func(yield func(Block, error) bool) {
		state := &blockState{
			threshold:        threshold,
			yield:            yield,
			startedResponses: map[string]bool{},
			liveResponses:    map[string]bool{},
			attributable:     true,
			executions:       map[string]*ToolExecution{},
			seenCalls:        map[string]bool{},
			seenResults:      map[string]bool{},
		}
		for event, err := range events {
			if err != nil {
				if !state.emit(nil, err) {
					return
				}
				continue
			}
			state.fold(event)
			if state.stopped {
				return
			}
		}
		// A sequence can end without a terminal response — a dropped stream, or a
		// caller that stopped early. Whatever was accumulated is still the answer.
		state.closeText()
		state.closeReasoning()
	}
}

// emit passes one block to the caller and records a caller that stopped reading.
func (s *blockState) emit(block Block, err error) bool {
	if s.stopped {
		return false
	}
	if !s.yield(block, err) {
		s.stopped = true
		return false
	}
	return true
}

func (s *blockState) put(block Block) { s.emit(block, nil) }

// at stamps the current context with a fresh timestamp.
func (s *blockState) at() blockCtx { return blockAt(s.ctx) }

// fold routes one event to the step that handles it.
//
// A switch rather than a map, unlike [eventRegistry]: each branch reads different
// fields off a different type, so there is no uniform signature to key on, and
// nothing else reads this table.
func (s *blockState) fold(event Event) {
	switch e := event.(type) {
	case ResponseCreatedEvent:
		s.startResponse(e.Response)
	case QueuedEvent:
		s.startResponse(e.Response)
	case InProgressEvent:
		s.startResponse(e.Response)

	case ReasoningStartedEvent:
		s.beginReasoning()
	case ReasoningTextDeltaEvent:
		s.appendReasoning(e.Delta, false)
	case ReasoningSummaryTextDeltaEvent:
		s.appendReasoning(e.Delta, true)

	case OutputTextDeltaEvent:
		s.appendText(e.Delta)

	case OutputItemDoneEvent:
		s.foldItem(e.Item)

	case CompactionInProgressEvent:
		s.put(CompactionBlock{blockCtx: s.at()})

	case RetryEvent:
		s.put(RetryBlock{
			blockCtx:    s.at(),
			Source:      e.Source,
			Attempt:     e.Attempt,
			MaxAttempts: e.MaxAttempts,
			Delay:       time.Duration(e.DelaySeconds * float64(time.Second)),
		})

	case ErrorEvent:
		s.put(ErrorBlock{
			blockCtx: s.at(),
			Message:  e.Error.Message,
			Source:   e.Source,
			Code:     e.Error.Code,
		})

	case OutputFileDoneEvent:
		s.put(FileBlock{blockCtx: s.at(), FileID: e.FileID, Filename: e.Filename})

	case ResponseCompletedEvent:
		s.endResponse(e.Response.Status, &e.Response)
	case ResponseFailedEvent:
		s.endResponse(e.Response.Status, &e.Response)
	case IncompleteEvent:
		s.endResponse(e.Response.Status, &e.Response)
	case ResponseCancelledEvent:
		s.endResponse(e.Response.Status, &e.Response)
	}
}

// startResponse reports a response beginning, once.
func (s *blockState) startResponse(response ResponseObject) {
	if response.ID == "" || s.startedResponses[response.ID] {
		return
	}
	s.startedResponses[response.ID] = true
	s.liveResponses[response.ID] = true
	if len(s.liveResponses) > 1 {
		// Two responses live at once, so a later delta cannot be credited to either.
		// Crediting stops for the rest of the fold rather than guessing.
		s.attributable = false
	}
	// The agent and its depth come from the response's model, which is the only place
	// the wire names them.
	if s.attributable {
		s.ctx.Agent = response.Model
		s.ctx.Depth = strings.Count(response.Model, ".")
	} else {
		s.ctx.Agent = ""
		s.ctx.Depth = 0
	}
	// The start block names its own model regardless, because that one event does say
	// which response it belongs to.
	start := ResponseStartBlock{blockCtx: s.at(), Model: response.Model, ResponseID: response.ID}
	s.put(start)
}

// endResponse closes whatever is open and reports the response's terminal state.
//
// Text and reasoning are flushed first, so a caller reading in order sees the
// answer before the turn closes.
func (s *blockState) endResponse(status string, response *ResponseObject) {
	if response != nil {
		delete(s.liveResponses, response.ID)
	}
	s.closeText()
	s.closeReasoning()
	s.put(ResponseEndBlock{blockCtx: s.at(), Status: status, Response: response})
	// The next terminal response is a new tool-loop iteration.
	s.ctx.Iteration++
}

func (s *blockState) beginReasoning() {
	if s.inReasoning {
		return
	}
	s.inReasoning = true
	s.reasoningChunked = false
	s.put(ReasoningStartBlock{blockCtx: s.at()})
}

// appendReasoning buffers reasoning text and flushes it on a line boundary.
//
// Reasoning is flushed by line rather than by word: it arrives as prose a reader
// scans rather than reads, and a part-line chunk reads as noise.
func (s *blockState) appendReasoning(delta string, summary bool) {
	if delta == "" {
		return
	}
	if !s.inReasoning {
		s.beginReasoning()
	}
	if summary {
		s.summaryText.WriteString(delta)
	} else {
		s.reasoningText.WriteString(delta)
	}
	// Scan only what arrived. Rescanning the whole buffer per delta is the other
	// half of the quadratic cost, and it is the half a reasoning section with no
	// newlines pays in full: the buffer grows to the section's length and every
	// delta walks all of it.
	scanned := len(s.reasoning)
	s.reasoning = append(s.reasoning, delta...)
	for {
		line := bytes.IndexByte(s.reasoning[scanned:], '\n')
		if line < 0 {
			break
		}
		line += scanned
		text := string(s.reasoning[:line])
		s.reasoning = s.reasoning[line+1:]
		scanned = 0
		if strings.TrimSpace(text) == "" {
			continue
		}
		s.reasoningChunked = true
		s.put(ReasoningChunk{blockCtx: s.at(), Text: text})
	}
}

// closeReasoning ends an open reasoning section.
//
// The closing [ReasoningBlock] is suppressed when the section already streamed
// chunks, so a renderer drawing both does not show the same reasoning twice —
// once as it arrived, again as a summary.
func (s *blockState) closeReasoning() {
	if !s.inReasoning {
		return
	}
	if rest := strings.TrimSpace(string(s.reasoning)); rest != "" {
		s.reasoningChunked = true
		s.put(ReasoningChunk{blockCtx: s.at(), Text: rest})
	}
	if !s.reasoningChunked && (s.reasoningText.Len() > 0 || s.summaryText.Len() > 0) {
		s.put(ReasoningBlock{
			blockCtx:      s.at(),
			ReasoningText: s.reasoningText.String(),
			SummaryText:   s.summaryText.String(),
		})
	}
	s.inReasoning = false
	s.reasoning = s.reasoning[:0]
	s.reasoningText.Reset()
	s.summaryText.Reset()
	s.reasoningChunked = false
}

// appendText buffers assistant text and flushes it on a word boundary.
//
// Text starting is what ends a reasoning section: the model has moved from
// thinking to answering, and no event says so on its own.
func (s *blockState) appendText(delta string) {
	if delta == "" {
		return
	}
	s.closeReasoning()
	s.inText += delta
	s.fullText.WriteString(delta)
	s.flushTextChunks()
}

// flushTextChunks emits whole words once enough have buffered.
//
// Split at the last space rather than at the threshold, so a chunk never ends
// mid-word — which a renderer cannot un-draw once it has shown it.
func (s *blockState) flushTextChunks() {
	for utf8.RuneCountInString(s.inText) >= s.threshold {
		cut := strings.LastIndexAny(s.inText, " \n\t")
		if cut <= 0 {
			// One long unbroken run. Waiting for a boundary that may never come
			// would hold the whole answer back, so emit what there is.
			s.put(TextChunk{blockCtx: s.at(), Text: s.inText})
			s.inText = ""
			return
		}
		chunk := s.inText[:cut+1]
		s.inText = s.inText[cut+1:]
		s.put(TextChunk{blockCtx: s.at(), Text: chunk})
	}
}

// closeText flushes the remaining text and reports the completed section.
func (s *blockState) closeText() {
	if s.inText != "" {
		s.put(TextChunk{blockCtx: s.at(), Text: s.inText})
		s.inText = ""
	}
	if s.fullText.Len() == 0 {
		return
	}
	text := s.fullText.String()
	s.fullText.Reset()
	s.put(TextDone{
		blockCtx:      s.at(),
		FullText:      text,
		HasCodeBlocks: strings.Contains(text, "```"),
	})
}

// foldItem routes one finished output item.
//
// The item is a map because the description gives this payload several shapes
// with no discriminator to pin them to; see [ConversationItem]. Its own "type"
// field is what distinguishes them, so it is read here rather than declared.
func (s *blockState) foldItem(item map[string]any) {
	switch itemString(item, "type") {
	case "function_call":
		s.foldToolCall(item)
	case "function_call_output":
		s.foldToolResult(item)
	case "message":
		s.foldMessage(item)
	case "":
		// Nothing names it, so nothing can route it.
	default:
		s.foldNativeTool(item)
	}
}

func (s *blockState) foldToolCall(item map[string]any) {
	callID := itemString(item, "call_id")
	if callID == "" {
		return
	}
	name := itemString(item, "name")
	arguments := itemArguments(item)
	execution := &ToolExecution{
		Name:        name,
		Arguments:   arguments,
		ArgsSummary: FormatToolArgsBrief(name, arguments),
		CallID:      callID,
		AgentName:   itemString(item, "agent_name"),
		ExecutedBy:  "server",
	}
	if prior, known := s.executions[callID]; known {
		execution = prior
	} else {
		s.executions[callID] = execution
	}
	if s.seenCalls[callID] {
		return
	}
	s.seenCalls[callID] = true

	s.closeReasoning()
	s.closeText()
	s.put(ToolGroup{blockCtx: s.at(), Executions: []ToolExecution{*execution}})
}

func (s *blockState) foldToolResult(item map[string]any) {
	callID := itemString(item, "call_id")
	if callID == "" || s.seenResults[callID] {
		return
	}
	output := itemString(item, "output")
	execution, known := s.executions[callID]
	if !known {
		// A result with no call: the call was reported before this stream was read.
		// Rendered with what the result itself carries rather than dropped.
		execution = &ToolExecution{Name: itemString(item, "name"), CallID: callID}
		s.executions[callID] = execution
	}
	execution.Output = &output
	s.seenResults[callID] = true
	s.put(ToolResultBlock{
		blockCtx:    s.at(),
		Name:        execution.Name,
		CallID:      callID,
		AgentName:   execution.AgentName,
		Output:      output,
		Arguments:   execution.Arguments,
		ArgsSummary: execution.ArgsSummary,
	})
}

// foldMessage reports the assistant text a finished message item carries.
//
// The deltas already produced chunks, so this is what closes the section: a
// message item is the server's own statement of the complete text, which is more
// authoritative than the sum of the deltas a reader may have missed.
func (s *blockState) foldMessage(item map[string]any) {
	text := outputTextFromContent(item)
	if text == "" {
		s.closeText()
		return
	}
	// The buffered deltas are not discarded: below the flush threshold they are the
	// only [TextChunk] a live reader gets, and clearing them here meant a short
	// answer streamed nothing at all. closeText flushes them, then reports the
	// item's own text — which is the server's complete statement of the section and
	// so the authoritative [TextDone].
	s.fullText.Reset()
	s.fullText.WriteString(text)
	s.closeText()
}

func (s *blockState) foldNativeTool(item map[string]any) {
	toolType := itemString(item, "type")
	data, _ := item["data"].(map[string]any)
	if data == nil {
		data = item
	}
	s.put(NativeToolBlock{
		blockCtx: s.at(),
		ToolType: toolType,
		Label:    formatNativeLabel(toolType, data),
		Data:     data,
	})
}

// itemString reads one string field off an undeclared item.
func itemString(item map[string]any, key string) string {
	text, _ := item[key].(string)
	return text
}

// itemArguments decodes a tool call's arguments.
//
// The field is a JSON string rather than an object on the wire, so a model that
// emits malformed JSON yields no arguments instead of failing the turn — the call
// still renders by name, which is what a reader needs to see.
func itemArguments(item map[string]any) map[string]any {
	switch raw := item["arguments"].(type) {
	case map[string]any:
		return raw
	case string:
		if raw == "" {
			return nil
		}
		var decoded map[string]any
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			return nil
		}
		return decoded
	}
	return nil
}

// outputTextFromContent concatenates the output_text parts of a message item.
func outputTextFromContent(item map[string]any) string {
	parts, _ := item["content"].([]any)
	var text strings.Builder
	for _, part := range parts {
		block, ok := part.(map[string]any)
		if !ok || itemString(block, "type") != "output_text" {
			continue
		}
		text.WriteString(itemString(block, "text"))
	}
	return text.String()
}

// briefArgKeys names the argument a tool's summary should show.
//
// A table rather than a heuristic: the field a reader wants is the tool's own
// decision, and guessing it produces a summary that is confidently wrong. A tool
// not listed falls back to the whole argument set.
var briefArgKeys = map[string]string{
	"Read":       "file_path",
	"Write":      "file_path",
	"Edit":       "file_path",
	"Bash":       "command",
	"Glob":       "pattern",
	"Grep":       "pattern",
	"web_search": "query",
}

// maxArgsSummaryRunes bounds a summary to one display line.
const maxArgsSummaryRunes = 80

// FormatToolArgsBrief renders a tool call's arguments as one line.
//
// Exported so a caller re-rendering a recorded turn produces the same summary the
// live stream produced. Without one source of truth, "what you saw then" and
// "what this draws now" diverge the moment either side changes.
func FormatToolArgsBrief(name string, arguments map[string]any) string {
	if len(arguments) == 0 {
		return ""
	}
	if key, listed := briefArgKeys[name]; listed {
		if value, present := arguments[key]; present {
			text := fmt.Sprint(value)
			if key == "file_path" {
				// The base name: a reader recognises the file, and the directory is
				// what pushes the useful part off the line.
				if slash := strings.LastIndexByte(text, '/'); slash >= 0 {
					text = text[slash+1:]
				}
			}
			return truncateRunes(text, maxArgsSummaryRunes)
		}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return truncateRunes(fmt.Sprint(arguments), maxArgsSummaryRunes)
	}
	return truncateRunes(string(encoded), maxArgsSummaryRunes)
}

// formatNativeLabel names a provider-native tool call for display.
func formatNativeLabel(toolType string, data map[string]any) string {
	switch toolType {
	case "web_search_call":
		action, _ := data["action"].(map[string]any)
		switch itemString(action, "type") {
		case "search":
			return "web search: " + truncateRunes(itemString(action, "query"), maxArgsSummaryRunes)
		case "open_page":
			return "web open: " + truncateRunes(itemString(action, "url"), maxArgsSummaryRunes)
		}
		return "web search"
	case "mcp_call":
		if name := itemString(data, "name"); name != "" {
			return "mcp: " + name
		}
		return "mcp call"
	}
	return strings.ReplaceAll(toolType, "_", " ")
}

// truncateRunes cuts to a rune count, not a byte count, so a multi-byte character
// is never split into invalid UTF-8.
func truncateRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "…"
}

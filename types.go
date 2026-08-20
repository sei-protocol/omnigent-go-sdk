package omnigent

// ConversationRef is the lightweight reference to a conversation, used in request and response bodies
// where only the conversation ID is needed.
type ConversationRef struct {
	// Conversation identifier, e.g. "conv_abc123".
	ID string `json:"id"`
}

// ElicitationRequestParams is the inner params block of a ElicitationRequestEvent.
type ElicitationRequestParams struct {
	// Truncated preview of the underlying request payload (≤1024 chars in current AP), for the
	// consumer's renderer.
	ContentPreview *string `json:"content_preview,omitempty"`

	// Human-readable prompt the consumer renders, e.g. "Approve running 'rm -rf /tmp/cache'?".
	Message string `json:"message"`

	// MCP-standard discriminator. "form" collects structured input via requestedSchema; "url"
	// directs upstream to an external URL for OAuth / out-of-band interaction.
	Mode *string `json:"mode,omitempty"`

	// Omnigent policy-engine phase the elicitation belongs to, e.g. "pre_tool_use".
	Phase *string `json:"phase,omitempty"`

	// Omnigent policy that triggered the elicitation, e.g. "approve_shell_commands".
	PolicyName *string `json:"policy_name,omitempty"`

	// JSON-Schema dict for form mode (or nil for url mode). camelCase preserved per MCP spec,
	// e.g. {"type": "object", "properties": {"approve": {"type": "boolean"}}}.
	RequestedSchema map[string]any `json:"requestedSchema,omitempty"`

	// AP session whose resolve endpoint owns this elicitation, e.g. "conv_child123". Present
	// when a child/sub-agent prompt is mirrored into an ancestor stream; nil means resolve
	// against the current session.
	TargetSessionID *string `json:"target_session_id,omitempty"`

	// External URL for url mode (or nil for form mode), e.g.
	// "https://oauth.example.com/authorize?...".
	URL *string `json:"url,omitempty"`
}

// ErrorDetail is machine-readable error information attached to a failed response.
type ErrorDetail struct {
	// Optional one/two-sentence explanation of why it failed. Paired with title.
	Cause *string `json:"cause,omitempty"`

	// Error code string, e.g. "server_error", "invalid_input".
	Code string `json:"code"`

	// Human-readable error description. Always populated; older clients render this verbatim.
	Message string `json:"message"`

	// Optional concrete next step to fix it, e.g. a command to run. nil when there is no
	// single clear fix.
	Remediation *string `json:"remediation,omitempty"`

	// Optional short headline naming what went wrong, e.g. "Claude Code can't run as root".
	// Present when the runner recognized the failure (see omnigent.runner.launch_failure);
	// lets the UI show a clear card title instead of the raw code.
	Title *string `json:"title,omitempty"`
}

// IncompleteDetails is the details explaining why a response is incomplete.
type IncompleteDetails struct {
	// Reason the response stopped early, e.g. "max_output_tokens", "max_tool_calls".
	Reason string `json:"reason"`
}

// MCPServerStartup is one MCP server's startup state within a session.mcp_startup event.
type MCPServerStartup struct {
	// Failure detail when status == "failed", e.g. "handshaking with MCP server failed". nil
	// otherwise.
	Error *string `json:"error,omitempty"`

	// Latest startup state reported by the harness, mirroring Codex's McpServerStartupState
	// enum.
	Status string `json:"status"`
}

// ModelUsage is the cumulative token/cost usage attributed to a single LLM model.
type ModelUsage struct {
	// Cumulative tokens written to the prompt cache, e.g. 2000. nil when not recorded.
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`

	// Cumulative tokens read from the prompt cache, e.g. 8000. nil when not recorded.
	CacheReadInputTokens *int `json:"cache_read_input_tokens,omitempty"`

	// Cumulative non-cached input (prompt) tokens for this model over the subtree, e.g. 12000.
	// nil when not recorded.
	InputTokens *int `json:"input_tokens,omitempty"`

	// Cumulative output (completion) tokens, e.g. 3400. nil when not recorded.
	OutputTokens *int `json:"output_tokens,omitempty"`

	// Cumulative USD spend attributed to this model, e.g. 0.42. Present **only when this
	// model's turns were priced** (same "priced ⟺ key present" contract as the session total);
	// nil when the model is unpriced, so the sum of priced per-model costs equals the session
	// total_cost_usd.
	TotalCostUsd *float64 `json:"total_cost_usd,omitempty"`

	// Cumulative total tokens (counts cache buckets too, as the harness reports), e.g. 15400.
	// nil when not recorded.
	TotalTokens *int `json:"total_tokens,omitempty"`
}

// PresenceViewer is one user currently viewing a session (holding its SSE stream open).
type PresenceViewer struct {
	// Whether every stream the user holds reports an idle (backgrounded) tab. The web greys
	// idle viewers' avatars.
	Idle *bool `json:"idle,omitempty"`

	// ISO 8601 UTC timestamp of when the user joined, e.g. "2026-06-10T17:00:00Z". Stable
	// across reconnects within the server's leave-grace window.
	JoinedAt string `json:"joined_at"`

	// The viewer's authenticated identity, e.g. "alice@example.com". Never the reserved
	// single-user "local" sentinel — presence only tracks distinct human actors (see
	// attribution_user).
	UserID string `json:"user_id"`
}

// ResponseObject is the API representation of a response, meaning a task execution result.
type ResponseObject struct {
	// Whether this response was created as a background task.
	Background *bool `json:"background,omitempty"`

	// Unix epoch timestamp of completion, or nil if not yet complete.
	CompletedAt *int `json:"completed_at,omitempty"`

	// Reference to the owning conversation.
	Conversation *ConversationRef `json:"conversation,omitempty"`

	// Unix epoch timestamp of creation.
	CreatedAt int `json:"created_at"`

	// Error details if the response failed.
	Error *ErrorDetail `json:"error,omitempty"`

	// Unique response identifier, e.g. "resp_abc123".
	ID string `json:"id"`

	// Details if the response is incomplete (e.g. hit token limit).
	IncompleteDetails *IncompleteDetails `json:"incomplete_details,omitempty"`

	// Per-request system instructions override, or nil.
	Instructions *string `json:"instructions,omitempty"`

	// Agent name that produced this response, e.g. "research-agent".
	Model string `json:"model"`

	// Fixed resource type, always "response".
	Object *string `json:"object,omitempty"`

	// Heterogeneous output items (messages, reasoning, function_calls) serialized as dicts;
	// shape varies by item type. Empty for non-completed responses.
	Output []map[string]any `json:"output,omitempty"`

	// ID of the prior response in the conversation thread, or nil for the first turn.
	PreviousResponseID *string `json:"previous_response_id,omitempty"`

	// Reasoning configuration, e.g. {"effort": "medium"}.
	Reasoning map[string]any `json:"reasoning,omitempty"`

	// Lifecycle status, one of "queued", "in_progress", "completed", "failed", "incomplete",
	// "cancelled".
	Status string `json:"status"`

	// Whether the server persists this response. True whenever the server sets it,
	// and nil when it does not.
	Store *bool `json:"store,omitempty"`

	// Token usage statistics, or nil if not yet available.
	Usage *Usage `json:"usage,omitempty"`
}

// RetryErrorDetail is the error block carried by RetryEvent and ErrorEvent.
type RetryErrorDetail struct {
	// Stable error classifier, e.g. "timeout", "rate_limit".
	Code string `json:"code"`

	// Optional provider-specific structured fields (e.g. {"status_code": 429, "retry_after":
	// 5}); nil when the classifier had no extra context.
	Detail map[string]any `json:"detail,omitempty"`

	// Human-readable summary, e.g. "Connection timed out after 30s".
	Message string `json:"message"`
}

// SessionInputConsumedPayload is the inner payload of a SessionInputConsumedEvent.
type SessionInputConsumedPayload struct {
	// When this consumed message drains a omnigent.runtime.pending_inputs entry (a native-
	// terminal web message round-tripping back from the transcript), the drained entry's id,
	// e.g. "pending_a1b2c3". Lets a client drop the matching optimistic bubble by id instead
	// of by position.
	ClearedPendingID *string `json:"cleared_pending_id,omitempty"`

	// Email of the human actor who posted the item, e.g. "alice@example.com". nil for
	// agent/tool/system items and single-user mode. Mirrors ConversationItem.to_api_dict for
	// live attribution.
	CreatedBy *string `json:"created_by,omitempty"`

	// Decoded item payload, e.g. {"role": "user", "content": [{"type": "input_text", "text":
	// "Hello"}]}. Heterogeneous and type-specific.
	Data map[string]any `json:"data"`

	// Stable identifier of the conversation item just persisted, e.g. "item_abc123".
	ItemID string `json:"item_id"`

	// The item type discriminator — "message" for user messages, "function_call_output" for
	// tool results, etc. Mirrors omnigent.server.schemas.SessionEventInput's type field.
	Type string `json:"type"`
}

// SessionInterruptedPayload is the inner payload of a SessionInterruptedEvent.
type SessionInterruptedPayload struct {
	// Unix epoch seconds when the interrupt request reached the server, e.g. 1704067200.
	RequestedAt int `json:"requested_at"`

	// Optional active response id for terminal-backed integrations, e.g. "codex_turn_abc123".
	ResponseID *string `json:"response_id,omitempty"`
}

// Usage is the token usage statistics for a response.
type Usage struct {
	// Prompt tokens written to the provider prompt cache (cache creation), billed at a premium
	// rate. Like cache_read_input_tokens, this is separate from input_tokens; nil when
	// the server did not report it.
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`

	// Prompt tokens served from a provider prompt cache (cache hit), billed at a reduced rate.
	// Reported by Anthropic-style providers as a count *separate* from input_tokens (which
	// carries only the non-cached portion); nil when the provider does not break out
	// cache usage. Consumed by the cache-aware server-side cost path.
	CacheReadInputTokens *int `json:"cache_read_input_tokens,omitempty"`

	// Context-fill estimate for the next turn — set only by executors that make multiple LLM
	// sub-calls per turn (e.g. openai-agents). For single-call executors this is absent and
	// total_tokens serves the same purpose. The toolbar context ring and /context command use
	// this field when present, falling back to total_tokens.
	ContextTokens *int `json:"context_tokens,omitempty"`

	// Authoritative per-turn cost in USD reported directly by the harness/provider (e.g.
	// GitHub Copilot's AI-credit total).
	CostUsd *float64 `json:"cost_usd,omitempty"`

	// Number of input (prompt) tokens consumed.
	InputTokens *int `json:"input_tokens,omitempty"`

	// The LLM model the harness actually used for this turn, e.g. "claude-opus-4-8" or
	// "databricks-gpt-5-5". Reported by relay executors so the server-side cost path can price
	// the turn even when the agent spec pins no llm.model (e.g. supervisors that delegate /
	// use the harness default).
	Model *string `json:"model,omitempty"`

	// Number of output (completion) tokens generated.
	OutputTokens *int `json:"output_tokens,omitempty"`

	// Breakdown of output token usage (e.g. reasoning tokens).
	OutputTokensDetails *UsageDetails `json:"output_tokens_details,omitempty"`

	// Sum of input and output tokens across all LLM sub-calls for this turn (billing total).
	TotalTokens *int `json:"total_tokens,omitempty"`
}

// UsageDetails is the breakdown of output token usage.
type UsageDetails struct {
	// Number of tokens consumed by chain-of-thought reasoning.
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
}

package omnigent

import "encoding/json"

// AgentObject is the API representation of a registered agent.
type AgentObject struct {
	// Whether this is a server-seeded built-in agent (deterministic, name-derived id) as
	// opposed to an operator/user-registered template (random id, e.g. via omnigent server
	// --agent) or a session-scoped upload.
	Builtin *bool `json:"builtin,omitempty"`

	// Unix epoch timestamp of creation.
	CreatedAt int `json:"created_at"`

	// Optional free-text description of the agent's purpose.
	Description *string `json:"description,omitempty"`

	// The agent's harness/kind, e.g. "codex", "codex-native", or "claude-native" for
	// executor.type: omnigent agents, otherwise the executor type ("claude_sdk",
	// "agents_sdk"). nil when the bundle cannot be loaded. Lets the Web UI Add Agent picker
	// recognise an agent's kind (Codex vs Claude) without hardcoding by name slug.
	Harness *string `json:"harness,omitempty"`

	// Unique agent identifier, e.g. "ag_abc123".
	ID string `json:"id"`

	// MCP servers the agent is connected to (secret fields omitted). Empty list when the spec
	// declares no MCP servers or when the bundle cannot be loaded.
	MCPServers []MCPServerSummary `json:"mcp_servers,omitempty"`

	// Whether the MCP list can be edited through the session UI. Built-in template agents are
	// read-only; session-scoped uploaded agents are editable.
	MCPServersEditable *bool `json:"mcp_servers_editable,omitempty"`

	// Human-readable agent name, e.g. "research-agent".
	Name string `json:"name"`

	// Resource type, "agent" when present.
	Object *string `json:"object,omitempty"`

	// Guardrails policies declared on the agent. Each entry summarises the policy name, type,
	// and phases. Empty list when the spec declares no policies or when the bundle cannot be
	// loaded.
	Policies []PolicySummary `json:"policies,omitempty"`

	// Skills bundled in the agent spec (skills/<dir>/SKILL.md). Lets the Web UI's new-session
	// composer offer a slash-command menu before a session (and its runner) exists. Host-
	// discovered skills are runner-owned, so they are NOT listed here — the session snapshot's
	// skills field carries the merged set once a runner is bound.
	Skills []SkillSummary `json:"skills,omitempty"`

	// Terminal names declared in the spec's terminals: block, in declaration order, e.g.
	// ["shell"]. The Web UI gates its "new terminal" affordance on this list (creation is only
	// offered for agents with terminal access) and offers these names as the launchable
	// choices.
	Terminals []string `json:"terminals,omitempty"`

	// Unix epoch timestamp of the last update, or nil if never updated.
	UpdatedAt *int `json:"updated_at,omitempty"`

	// Monotonic version counter. Starts at 1, incremented on each update.
	Version *int `json:"version,omitempty"`
}

// ChildSessionList is the paginated list of child sessions; data is a page of ChildSessionSummary.
type ChildSessionList struct {
	Data    []ChildSessionSummary `json:"data,omitempty"`
	FirstID *string               `json:"first_id,omitempty"`
	HasMore *bool                 `json:"has_more,omitempty"`
	LastID  *string               `json:"last_id,omitempty"`
	Object  *string               `json:"object,omitempty"`
}

// ChildSessionSummary is the summary of a sub-agent (child) session under a parent session.
type ChildSessionSummary struct {
	// Agent id recorded on the latest task, e.g. "ag_abc123". nil if the child has no tasks
	// yet (rare — _spawn_one creates a task atomically with the conversation).
	AgentID *string `json:"agent_id,omitempty"`

	// Agent type recorded on the latest task, e.g. "researcher". Mirrors the tool prefix in
	// title and is provided alongside it because the title is a denormalized string while
	// agent_name is the durable per-task value.
	AgentName *string `json:"agent_name,omitempty"`

	// true when the child's session loop is live. Mirrors the algorithm used by GET
	// /v1/sessions/{id} to compute status: read the live in-memory cache first
	// ("running"/"waiting" → busy), and fall back to the latest task's status on cache miss
	// ("queued" / "in_progress" → busy).
	Busy *bool `json:"busy,omitempty"`

	// Unix epoch timestamp of child creation.
	CreatedAt int `json:"created_at"`

	// Latest task id for the child (newest by created_at), e.g. "task_abc123". nil if no
	// tasks exist.
	CurrentTaskID *string `json:"current_task_id,omitempty"`

	// Status of the latest task, e.g. "completed", "in_progress", "failed". nil if no tasks
	// exist.
	CurrentTaskStatus *string `json:"current_task_status,omitempty"`

	// Child conversation/session identifier, e.g. "conv_child123".
	ID string `json:"id"`

	// Conversation kind discriminator, "sub_agent" for rows surfaced by this endpoint.
	// nil when the server omits it.
	Kind *string `json:"kind,omitempty"`

	// Session-scoped guardrails labels on the child conversation (mirrors
	// ConversationObject.labels).
	Labels map[string]string `json:"labels,omitempty"`

	// Single-line preview of the most recent message item in the child's conversation,
	// truncated to ~150 chars with a trailing ellipsis when longer. nil when the child has no
	// message items yet (rare — the spawn tool immediately commits a user message).
	LastMessagePreview *string `json:"last_message_preview,omitempty"`

	// Error details from the child's most recent failed run, e.g. {"code":
	// "required_terminal_exited", "message": "..."}. null when the child has no durable
	// failure detail. This is the typed projection of runner-owned failure labels; clients
	// should not parse those labels directly.
	LastTaskError map[string]string `json:"last_task_error,omitempty"`

	// Resource type, "child_session" when present.
	Object *string `json:"object,omitempty"`

	// Parent conversation id (echo of the route's session_id path parameter), e.g.
	// "conv_parent987". Stable join key for clients that cache child rows across multiple
	// parents.
	ParentSessionID string `json:"parent_session_id"`

	// Number of approval / input prompts the child is currently blocked on, read from the
	// server's omnigent.runtime.pending_elicitations index.
	PendingElicitationsCount *int `json:"pending_elicitations_count,omitempty"`

	// Model this sub-agent runs on when one was pinned for it, e.g. "databricks-claude-
	// opus-4-8". Read from the child's model_override — the field intelligent routing writes
	// when it picks a model for a spawned child. nil when the child inherits the parent/spec
	// model.
	RoutedModel *string `json:"routed_model,omitempty"`

	// Identifier of the routing decision that produced routed_model, mirroring
	// RoutingDecisionData.decision_id. Read from the child's omnigent.routing.decision_id
	// label, stamped when routing pins the model. nil when the child was not routed.
	RoutingDecisionID *string `json:"routing_decision_id,omitempty"`

	// Sub-agent instance name, the suffix of title after the first ":", e.g. "auth". nil if
	// title is nil or missing a colon.
	SessionName *string `json:"session_name,omitempty"`
	TaskSummary *string `json:"task_summary,omitempty"`

	// Sub-agent title, "{agent_type}:{session_name}" as written by
	// omnigent.tools.builtins.spawn._spawn_one, e.g. "researcher:auth". nil only for legacy /
	// malformed rows; the spawn path always sets it.
	Title *string `json:"title,omitempty"`

	// UI-facing sub-agent label. For Omnigent-spawned children this is derived from the prefix
	// of title before the first ":", e.g. "researcher". For Codex-native children this is the
	// Codex-assigned agent_nickname when available, then agent_role, then "Codex".
	Tool *string `json:"tool,omitempty"`

	// Unix epoch timestamp of the child's most recent update.
	UpdatedAt int `json:"updated_at"`
}

// CompactionData is the data payload for a compaction summary item.
type CompactionData struct {
	CompactedMessages []map[string]any `json:"compacted_messages,omitempty"`

	// The item ID (inclusive) of the last conversation item covered by this summary, e.g.
	// "msg_abc123". Items at positions <= this item are summarized and do not need to be
	// loaded for prompt construction.
	LastItemID string `json:"last_item_id"`

	// The model used to generate the summary, e.g. "openai/gpt-4o".
	Model *string `json:"model,omitempty"`

	// The LLM-generated summary text covering all conversation items up through last_item_id,
	// e.g. "User asked to analyze a dataset. Agent loaded data.csv and computed statistics.".
	Summary string `json:"summary"`

	// Approximate token count of the summary text, for budget tracking, e.g. 342.
	TokenCount int  `json:"token_count"`
	WindowID   *int `json:"window_id,omitempty"`
}

// ConversationDeleted is the confirmation payload returned after deleting a conversation.
type ConversationDeleted struct {
	// true on success. nil when the server omits the field.
	Deleted *bool `json:"deleted,omitempty"`

	// ID of the deleted conversation, e.g. "conv_abc123".
	ID string `json:"id"`

	// Resource type, "conversation.deleted" when present.
	Object *string `json:"object,omitempty"`
}

// ConversationItem is a persisted item with a store-assigned ID.
type ConversationItem struct {
	// Unix epoch timestamp of creation.
	CreatedAt int `json:"created_at"`

	// Identity of the human actor who authored this item, or nil for agent/tool/system items
	// and single-user mode. Lets owner and collaborator messages be distinguished.
	CreatedBy *string `json:"created_by,omitempty"`

	// Payload for this item, left undecoded because the description declares
	// eleven variants for it and no discriminator to choose between them.
	//
	// Switch on Type, then unmarshal into the matching variant:
	// [MessageData], [FunctionCallData], [FunctionCallOutputData], [ErrorData],
	// [ReasoningData], [CompactionData], [NativeToolData], [SlashCommandData],
	// [TerminalCommandData], [ResourceEventData] or [RoutingDecisionData].
	Data json.RawMessage `json:"data"`

	// Store-assigned item ID, e.g. "msg_abc123".
	ID string `json:"id"`

	// The task/response ID this item belongs to.
	ResponseID string `json:"response_id"`

	// Item status, e.g. "completed".
	Status string `json:"status"`

	// Item type, e.g. "message", "function_call".
	Type string `json:"type"`
}

// ErrorData is the data for a persisted error banner item.
type ErrorData struct {
	// Stable error classifier, e.g. "native_terminal_start_failed".
	Code string `json:"code"`

	// Human-readable error message, e.g. "Native Codex requires the 'codex' CLI on PATH.".
	Message string `json:"message"`

	// Error source, e.g. "execution".
	Source string `json:"source"`
}

// FunctionCallData is the data for a function_call item.
type FunctionCallData struct {
	// JSON-encoded arguments string.
	Arguments string `json:"arguments"`

	// Unique call identifier from the LLM, e.g. "call_abc123".
	CallID string `json:"call_id"`
	Model  string `json:"model"`

	// Tool function name, e.g. "search.web".
	Name string `json:"name"`
}

// FunctionCallOutputData is the data for a function_call_output item.
type FunctionCallOutputData struct {
	// The call_id this output corresponds to, e.g. "call_abc123".
	CallID string `json:"call_id"`

	// The tool's string result.
	Output string `json:"output"`
}

// MCPServerSummary is the safe subset of an MCP server's configuration for API exposure.
type MCPServerSummary struct {
	// Command-line arguments for transport="stdio" servers, e.g. ["mcp-server-github"]. Empty
	// list when unset.
	Args []string `json:"args,omitempty"`

	// Executable path for transport="stdio" servers, e.g. "uvx". nil for http servers.
	Command *string `json:"command,omitempty"`

	// Optional free-text description from the spec, e.g. "GitHub MCP server". nil when unset.
	Description *string `json:"description,omitempty"`

	// HTTP headers for transport="http" servers. Values are always "[REDACTED]"; only the key
	// names are exposed.
	Headers map[string]string `json:"headers,omitempty"`

	// Server name as declared in the agent spec, e.g. "github".
	Name string `json:"name"`

	// Transport type — "stdio" or "http".
	Transport string `json:"transport"`

	// HTTP(S) endpoint URL for transport="http" servers, e.g. "https://mcp.example.com/sse".
	// nil for stdio servers.
	URL *string `json:"url,omitempty"`
}

// MessageData is the data for a message item (user or assistant).
type MessageData struct {
	// Heterogeneous content blocks, e.g. [{"type": "input_text", "text": "Hello"}].
	Content []map[string]any `json:"content"`

	// true when an assistant message is a durable partial response from an interrupted
	// external-native turn, e.g. Codex turn/completed with status "interrupted". nil when
	// false, because the server omits it from the payload in that case.
	Interrupted *bool `json:"interrupted,omitempty"`

	// true for durable context that must be replayed to agents but hidden from user-facing
	// transcripts, e.g. injected skill instructions. nil when false, because the server
	// omits it from the payload in that case.
	IsMeta *bool   `json:"is_meta,omitempty"`
	Model  *string `json:"model,omitempty"`

	// "user" or "assistant".
	Role string `json:"role"`
}

// NativeModelOption is one runner-owned native model-picker row.
type NativeModelOption struct {
	DefaultReasoningEffort    *string                       `json:"defaultReasoningEffort,omitempty"`
	DisplayName               *string                       `json:"displayName,omitempty"`
	ID                        string                        `json:"id"`
	IsDefault                 *bool                         `json:"isDefault,omitempty"`
	Model                     *string                       `json:"model,omitempty"`
	SupportedReasoningEfforts []NativeReasoningEffortOption `json:"supportedReasoningEfforts,omitempty"`
}

// NativeReasoningEffortOption is the reasoning-effort metadata advertised by a native model catalog.
type NativeReasoningEffortOption struct {
	Description     *string `json:"description,omitempty"`
	ReasoningEffort string  `json:"reasoningEffort"`
}

// NativeToolData is a provider-native tool output item (e.g. web_search_call).
type NativeToolData struct {
	// The raw dict from the Responses API output, e.g. {"type": "web_search_call", "id":
	// "ws_abc", "status": "completed", "action": {...}}.
	Item map[string]any `json:"item"`
}

// PaginatedList is a paginated list response following cursor-based pagination.
type PaginatedList struct {
	// Page of results. Items are heterogeneous and Go has no sum type, so each
	// element decodes as a map. Switch on the element's "object" key, then
	// unmarshal into the matching type.
	Data []map[string]any `json:"data,omitempty"`

	// ID of the first item in the page, or nil if the page is empty, e.g. "resp_abc123".
	FirstID *string `json:"first_id,omitempty"`

	// Whether more items exist beyond this page.
	HasMore *bool `json:"has_more,omitempty"`

	// ID of the last item in the page, or nil if the page is empty, e.g. "resp_xyz789".
	LastID *string `json:"last_id,omitempty"`

	// Resource type, "list" when present.
	Object *string `json:"object,omitempty"`
}

// PolicySummary is the safe subset of a policy's spec for API exposure.
type PolicySummary struct {
	// Short detail string about the policy implementation. For function policies: the callable
	// dotted path. For prompt policies: the first line of the prompt. nil when not available.
	Description *string `json:"description,omitempty"`

	// Policy name as declared in the agent spec, e.g. "block_long_sleep".
	Name string `json:"name"`

	// List of phase selectors the policy fires on, e.g. ["tool_call"] or ["request",
	// "response"].
	On []string `json:"on"`

	// Policy type discriminator — "function" or "prompt".
	Type string `json:"type"`
}

// ReasoningData is the data for a reasoning item.
type ReasoningData struct {
	// Raw reasoning content blocks, or nil if redacted.
	Content []map[string]any `json:"content,omitempty"`

	// Encrypted reasoning content, or nil.
	EncryptedContent *string `json:"encrypted_content,omitempty"`
	Model            string  `json:"model"`

	// Summary text blocks, e.g. [{"type": "summary_text", "text": "..."}].
	Summary []map[string]any `json:"summary"`
}

// ResourceEventData is the data payload for a persisted resource lifecycle event.
type ResourceEventData struct {
	// The SSE event type literal, e.g. "session.resource.created" or
	// "session.resource.deleted".
	EventType string `json:"event_type"`

	// Full resource object dict for created events. nil for deleted events.
	Resource map[string]any `json:"resource,omitempty"`

	// Opaque id of the affected resource, e.g. "terminal_bash_s1" or "file_abc123".
	ResourceID string `json:"resource_id"`

	// Kind of resource, e.g. "terminal", "file", "environment".
	ResourceType string `json:"resource_type"`
}

// RoutingDecisionData is the data payload for an intelligent model-router decision item.
type RoutingDecisionData struct {
	Agent *string `json:"agent,omitempty"`

	// true when the brain actually ran on model this turn (optimize mode, no user pin); false
	// when the router only WOULD have picked it (advise/shadow mode, or a user model pin won)
	// — the UI renders "would have picked".
	Applied bool `json:"applied"`

	// Model the spawning agent asked for and the router overrode, e.g. "databricks-gpt-5-5" —
	// an LLM-supplied args.model on a child session, or a native spawn's own requested_model.
	// nil when nothing was asked for, or when the router's pick names the same arm as the
	// ask.
	AttemptedOverride *string `json:"attempted_override,omitempty"`

	// Router decision identifier, e.g. "3f1c…". Correlates the transcript item with the
	// routing telemetry event and the child-sessions API row. nil for decisions made before
	// decision ids existed.
	DecisionID *string `json:"decision_id,omitempty"`

	// Harness the decision applies to, e.g. "claude-native" or "codex". nil when the decision
	// picked a model only (no harness dimension).
	Harness *string `json:"harness,omitempty"`

	// The concrete brain model the router chose, e.g. "databricks-claude-opus-4-8".
	Model string `json:"model"`

	// The router's one-line explanation, shown as muted secondary text, e.g. "Multi-file
	// refactor needs deep reasoning.".
	Rationale string `json:"rationale"`

	// The router-vocabulary pick before resolution to a servable catalog id, e.g.
	// "gpt-5-6-sol". nil when the pick needed no resolution.
	RawModel *string `json:"raw_model,omitempty"`

	// Which router produced the decision — "databricks-aigw" for the external AI-Gateway
	// task_v1 service, "oss-llm" for the built-in judge. Deliberately a plain str rather than
	// a Literal: a source added later must still round-trip through stored rows and the wire
	// instead of failing validation.
	RouterSource *string `json:"router_source,omitempty"`

	// What the decision governs — "session" (auto-harness session routing), "turn" (per-turn
	// routing), "child_session" (an Omnigent-spawned sub-agent) or "native_subagent" (a Task /
	// spawn_agent spawn routed inside the harness). nil on a row persisted before this
	// field existed; treat nil as "turn".
	Scope *string `json:"scope,omitempty"`
}

// SandboxStatus is the managed-sandbox launch progress for a host_type="managed" session.
type SandboxStatus struct {
	// Failure detail when stage == "failed", e.g. "managed sandbox launch failed: spend limit
	// reached". nil otherwise.
	Error *string `json:"error,omitempty"`

	// Current launch stage, e.g. "provisioning" — one of SandboxLaunchStage, in pipeline
	// order: provisioning (creating the sandbox) → cloning (cloning the repository workspace;
	// skipped when the session has none) → starting (starting the in-sandbox host) →
	// connecting (launching the agent runner) → ready / failed.
	Stage string `json:"stage"`
}

// SessionForkRequest is the request body for POST /v1/sessions/{source_id}/fork.
type SessionForkRequest struct {
	// Built-in agent to bind the fork to, switching it away from the source's agent/harness
	// (e.g. fork a Claude session into a Codex one, or a Claude-SDK session into Claude Code).
	// When nil, the fork keeps the source's agent. Must be a built-in agent (one listed by
	// GET /v1/agents).
	AgentID *string `json:"agent_id,omitempty"`

	// Title for the forked session. When nil, the server derives "Fork of <source_title>".
	Title *string `json:"title,omitempty"`

	// Truncation point for the copied history, e.g. "resp_abc123". When set, only items up to
	// and including the last item of that response are copied — items after it are dropped
	// from the fork. When nil (default), the full history is copied.
	UpToResponseID *string `json:"up_to_response_id,omitempty"`
}

// SessionList is the paginated list of sessions; data is a page of SessionListItem.
type SessionList struct {
	Data    []SessionListItem `json:"data,omitempty"`
	FirstID *string           `json:"first_id,omitempty"`
	HasMore *bool             `json:"has_more,omitempty"`
	LastID  *string           `json:"last_id,omitempty"`
	Object  *string           `json:"object,omitempty"`
}

// SessionListItem is the lightweight session summary for GET /v1/sessions list responses.
type SessionListItem struct {
	// Durable identifier of the bound agent.
	AgentID string `json:"agent_id"`

	// Human-readable name of the bound agent, e.g. "research-agent". nil when the agent row
	// cannot be found.
	AgentName *string `json:"agent_name,omitempty"`

	// Whether the session is archived. Archived sessions are returned by GET /v1/sessions only
	// when the request passes include_archived=true; the sidebar groups them into a dedicated
	// "Archived" section. false for normal sessions.
	Archived *bool `json:"archived,omitempty"`

	// Total number of review comments (any status) on this session. Together with
	// comments_updated_at it forms a change fingerprint: an add or edit bumps the timestamp, a
	// delete changes the count, so the web client can invalidate its cached comment list when
	// either field changes in a WS /v1/sessions/updates frame.
	CommentsCount *int `json:"comments_count,omitempty"`

	// Unix epoch microseconds of the most recently mutated comment on this session (max
	// updated_at across its comments). Microsecond precision keeps back-to-back mutations
	// within one second distinguishable while staying an exact integer in JavaScript; clients
	// only compare it for change.
	CommentsUpdatedAt *int `json:"comments_updated_at,omitempty"`

	// Unix epoch seconds of creation.
	CreatedAt int `json:"created_at"`

	// Runtime-native session id this conversation wraps, e.g. a Claude Code session uuid for
	// omnigent claude sessions. nil for regular AP-only conversations. Lets the sidebar /
	// picker render a runtime badge without a follow-up GET.
	ExternalSessionID *string `json:"external_session_id,omitempty"`

	// Git branch checked out in the session's worktree, e.g. "feature/login". Set only when
	// the session was created with a server-created git worktree; nil otherwise. The Web UI
	// uses a non-nil value to offer the "delete local branch" cleanup checkbox on session
	// delete. See designs/SESSION_GIT_WORKTREE.md.
	GitBranch *string `json:"git_branch,omitempty"`

	// Host that launched the runner for this session.
	HostID *string `json:"host_id,omitempty"`

	// Whether the session's host tunnel is live (status online and fresh within the host
	// liveness TTL). nil when the session has no host_id (CLI/local). Distinguishes "runner
	// down but host can relaunch" from "host offline" for the open-session view; not used by
	// the sidebar.
	HostOnline *bool `json:"host_online,omitempty"`

	// Session/conversation identifier, e.g. "conv_abc123".
	ID string `json:"id"`

	// Session-scoped guardrails labels.
	Labels map[string]string `json:"labels,omitempty"`

	// The user_id of the session owner, or nil when permissions are disabled. Included so the
	// sidebar can display the owner without a separate API call.
	Owner           *string `json:"owner,omitempty"`
	ParentSessionID *string `json:"parent_session_id,omitempty"`

	// Number of approval prompts currently waiting on this session. Powers the sidebar's
	// "needs attention" badge so a user with several sessions running can tell which ones are
	// blocked on them without opening each chat.
	PendingElicitationsCount *int `json:"pending_elicitations_count,omitempty"`

	// The requesting user's numeric permission level on this session: 1 = read, 2 = edit, 3 =
	// manage. nil when permissions are disabled.
	PermissionLevel *int    `json:"permission_level,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`

	// Per-session reasoning-effort hint.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`

	// Runner currently bound to the session.
	RunnerID *string `json:"runner_id,omitempty"`

	// Strict runner liveness — true iff a runner tunnel is currently registered for this
	// session. Matches GET /health's runner_online value. Strict: a dead runner on a live host
	// reads false here (no host-relaunch optimism folded in), unlike the legacy conflated
	// value. nil when the server has no runner liveness lookup wired.
	RunnerOnline *bool `json:"runner_online,omitempty"`

	// Excerpt of the chat content that matched the request's search_query, centered on the
	// match with … marking elided ends, so the search UI can show where a session matched in
	// its body. Present whenever the query hit an item body (even if the title also matched);
	// nil on non-search reads and when only the title matched.
	SearchSnippet *string `json:"search_snippet,omitempty"`

	// Derived session lifecycle status.
	Status string `json:"status"`

	// Optional human-readable title.
	Title *string `json:"title,omitempty"`

	// Unix epoch seconds of last update.
	UpdatedAt int `json:"updated_at"`

	// The *requesting user's* "last seen" wall-clock baseline in seconds for this session, or
	// nil when they have never seen it.
	ViewerLastSeen *int `json:"viewer_last_seen,omitempty"`

	// Whether the requesting user explicitly marked this session unread. Per-viewer; lifts
	// the active-row dot suppression on the client. false by default.
	ViewerUnread *bool `json:"viewer_unread,omitempty"`

	// Absolute path on disk where the runner cd's, e.g. "/Users/corey/universe/src/foo". nil
	// for sessions that haven't been bound to a host workspace.
	Workspace *string `json:"workspace,omitempty"`
}

// SessionResponse is the API representation of a session.
type SessionResponse struct {
	// Response id of the turn currently in flight, or nil when the session is idle.
	ActiveResponseID *string `json:"active_response_id,omitempty"`

	// Durable identifier of the bound agent, e.g. "ag_abc123". Stable across renames of the
	// agent.
	AgentID string `json:"agent_id"`

	// Human-readable name of the bound agent, e.g. "research-agent". Loaded from the agent row
	// at snapshot-build time. nil when the agent row cannot be found (deleted or orphaned
	// session).
	AgentName *string `json:"agent_name,omitempty"`

	// Whether the session is archived. Archived sessions are hidden from the default sidebar
	// listing and surface only behind the "Show archived" toggle. false for normal sessions.
	// Toggled via PATCH /v1/sessions/{id}.
	Archived *bool `json:"archived,omitempty"`

	// Background shells (claude-native) still running as of the last status edge, so a reload
	// re-shows "N shells still running" even though the session has settled to "idle". nil
	// (the default / omitted) when no shells are tracked.
	BackgroundTaskCount *int `json:"background_task_count,omitempty"`

	// The model's context window size in tokens as looked up server-side from litellm's
	// registry (or from the AP_CONTEXT_WINDOW_OVERRIDE env var), e.g. 200_000. nil when the
	// model is not in litellm's registry and no override is set.
	ContextWindow *int `json:"context_window,omitempty"`

	// Per-session cost-control switch: "on" activates the spec's configured cost-control mode,
	// "off" disables cost control for this session. nil means no override is active (the spec
	// default applies).
	CostControlModeOverride *string `json:"cost_control_mode_override,omitempty"`

	// Unix epoch seconds of creation.
	CreatedAt int `json:"created_at"`

	// Runtime-native session id this conversation wraps, e.g. a Claude Code session uuid for
	// omnigent claude sessions. nil for regular AP-only conversations. Populated by the
	// wrapper bridge.
	ExternalSessionID *string `json:"external_session_id,omitempty"`

	// Git branch checked out in the session's worktree, e.g. "feature/login". Set only when
	// the session was created with a server-created git worktree; nil otherwise. The Web UI
	// uses a non-nil value to offer the "delete local branch" cleanup checkbox on session
	// delete. See designs/SESSION_GIT_WORKTREE.md.
	GitBranch *string `json:"git_branch,omitempty"`

	// The bound agent's canonical harness, e.g. "claude-sdk" or "openai-agents". Lets the
	// client render the active credential for the correct provider family instead of inferring
	// it from the model string (which is wrong when the agent declares no model). nil when
	// the agent cannot be looked up.
	Harness *string `json:"harness,omitempty"`

	// Host that launched (or should launch) the runner for this session, e.g.
	// "host_a1b2c3d4...". nil for CLI-initiated sessions.
	HostID *string `json:"host_id,omitempty"`

	// Whether the session's host tunnel is live (status online and fresh within the host
	// liveness TTL). nil when the session has no host_id (CLI/local). Used only to choose
	// what the open view shows when runner_online is false — host alive ⇒ "send a message to
	// wake the runner"; host dead ⇒ "reconnect / fork".
	HostOnline *bool `json:"host_online,omitempty"`

	// Whether this session is bound to a dormant managed host the server can wake in place
	// (its provider sets SandboxLauncher.can_resume).
	HostResumable *bool `json:"host_resumable,omitempty"`

	// Unique session identifier (also the underlying conversation ID), e.g. "conv_abc123".
	ID string `json:"id"`

	// Committed conversation items in chronological order. Empty for a freshly created
	// session.
	Items []ConversationItem `json:"items,omitempty"`
	Kind  *string            `json:"kind,omitempty"`

	// Session-scoped guardrails labels. Empty dict when no labels have been written.
	Labels map[string]string `json:"labels,omitempty"`

	// Error details from the most recently failed task. Only present when status == "failed"
	// and the task stored an error. Lets clients display the failure reason on historical load
	// without relying on the transient response.error SSE event (which may have been emitted
	// before the web client subscribed).
	LastTaskError map[string]string `json:"last_task_error,omitempty"`

	// Total token count (input + output) from the most recently completed task's usage, e.g.
	// 45231. nil when no task has completed yet. Lets clients seed their context-ring on
	// conversation resume without waiting for the next response.completed SSE event.
	LastTotalTokens *int `json:"last_total_tokens,omitempty"`

	// The LLM model identifier from the bound agent's spec, e.g. "anthropic/claude-
	// sonnet-4-6". nil when the agent has no explicit llm: block or the agent cannot be
	// looked up.
	LLMModel   *string                     `json:"llm_model,omitempty"`
	MCPStartup map[string]MCPServerStartup `json:"mcp_startup,omitempty"`

	// Runner-owned model-picker options for native sessions. Claude supplies launch-time
	// gateway aliases; Codex includes each model's supported reasoning efforts. Empty while
	// unavailable.
	ModelOptions []NativeModelOption `json:"model_options,omitempty"`

	// Per-session LLM model override, e.g. "claude-opus-4-7". nil means no override is active
	// (the agent's llm_model applies). Set via PATCH /v1/sessions/{id} or the REPL's /model
	// command; both write the same column so the web UI and the TUI stay in sync.
	ModelOverride *string `json:"model_override,omitempty"`

	// For sub-agent sessions, the parent conversation's id, e.g. "conv_parent987". nil for
	// top-level sessions. Lets clients identify a session as a child and link back to its
	// parent without an extra round-trip — the same conversation row exposes this via
	// parent_conversation_id internally.
	ParentSessionID *string `json:"parent_session_id,omitempty"`

	// Outstanding approval prompts on this session at the moment the snapshot was built — the
	// original response.elicitation_request event dicts. Lets the UI render the ApprovalCard
	// on cold load, since the live SSE stream has no replay and a prompt emitted before the
	// user opened the chat would otherwise vanish.
	PendingElicitations []map[string]any `json:"pending_elicitations,omitempty"`

	// Un-consumed web-composer user messages on native-terminal (claude-native / codex-native)
	// sessions at snapshot time, each {"pending_id", "content"}.
	PendingInputs []map[string]any `json:"pending_inputs,omitempty"`

	// The requesting user's numeric permission level on this session: 1 = read, 2 = edit, 3 =
	// manage. nil when permissions are disabled (single-user mode without a permission
	// store).
	PermissionLevel *int    `json:"permission_level,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`

	// Per-session reasoning-effort hint. Accepted metadata values are "none", "minimal",
	// "low", "medium", "high", "xhigh", and "max". Provider-specific support is validated when
	// a turn executes. nil means use the agent default.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`

	// The id of this session's spawn-tree root, e.g. "conv_root1". Equals id for top-level
	// sessions; for sub-agents it points at the top-level ancestor. Lets orchestration tools
	// (e.g. sys_session_close) confirm a target shares the caller's spawn tree over the REST
	// path.
	RootConversationID *string `json:"root_conversation_id,omitempty"`

	// Runner currently bound to this session, e.g. "runner_abc123". nil until a client binds
	// one via PATCH /v1/sessions/{id}.
	RunnerID *string `json:"runner_id,omitempty"`

	// Strict runner liveness — true iff a runner tunnel is currently registered for this
	// session. This is the sole reachability signal: true means the client can chat normally.
	RunnerOnline *bool `json:"runner_online,omitempty"`

	// Managed-sandbox launch progress while the session's background sandbox launch is in
	// flight or has failed — see SandboxStatus. nil for sessions without a managed launch and
	// once the launch succeeds.
	SandboxStatus *SandboxStatus `json:"sandbox_status,omitempty"`

	// Skills the bound agent has access to — the merged result of the agent spec's bundled
	// skills and the host-scope skills discovered along the agent workdir / ~/.claude/skills/
	// (subject to the spec's skills_filter). Mirrors what the TUI passes to the runner at
	// startup.
	Skills []SkillSummary `json:"skills,omitempty"`

	// Session lifecycle status. One of "idle" (no loop running), "running" (loop executing),
	// "waiting" (loop parked on background work / sub-agents), or "failed" (terminal failure).
	Status string `json:"status"`

	// For sub-agent sessions, the sub-agent type name within the parent's spec tree, e.g.
	// "summarizer". nil for top-level sessions.
	SubAgentName *string `json:"sub_agent_name,omitempty"`

	// Per-session subagent-routing switch, two-state: "on" routes subagent spawns, and "off"
	// or nil (unset) both leave them unrouted — the in-session "Subagent routing" row renders
	// either as "Default". nil on a row created before this became explicit inherits nothing.
	SubagentRoutingOverride *string `json:"subagent_routing_override,omitempty"`

	// Pass-through CLI args the native terminal wrapper (claude / codex) was launched with,
	// e.g. ["--dangerously-skip-permissions"]. nil for non-native sessions or a native
	// session launched with none. Lets the launcher reproduce the command on resume.
	TerminalLaunchArgs []string `json:"terminal_launch_args"`

	// true while the runner is auto-creating a terminal-first session's terminal (claude-
	// native / codex-native), so the Web UI shows a spinner on the Terminal pill instead of a
	// silent greyed-out button.
	TerminalPending *bool `json:"terminal_pending,omitempty"`

	// Optional human-readable title, e.g. "debugging auth flow". nil when unset.
	Title *string `json:"title,omitempty"`

	// Current Claude Code todo list items for omnigent claude sessions, as raw dicts from
	// Claude's todo JSON file. Each dict has content, status, and activeForm keys. Empty list
	// for non-claude-native sessions or when no todos have been reported yet. Sourced from the
	// Omnigent server's in-memory _session_todos_cache.
	Todos []map[string]any `json:"todos,omitempty"`

	// Cumulative LLM spend for this session in USD, e.g. 0.42. nil when the session is
	// unpriced — no turn has been priced yet (the model is absent from the pricing
	// catalog, or no usage has been recorded) — so clients render "—" rather than a misleading
	// $0.00.
	TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`

	// Unix epoch timestamp of the last persisted session activity. Advances when conversation
	// items are appended and on session metadata edits (rename, agent switch, archive); a mid-
	// stall rename therefore resets the clock, so an orchestrator treating this as a pure
	// item-append heartbeat should account for that.
	UpdatedAt *int `json:"updated_at,omitempty"`

	// Per-model breakdown of the same subtree usage, keyed by the raw harness model id, e.g.
	// {"claude-sonnet-4-6": ModelUsage(input_tokens=12000, ...)}. null when no per-model usage
	// has been recorded (older sessions recorded before this field existed, or before the
	// first turn).
	UsageByModel map[string]ModelUsage `json:"usage_by_model,omitempty"`

	// Absolute path on disk where the runner cd's, e.g. "/Users/corey/universe/src/foo". Set
	// when the session was bound to a host workspace at create-time, or when the CLI captured
	// os.getcwd() at session-create. Always nil when not yet validated against a host.
	Workspace *string `json:"workspace,omitempty"`
}

// SkillSummary is the safe subset of a discovered skill for API exposure.
type SkillSummary struct {
	// One-line summary from the SKILL.md frontmatter, e.g. "Triage open GitHub issues in the
	// repo.".
	Description string `json:"description"`

	// Skill identifier as parsed from the SKILL.md frontmatter, e.g. "triage-issues".
	// Lowercase kebab-case.
	Name string `json:"name"`
}

// SlashCommandData is the data payload for a slash-command invocation observed in a harness transcript
// (today: Claude Code's embedded TUI).
type SlashCommandData struct {
	// Raw <command-args> text. Empty when none.
	Arguments string `json:"arguments"`

	// "skill" for plugin/Skill invocations, "command" for surfaced CLI built-ins (/effort,
	// /clear, /compact, /model, /ultrareview). The web renderer uses this to pick the prefix
	// label and icon. nil on an item persisted before this field existed; treat nil as "skill", so persisted items predating this field deserialize
	// without backfill.
	Kind  *string `json:"kind,omitempty"`
	Model string  `json:"model"`

	// Command name with leading / stripped, e.g. "dev-productivity:simplify".
	Name string `json:"name"`

	// <local-command-stdout> text when present, else nil (the common case — Skills act via
	// the next assistant turn, not stdout).
	Output *string `json:"output,omitempty"`
}

// TerminalCommandData is the data payload for a runner-side terminal command (!cmd) observed in a harness
// transcript (today: Claude Code's embedded TUI).
type TerminalCommandData struct {
	// The raw command string, e.g. "pwd". Present when kind="input", nil otherwise.
	Input *string `json:"input,omitempty"`

	// "input" for the command text, "output" for the combined stdout/stderr result.
	Kind string `json:"kind"`

	// Captured stderr text. Present when kind="output", nil otherwise.
	Stderr *string `json:"stderr,omitempty"`

	// Captured stdout text. Present when kind="output", nil otherwise.
	Stdout *string `json:"stdout,omitempty"`
}

// UpdateSessionRequest is the request body for PATCH /v1/sessions/{id}.
type UpdateSessionRequest struct {
	// New archived state. true archives (hides the session from the default sidebar listing),
	// false unarchives, nil leaves unchanged. Owner-only (unlike title, which needs only edit
	// access).
	Archived *bool `json:"archived,omitempty"`

	// Codex-native collaboration-mode string. "plan" enters Plan mode and "default" returns to
	// Default mode for subsequent Codex turns. Only valid for sessions stamped with the codex-
	// native wrapper label. Omitted leaves unchanged.
	CollaborationMode *string `json:"collaboration_mode,omitempty"`

	// Per-session cost-control switch: "on" activates the spec's configured cost-control mode,
	// "off" disables cost control for this session.
	CostControlModeOverride *string `json:"cost_control_mode_override,omitempty"`

	// Runtime-native session id captured by a wrapper bridge (e.g. Claude Code's session uuid
	// for omnigent claude sessions). Idempotent on same-value writes; the server rejects
	// attempts to overwrite an already-set different value with invalid_input to surface
	// programmer errors. nil leaves unchanged.
	ExternalSessionID *string `json:"external_session_id,omitempty"`

	// Guardrails labels to upsert. Merges with existing labels; keys not present are left
	// untouched.
	Labels map[string]string `json:"labels,omitempty"`

	// Per-session LLM model override, e.g. "claude-opus-4-7". The value is forwarded as-is to
	// the executor at turn start; the server does not enumerate valid models. Clear aliases
	// such as "default", "off", or "reset" remove the override (matching the REPL's /model
	// semantics). nil leaves unchanged.
	ModelOverride *string `json:"model_override,omitempty"`

	// File this session into a first-class project (see designs/PROJECTS_PRD.md). A non-empty
	// id moves the session into that project; the empty string "" unfiles it. Omitting the
	// field leaves membership unchanged; an explicit null is rejected (400) so it can't
	// silently unfile.
	ProjectID *string `json:"project_id,omitempty"`

	// Per-session reasoning-effort hint. Accepted metadata values are "none", "minimal",
	// "low", "medium", "high", "xhigh", and "max". Provider-specific support is validated when
	// a turn executes. Clear aliases such as "default" remove the session override. nil
	// leaves unchanged.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`

	// Identifier of a registered runner, e.g. "runner_abc123". nil leaves runner binding
	// unchanged.
	RunnerID *string `json:"runner_id,omitempty"`

	// When true, persist metadata changes but skip the runner-side side effects — specifically
	// the native /effort / /model / Codex collaboration-mode forwards into the live runtime.
	Silent *bool `json:"silent,omitempty"`

	// Per-session subagent-routing switch: "on" routes subagent spawns, "off" leaves them
	// unrouted.
	SubagentRoutingOverride *string `json:"subagent_routing_override,omitempty"`

	// Per-session native-terminal pass-through args, e.g. ["--dangerously-skip-permissions"].
	// A list (including []) replaces the stored value wholesale — resume is last-write-wins,
	// never an append. Bounds (count / length) are validated server-side. nil leaves
	// unchanged.
	TerminalLaunchArgs []string `json:"terminal_launch_args,omitempty"`

	// New title, e.g. "debugging auth flow". nil leaves unchanged.
	Title *string `json:"title,omitempty"`
}

// SessionGitOptions is the git worktree options for POST /v1/sessions.
type SessionGitOptions struct {
	// Optional base ref to branch from, e.g. "main" or "origin/main". nil branches from the
	// source repository's current HEAD. Create mode only — invalid with existing_worktree.
	BaseBranch *string `json:"base_branch,omitempty"`

	// In create mode, the new branch to create and check out, e.g. "feature/login". In bind
	// mode, the branch already checked out in the existing worktree. Validated against git
	// ref-format rules; invalid names fail with invalid_input.
	BranchName string `json:"branch_name"`

	// When true, bind to the pre-existing worktree at workspace instead of creating one (see
	// above).
	ExistingWorktree *bool `json:"existing_worktree,omitempty"`
}

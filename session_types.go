package omnigent

import (
	"github.com/sei-protocol/omnigent-go-sdk/internal/api"
)

// AgentObject is the API representation of a registered agent.
type AgentObject = api.AgentObject

// ChildSessionList is the paginated list of child sessions; data is a page of ChildSessionSummary.
type ChildSessionList = api.ChildSessionList

// ChildSessionSummary is the summary of a sub-agent (child) session under a parent session.
type ChildSessionSummary = api.ChildSessionSummary

// CompactionData is the data payload for a compaction summary item.
type CompactionData = api.CompactionData

// ConversationDeleted is the confirmation payload returned after deleting a conversation.
type ConversationDeleted = api.ConversationDeleted

// ConversationItem is a persisted item with a store-assigned ID.
type ConversationItem = api.ConversationItem

// ErrorData is the data for a persisted error banner item.
type ErrorData = api.ErrorData

// FunctionCallData is the data for a function_call item.
type FunctionCallData = api.FunctionCallData

// FunctionCallOutputData is the data for a function_call_output item.
type FunctionCallOutputData = api.FunctionCallOutputData

// MCPServerSummary is the safe subset of an MCP server's configuration for API exposure.
type MCPServerSummary = api.MCPServerSummary

// MessageData is the data for a message item (user or assistant).
type MessageData = api.MessageData

// NativeModelOption is one runner-owned native model-picker row.
type NativeModelOption = api.NativeModelOption

// NativeReasoningEffortOption is the reasoning-effort metadata advertised by a native model catalog.
type NativeReasoningEffortOption = api.NativeReasoningEffortOption

// NativeToolData is a provider-native tool output item (e.g. web_search_call).
type NativeToolData = api.NativeToolData

// PaginatedList is a paginated list response following cursor-based pagination.
type PaginatedList = api.PaginatedList

// PolicySummary is the safe subset of a policy's spec for API exposure.
type PolicySummary = api.PolicySummary

// ReasoningData is the data for a reasoning item.
type ReasoningData = api.ReasoningData

// ResourceEventData is the data payload for a persisted resource lifecycle event.
type ResourceEventData = api.ResourceEventData

// RoutingDecisionData is the data payload for an intelligent model-router decision item.
type RoutingDecisionData = api.RoutingDecisionData

// SandboxStatus is the managed-sandbox launch progress for a host_type="managed" session.
type SandboxStatus = api.SandboxStatus

// SessionForkRequest is the request body for POST /v1/sessions/{source_id}/fork.
type SessionForkRequest = api.SessionForkRequest

// SessionList is the paginated list of sessions; data is a page of SessionListItem.
type SessionList = api.SessionList

// SessionListItem is the lightweight session summary for GET /v1/sessions list responses.
type SessionListItem = api.SessionListItem

// SessionResponse is the API representation of a session.
type SessionResponse = api.SessionResponse

// SkillSummary is the safe subset of a discovered skill for API exposure.
type SkillSummary = api.SkillSummary

// SlashCommandData is the data payload for a slash-command invocation observed in a harness transcript
// (today: Claude Code's embedded TUI).
type SlashCommandData = api.SlashCommandData

// TerminalCommandData is the data payload for a runner-side terminal command (!cmd) observed in a harness
// transcript (today: Claude Code's embedded TUI).
type TerminalCommandData = api.TerminalCommandData

// UpdateSessionRequest is the request body for PATCH /v1/sessions/{id}.
type UpdateSessionRequest = api.UpdateSessionRequest

// SessionGitOptions is the git worktree options for POST /v1/sessions.
type SessionGitOptions = api.SessionGitOptions

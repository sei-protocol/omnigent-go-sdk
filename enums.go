package omnigent

// The values the description declares for this package's enumerated fields. The
// fields stay plain strings, so an unknown value still decodes and a switch needs
// a default arm. doc.go, under "Enumerated values", has the reasoning.

// The values ElicitationRequestParams.Mode carries.
//
// MCP-standard discriminator. "form" collects structured input via
// requestedSchema; "url" directs upstream to an external URL for OAuth /
// out-of-band interaction.
const (
	ElicitationRequestParamsModeForm = "form"
	ElicitationRequestParamsModeURL  = "url"
)

// The values ErrorData.Source carries.
//
// Error source, e.g. "execution".
const (
	ErrorDataSourceLLM       = "llm"
	ErrorDataSourceExecution = "execution"
	ErrorDataSourceTool      = "tool"
)

// The values ErrorEvent.Source carries.
//
// Origin of the error — "llm" for LLM-call failures, "execution" for timeouts,
// "tool" for tool failures (currently emitted by retry exhaustion paths).
const (
	ErrorEventSourceLLM       = "llm"
	ErrorEventSourceExecution = "execution"
	ErrorEventSourceTool      = "tool"
)

// The values MCPServerStartup.Status carries.
//
// Latest startup state reported by the harness, mirroring Codex's
// McpServerStartupState enum.
const (
	MCPServerStartupStatusStarting  = "starting"
	MCPServerStartupStatusReady     = "ready"
	MCPServerStartupStatusFailed    = "failed"
	MCPServerStartupStatusCancelled = "cancelled"
)

// The values MessageData.Role carries.
//
// "user" or "assistant".
const (
	MessageDataRoleUser      = "user"
	MessageDataRoleAssistant = "assistant"
)

// The values RetryEvent.Source carries.
//
// Origin of the retried failure — "llm" for LLM-call retries, "tool" for
// tool-call retries.
const (
	RetryEventSourceLLM  = "llm"
	RetryEventSourceTool = "tool"
)

// The values RoutingDecisionData.Scope carries.
//
// What the decision governs — "session" (auto-harness session routing), "turn"
// (per-turn routing), "child_session" (an Omnigent-spawned sub-agent) or
// "native_subagent" (a Task / spawn_agent spawn routed inside the harness).
const (
	RoutingDecisionDataScopeSession        = "session"
	RoutingDecisionDataScopeTurn           = "turn"
	RoutingDecisionDataScopeChildSession   = "child_session"
	RoutingDecisionDataScopeNativeSubagent = "native_subagent"
)

// The values SandboxStatus.Stage carries.
//
// Current launch stage, e.g.
const (
	SandboxStatusStageProvisioning = "provisioning"
	SandboxStatusStageCloning      = "cloning"
	SandboxStatusStageStarting     = "starting"
	SandboxStatusStageConnecting   = "connecting"
	SandboxStatusStageReady        = "ready"
	SandboxStatusStageFailed       = "failed"
)

// The values SessionListItem.Status carries.
//
// Derived session lifecycle status.
const (
	SessionListItemStatusIdle    = "idle"
	SessionListItemStatusRunning = "running"
	SessionListItemStatusWaiting = "waiting"
	SessionListItemStatusFailed  = "failed"
)

// The values SessionResponse.Status carries.
//
// Session lifecycle status. One of "idle" (no loop running), "running" (loop
// executing), "waiting" (loop parked on background work / sub-agents), or
// "failed" (terminal failure).
const (
	SessionResponseStatusIdle    = "idle"
	SessionResponseStatusRunning = "running"
	SessionResponseStatusWaiting = "waiting"
	SessionResponseStatusFailed  = "failed"
)

// The values SessionSandboxStatusEvent.Stage carries.
//
// The launch stage just entered, e.g. "provisioning" — see SandboxStatus for
// the full pipeline order.
const (
	SessionSandboxStatusEventStageProvisioning = "provisioning"
	SessionSandboxStatusEventStageCloning      = "cloning"
	SessionSandboxStatusEventStageStarting     = "starting"
	SessionSandboxStatusEventStageConnecting   = "connecting"
	SessionSandboxStatusEventStageReady        = "ready"
	SessionSandboxStatusEventStageFailed       = "failed"
)

// The values SessionStatusEvent.Status carries.
//
// New session status.
const (
	SessionStatusEventStatusIdle      = "idle"
	SessionStatusEventStatusLaunching = "launching"
	SessionStatusEventStatusRunning   = "running"
	SessionStatusEventStatusWaiting   = "waiting"
	SessionStatusEventStatusFailed    = "failed"
)

// The values SlashCommandData.Kind carries.
//
// "skill" for plugin/Skill invocations, "command" for surfaced CLI built-ins
// (/effort, /clear, /compact, /model, /ultrareview). The web renderer uses
// this to pick the prefix label and icon.
const (
	SlashCommandDataKindSkill   = "skill"
	SlashCommandDataKindCommand = "command"
)

// The values TerminalCommandData.Kind carries.
//
// "input" for the command text, "output" for the combined stdout/stderr
// result.
const (
	TerminalCommandDataKindInput  = "input"
	TerminalCommandDataKindOutput = "output"
)

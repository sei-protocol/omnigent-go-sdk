package omnigent

// Values the description declares for the enumerated fields on this package's
// types. The fields stay plain strings, because the server may add a value and a
// client built against an older description has to keep decoding one it does not
// know. These name the values it does know.
//
// TestEveryDeclaredEnumValueHasAConstant holds this file to the description.
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
// Origin of the error — "llm" for LLM-call failures, "execution" for
// timeouts, "tool" for tool failures (currently emitted by retry
// exhaustion paths).
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
// What the decision governs — "session" (auto-harness session routing),
// "turn" (per-turn routing), "child_session" (an Omnigent-spawned sub-
// agent) or "native_subagent" (a Task / spawn_agent spawn routed inside
const (
	RoutingDecisionDataScopeSession        = "session"
	RoutingDecisionDataScopeTurn           = "turn"
	RoutingDecisionDataScopeChildSession   = "child_session"
	RoutingDecisionDataScopeNativeSubagent = "native_subagent"
)

// The values SandboxStatus.Stage carries.
//
// Current launch stage, e.g. "provisioning" — one of SandboxLaunchStage,
// in pipeline order: provisioning (creating the sandbox) → cloning
// (cloning the repository workspace; skipped when the session has none) →
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
// Session lifecycle status. One of "idle" (no loop running), "running"
// (loop executing), "waiting" (loop parked on background work / sub-
// agents), or "failed" (terminal failure). Current read paths collapse
const (
	SessionResponseStatusIdle    = "idle"
	SessionResponseStatusRunning = "running"
	SessionResponseStatusWaiting = "waiting"
	SessionResponseStatusFailed  = "failed"
)

// The values SessionSandboxStatusEvent.Stage carries.
//
// The launch stage just entered, e.g. "provisioning" — see SandboxStatus
// for the full pipeline order.
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
// New session status. "launching" (session or child task created, but no
// concrete harness start observed), "idle" (no loop running), "running"
// (loop executing), "waiting" (parent turn parked on the async-work
const (
	SessionStatusEventStatusIdle      = "idle"
	SessionStatusEventStatusLaunching = "launching"
	SessionStatusEventStatusRunning   = "running"
	SessionStatusEventStatusWaiting   = "waiting"
	SessionStatusEventStatusFailed    = "failed"
)

// The values SlashCommandData.Kind carries.
//
// "skill" for plugin/Skill invocations, "command" for surfaced CLI built-
// ins (/effort, /clear, /compact, /model, /ultrareview). The web renderer
// uses this to pick the prefix label and icon. Defaults to "skill" so
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

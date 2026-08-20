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

// The values RetryEvent.Source carries.
//
// Origin of the retried failure — "llm" for LLM-call retries, "tool" for
// tool-call retries.
const (
	RetryEventSourceLLM  = "llm"
	RetryEventSourceTool = "tool"
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

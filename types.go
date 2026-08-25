package omnigent

import "github.com/sei-protocol/omnigent-go-sdk/internal/api"

// ConversationRef is the lightweight reference to a conversation, used in request and response bodies
// where only the conversation ID is needed.
type ConversationRef = api.ConversationRef

// ElicitationRequestParams is the inner params block of a ElicitationRequestEvent.
type ElicitationRequestParams = api.ElicitationRequestParams

// ErrorDetail is machine-readable error information attached to a failed response.
type ErrorDetail = api.ErrorDetail

// IncompleteDetails is the details explaining why a response is incomplete.
type IncompleteDetails = api.IncompleteDetails

// MCPServerStartup is one MCP server's startup state within a session.mcp_startup event.
type MCPServerStartup = api.MCPServerStartup

// ModelUsage is the cumulative token/cost usage attributed to a single LLM model.
type ModelUsage = api.ModelUsage

// PresenceViewer is one user currently viewing a session (holding its SSE stream open).
type PresenceViewer = api.PresenceViewer

// ResponseObject is the API representation of a response, meaning a task execution result.
type ResponseObject = api.ResponseObject

// RetryErrorDetail is the error block carried by RetryEvent and ErrorEvent.
type RetryErrorDetail = api.RetryErrorDetail

// SessionInputConsumedPayload is the inner payload of a SessionInputConsumedEvent.
type SessionInputConsumedPayload = api.SessionInputConsumedPayload

// SessionInterruptedPayload is the inner payload of a SessionInterruptedEvent.
type SessionInterruptedPayload = api.SessionInterruptedPayload

// Usage is the token usage statistics for a response.
type Usage = api.Usage

// UsageDetails is the breakdown of output token usage.
type UsageDetails = api.UsageDetails

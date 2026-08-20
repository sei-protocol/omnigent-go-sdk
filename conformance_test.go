package omnigent

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file is the gate that replaces the deleted generator's own.
//
// The generator proved the types matched spec/openapi.json in both directions and
// needed 162 exported symbols to do it. These tests prove the one direction that
// can hurt a caller: this package never declares a field the server does not
// have. They deliberately do not prove the reverse, because the surface is meant
// to be smaller than the document.

// schemaFor names the spec schema each hand-authored type mirrors.
//
// A type absent from this map is not checked, so [TestEveryMirroredTypeIsMapped]
// holds the event half to the decoder's own registry: a variant the decoder knows
// and this map does not is a failure, not a silent skip. The support half is a
// plain list, because nothing else enumerates it.
var schemaFor = map[string]string{
	"BrowserActionRequestEvent":           "BrowserActionRequestEvent",
	"ClientTaskCancelEvent":               "ClientTaskCancelEvent",
	"CompactionCompletedEvent":            "CompactionCompletedEvent",
	"CompactionFailedEvent":               "CompactionFailedEvent",
	"CompactionInProgressEvent":           "CompactionInProgressEvent",
	"ElicitationRequestEvent":             "ElicitationRequestEvent",
	"ElicitationResolvedEvent":            "ElicitationResolvedEvent",
	"ErrorEvent":                          "ErrorEvent",
	"InProgressEvent":                     "InProgressEvent",
	"IncompleteEvent":                     "IncompleteEvent",
	"OutputFileDoneEvent":                 "OutputFileDoneEvent",
	"OutputItemDoneEvent":                 "OutputItemDoneEvent",
	"OutputTextDeltaEvent":                "OutputTextDeltaEvent",
	"PolicyDeniedEvent":                   "PolicyDeniedEvent",
	"QueuedEvent":                         "QueuedEvent",
	"ReasoningStartedEvent":               "ReasoningStartedEvent",
	"ReasoningSummaryTextDeltaEvent":      "ReasoningSummaryTextDeltaEvent",
	"ReasoningTextDeltaEvent":             "ReasoningTextDeltaEvent",
	"ResponseCancelledEvent":              "CancelledEvent",
	"ResponseCompletedEvent":              "CompletedEvent",
	"ResponseCreatedEvent":                "CreatedEvent",
	"ResponseFailedEvent":                 "FailedEvent",
	"ResponseHeartbeatEvent":              "HeartbeatEvent",
	"RetryEvent":                          "RetryEvent",
	"SessionAgentChangedEvent":            "SessionAgentChangedEvent",
	"SessionChangedFilesInvalidatedEvent": "SessionChangedFilesInvalidatedEvent",
	"SessionChildSessionUpdatedEvent":     "SessionChildSessionUpdatedEvent",
	"SessionCollaborationModeEvent":       "SessionCollaborationModeEvent",
	"SessionCreatedEvent":                 "SessionCreatedEvent",
	"SessionHeartbeatEvent":               "SessionHeartbeatEvent",
	"SessionInputConsumedEvent":           "SessionInputConsumedEvent",
	"SessionInterruptedEvent":             "SessionInterruptedEvent",
	"SessionMcpStartupEvent":              "SessionMcpStartupEvent",
	"SessionModelEvent":                   "SessionModelEvent",
	"SessionModelOptionsEvent":            "SessionModelOptionsEvent",
	"SessionPresenceEvent":                "SessionPresenceEvent",
	"SessionReasoningEffortEvent":         "SessionReasoningEffortEvent",
	"SessionResourceCreatedEvent":         "SessionResourceCreatedEvent",
	"SessionResourceDeletedEvent":         "SessionResourceDeletedEvent",
	"SessionSandboxStatusEvent":           "SessionSandboxStatusEvent",
	"SessionSkillsEvent":                  "SessionSkillsEvent",
	"SessionStatusEvent":                  "SessionStatusEvent",
	"SessionSupersededEvent":              "SessionSupersededEvent",
	"SessionTerminalActivityEvent":        "SessionTerminalActivityEvent",
	"SessionTerminalPendingEvent":         "SessionTerminalPendingEvent",
	"SessionTodosEvent":                   "SessionTodosEvent",
	"SessionUsageEvent":                   "SessionUsageEvent",
	"ToolOutputDeltaEvent":                "ToolOutputDeltaEvent",
	"TurnCancelledEvent":                  "TurnCancelledEvent",
	"TurnCompletedEvent":                  "TurnCompletedEvent",
	"TurnFailedEvent":                     "TurnFailedEvent",
	"TurnStartedEvent":                    "TurnStartedEvent",

	// Support types the event union reaches.
	"ConversationRef":             "ConversationRef",
	"ElicitationRequestParams":    "ElicitationRequestParams",
	"ErrorDetail":                 "ErrorDetail",
	"IncompleteDetails":           "IncompleteDetails",
	"MCPServerStartup":            "McpServerStartup",
	"ModelUsage":                  "ModelUsage",
	"PresenceViewer":              "PresenceViewer",
	"ResponseObject":              "ResponseObject",
	"RetryErrorDetail":            "RetryErrorDetail",
	"SessionInputConsumedPayload": "SessionInputConsumedPayload",
	"SessionInterruptedPayload":   "SessionInterruptedPayload",
	"Usage":                       "Usage",
	"UsageDetails":                "UsageDetails",
}

// loadSpec reads the vendored description once per test.
func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("spec/openapi.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("spec carries no schemas")
	}
	return doc.Components.Schemas
}

// declaredProperties returns the property names a schema declares.
func declaredProperties(t *testing.T, schemas map[string]any, name string) map[string]bool {
	t.Helper()
	node, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("spec declares no schema %q", name)
	}
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(props))
	for k := range props {
		out[k] = true
	}
	return out
}

// mirroredTypes returns one zero value per hand-authored type, keyed by Go name.
//
// The event half comes from [eventRegistry] by decoding an empty object, so the
// set is exactly what the decoder can produce. The support half is reached
// through the event fields, so reflection finds it without a second list.
func mirroredTypes(t *testing.T) map[string]reflect.Type {
	t.Helper()
	found := map[string]reflect.Type{}

	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		// Map is here because a schema's additionalProperties can be a $ref, so a
		// type is sometimes reachable only as a map's value. Omitting it left two
		// mapped types unchecked.
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt.PkgPath() == "" {
			return
		}
		if _, seen := found[rt.Name()]; seen {
			return
		}
		found[rt.Name()] = rt
		for i := range rt.NumField() {
			walk(rt.Field(i).Type)
		}
	}

	for wire, decode := range eventRegistry {
		ev, err := decode([]byte(`{}`))
		if err != nil {
			t.Fatalf("%s: decoding an empty object must succeed: %v", wire, err)
		}
		walk(reflect.TypeOf(ev))
	}
	return found
}

// TestEveryMirroredTypeIsMapped fails when a type reachable from the decoder has
// no entry in [schemaFor]. Without it, adding an event variant would silently
// skip the field check for that variant.
func TestEveryMirroredTypeIsMapped(t *testing.T) {
	for name := range mirroredTypes(t) {
		if _, ok := schemaFor[name]; !ok {
			t.Errorf("%s is reachable from the decoder but names no spec schema; add it to schemaFor", name)
		}
	}
}

// TestEveryDeclaredFieldExistsInTheSpec is the direction that can hurt a caller.
// A field this package declares that the server does not is a promise nothing
// keeps.
func TestEveryDeclaredFieldExistsInTheSpec(t *testing.T) {
	schemas := loadSpec(t)
	types := mirroredTypes(t)

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, goName := range names {
		schemaName, mapped := schemaFor[goName]
		if !mapped {
			continue // TestEveryMirroredTypeIsMapped owns this failure
		}
		declared := declaredProperties(t, schemas, schemaName)
		rt := types[goName]
		for i := range rt.NumField() {
			field := rt.Field(i)
			tag := field.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			wire := strings.Split(tag, ",")[0]
			if wire == "" {
				continue
			}
			if !declared[wire] {
				t.Errorf("%s.%s declares json:%q, which schema %s does not carry",
					goName, field.Name, wire, schemaName)
			}
		}
	}
}

// TestEveryUnionMemberDecodes pins the registry against the spec's own
// discriminator mapping, so a variant the server publishes and this package does
// not know is visible here rather than as an UnknownEvent in production.
func TestEveryUnionMemberDecodes(t *testing.T) {
	raw, err := os.ReadFile("spec/openapi.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas struct {
				ServerStreamEvent struct {
					Discriminator struct {
						Mapping map[string]string `json:"mapping"`
					} `json:"discriminator"`
				} `json:"ServerStreamEvent"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	mapping := doc.Components.Schemas.ServerStreamEvent.Discriminator.Mapping
	if len(mapping) == 0 {
		t.Fatal("spec carries no discriminator mapping for ServerStreamEvent")
	}
	for wire := range mapping {
		if _, known := eventRegistry[wire]; !known {
			t.Errorf("the spec publishes event %q, which eventRegistry does not decode", wire)
		}
	}
	for wire := range eventRegistry {
		if _, published := mapping[wire]; !published {
			t.Errorf("eventRegistry decodes %q, which the spec does not publish", wire)
		}
	}
}

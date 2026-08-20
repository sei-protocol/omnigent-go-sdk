package omnigent

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests hold the hand-authored types to spec/openapi.json.
//
// Together they check that every exported field the decoder reaches names a
// property the description declares, that its Go type and optionality match, and
// that the decoder's variant set equals the description's discriminator mapping
// in both directions.
//
// They do not check presence. A property the description declares and this
// package omits passes, deliberately: the surface is meant to be smaller than the
// document, not equal to it.

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
	"SessionMCPStartupEvent":              "SessionMcpStartupEvent",
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

	// Types the session and file surface reaches. Their schemas are declared, so
	// the gate covers them; the file routes themselves declare no shape at all and
	// are named in doc.go instead.
	"AgentObject":                 "AgentObject",
	"ChildSessionList":            "ChildSessionList",
	"ChildSessionSummary":         "ChildSessionSummary",
	"CompactionData":              "CompactionData",
	"ConversationDeleted":         "ConversationDeleted",
	"ConversationItem":            "ConversationItem",
	"ErrorData":                   "ErrorData",
	"FunctionCallData":            "FunctionCallData",
	"FunctionCallOutputData":      "FunctionCallOutputData",
	"MCPServerSummary":            "MCPServerSummary",
	"MessageData":                 "MessageData",
	"NativeModelOption":           "NativeModelOption",
	"NativeReasoningEffortOption": "NativeReasoningEffortOption",
	"NativeToolData":              "NativeToolData",
	"PaginatedList":               "PaginatedList",
	"PolicySummary":               "PolicySummary",
	"ReasoningData":               "ReasoningData",
	"ResourceEventData":           "ResourceEventData",
	"RoutingDecisionData":         "RoutingDecisionData",
	"SandboxStatus":               "SandboxStatus",
	"SessionForkRequest":          "SessionForkRequest",
	"SessionList":                 "SessionList",
	"SessionListItem":             "SessionListItem",
	"SessionResponse":             "SessionResponse",
	"SkillSummary":                "SkillSummary",
	"SlashCommandData":            "SlashCommandData",
	"TerminalCommandData":         "TerminalCommandData",
	"UpdateSessionRequest":        "UpdateSessionRequest",

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
		// Errorf, not Fatalf: one renamed schema must not hide the other sixty.
		t.Errorf("spec declares no schema %q", name)
		return nil
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
	slices.Sort(names)

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

// goKindFor maps a schema's declared type to the reflect.Kind a Go field
// mirroring it must have. A schema that declares no type, or declares several,
// carries no expectation and is absent from this map.
func goKindFor(node map[string]any) (reflect.Kind, bool) {
	switch node["type"] {
	case "string":
		return reflect.String, true
	case "integer":
		return reflect.Int, true
	case "number":
		return reflect.Float64, true
	case "boolean":
		return reflect.Bool, true
	case "array":
		return reflect.Slice, true
	case "object":
		return reflect.Map, true
	}
	if _, isRef := node["$ref"]; isRef {
		return reflect.Struct, true
	}
	return reflect.Invalid, false
}

// unwrapProperty strips the anyOf-with-null wrapper the server uses for an
// optional field and reports whether it found one. It returns the inner schema,
// so the caller compares against the type the field actually carries.
func unwrapProperty(prop map[string]any) (inner map[string]any, optional bool) {
	variants, wrapped := prop["anyOf"].([]any)
	if !wrapped {
		return prop, false
	}
	var nonNull []map[string]any
	for _, v := range variants {
		node, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if node["type"] == "null" {
			optional = true
			continue
		}
		nonNull = append(nonNull, node)
	}
	// A union of two real types carries no single expectation; leave it alone.
	if len(nonNull) != 1 {
		return nil, optional
	}
	return nonNull[0], optional
}

// freeFormFields are fields the description declares as an untyped object, so Go
// carries map[string]any and no stronger expectation exists. Each entry is
// deliberate: the schema genuinely publishes no shape, rather than this package
// declining to mirror one.
var freeFormFields = map[string]bool{}

// TestEveryDeclaredFieldMatchesItsSchemaType is the dimension a field-name check
// cannot reach. A field whose Go type contradicts the description decodes to the
// zero value or fails the whole event, and the name check passes either way.
func TestEveryDeclaredFieldMatchesItsSchemaType(t *testing.T) {
	schemas := loadSpec(t)
	types := mirroredTypes(t)

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, goName := range names {
		schemaName, mapped := schemaFor[goName]
		if !mapped {
			continue
		}
		node, ok := schemas[schemaName].(map[string]any)
		if !ok {
			continue
		}
		props, _ := node["properties"].(map[string]any)
		required := map[string]bool{}
		if list, ok := node["required"].([]any); ok {
			for _, r := range list {
				if name, ok := r.(string); ok {
					required[name] = true
				}
			}
		}

		rt := types[goName]
		for i := range rt.NumField() {
			field := rt.Field(i)
			tag := field.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			wire := strings.Split(tag, ",")[0]
			prop, ok := props[wire].(map[string]any)
			if !ok {
				continue // the name check owns a property the schema lacks
			}
			if freeFormFields[goName+"."+field.Name] {
				continue
			}

			inner, optional := unwrapProperty(prop)
			if inner == nil {
				continue // a genuine union carries no single expectation
			}
			want, known := goKindFor(inner)
			if !known {
				continue
			}

			got := field.Type
			isPointer := got.Kind() == reflect.Pointer
			if isPointer {
				got = got.Elem()
			}

			// A slice or map already encodes absence as nil, so it never needs a
			// pointer and the optionality rule does not apply to it.
			nilable := got.Kind() == reflect.Slice || got.Kind() == reflect.Map

			if got.Kind() != want {
				t.Errorf("%s.%s is %s, but schema %s declares %q for %q",
					goName, field.Name, field.Type, schemaName, inner["type"], wire)
				continue
			}
			switch {
			case required[wire] && isPointer:
				t.Errorf("%s.%s is a pointer, but schema %s lists %q as required",
					goName, field.Name, schemaName, wire)
			case optional && !isPointer && !nilable:
				t.Errorf("%s.%s is a value type, but schema %s declares %q optional, so a caller cannot tell zero from absent",
					goName, field.Name, schemaName, wire)
			}
		}
	}
}

// TestEveryDeclaredEnumValueHasAConstant ties enums.go to the description.
//
// The fields themselves stay plain strings so an older client keeps decoding a
// value the server added. That makes the constants a convenience rather than a
// contract, and a convenience with a missing entry is worse than none: a caller
// comparing against a name that does not exist does not compile, and one
// comparing against a stale literal silently never matches.
func TestEveryDeclaredEnumValueHasAConstant(t *testing.T) {
	schemas := loadSpec(t)
	constants := declaredConstants(t)
	declared := 0

	for goName, schemaName := range schemaFor {
		node, ok := schemas[schemaName].(map[string]any)
		if !ok {
			continue
		}
		props, _ := node["properties"].(map[string]any)
		for wire, raw := range props {
			if wire == "type" {
				continue // the discriminator is the Go type itself
			}
			prop, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			inner, _ := unwrapProperty(prop)
			if inner == nil {
				inner = prop
			}
			values, ok := inner["enum"].([]any)
			if !ok {
				continue
			}
			for _, v := range values {
				value, ok := v.(string)
				if !ok {
					continue
				}
				declared++
				name := goName + goFieldName(wire) + goFieldName(value)
				got, ok := constants[name]
				switch {
				case !ok:
					t.Errorf("schema %s declares %q for %q, so enums.go needs %s",
						schemaName, value, wire, name)
				case got != value:
					t.Errorf("%s is %q, but schema %s declares %q for %q",
						name, got, schemaName, value, wire)
				}
			}
		}
	}
	if declared == 0 {
		t.Fatal("found no enum values to check; the walk is not reaching the schemas")
	}
	t.Logf("checked %d declared enum values", declared)
}

// declaredConstants parses enums.go and returns the constant names it declares,
// mapped to their values.
//
// It parses rather than string-matches, because gofmt aligns a const block and a
// literal " = " never survives that. It reads the source rather than reflecting,
// because an untyped string constant leaves no runtime handle to enumerate — the
// cost of keeping the fields plain strings, and the reason this test exists.
func declaredConstants(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "enums.go", nil, 0)
	if err != nil {
		t.Fatalf("parse enums.go: %v", err)
	}

	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != len(value.Values) {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[name.Name] = unquoted
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("enums.go declares no string constants")
	}
	return out
}

// goFieldName renders a wire name the way this package's identifiers do.
func goFieldName(wire string) string {
	initialisms := map[string]string{"id": "ID", "url": "URL", "api": "API", "mcp": "MCP", "llm": "LLM"}
	var b strings.Builder
	for _, part := range strings.FieldsFunc(wire, func(r rune) bool { return r == '_' || r == '-' || r == ' ' }) {
		if upper, ok := initialisms[strings.ToLower(part)]; ok {
			b.WriteString(upper)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

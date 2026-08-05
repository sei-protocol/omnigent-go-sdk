package omnigent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// generatedFiles are the two files scripts/gen_go_client.py writes. Several of
// the guarantees below are about what the generator emits rather than about a
// type's runtime shape, so they are asserted against its output.
var generatedFiles = []string{"models.gen.go", "events.gen.go"}

func readGenerated(t *testing.T) map[string]string {
	t.Helper()

	sources := make(map[string]string, len(generatedFiles))
	for _, name := range generatedFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources[name] = string(data)
	}
	return sources
}

// TestNoGeneratedFieldIsAPointerToACollection is D3. oapi-codegen renders every
// optional field as a pointer, which for an array or an object means *[]T and
// *map[K]V — not Go: nil already expresses absence, so the pointer adds a second
// empty state and a dereference to every read. 23 fields had it, including
// SessionResponse.Items, the field callers touch most. Fixing it after a v1 tag
// would be a breaking change.
//
// The generator is what enforces this, so the check is on both the shape of the
// types and the bytes it emitted. Against feat/go-client-v2 both halves fail.
func TestNoGeneratedFieldIsAPointerToACollection(t *testing.T) {
	t.Parallel()

	// The hand-written surface's roots, walked transitively: every generated type
	// this package's own signatures can put in front of a caller.
	roots := []reflect.Type{
		reflect.TypeFor[SessionResponse](),
		reflect.TypeFor[ConversationDeleted](),
		reflect.TypeFor[SessionGitOptions](),
		reflect.TypeFor[ValidationError](),
		// The listing element types. Both carry collections a caller ranges
		// over — AgentObject.Skills and .McpServers, SessionListItem.Labels —
		// so both are exactly the shape this invariant exists to protect.
		reflect.TypeFor[AgentObject](),
		reflect.TypeFor[SessionListItem](),
	}
	seen := map[reflect.Type]bool{}
	var walk func(t *testing.T, typ reflect.Type, path string)
	walk = func(t *testing.T, typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Struct:
			for i := range typ.NumField() {
				field := typ.Field(i)
				where := path + "." + field.Name
				if field.Type.Kind() == reflect.Pointer {
					switch field.Type.Elem().Kind() {
					case reflect.Slice, reflect.Map:
						t.Errorf("%s is %s; nil already expresses absence for a %s",
							where, field.Type, field.Type.Elem().Kind())
					}
				}
				walk(t, field.Type, where)
			}
		case reflect.Slice, reflect.Array, reflect.Map, reflect.Pointer:
			walk(t, typ.Elem(), path+"[]")
		}
	}
	for _, root := range roots {
		walk(t, root, root.Name())
	}

	// And the same statement over everything the generator emitted, including the
	// event types no root reaches.
	for name, source := range readGenerated(t) {
		for _, token := range []string{"*[]", "*map["} {
			if strings.Contains(source, token) {
				line := 1 + strings.Count(source[:strings.Index(source, token)], "\n")
				t.Errorf("%s:%d contains %q; the generator should have skipped the optional pointer",
					name, line, token)
			}
		}
	}
}

// TestEventNamesSayWhichWireTypeTheyAre is D4. Five trailing names are published
// under two `type` prefixes each, and the spec named only one member of each pair
// with its prefix — so the bare name a reader reaches for is whichever the spec
// happened to leave unprefixed. For the heartbeat that is the wrong one:
// HeartbeatEvent was "response.heartbeat", while the heartbeat a caller sees in
// the stream's prologue and every 15 seconds after is "session.heartbeat".
//
// Against feat/go-client-v2 this does not compile: ResponseHeartbeatEvent and its
// four siblings do not exist.
func TestEventNamesSayWhichWireTypeTheyAre(t *testing.T) {
	t.Parallel()

	// Each pair, and the Go type its wire type must decode to.
	pairs := []struct {
		wire string
		want any
	}{
		{"response.heartbeat", ResponseHeartbeatEvent{}},
		{"session.heartbeat", SessionHeartbeatEvent{}},
		{"response.created", ResponseCreatedEvent{}},
		{"session.created", SessionCreatedEvent{}},
		{"response.completed", ResponseCompletedEvent{}},
		{"turn.completed", TurnCompletedEvent{}},
		{"response.failed", ResponseFailedEvent{}},
		{"turn.failed", TurnFailedEvent{}},
		{"response.cancelled", ResponseCancelledEvent{}},
		{"turn.cancelled", TurnCancelledEvent{}},
	}

	models := readGenerated(t)["models.gen.go"]
	for _, pair := range pairs {
		t.Run(pair.wire, func(t *testing.T) {
			t.Parallel()

			event, err := decodeEvent(pair.wire, []byte(`{"type":"`+pair.wire+`"}`))
			if err != nil {
				t.Fatalf("decodeEvent(%q): %v", pair.wire, err)
			}
			if got, want := reflect.TypeOf(event), reflect.TypeOf(pair.want); got != want {
				t.Fatalf("%q decodes to %s, want %s", pair.wire, got, want)
			}

			// The name is derived from the wire type, but is not the wire type, so
			// the doc has to state it literally — that is what a caller compares
			// against a captured frame.
			name := reflect.TypeOf(event).Name()
			note := fmt.Sprintf("// %s ", name)
			doc, found := "", false
			for _, line := range strings.Split(models, "\n") {
				if strings.HasPrefix(line, note) {
					doc, found = line, true
					break
				}
			}
			if !found {
				t.Errorf("models.gen.go has no doc comment for %s", name)
			} else if doc == "" {
				t.Errorf("%s's doc comment is empty", name)
			}
			if want := fmt.Sprintf("// Wire type: %q.", pair.wire); !strings.Contains(models, want) {
				t.Errorf("models.gen.go does not state %s's wire type verbatim (%s)", name, want)
			}
		})
	}

	// And no bare name survives to be reached for by accident.
	for _, bare := range []string{
		"HeartbeatEvent", "CreatedEvent", "CompletedEvent", "FailedEvent", "CancelledEvent",
	} {
		if strings.Contains(models, "\ntype "+bare+" struct") {
			t.Errorf("models.gen.go still declares the ambiguous type %s", bare)
		}
	}
}

// serverInternals are the reference shapes the generator must keep out of the
// published doc comments: a home directory, a .py source reference, and a private
// attribute name.
var serverInternals = []*regexp.Regexp{
	regexp.MustCompile(`/Users/[A-Za-z0-9._-]+`),
	regexp.MustCompile(`/home/[A-Za-z0-9._-]+`),
	regexp.MustCompile(`/root/`),
	regexp.MustCompile(`[\w./-]+\.py\b`),
	regexp.MustCompile("`_[A-Za-z]"),
}

// TestGeneratedDocsPublishNoServerInternals is D5. openapi.json's descriptions are
// written for the people who maintain the server, so they cite emit sites, private
// attribute names and — in four example paths — a real home directory. oapi-codegen
// copies them into Go doc comments verbatim, and pkg.go.dev then publishes them.
//
// Against feat/go-client-v2 this fails on `/Users/corey/universe/src/foo` in
// SessionResponse.Workspace and on dozens of `omnigent/runtime/*.py` references.
func TestGeneratedDocsPublishNoServerInternals(t *testing.T) {
	t.Parallel()

	for name, source := range readGenerated(t) {
		for number, line := range strings.Split(source, "\n") {
			// The generator's own path is in the header it writes, and is a
			// pointer at this repository rather than at the server's internals.
			if strings.Contains(line, "scripts/gen_go_client.py") {
				continue
			}
			for _, pattern := range serverInternals {
				if match := pattern.FindString(line); match != "" {
					t.Errorf("%s:%d publishes a server internal (%q): %s",
						name, number+1, match, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestModuleShipsItsOwnLicence keeps the licence and its attribution in the
// module zip. Without them the module is not redistributable, and pkg.go.dev
// renders no licence — which is how it decides whether to publish documentation
// at all.
//
// Apache rather than anything more permissive, and a NOTICE beside it, because
// models.gen.go and events.gen.go are generated from a vendored copy of the
// Omnigent OpenAPI specification and reproduce its schema descriptions as doc
// comments. That makes the generated half a derivative of an Apache-2.0 input,
// and section 4(d) requires a derivative to carry the upstream attribution.
//
// This guard was written when the module was nested inside that Apache-2.0
// repository, where the root's files would not have travelled with the zip. It
// still earns its place now that the module is its own repository: it is what
// caught the move to a scaffolded MIT licence, which would have relicensed
// generated code the project does not own.
func TestModuleShipsItsOwnLicence(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"LICENSE", "NOTICE"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			local, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("the module ships no %s, so its zip carries none: %v", name, err)
			}
			if len(local) == 0 {
				t.Fatalf("%s is empty", name)
			}

			// When a repository root sits above this module — the nested layout
			// this began in — the two must be the same licence verbatim. Skipped
			// when the module IS the root, where there is nothing above to differ
			// from.
			root, err := os.ReadFile("../../" + name)
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("no repository root %s alongside this module; nothing to compare", name)
			}
			if err != nil {
				t.Fatalf("read ../../%s: %v", name, err)
			}
			if string(local) != string(root) {
				t.Errorf("%s differs from the repository root's; it must be the same licence, verbatim", name)
			}
		})
	}

	if licence := string(mustRead(t, "LICENSE")); !strings.Contains(licence, "Apache License") {
		t.Errorf("LICENSE is not the Apache licence the repository root carries")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

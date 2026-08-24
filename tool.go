package omnigent

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// ToolCallInfo describes one tool call the agent asked the client to run.
//
// It carries what the streamed item carries and nothing more. A response id and an
// agent name would both be useful and neither is on this wire: the call's payload
// declares call_id, name, arguments and model. Upstream draws the same line — its
// sessions-API tool info carries only what an item does.
//
// [StreamHooks.OnToolCallStart] still reports the agent name, because an observer
// wants everything the item offers. A tool does not need it to answer, and it is a
// field the server chooses, so it is not identity.
type ToolCallInfo struct {
	// Name is the tool the agent called.
	Name string

	// Arguments are the call's decoded arguments, or nil when the agent sent none
	// or they did not decode.
	Arguments map[string]any

	// CallID identifies the call, and is what the result is posted against.
	CallID string

	// ItemID is the conversation item the call arrived on, or empty when the
	// server sent none.
	ItemID string
}

// ToolFunc runs one client-side tool call.
//
// The returned string is posted to the server as the call's output. An error is
// posted too, as the output, because the server is parked waiting for one: a tool
// that fails still has to answer, or the turn never resumes. The error also
// reaches the caller, so a failure is visible rather than only recorded in a
// transcript.
type ToolFunc func(context.Context, ToolCallInfo) (string, error)

// ToolRegistry holds the tools a client will run on the agent's behalf.
//
// A registry is built before a turn and read during it. Register every tool
// first; a registry is not safe to modify while a turn is reading it.
type ToolRegistry struct {
	tools   map[string]ToolFunc
	schemas map[string]map[string]any
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:   map[string]ToolFunc{},
		schemas: map[string]map[string]any{},
	}
}

// Register adds a tool under a name, with the schema the server advertises it by.
//
// The schema is the caller's: this package does not derive one from a Go
// signature, because a derived schema is a second statement of the tool's
// contract that drifts from the first. Pass the same object the session was
// created with.
//
// Registering a name twice replaces it, so a caller can override a default.
func (r *ToolRegistry) Register(name string, schema map[string]any, run ToolFunc) error {
	if name == "" {
		return fmt.Errorf("register tool: %w: name is required", ErrInvalidArgument)
	}
	if run == nil {
		return fmt.Errorf("register tool %q: %w: run is nil", name, ErrInvalidArgument)
	}
	// A caller can write &ToolRegistry{} — the type is exported — and a nil map
	// write panics. The read paths tolerate a nil receiver; the maps are made here so
	// the zero value works too.
	if r.tools == nil {
		r.tools = map[string]ToolFunc{}
		r.schemas = map[string]map[string]any{}
	}
	r.tools[name] = run
	r.schemas[name] = maps.Clone(schema)
	return nil
}

// Names lists the registered tools, sorted, for a diagnostic that has to say what
// was available.
func (r *ToolRegistry) Names() []string {
	if r == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(r.tools))
}

// Schemas returns the registered schemas, for passing to session creation.
func (r *ToolRegistry) Schemas() []map[string]any {
	if r == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(r.schemas))
	for _, name := range r.Names() {
		if schema := r.schemas[name]; len(schema) > 0 {
			out = append(out, maps.Clone(schema))
		}
	}
	return out
}

// lookup reports the tool registered under a name.
func (r *ToolRegistry) lookup(name string) (ToolFunc, bool) {
	if r == nil {
		return nil, false
	}
	run, known := r.tools[name]
	return run, known
}

// run executes one call and returns the output to post.
//
// Always returns an output. A tool that panics, fails, or is not registered still
// has to answer the server, because the turn is parked on this call: the output
// carries the reason, and the error tells the caller.
func (r *ToolRegistry) run(ctx context.Context, info ToolCallInfo) (output string, err error) {
	// The name comes from the model by way of the server, so its length and bytes
	// are not the caller's choice. Bounded once, here, for every message below.
	safeName := sanitizeForError(info.Name, maxToolNameRunes)

	run, known := r.lookup(info.Name)
	if !known {
		// Answered to the server so the turn is not parked, and reported to the
		// caller because a server asking for a tool nobody registered is a
		// misconfigured or hostile session.
		//
		// The server is told the name it asked for and nothing else. The registry is
		// the caller's capability surface, and naming it here would let one bogus
		// call enumerate the lot.
		return fmt.Sprintf("error: no tool named %q is registered with this client", safeName),
			fmt.Errorf("%w: %q; registered: %v", ErrToolNotRegistered, safeName, r.Names())
	}

	defer func() {
		// A panicking tool is a bug in the caller's code, and the turn is still
		// parked on it. Converted rather than propagated, so one bad tool fails its
		// own call instead of killing the turn loop and every other tool in it.
		if recovered := recover(); recovered != nil {
			reason := sanitizeForError(fmt.Sprint(recovered), maxErrorFieldRunes)
			output = fmt.Sprintf("error: the tool panicked: %s", reason)
			err = fmt.Errorf("%w: tool %q panicked: %s", ErrToolFailed, safeName, reason)
		}
	}()

	output, err = run(ctx, info)
	if err != nil {
		return fmt.Sprintf("error: %s", err), fmt.Errorf("%w: tool %q: %w", ErrToolFailed, safeName, err)
	}
	return output, nil
}

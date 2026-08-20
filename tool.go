package omnigent

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// ToolCallInfo describes one tool call the agent asked the client to run.
type ToolCallInfo struct {
	// Name is the tool the agent called.
	Name string

	// Arguments are the call's decoded arguments, or nil when the agent sent none
	// or they did not decode.
	Arguments map[string]any

	// CallID identifies the call, and is what the result is posted against.
	CallID string

	// AgentName is the agent that called it, e.g. "coder.researcher".
	AgentName string

	// ResponseID is the response the call belongs to.
	ResponseID string

	// Iteration is the tool-loop pass within the response, from zero.
	Iteration int
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
	// write panics. The read paths already tolerate a nil receiver; the zero value
	// was the case they missed.
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
	run, known := r.lookup(info.Name)
	if !known {
		// Reported to the caller and answered to the server. Leaving it unanswered
		// parks the turn until its deadline, which reads as a hung agent rather
		// than as a tool this client does not have.
		return fmt.Sprintf("error: no tool named %q is registered with this client; registered: %v",
				info.Name, r.Names()),
			fmt.Errorf("%w: %q; registered: %v", ErrToolNotRegistered, info.Name, r.Names())
	}

	defer func() {
		// A panicking tool is a bug in the caller's code, and the turn is still
		// parked on it. Converted rather than propagated, so one bad tool fails its
		// own call instead of killing the turn loop and every other tool in it.
		if recovered := recover(); recovered != nil {
			output = fmt.Sprintf("error: the tool panicked: %v", recovered)
			err = fmt.Errorf("%w: tool %q panicked: %v", ErrToolFailed, info.Name, recovered)
		}
	}()

	output, err = run(ctx, info)
	if err != nil {
		return fmt.Sprintf("error: %s", err), fmt.Errorf("%w: tool %q: %w", ErrToolFailed, info.Name, err)
	}
	return output, nil
}

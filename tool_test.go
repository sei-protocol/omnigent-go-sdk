package omnigent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTheZeroRegistryIsUsable pins that a legal construction does not panic.
//
// ToolRegistry is exported, so &ToolRegistry{} is something a caller writes, and
// Register has to make its maps.
func TestTheZeroRegistryIsUsable(t *testing.T) {
	t.Parallel()

	registry := &ToolRegistry{}
	if err := registry.Register("Echo", nil, func(context.Context, ToolCallInfo) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register on a zero registry: %v", err)
	}
	if got := registry.Names(); len(got) != 1 || got[0] != "Echo" {
		t.Errorf("Names() = %v", got)
	}
}

// TestANilRegistryAnswersRatherThanPanicking pins the path a caller who registered
// no tools takes. The turn is parked on the call, so an answer is required.
func TestANilRegistryAnswersRatherThanPanicking(t *testing.T) {
	t.Parallel()

	var registry *ToolRegistry
	output, err := registry.run(context.Background(), ToolCallInfo{Name: "Absent", CallID: "c1"})
	if !errors.Is(err, ErrToolNotRegistered) {
		t.Fatalf("got %v, want ErrToolNotRegistered", err)
	}
	if !strings.Contains(output, "Absent") {
		t.Errorf("the posted output does not name the tool: %q", output)
	}
}

// TestAPanickingToolFailsItsOwnCall pins the containment: one bad tool must not end
// the turn or stop the others.
func TestAPanickingToolFailsItsOwnCall(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()
	if err := registry.Register("Boom", nil, func(context.Context, ToolCallInfo) (string, error) {
		panic("bad tool")
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	output, err := registry.run(context.Background(), ToolCallInfo{Name: "Boom", CallID: "c1"})
	if !errors.Is(err, ErrToolFailed) {
		t.Fatalf("got %v, want ErrToolFailed", err)
	}
	if !strings.Contains(output, "panicked") {
		t.Errorf("the posted output does not say what happened: %q", output)
	}
}

// TestAFailingToolStillAnswers pins that the error text is posted, because the
// server is parked on this call.
func TestAFailingToolStillAnswers(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()
	if err := registry.Register("Fails", nil, func(context.Context, ToolCallInfo) (string, error) {
		return "", errors.New("disk full")
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	output, err := registry.run(context.Background(), ToolCallInfo{Name: "Fails", CallID: "c1"})
	if !errors.Is(err, ErrToolFailed) {
		t.Fatalf("got %v, want ErrToolFailed", err)
	}
	if !strings.Contains(output, "disk full") {
		t.Errorf("the tool's reason was not posted: %q", output)
	}
}

// TestRegisterRejectsWhatItCannotRun pins the two arguments that make a
// registration meaningless.
func TestRegisterRejectsWhatItCannotRun(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()
	if err := registry.Register("", nil, func(context.Context, ToolCallInfo) (string, error) {
		return "", nil
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("an empty name returned %v", err)
	}
	if err := registry.Register("Echo", nil, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("a nil run returned %v", err)
	}
}

package omnigent

import "testing"

// TestSessionItemInterchangesWithAPlainMap pins the property the type's doc rests
// on, and the one it says it gives up.
//
// The doc justifies a defined type by claiming it still assigns both ways with a
// plain map[string]any, including the stream's own item field. That claim is the
// whole reason the methods are affordable, so it is checked here rather than
// asserted: an earlier revision justified an alias on the same interchange and was
// wrong about needing one.
func TestSessionItemInterchangesWithAPlainMap(t *testing.T) {
	t.Parallel()

	// Assignable from an unnamed map, which is what the stream event carries.
	var event OutputItemDoneEvent
	event.Item = map[string]any{"id": "fc_1", "type": "function_call"}
	var item SessionItem = event.Item
	if item.ID() != "fc_1" || item.Type() != "function_call" {
		t.Errorf("assigned from a plain map badly: id=%q type=%q", item.ID(), item.Type())
	}

	// And back, so a caller can hand one to anything taking a plain map.
	event.Item = item
	if got, _ := event.Item["id"].(string); got != "fc_1" {
		t.Errorf("assigning back to the event's field lost the value: %q", got)
	}

	// The cost the doc names: boxed in an any, this is not a map[string]any.
	var boxed any = item
	if _, ok := boxed.(map[string]any); ok {
		t.Error("a boxed SessionItem matched map[string]any; the doc says it does " +
			"not, so either the doc or the type declaration is wrong")
	}
	// Which is why the doc tells a caller to convert explicitly.
	if _, ok := any(map[string]any(item)).(map[string]any); !ok {
		t.Error("the explicit conversion the doc recommends does not work")
	}
}

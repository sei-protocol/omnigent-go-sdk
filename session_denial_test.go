package omnigent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSendInputCarriesADenial pins the fields that tell a caller why an input was
// not queued.
//
// Queued can be false for two unrelated reasons: a control input queues nothing
// by design, and a denied input was refused. Without the denial the two are
// indistinguishable, and a caller reports that the server accepted something
// without queueing it when the server had already said what was wrong.
func TestSendInputCarriesADenial(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"queued":false,"denied":true,"reason":"Denied by policy"}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	accepted, err := client.SendInput(context.Background(), "conv_1", UserMessage("review it"))
	if err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if accepted.Queued {
		t.Error("Queued = true, want false on a denial")
	}
	if !accepted.Denied {
		t.Error("Denied = false, want true: a refused send is not a control input")
	}
	if accepted.Reason != "Denied by policy" {
		t.Errorf("Reason = %q, want the server's explanation", accepted.Reason)
	}
}

// TestSendInputWithoutADenialLeavesItUnset keeps the ordinary unqueued case
// distinguishable from a refusal.
func TestSendInputWithoutADenialLeavesItUnset(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"queued":false}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	accepted, err := client.SendInput(context.Background(), "conv_1", UserMessage("review it"))
	if err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if accepted.Denied || accepted.Reason != "" {
		t.Errorf("Denied=%t Reason=%q, want both unset: nothing was refused",
			accepted.Denied, accepted.Reason)
	}
}

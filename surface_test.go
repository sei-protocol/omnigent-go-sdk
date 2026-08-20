package omnigent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestOptionsReachTheWire covers the options a caller sets and nothing else
// exercises: each one has to change a request, not just a struct field.
func TestOptionsReachTheWire(t *testing.T) {
	t.Parallel()

	var header http.Header
	server := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"conv_1"}`))
	})
	client, err := New(server,
		WithUserAgent("test-agent/1.0"),
		WithInternalClientOrigin("https://internal.example"),
		WithUnaryTimeout(30*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Sessions().Get(context.Background(), "conv_1", GetSessionOptions{}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := header.Get("User-Agent"); got != "test-agent/1.0" {
		t.Errorf("User-Agent = %q, want the option's value", got)
	}
	if got := header.Get("Origin"); got != "https://internal.example" {
		t.Errorf("Origin = %q, want the option's value", got)
	}
	if client.unary.Timeout != 30*time.Second {
		t.Errorf("unary timeout = %v, want 30s", client.unary.Timeout)
	}
	// The streaming client shares the transport and drops the timeout, because a
	// whole-exchange deadline would sever a healthy stream.
	if client.stream.Timeout != 0 {
		t.Errorf("stream timeout = %v, want 0", client.stream.Timeout)
	}
}

// TestInsecureCredentialTransportIsTheOnlyWayToCarryOneInClear pins the opt-in.
func TestInsecureCredentialTransportIsTheOnlyWayToCarryOneInClear(t *testing.T) {
	t.Parallel()

	// A credential over plain http to a non-loopback host is refused by default.
	if _, err := New("http://sdk.example.test", WithBearerToken("secret")); err == nil {
		t.Fatal("New = nil error for a credential over plaintext to a remote host")
	}
	if _, err := New("http://sdk.example.test",
		WithBearerToken("secret"), WithInsecureCredentialTransport()); err != nil {
		t.Errorf("New with the explicit opt-in: %v", err)
	}
}

// TestNewRefusesAUserinfoBaseURLWithoutQuotingIt covers the redaction on the one
// path where a base URL can hide a password.
func TestNewRefusesAUserinfoBaseURLWithoutQuotingIt(t *testing.T) {
	t.Parallel()

	_, err := New("https://svc:s3cr3t-password@sdk.example.test")
	if err == nil {
		t.Fatal("New = nil error for a base URL carrying userinfo")
	}
	if strings.Contains(err.Error(), "s3cr3t-password") {
		t.Errorf("the error quotes the password back:\n  %v", err)
	}
	if !strings.Contains(err.Error(), "sdk.example.test") {
		t.Errorf("the error names no host, so a caller cannot tell which URL: %v", err)
	}
}

// TestNewReportsAnUnparseableBaseURLWithoutQuotingIt covers the same redaction on
// the parse-failure path, where url.Parse's own error quotes the input.
func TestNewReportsAnUnparseableBaseURLWithoutQuotingIt(t *testing.T) {
	t.Parallel()

	_, err := New("https://svc:s3cr3t-password@sdk.example.test:notaport/")
	if err == nil {
		t.Fatal("New = nil error for an unparseable base URL")
	}
	if strings.Contains(err.Error(), "s3cr3t-password") {
		t.Errorf("the parse failure quotes the password back:\n  %v", err)
	}
}

// TestErrorInterfaceCarriesTheStatus covers branching on status without naming a
// concrete type, which is why the interface exists.
func TestErrorInterfaceCarriesTheStatus(t *testing.T) {
	t.Parallel()

	server := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
	})
	client, err := New(server)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Sessions().Get(context.Background(), "conv_1", GetSessionOptions{})
	if err == nil {
		t.Fatal("Get = nil error for a 429")
	}
	var apiErr Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error does not satisfy Error: %v", err)
	}
	if apiErr.Status() != http.StatusTooManyRequests {
		t.Errorf("Status() = %d, want 429", apiErr.Status())
	}
}

// TestDecodeEventTakesTheDiscriminatorFromTheFrame covers the exported entry
// point, which differs from the internal one by finding the type itself.
func TestDecodeEventTakesTheDiscriminatorFromTheFrame(t *testing.T) {
	t.Parallel()

	event, err := DecodeEvent([]byte(`{"type":"session.status","conversation_id":"conv_1","status":"idle"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	status, ok := event.(SessionStatusEvent)
	if !ok {
		t.Fatalf("event is %T, want SessionStatusEvent", event)
	}
	if status.Status != SessionStatusEventStatusIdle {
		t.Errorf("status = %q, want %q", status.Status, SessionStatusEventStatusIdle)
	}

	if _, err := DecodeEvent([]byte(`{"conversation_id":"conv_1"}`)); err == nil {
		t.Error("DecodeEvent = nil error for a frame with no discriminator")
	}
	unknown, err := DecodeEvent([]byte(`{"type":"session.something.new"}`))
	if err != nil {
		t.Fatalf("DecodeEvent on an unknown type: %v", err)
	}
	if _, ok := unknown.(UnknownEvent); !ok {
		t.Errorf("unknown frame decoded to %T, want UnknownEvent so an older client keeps streaming", unknown)
	}
}

// TestSessionFilesNamesItsSession covers the accessor a caller uses to log which
// session a handle is bound to.
func TestSessionFilesNamesItsSession(t *testing.T) {
	t.Parallel()

	client, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.Files().ForSession("conv_7").SessionID(); got != "conv_7" {
		t.Errorf("SessionID() = %q, want conv_7", got)
	}
}

// TestListOptionsRenderTheirQuery covers the query builders, which are the one
// place a listing option can silently not reach the server.
func TestListOptionsRenderTheirQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query url.Values
		want  map[string]string
	}{
		{
			name:  "sessions",
			query: ListSessionsOptions{Limit: 25, Order: SortAscending, SortBy: SessionSortByUpdatedAt}.query(),
			want:  map[string]string{"limit": "25", "order": "asc", "sort_by": "updated_at"},
		},
		{
			name:  "agents",
			query: ListAgentsOptions{Limit: 10, Order: SortDescending}.query(),
			want:  map[string]string{"limit": "10", "order": "desc"},
		},
		{
			name:  "files",
			query: ListFilesOptions{Limit: 5, Order: SortAscending}.query(),
			want:  map[string]string{"limit": "5", "order": "asc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for key, want := range tc.want {
				if got := tc.query.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// newRecordingServer starts a server for the duration of one test and returns its
// base URL.
func newRecordingServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

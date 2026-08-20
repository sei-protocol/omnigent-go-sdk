package omnigent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        []error
		notWant     []error
		wantCode    string
		wantMessage string
	}{
		{
			name:        "structured envelope",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"error":{"code":"not_found","message":"no such session"}}`,
			want:        []error{ErrNotFound},
			notWant:     []error{ErrForbidden, ErrServer},
			wantCode:    "not_found",
			wantMessage: "no such session",
		},
		{
			name:        "unstructured detail from a media-type guard",
			status:      http.StatusUnsupportedMediaType,
			contentType: "application/json",
			body:        `{"detail":"Unsupported Media Type"}`,
			want:        nil,
			notWant:     []error{ErrValidation, ErrServer},
			wantMessage: "Unsupported Media Type",
		},
		{
			name:        "validation detail is a list, not a string",
			status:      http.StatusUnprocessableEntity,
			contentType: "application/json",
			body:        `{"detail":[{"type":"missing","loc":["body","agent_id"],"msg":"Field required"}]}`,
			want:        []error{ErrValidation},
			notWant:     []error{ErrInvalidInput},
		},
		{
			name:    "unauthorized",
			status:  http.StatusUnauthorized,
			body:    `{"error":{"code":"unauthorized","message":"no identity"}}`,
			want:    []error{ErrUnauthorized},
			notWant: []error{ErrForbidden},
		},
		{
			name:    "forbidden",
			status:  http.StatusForbidden,
			body:    `{"detail":"not permitted"}`,
			want:    []error{ErrForbidden},
			notWant: []error{ErrNotFound},
		},
		{
			name:    "invalid input",
			status:  http.StatusBadRequest,
			body:    `{"error":{"code":"invalid_input","message":"bad workspace"}}`,
			want:    []error{ErrInvalidInput},
			notWant: []error{ErrValidation},
		},
		{
			name:    "conflict",
			status:  http.StatusConflict,
			body:    `{"error":{"code":"already_exists","message":"taken"}}`,
			want:    []error{ErrConflict},
			notWant: []error{ErrInvalidInput},
		},
		{
			name:    "harness not configured",
			status:  http.StatusPreconditionFailed,
			body:    `{"error":{"code":"harness_not_configured","message":"none"}}`,
			want:    []error{ErrHarnessNotConfigured},
			notWant: []error{ErrServer},
		},
		{
			name:    "unavailable is also a server error",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"code":"runner_unavailable","message":"no runner"}}`,
			want:    []error{ErrUnavailable, ErrServer},
			notWant: []error{ErrConflict},
		},
		{
			name:    "internal error",
			status:  http.StatusInternalServerError,
			body:    `{"error":{"code":"internal_error","message":"boom"}}`,
			want:    []error{ErrServer},
			notWant: []error{ErrUnavailable},
		},
		{
			name:        "a proxy answering with HTML is not fatal to decoding",
			status:      http.StatusBadGateway,
			contentType: "text/html",
			body:        "<html><body>gateway</body></html>",
			want:        []error{ErrServer},
			notWant:     []error{ErrNotFound},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client, err := New(server.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			var out map[string]any
			err = client.doJSON(t.Context(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)
			if err == nil {
				t.Fatal("doJSON = nil error, want a failure")
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v does not unwrap to *APIError", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if tc.wantCode != "" && apiErr.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", apiErr.Code, tc.wantCode)
			}
			if tc.wantMessage != "" && apiErr.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMessage)
			}
			if string(apiErr.Body) != tc.body {
				t.Errorf("Body = %q, want %q", apiErr.Body, tc.body)
			}
			for _, sentinel := range tc.want {
				if !errors.Is(err, sentinel) {
					t.Errorf("errors.Is(err, %v) = false, want true", sentinel)
				}
			}
			for _, sentinel := range tc.notWant {
				if errors.Is(err, sentinel) {
					t.Errorf("errors.Is(err, %v) = true, want false", sentinel)
				}
			}
			if !strings.Contains(err.Error(), "omnigent:") {
				t.Errorf("error text %q does not identify itself", err)
			}
		})
	}
}

func TestAPIErrorValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		detail    string
		wantCount int
		wantErr   bool
	}{
		{name: "absent detail asks nothing of the caller"},
		{
			name:      "a 422 list decodes",
			detail:    `[{"type":"missing","loc":["body","agent_id"],"msg":"Field required"}]`,
			wantCount: 1,
		},
		{name: "a string detail is not a list", detail: `"nope"`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			apiErr := &APIError{StatusCode: http.StatusUnprocessableEntity}
			if tc.detail != "" {
				apiErr.Detail = []byte(tc.detail)
			}
			details, err := apiErr.ValidationErrors()
			if tc.wantErr {
				if err == nil {
					t.Fatal("ValidationErrors = nil error, want one")
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidationErrors: %v", err)
			}
			if len(details) != tc.wantCount {
				t.Fatalf("got %d details, want %d", len(details), tc.wantCount)
			}
			if tc.wantCount > 0 && details[0].Msg != "Field required" {
				t.Errorf("Msg = %q, want %q", details[0].Msg, "Field required")
			}
		})
	}
}

func TestAPIErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *APIError
		want string
	}{
		{
			name: "code and message",
			err:  &APIError{StatusCode: 404, Code: "not_found", Message: "gone"},
			want: "omnigent: 404 Not Found: not_found: gone",
		},
		{
			name: "message only",
			err:  &APIError{StatusCode: 415, Message: "Unsupported Media Type"},
			want: "omnigent: 415 Unsupported Media Type: Unsupported Media Type",
		},
		{
			// The body is described, never rendered: a 502 body is a proxy's, not
			// this API's, and Error()'s return value is what reaches a log.
			name: "reports an unstructured body without quoting it",
			err:  &APIError{StatusCode: 502, ContentType: "text/html", Body: []byte("<html>")},
			want: "omnigent: 502 Bad Gateway: no error envelope, 6-byte text/html body withheld (see APIError.Body)",
		},
		{
			name: "an unstructured body with no content type still reports its size",
			err:  &APIError{StatusCode: 502, Body: []byte("<html>")},
			want: "omnigent: 502 Bad Gateway: no error envelope, 6-byte untyped body withheld (see APIError.Body)",
		},
		{
			name: "falls back to the status alone",
			err:  &APIError{StatusCode: 503},
			want: "omnigent: 503 Service Unavailable",
		},
		{
			name: "an unknown status still renders",
			err:  &APIError{StatusCode: 599},
			want: "omnigent: 599",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAPIErrorMessageNeverRendersTheBody replaces the truncation test that used
// to live here. Truncating a body preview bounded how much of a server response
// reached a log line; not rendering it bounds it at none, which is the point:
// what the message says about the body no longer depends on the body's bytes.
func TestAPIErrorMessageNeverRendersTheBody(t *testing.T) {
	t.Parallel()

	// A multi-byte body longer than any preview bound, so a leak of any prefix
	// shows up, and short enough that a naive test could still miss it.
	body := strings.Repeat("é", 4096)
	apiErr := &APIError{StatusCode: 500, Body: []byte(body)}
	message := apiErr.Error()

	if strings.Contains(message, "é") {
		t.Errorf("message %q renders the response body", message)
	}
	if len(message) > 256 {
		t.Errorf("message is %d bytes; it should be a fixed-shape summary", len(message))
	}
	if !strings.Contains(message, "8192-byte") {
		t.Errorf("message %q does not say how much body arrived", message)
	}
	if string(apiErr.Body) != body {
		t.Error("Body was not retained; a caller who needs to inspect it cannot")
	}
}

// TestAPIErrorDoesNotCopyCredentialsIntoLogLines is the finding itself. An auth
// proxy in front of the server answers a non-2xx with a body and headers of its
// own choosing, and neither is this API's: Error() must not lift either into the
// one string that reliably reaches a log aggregator, and the header set kept for
// diagnostics must not carry the cookie the proxy just minted.
func TestAPIErrorDoesNotCopyCredentialsIntoLogLines(t *testing.T) {
	t.Parallel()

	const leaked = "eyJhbGciOiJIUzI1NiJ9.THIS-MUST-NOT-BE-LOGGED"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Set-Cookie", "proxy_session="+leaked+"; Path=/")
		w.Header().Set("X-Request-Id", "req_abc123")
		w.Header().Set("X-Ratelimit-Remaining", "17")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<html><input name="csrf" value="`+leaked+`"></html>`)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out map[string]any
	err = client.doJSON(t.Context(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v does not unwrap to *APIError", err)
	}
	if strings.Contains(err.Error(), leaked) {
		t.Errorf("the error message carries a credential from the response body: %q", err)
	}
	if got := apiErr.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie = %q, want it dropped: it mints a client credential", got)
	}
	if got := apiErr.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie lines = %q, want none", got)
	}

	// The diagnostics that made the header set worth keeping still work, and so
	// do the status and the request id.
	if got := apiErr.Header.Get("X-Ratelimit-Remaining"); got != "17" {
		t.Errorf("X-Ratelimit-Remaining = %q, want 17: a non-credential header was lost", got)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.RequestID != "req_abc123" {
		t.Errorf("RequestID = %q, want req_abc123", apiErr.RequestID)
	}
	if !strings.Contains(err.Error(), "req_abc123") {
		t.Errorf("message %q omits the request id, so a copied error cannot be traced", err)
	}
}

func TestAPIErrorRetainsTheRequestID(t *testing.T) {
	t.Parallel()

	// The server's middleware stamps X-Request-Id on every response and logs it
	// alongside the failure, so it is the handle that ties a client-side error
	// to the server's own record of it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_abc123")
		w.Header().Set("X-Deployment-Hint", "edge-1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"runner_unavailable","message":"no runner"}}`)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out map[string]any
	err = client.doJSON(t.Context(), http.MethodGet, []string{"v1", "sessions", "conv_1"}, nil, nil, &out)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v does not unwrap to *APIError", err)
	}
	if apiErr.RequestID != "req_abc123" {
		t.Errorf("RequestID = %q, want %q", apiErr.RequestID, "req_abc123")
	}
	if got := apiErr.Header.Get("X-Deployment-Hint"); got != "edge-1" {
		t.Errorf("Header X-Deployment-Hint = %q, want edge-1; the header set was discarded", got)
	}
	if !strings.Contains(err.Error(), "req_abc123") {
		t.Errorf("message %q omits the request id, so a copied error cannot be traced", err)
	}
}

func TestAPIErrorMessageOmitsAnAbsentRequestID(t *testing.T) {
	t.Parallel()

	apiErr := &APIError{StatusCode: 503, Code: "runner_unavailable", Message: "no runner"}
	want := "omnigent: 503 Service Unavailable: runner_unavailable: no runner"
	if got := apiErr.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestErrorRendersNoServerControlBytes pins the sanitizer on both paths that
// render server-chosen text: [APIError.Error]'s title and message, and the
// stream reader's frame preview.
//
// An error string is what a caller logs, and a log line is a record other tools
// parse. A raw newline lets the server forge a second line; a raw escape drives
// the reader's terminal; an unbounded field floods the log.
func TestErrorRendersNoServerControlBytes(t *testing.T) {
	t.Parallel()

	forged := "ok\n2026-08-19T12:00:00Z INFO driver: audit \x1b[1;32mPASS\x1b[0m"

	t.Run("a forged log line in the title", func(t *testing.T) {
		t.Parallel()
		got := (&APIError{StatusCode: 412, Title: forged, Message: "m"}).Error()
		for _, banned := range []string{"\n", "\r", "\x1b", "\x00"} {
			if strings.Contains(got, banned) {
				t.Errorf("Error() renders %q:\n  %q", banned, got)
			}
		}
		if !strings.Contains(got, "PASS") {
			t.Errorf("Error() dropped the readable text entirely: %q", got)
		}
	})

	t.Run("an unbounded title", func(t *testing.T) {
		t.Parallel()
		got := (&APIError{StatusCode: 500, Title: strings.Repeat("A", 60<<10)}).Error()
		if len(got) > 512 {
			t.Errorf("Error() is %d bytes; a server-chosen field is not bounded", len(got))
		}
	})

	t.Run("control bytes in a frame preview", func(t *testing.T) {
		t.Parallel()
		got := bodyPreview([]byte("\x1b[2Knot json\nforged: line"))
		for _, banned := range []string{"\n", "\x1b"} {
			if strings.Contains(got, banned) {
				t.Errorf("bodyPreview renders %q:\n  %q", banned, got)
			}
		}
	})

	t.Run("invalid utf-8 does not eat the whole preview", func(t *testing.T) {
		t.Parallel()
		got := bodyPreview(append([]byte{0xff}, []byte(strings.Repeat("A", 400))...))
		if !strings.Contains(got, "AAAA") {
			t.Errorf("one invalid byte discarded the readable preview: %q", got)
		}
	})
}

package omnigent

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// endlessString reads as a JSON object whose one value never ends.
//
// A fixture that allocates the body would measure the test's memory rather than
// the decoder's: the point is that the bound stops reading, so the reader has to
// be able to outlast it without holding anything.
type endlessString struct{ sent int64 }

func (e *endlessString) Read(p []byte) (int, error) {
	const prefix = `{"padding":"`
	n := 0
	for n < len(p) {
		if e.sent < int64(len(prefix)) {
			p[n] = prefix[e.sent]
		} else {
			p[n] = 'x'
		}
		e.sent++
		n++
	}
	return n, nil
}

func TestDecodeRefusesABodyOverTheCap(t *testing.T) {
	t.Parallel()

	var out struct{}
	err := decodeBounded(&endlessString{}, &out, http.MethodGet, "/v1/sessions/s")

	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("decoding an endless body returned %v, want ErrResponseTooLarge", err)
	}
	// The message has to name the limit, because a caller who hits it cannot see
	// the body and has nothing else to reason from.
	if !strings.Contains(err.Error(), "over 33554432 bytes") {
		t.Errorf("error does not name the cap: %v", err)
	}
}

func TestDecodeAcceptsABodyUnderTheCap(t *testing.T) {
	t.Parallel()

	var out struct {
		Object string `json:"object"`
	}
	body := strings.NewReader(`{"object":"conversation","padding":"` +
		strings.Repeat("x", 1<<20) + `"}`)

	if err := decodeBounded(body, &out, http.MethodGet, "/v1/sessions/s"); err != nil {
		t.Fatalf("decoding a 1 MiB body: %v", err)
	}
	if out.Object != "conversation" {
		t.Errorf("Object = %q, want %q", out.Object, "conversation")
	}
}

// TestDecodeReportsARealDecodeErrorAsItself pins that the cap check does not
// swallow the ordinary failure.
//
// The over-cap branch fires on the reader running dry, so a malformed body that
// happens to be short must still surface its own error rather than being
// reported as too large.
func TestDecodeReportsARealDecodeErrorAsItself(t *testing.T) {
	t.Parallel()

	var out struct{}
	err := decodeBounded(strings.NewReader(`{"object":`), &out, http.MethodGet, "/v1/x")

	if err == nil {
		t.Fatal("decoding truncated JSON returned no error")
	}
	if errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("a short malformed body was reported as too large: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("want the decoder's own error, got %v", err)
	}
}

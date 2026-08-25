package omnigent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoticeNamesEveryFileCarryingUpstreamProse keeps NOTICE exhaustive.
//
// Doc comments in this package reproduce the vendored description's own schema
// and property text, which makes those files derivative work under the upstream
// Apache-2.0 licence, and NOTICE names them. The list is a measurement, not a
// convention: a file that starts carrying upstream prose has to be added, and one
// that stops has to be removed.
//
// Checked in both directions, because either error is wrong. An unnamed file is
// unattributed reproduction; a named file that carries none overstates what this
// module borrowed.
//
// A description must be at least 45 runes to count, which is long enough that a
// match is reproduction rather than two people describing one field the same way.
func TestNoticeNamesEveryFileCarryingUpstreamProse(t *testing.T) {
	descriptions := upstreamDescriptions(t)
	if len(descriptions) < 100 {
		t.Fatalf("found only %d usable descriptions in the spec; the extraction is broken", len(descriptions))
	}

	notice, err := os.ReadFile("NOTICE")
	if err != nil {
		t.Fatalf("read NOTICE: %v", err)
	}

	// internal/api is in scope because the generated file is where the upstream
	// descriptions now land. A scan of the root package alone would report the
	// attribution as complete while the file carrying the prose went unnamed.
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	generated, err := filepath.Glob("internal/api/*.go")
	if err != nil {
		t.Fatalf("glob internal/api: %v", err)
	}
	sources = append(sources, generated...)
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		carried := carriedDescriptionCount(t, path, descriptions)
		named := strings.Contains(string(notice), path)
		switch {
		case carried > 0 && !named:
			t.Errorf("%s reproduces %d upstream descriptions, and NOTICE does not name it",
				path, carried)
		case carried == 0 && named:
			t.Errorf("NOTICE names %s, which reproduces none; the list must stay exhaustive in both directions",
				path)
		}
	}
}

// commentText is every comment line in a Go source file, joined, so a
// description that wraps across lines still matches.
var commentText = regexp.MustCompile(`(?m)^\s*//\s?`)

// upstreamDescriptions returns the description strings the vendored document
// declares, normalised and long enough that a coincidental match is implausible.
func upstreamDescriptions(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile("spec/openapi.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Description string `json:"description"`
				Properties  map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode spec: %v", err)
	}

	var out []string
	add := func(text string) {
		// 45 runes is long enough that a match is reproduction, not coincidence.
		if normalised := normaliseProse(text); len(normalised) >= 45 {
			out = append(out, normalised[:45])
		}
	}
	for _, schema := range doc.Components.Schemas {
		add(schema.Description)
		for _, prop := range schema.Properties {
			add(prop.Description)
		}
	}
	return out
}

// carriedDescriptionCount reports how many upstream descriptions appear in one
// file's comments.
func carriedDescriptionCount(t *testing.T, path string, descriptions []string) int {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var comments strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
			comments.WriteString(commentText.ReplaceAllString(line, ""))
			comments.WriteString(" ")
		}
	}
	haystack := normaliseProse(comments.String())

	// Case-folded, because the house doc-comment form lowercases the first letter
	// of the description it reproduces: an upstream "Machine-readable error
	// information..." becomes "// ErrorDetail is machine-readable error
	// information...". A case-sensitive match reads that as carrying nothing, and
	// this test's answer decides whether NOTICE names the file.
	haystack = strings.ToLower(haystack)
	count := 0
	for _, description := range descriptions {
		if strings.Contains(haystack, strings.ToLower(description)) {
			count++
		}
	}
	return count
}

// normaliseProse collapses whitespace and drops the markup that differs between
// a JSON description and a Go comment, so the comparison is about words.
func normaliseProse(text string) string {
	text = strings.ReplaceAll(text, "`", "")
	return strings.Join(strings.Fields(text), " ")
}

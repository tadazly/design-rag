package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealCorpusDateParity is opt-in because the source corpus is not part of
// the repository. The expected dates name real documents, so they live in a
// local, untracked JSON file (DRAG_DATE_PARITY_CASES, default
// tests/.tmp/real-corpus/date-parity-cases.json) instead of this file.
func TestRealCorpusDateParity(t *testing.T) {
	root := os.Getenv("DRAG_DATE_PARITY_ROOT")
	if root == "" {
		t.Skip("set DRAG_DATE_PARITY_ROOT to run the read-only real-corpus date parity gate")
	}
	casesPath := os.Getenv("DRAG_DATE_PARITY_CASES")
	if casesPath == "" {
		casesPath = filepath.Join("..", "..", "tests", ".tmp", "real-corpus", "date-parity-cases.json")
	}
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatalf("read local date parity cases: %v", err)
	}
	var tests []struct {
		Relative string `json:"relativePath"`
		Want     string `json:"expected"`
		Source   string `json:"dateSource"`
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatalf("parse local date parity cases %s: %v", casesPath, err)
	}
	if len(tests) == 0 {
		t.Fatalf("local date parity cases %s are empty", casesPath)
	}
	for _, test := range tests {
		t.Run(test.Relative, func(t *testing.T) {
			relative := filepath.FromSlash(test.Relative)
			absolutePath := filepath.Join(root, relative)
			info, err := os.Stat(absolutePath)
			if err != nil {
				t.Fatal(err)
			}
			candidate := Candidate{
				SourceKind:        "design",
				AbsolutePath:      absolutePath,
				RelativePath:      relative,
				Extension:         strings.ToLower(filepath.Ext(absolutePath)),
				FilesystemMtimeMS: info.ModTime().UnixMilli(),
			}
			document, needsFallback, extractErr := extractByExtension(candidate)
			if extractErr != nil {
				t.Fatal(extractErr)
			}
			if needsFallback {
				t.Fatal("date parity fixture unexpectedly requires TypeScript fallback")
			}
			want, parseErr := time.Parse(time.RFC3339, test.Want)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			got := ResolveEffectiveDate(candidate, document)
			if got.DateSource != test.Source || got.EffectiveUpdatedAtMS != want.UnixMilli() {
				t.Fatalf("date = %#v, want %s from %s", got, test.Want, test.Source)
			}
		})
	}
}

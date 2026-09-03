package core

import (
	"strings"
	"testing"
)

func TestUTF16SliceNeverEmitsReplacementForSplitSurrogate(t *testing.T) {
	for _, bounds := range [][2]int{{0, 1}, {1, 1}, {1, 2}, {0, 2}} {
		value, err := utf16Slice("🙂x", bounds[0], bounds[1])
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(value, '\uFFFD') {
			t.Fatalf("slice %v emitted replacement character: %q", bounds, value)
		}
	}
}

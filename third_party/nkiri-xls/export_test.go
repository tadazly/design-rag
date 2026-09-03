package xls

// NewSheetForTest creates a Sheet with the given name for use in tests.
func NewSheetForTest(name string) *Sheet {
	return &Sheet{name: name}
}

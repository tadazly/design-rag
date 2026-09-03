package xls

// Style holds formatting attributes for a cell.
type Style struct {
	FontIndex   int
	FormatIndex int // Number format index
	XFIndex     int // Extended format index
}

// Font holds font information.
type Font struct {
	Name      string
	Size      float64 // in points
	Bold      bool
	Italic    bool
	Underline bool
	Color     uint32 // RGB
}

// NumberFormat holds a number format string.
type NumberFormat struct {
	Index  int
	Format string
}

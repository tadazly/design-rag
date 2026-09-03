package xls

// Row represents a single row in a worksheet.
type Row struct {
	Index int
	cells []*Cell
}

// Cell returns the cell at the given 0-based column index, or nil if empty.
func (r *Row) Cell(col int) *Cell {
	if r == nil || col < 0 || col >= len(r.cells) {
		return nil
	}
	return r.cells[col]
}

// CellCount returns the number of cells in the row.
func (r *Row) CellCount() int {
	if r == nil {
		return 0
	}
	return len(r.cells)
}

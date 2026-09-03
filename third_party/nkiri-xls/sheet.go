package xls

// Sheet represents a single worksheet within a workbook.
type Sheet struct {
	name string
	rows []*Row
}

// Name returns the sheet name.
func (s *Sheet) Name() string {
	return s.name
}

// Row returns the row at the given 0-based index, or nil if it does not exist.
func (s *Sheet) Row(index int) *Row {
	if index < 0 || index >= len(s.rows) {
		return nil
	}
	return s.rows[index]
}

// RowCount returns the number of rows in the sheet.
func (s *Sheet) RowCount() int {
	return len(s.rows)
}

// Strings returns all cell values as a 2-D slice of strings.
//
// Each element is the result of Cell.Value(): numbers, booleans, dates, and
// errors are converted to their human-readable form; empty/nil cells become
// empty strings.  Trailing empty columns within a row are preserved up to the
// row's last non-empty column, and trailing empty rows at the bottom of the
// sheet are omitted.
func (s *Sheet) Strings() [][]string {
	out := make([][]string, len(s.rows))
	for i, r := range s.rows {
		if r == nil {
			out[i] = []string{}
			continue
		}
		row := make([]string, len(r.cells))
		for j, c := range r.cells {
			row[j] = c.Value()
		}
		out[i] = row
	}
	// Drop trailing all-empty rows
	end := len(out)
	for end > 0 && isEmptyRow(out[end-1]) {
		end--
	}
	return out[:end]
}

// isEmptyRow reports whether every element in row is an empty string.
func isEmptyRow(row []string) bool {
	for _, v := range row {
		if v != "" {
			return false
		}
	}
	return true
}

// setCell places c at (row, col), growing the internal slices as needed.
// This is used by the BIFF8 decoder; it is not part of the public API.
func (s *Sheet) setCell(row, col int, c *Cell) {
	for len(s.rows) <= row {
		s.rows = append(s.rows, nil)
	}
	if s.rows[row] == nil {
		s.rows[row] = &Row{Index: row}
	}
	r := s.rows[row]
	for len(r.cells) <= col {
		r.cells = append(r.cells, nil)
	}
	r.cells[col] = c
}

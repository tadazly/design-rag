package xls

// Workbook represents an XLS workbook.
type Workbook struct {
	Sheets []*Sheet
}

// Sheet returns the sheet at the given index (0-based).
func (wb *Workbook) Sheet(index int) *Sheet {
	if index < 0 || index >= len(wb.Sheets) {
		return nil
	}
	return wb.Sheets[index]
}

// SheetByName returns the first sheet with the given name, or nil if not found.
func (wb *Workbook) SheetByName(name string) *Sheet {
	for _, s := range wb.Sheets {
		if s.name == name {
			return s
		}
	}
	return nil
}

// SheetCount returns the number of sheets in the workbook.
func (wb *Workbook) SheetCount() int {
	return len(wb.Sheets)
}

// SheetList returns the names of all sheets in the workbook.
func (wb *Workbook) SheetList() []string {
	names := make([]string, len(wb.Sheets))
	for i, s := range wb.Sheets {
		names[i] = s.name
	}
	return names
}

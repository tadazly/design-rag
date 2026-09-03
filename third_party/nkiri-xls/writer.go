package xls

import (
	"fmt"
	"io"
	"os"
)

// Save writes the workbook to the file at path, creating or truncating it.
func (wb *Workbook) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return wb.Write(f)
}

// Write serialises the workbook in XLS format to w.
func (wb *Workbook) Write(w io.Writer) error {
	return fmt.Errorf("xls: write not implemented")
}

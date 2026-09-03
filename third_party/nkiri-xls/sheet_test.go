package xls_test

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/nkiri/xls"
	"github.com/nkiri/xls/internal/biff"
)

// TestSheet_Strings verifies Sheet.Strings() via a real XLS round-trip.
func TestSheet_Strings(t *testing.T) {
	biffData := buildBIFF8ForStrings(t)
	cfbData := wrapCFBForStrings(t, biffData)

	wb, err := xls.Read(bytes.NewReader(cfbData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if wb.SheetCount() == 0 {
		t.Fatal("no sheets")
	}

	got := wb.Sheets[0].Strings()
	want := [][]string{
		{"hello", "world"},
		{"42"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Strings() =\n%v\nwant\n%v", got, want)
	}
}

// TestSheet_Strings_Empty verifies that an all-empty sheet returns empty.
func TestSheet_Strings_Empty(t *testing.T) {
	sh := &xls.Sheet{}
	got := sh.Strings()
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestSheet_RowCount checks Sheet.RowCount() via a real workbook.
func TestSheet_RowCount(t *testing.T) {
	biffData := buildBIFF8ForStrings(t)
	cfbData := wrapCFBForStrings(t, biffData)
	wb, err := xls.Read(bytes.NewReader(cfbData))
	if err != nil {
		t.Fatal(err)
	}
	sh := wb.Sheet(0)
	if sh == nil {
		t.Fatal("Sheet(0) is nil")
	}
	// buildBIFF8ForStrings creates rows 0 and 1
	if got := sh.RowCount(); got != 2 {
		t.Errorf("RowCount() = %d, want 2", got)
	}
}

// TestSheet_Row_OutOfBounds verifies nil is returned for invalid row indices.
func TestSheet_Row_OutOfBounds(t *testing.T) {
	sh := &xls.Sheet{}
	if sh.Row(0) != nil {
		t.Error("Row(0) on empty sheet should be nil")
	}
	if sh.Row(-1) != nil {
		t.Error("Row(-1) should be nil")
	}
}

// TestRow_CellCount checks Row.CellCount() and nil-row behaviour.
func TestRow_CellCount(t *testing.T) {
	biffData := buildBIFF8ForStrings(t)
	cfbData := wrapCFBForStrings(t, biffData)
	wb, err := xls.Read(bytes.NewReader(cfbData))
	if err != nil {
		t.Fatal(err)
	}
	sh := wb.Sheet(0)

	// Row 0 has 2 cells (A1="hello", B1="world")
	row := sh.Row(0)
	if row == nil {
		t.Fatal("Row(0) is nil")
	}
	if got := row.CellCount(); got != 2 {
		t.Errorf("Row(0).CellCount() = %d, want 2", got)
	}

	// Nil row returns 0
	var nilRow *xls.Row
	if got := nilRow.CellCount(); got != 0 {
		t.Errorf("nil.CellCount() = %d, want 0", got)
	}
}

// TestSheet_Strings_WithFormulas verifies that Strings() returns computed
// values (not empty strings) for formula cells.
func TestSheet_Strings_WithFormulas(t *testing.T) {
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0))
	gw.WriteRecord(biff.RecSST, sstRecord(nil))
	bsOff := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))
	gw.WriteEmpty(biff.RecEOF)
	glob := globBuf.Bytes()

	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	// Row 0: numeric formula (100), string formula ("result")
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 0, 0, formulaNumericResult(100)))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 1, 0, formulaStringResult()))
	sw.WriteRecord(biff.RecString, stringRecord("result"))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	full := append(glob, shBuf.Bytes()...)

	xlsData := wrapCFBForStrings(t, full)
	wb, err := xls.Read(bytes.NewReader(xlsData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := [][]string{{"100", "result"}}
	got := wb.Sheet(0).Strings()
	if len(got) != 1 || len(got[0]) < 2 || got[0][0] != "100" || got[0][1] != "result" {
		t.Errorf("Strings() = %v, want %v", got, want)
	}
}

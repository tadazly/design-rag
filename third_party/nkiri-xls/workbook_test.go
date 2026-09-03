package xls_test

import (
	"testing"

	"github.com/nkiri/xls"
)

func TestWorkbookSheetCount(t *testing.T) {
	wb := &xls.Workbook{
		Sheets: []*xls.Sheet{},
	}
	if wb.SheetCount() != 0 {
		t.Fatalf("expected 0 sheets, got %d", wb.SheetCount())
	}
}

func TestWorkbook_Sheet_OutOfBounds(t *testing.T) {
	wb := &xls.Workbook{
		Sheets: []*xls.Sheet{xls.NewSheetForTest("A")},
	}
	if wb.Sheet(-1) != nil {
		t.Error("Sheet(-1) should be nil")
	}
	if wb.Sheet(1) != nil {
		t.Error("Sheet(1) should be nil (only index 0 exists)")
	}
	if wb.Sheet(0) == nil {
		t.Error("Sheet(0) should not be nil")
	}
}

func TestWorkbook_SheetByName_Found(t *testing.T) {
	s1 := xls.NewSheetForTest("Alpha")
	s2 := xls.NewSheetForTest("Beta")
	wb := &xls.Workbook{Sheets: []*xls.Sheet{s1, s2}}

	got := wb.SheetByName("Beta")
	if got != s2 {
		t.Errorf("SheetByName(Beta) = %v, want Beta sheet", got)
	}
}

func TestWorkbook_SheetByName_NotFound(t *testing.T) {
	wb := &xls.Workbook{
		Sheets: []*xls.Sheet{xls.NewSheetForTest("Alpha")},
	}
	if got := wb.SheetByName("Missing"); got != nil {
		t.Errorf("SheetByName(Missing) = %v, want nil", got)
	}
}

func TestWorkbook_SheetList(t *testing.T) {
	wb := &xls.Workbook{
		Sheets: []*xls.Sheet{
			xls.NewSheetForTest("Sheet1"),
			xls.NewSheetForTest("Sheet2"),
			xls.NewSheetForTest("Sheet3"),
		},
	}
	got := wb.SheetList()
	want := []string{"Sheet1", "Sheet2", "Sheet3"}
	if len(got) != len(want) {
		t.Fatalf("SheetList() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("SheetList()[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestWorkbook_SheetList_Empty(t *testing.T) {
	wb := &xls.Workbook{}
	if got := wb.SheetList(); len(got) != 0 {
		t.Errorf("SheetList() = %v, want empty", got)
	}
}

func TestSheet_Name(t *testing.T) {
	s := xls.NewSheetForTest("MySheet")
	if got := s.Name(); got != "MySheet" {
		t.Errorf("Name() = %q, want %q", got, "MySheet")
	}
}

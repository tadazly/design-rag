// Integration tests for xls.Read and xls.Open.
// These tests build a minimal BIFF8 stream programmatically using the
// internal/biff and internal/cfb packages, wrap it in a CFB container, and
// then verify that xls.Read returns the expected Workbook/Sheet/Cell values.
package xls_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"github.com/nkiri/xls"
	"github.com/nkiri/xls/internal/biff"
	"github.com/nkiri/xls/internal/cfb"
)

// ── record builder helpers ────────────────────────────────────────────────────

// xfRecord returns a 20-byte XF record payload with fmtIdx at bytes 2-3.
func xfRecord(fmtIdx uint16) []byte {
	var b [20]byte
	binary.LittleEndian.PutUint16(b[2:4], fmtIdx) // iFmt
	binary.LittleEndian.PutUint16(b[4:6], 0xFFF5) // fTypeProt (cell-XF flag)
	return b[:]
}

// sstRecord encodes an SST record payload for the given string list.
func sstRecord(strs []string) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(strs))) // cstTotal
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(strs))) // cstUnique
	result := b[:]
	for _, s := range strs {
		result = append(result, biff.EncodeLongString(s)...)
	}
	return result
}

// labelSSTRecord returns a LABELSST cell payload.
func labelSSTRecord(row, col, xfIdx int, sstIdx uint32) []byte {
	var b [10]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	binary.LittleEndian.PutUint32(b[6:10], sstIdx)
	return b[:]
}

// numberRecord returns a NUMBER cell payload.
func numberRecord(row, col, xfIdx int, val float64) []byte {
	var b [14]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	binary.LittleEndian.PutUint64(b[6:14], math.Float64bits(val))
	return b[:]
}

// rkRecord returns an RK cell payload.
func rkRecord(row, col, xfIdx int, rkVal uint32) []byte {
	var b [10]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	binary.LittleEndian.PutUint32(b[6:10], rkVal)
	return b[:]
}

// boolErrRecord returns a BOOLERR cell payload.  kind: 0=bool, 1=error.
func boolErrRecord(row, col, xfIdx int, val, kind byte) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	b[6] = val
	b[7] = kind
	return b[:]
}

// mulRKRecord encodes a MULRK payload for one row with consecutive RK cells.
func mulRKRecord(row, firstCol int, cells []struct {
	xfIdx int
	val   float64
}) []byte {
	lastCol := firstCol + len(cells) - 1
	b := make([]byte, 4+len(cells)*6+2)
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(firstCol))
	for i, c := range cells {
		off := 4 + i*6
		binary.LittleEndian.PutUint16(b[off:], uint16(c.xfIdx))
		rk, _ := biff.EncodeRK(c.val)
		binary.LittleEndian.PutUint32(b[off+2:], rk)
	}
	binary.LittleEndian.PutUint16(b[len(b)-2:], uint16(lastCol))
	return b
}

// ── BIFF8 stream builders ─────────────────────────────────────────────────────

// buildBIFF8 constructs a minimal BIFF8 Workbook stream containing one sheet.
// It uses a two-phase approach: write globals with a placeholder BOUNDSHEET
// bofOffset, then patch it after knowing the total globals size.
func buildBIFF8(t *testing.T) []byte {
	t.Helper()

	// ── Globals sub-stream ────────────────────────────────────────────────────
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)

	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))

	// XF[0]: format 0  (General → treated as plain number)
	gw.WriteRecord(biff.RecXF, xfRecord(0))
	// XF[1]: format 14 (m/d/yy → built-in date format)
	gw.WriteRecord(biff.RecXF, xfRecord(14))

	// SST with two shared strings.
	gw.WriteRecord(biff.RecSST, sstRecord([]string{"Hello", "World"}))

	// Record where the BOUNDSHEET data begins (after the 4-byte record header).
	boundSheetDataOff := globBuf.Len() + 4

	// Write BOUNDSHEET with placeholder bofOffset = 0 (patched below).
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))

	gw.WriteEmpty(biff.RecEOF)

	// After writing globals EOF, the next byte is the sheet sub-stream's BOF.
	sheetBOFOffset := uint32(globBuf.Len())

	// Patch the bofOffset field in the already-written BOUNDSHEET record.
	globBytes := globBuf.Bytes()
	binary.LittleEndian.PutUint32(globBytes[boundSheetDataOff:], sheetBOFOffset)

	// ── Sheet sub-stream ──────────────────────────────────────────────────────
	var sheetBuf bytes.Buffer
	sw := biff.NewWriter(&sheetBuf)

	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))

	// Row 0:
	// A1  LABELSST → "Hello"  (xf=0)
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 0, 0, 0))
	// B1  NUMBER   → 42.0     (xf=0, plain number)
	sw.WriteRecord(biff.RecNumber, numberRecord(0, 1, 0, 42.0))
	// C1  NUMBER   → 44927.0  (xf=1, date format → 2023-01-01)
	sw.WriteRecord(biff.RecNumber, numberRecord(0, 2, 1, 44927.0))

	// Row 1:
	// A2  RK       → 100      (xf=0)
	rk100, ok := biff.EncodeRK(100)
	if !ok {
		t.Fatal("EncodeRK(100) failed unexpectedly")
	}
	sw.WriteRecord(biff.RecRK, rkRecord(1, 0, 0, rk100))
	// B2  BOOLERR  → true     (xf=0, kind=0)
	sw.WriteRecord(biff.RecBoolErr, boolErrRecord(1, 1, 0, 1, 0))
	// C2  LABELSST → "World"  (xf=0)
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(1, 2, 0, 1))

	// Row 2: MULRK with three cells.
	sw.WriteRecord(biff.RecMulRK, mulRKRecord(2, 0, []struct {
		xfIdx int
		val   float64
	}{
		{0, 1.5},
		{0, 2.5},
		{0, 3.5},
	}))

	sw.WriteEmpty(biff.RecEOF)

	return append(globBytes, sheetBuf.Bytes()...)
}

// wrapCFB wraps a raw BIFF8 byte slice in a CFB "Workbook" stream.
func wrapCFB(t *testing.T, biffData []byte) []byte {
	t.Helper()
	w := cfb.NewWriter()
	w.AddStream("Workbook", biffData)
	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatalf("CFB WriteTo: %v", err)
	}
	return buf.Bytes()
}

// buildBIFF8ForStrings builds a minimal BIFF8 stream with:
//
//	Row 0: A1="hello"(SST 0)  B1="world"(SST 1)
//	Row 1: A2=42 (NUMBER)
func buildBIFF8ForStrings(t *testing.T) []byte {
	t.Helper()

	// ── globals sub-stream ────────────────────────────────────────────────
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0)) // XF 0: plain number
	gw.WriteRecord(biff.RecSST, sstRecord([]string{"hello", "world"}))

	// BOUNDSHEET placeholder – offset will be patched below
	bsOffset := globBuf.Len() + 4 // position of the 4-byte lbPlyPos inside the record
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))
	gw.WriteEmpty(biff.RecEOF)
	globBytes := globBuf.Bytes()

	// ── sheet sub-stream ──────────────────────────────────────────────────
	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	// Row 0, Col 0: LABELSST → "hello" (SST index 0)
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 0, 0, 0))
	// Row 0, Col 1: LABELSST → "world" (SST index 1)
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 1, 0, 1))
	// Row 1, Col 0: NUMBER → 42
	sw.WriteRecord(biff.RecNumber, numberRecord(1, 0, 0, 42))
	sw.WriteEmpty(biff.RecEOF)
	shBytes := shBuf.Bytes()

	// Patch BOUNDSHEET offset
	binary.LittleEndian.PutUint32(globBytes[bsOffset:], uint32(len(globBytes)))

	return append(globBytes, shBytes...)
}

// wrapCFBForStrings wraps raw BIFF8 bytes in a CFB container.
func wrapCFBForStrings(t *testing.T, biffData []byte) []byte {
	t.Helper()
	w := cfb.NewWriter()
	w.AddStream("Workbook", biffData)
	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatalf("cfb.WriteTo: %v", err)
	}
	return buf.Bytes()
}

// ── Formula record helpers ────────────────────────────────────────────────────

// formulaRecord builds a FORMULA record payload with a pre-computed result.
//
// The FORMULA record is 22+ bytes:
//
//	[0:2]  row
//	[2:4]  col
//	[4:6]  XF index
//	[6:14] cached result
//	[14:16] grbit (0 = recalc on open)
//	[16:18] chn (0)
//	[18:20] cce (formula token byte count)
//	[20:]  formula tokens (minimal: PtgInt 0)
//
// resultBytes must be exactly 8 bytes.
func formulaRecord(row, col, xfIdx int, resultBytes [8]byte) []byte {
	b := make([]byte, 22)
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	copy(b[6:14], resultBytes[:])
	// grbit=0x0000, chn=0x00000000
	binary.LittleEndian.PutUint16(b[20:22], 3) // cce = 3 (PtgInt token = 3 bytes)
	// PtgInt(0): opcode 0x1E, value 0x0000
	b = append(b, 0x1E, 0x00, 0x00)
	return b
}

func formulaRecordWithTokens(row, col, xfIdx int, resultBytes [8]byte, tokens []byte) []byte {
	b := make([]byte, 22)
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	copy(b[6:14], resultBytes[:])
	binary.LittleEndian.PutUint16(b[20:22], uint16(len(tokens)))
	return append(b, tokens...)
}

func definedNameRecord(name string) []byte {
	units := []rune(name)
	payload := make([]byte, 15+len(units)*2+2)
	binary.LittleEndian.PutUint16(payload[0:2], 0x038b)
	payload[3] = byte(len(units))
	binary.LittleEndian.PutUint16(payload[4:6], 2)
	payload[14] = 1
	for index, value := range units {
		binary.LittleEndian.PutUint16(payload[15+index*2:], uint16(value))
	}
	payload[len(payload)-2], payload[len(payload)-1] = 0x1c, 0x1d
	return payload
}

func sharedFormulaRecord(firstRow, lastRow, firstCol, lastCol int, tokens []byte) []byte {
	payload := make([]byte, 10)
	binary.LittleEndian.PutUint16(payload[0:2], uint16(firstRow))
	binary.LittleEndian.PutUint16(payload[2:4], uint16(lastRow))
	payload[4], payload[5] = byte(firstCol), byte(lastCol)
	payload[7] = byte(lastRow - firstRow + 1)
	binary.LittleEndian.PutUint16(payload[8:10], uint16(len(tokens)))
	return append(payload, tokens...)
}

// formulaNumericResult encodes the 8-byte result field for a numeric formula.
func formulaNumericResult(val float64) [8]byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(val))
	return b
}

// formulaBoolResult encodes the 8-byte result field for a boolean formula.
//
// FORMULA result bytes [6:14] layout when bytes [12:14]=0xFF 0xFF:
//
//	[0] = byte[6]  type indicator (1 = bool)
//	[2] = byte[8]  bool value (0=FALSE, 1=TRUE)
//	[6] = byte[12] 0xFF marker
//	[7] = byte[13] 0xFF marker
func formulaBoolResult(v bool) [8]byte {
	var b [8]byte
	b[0] = 1 // type indicator: bool
	if v {
		b[2] = 1 // TRUE
	}
	b[6] = 0xFF
	b[7] = 0xFF
	return b
}

// formulaStringResult encodes the 8-byte result field for a string formula.
// The actual string value arrives in the following STRING record.
func formulaStringResult() [8]byte {
	var b [8]byte
	b[0] = 0    // type indicator: string
	b[6] = 0xFF // byte[12] marker
	b[7] = 0xFF // byte[13] marker
	return b
}

// formulaErrorResult encodes the 8-byte result field for an error formula.
//
//	[0] = byte[6]  type indicator (2 = error)
//	[2] = byte[8]  error code
//	[6] = byte[12] 0xFF marker
//	[7] = byte[13] 0xFF marker
func formulaErrorResult(code byte) [8]byte {
	var b [8]byte
	b[0] = 2    // type indicator: error
	b[2] = code // error code
	b[6] = 0xFF
	b[7] = 0xFF
	return b
}

// formulaBlankResult marks a formula whose cached result is unavailable.
func formulaBlankResult() [8]byte {
	var b [8]byte
	b[0] = 3
	b[6] = 0xFF
	b[7] = 0xFF
	return b
}

// stringRecord builds a STRING record payload for a cached formula string.
func stringRecord(s string) []byte {
	return biff.EncodeLongString(s)
}

// buildBIFF8ForFormulas builds a minimal BIFF8 stream with one sheet containing
// five formula cells in row 0:
//
//	A1: numeric  → 3.14
//	B1: boolean  → TRUE
//	C1: string   → "hello"  (via following STRING record)
//	D1: error    → #DIV/0!
func buildBIFF8ForFormulas(t *testing.T) []byte {
	t.Helper()

	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0)) // XF 0: plain
	gw.WriteRecord(biff.RecSST, sstRecord(nil))
	bsOff := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Formulas", 0))
	gw.WriteEmpty(biff.RecEOF)
	glob := globBuf.Bytes()

	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 0, 0, formulaNumericResult(3.14)))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 1, 0, formulaBoolResult(true)))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 2, 0, formulaStringResult()))
	sw.WriteRecord(biff.RecString, stringRecord("hello"))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 3, 0, formulaErrorResult(0x07)))
	sw.WriteRecord(biff.RecFormula, formulaRecord(0, 4, 0, formulaBlankResult()))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	return append(glob, shBuf.Bytes()...)
}

func buildBIFF8ForSharedFutureFormula(t *testing.T) []byte {
	t.Helper()
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0))
	gw.WriteRecord(biff.RecSST, sstRecord(nil))
	gw.WriteRecord(biff.RecName, definedNameRecord("_xlfn.XLOOKUP"))
	boundSheetOffset := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Shared", 0))
	gw.WriteEmpty(biff.RecEOF)
	globals := globBuf.Bytes()

	anchor := []byte{0x01, 0x01, 0x00, 0x00, 0x00}
	definition := []byte{0x23, 0x01, 0x00, 0x00, 0x00, 0x22, 0x01, 0xff, 0x00}
	var sheetBuffer bytes.Buffer
	sw := biff.NewWriter(&sheetBuffer)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	// The defining FORMULA is intentionally not the top row of ref. Real
	// workbooks use the immediately preceding FORMULA as the PtgExp anchor.
	sw.WriteRecord(biff.RecFormula, formulaRecordWithTokens(1, 0, 0, formulaBlankResult(), anchor))
	sw.WriteRecord(biff.RecShrFmla, sharedFormulaRecord(0, 2, 0, 0, definition))
	sw.WriteRecord(biff.RecFormula, formulaRecordWithTokens(2, 0, 0, formulaBlankResult(), anchor))
	sw.WriteEmpty(biff.RecEOF)
	binary.LittleEndian.PutUint32(globals[boundSheetOffset:], uint32(len(globals)))
	return append(globals, sheetBuffer.Bytes()...)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestRead_Integration(t *testing.T) {
	xlsBytes := wrapCFB(t, buildBIFF8(t))
	wb, err := xls.Read(bytes.NewReader(xlsBytes))
	if err != nil {
		t.Fatalf("xls.Read: %v", err)
	}
	if wb.SheetCount() != 1 {
		t.Fatalf("SheetCount: got %d, want 1", wb.SheetCount())
	}
	sh := wb.Sheet(0)
	if sh == nil {
		t.Fatal("Sheet(0) returned nil")
	}
	if sh.Name() != "Sheet1" {
		t.Errorf("sheet name: got %q, want %q", sh.Name(), "Sheet1")
	}

	cases := []struct {
		name     string
		row, col int
		wantType xls.CellType
		want     any // string | float64 | time.Time | bool
	}{
		{"A1", 0, 0, xls.CellTypeString, "Hello"},
		{"B1", 0, 1, xls.CellTypeNumber, 42.0},
		{"C1", 0, 2, xls.CellTypeDate, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"A2", 1, 0, xls.CellTypeNumber, 100.0},
		{"B2", 1, 1, xls.CellTypeBool, true},
		{"C2", 1, 2, xls.CellTypeString, "World"},
		{"A3", 2, 0, xls.CellTypeNumber, 1.5},
		{"B3", 2, 1, xls.CellTypeNumber, 2.5},
		{"C3", 2, 2, xls.CellTypeNumber, 3.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := sh.Row(tc.row)
			if row == nil {
				t.Fatalf("Row(%d) is nil", tc.row)
			}
			c := row.Cell(tc.col)
			if c == nil {
				t.Fatal("cell is nil")
			}
			if c.Type != tc.wantType {
				t.Errorf("type: got %v, want %v", c.Type, tc.wantType)
			}
			switch want := tc.want.(type) {
			case string:
				if got := c.String(); got != want {
					t.Errorf("String(): got %q, want %q", got, want)
				}
			case float64:
				if got := c.Float(); got != want {
					t.Errorf("Float(): got %v, want %v", got, want)
				}
			case time.Time:
				if got := c.Time(); !got.Equal(want) {
					t.Errorf("Time(): got %v, want %v", got, want)
				}
			case bool:
				if got := c.Bool(); got != want {
					t.Errorf("Bool(): got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestReadRejectsImpossibleSSTCountWithoutAllocatingIt(t *testing.T) {
	var stream bytes.Buffer
	writer := biff.NewWriter(&stream)
	writer.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	sst := make([]byte, 8)
	binary.LittleEndian.PutUint32(sst[0:4], ^uint32(0))
	binary.LittleEndian.PutUint32(sst[4:8], ^uint32(0))
	writer.WriteRecord(biff.RecSST, sst)
	writer.WriteEmpty(biff.RecEOF)
	if _, err := xls.Read(bytes.NewReader(wrapCFB(t, stream.Bytes()))); err == nil {
		t.Fatal("impossible SST count was accepted")
	}
}

func TestReadRejectsNonBIFF8Workbook(t *testing.T) {
	data := buildBIFF8(t)
	binary.LittleEndian.PutUint16(data[4:6], 0x0500)
	if _, err := xls.Read(bytes.NewReader(wrapCFB(t, data))); err == nil {
		t.Fatal("BIFF5 workbook was accepted by BIFF8-only reader")
	}
}

// TestRead_MultipleSheets verifies that multiple BOUNDSHEET entries are parsed.
func TestRead_MultipleSheets(t *testing.T) {
	sheets := []struct {
		name      string
		cellValue string
	}{
		{"Alpha", "A"},
		{"Beta", "B"},
	}

	// ── Build BIFF8 stream ────────────────────────────────────────────────────
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0))

	sst := make([]string, len(sheets))
	for i, tc := range sheets {
		sst[i] = tc.cellValue
	}
	gw.WriteRecord(biff.RecSST, sstRecord(sst))

	bsOffsets := make([]int, len(sheets))
	for i, tc := range sheets {
		bsOffsets[i] = globBuf.Len() + 4 // data starts after 4-byte header
		gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, tc.name, 0))
	}
	gw.WriteEmpty(biff.RecEOF)
	globBytes := globBuf.Bytes()

	sheetData := make([][]byte, len(sheets))
	for i := range sheets {
		var sb bytes.Buffer
		sw := biff.NewWriter(&sb)
		sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
		sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 0, 0, uint32(i)))
		sw.WriteEmpty(biff.RecEOF)
		sheetData[i] = sb.Bytes()
	}

	offset := uint32(len(globBytes))
	for i, s := range sheetData {
		binary.LittleEndian.PutUint32(globBytes[bsOffsets[i]:], offset)
		offset += uint32(len(s))
	}

	var full []byte
	full = append(full, globBytes...)
	for _, s := range sheetData {
		full = append(full, s...)
	}

	// ── Read and assert ───────────────────────────────────────────────────────
	wb, err := xls.Read(bytes.NewReader(wrapCFB(t, full)))
	if err != nil {
		t.Fatal(err)
	}
	if wb.SheetCount() != len(sheets) {
		t.Fatalf("SheetCount: got %d, want %d", wb.SheetCount(), len(sheets))
	}
	for i, tc := range sheets {
		t.Run(tc.name, func(t *testing.T) {
			sh := wb.Sheet(i)
			if sh.Name() != tc.name {
				t.Errorf("name: got %q, want %q", sh.Name(), tc.name)
			}
			if got := sh.Row(0).Cell(0).String(); got != tc.cellValue {
				t.Errorf("cell[0][0]: got %q, want %q", got, tc.cellValue)
			}
		})
	}
}

// TestRead_Errors checks that malformed or incomplete inputs return errors.
func TestRead_Errors(t *testing.T) {
	// Build a valid CFB that contains no Workbook stream.
	w := cfb.NewWriter()
	w.AddStream("NotWorkbook", []byte("something"))
	var noWorkbookBuf bytes.Buffer
	if err := w.Write(&noWorkbookBuf); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		input []byte
	}{
		{"invalid CFB", bytes.Repeat([]byte{0x00}, 512)},
		{"no Workbook stream", noWorkbookBuf.Bytes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := xls.Read(bytes.NewReader(tc.input))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// ── Additional record helpers ─────────────────────────────────────────────────

// labelRecord returns a LABEL cell payload (inline string, BIFF5/7 compat).
func labelRecord(row, col, xfIdx int, s string) []byte {
	var h [6]byte
	binary.LittleEndian.PutUint16(h[0:2], uint16(row))
	binary.LittleEndian.PutUint16(h[2:4], uint16(col))
	binary.LittleEndian.PutUint16(h[4:6], uint16(xfIdx))
	return append(h[:], biff.EncodeLongString(s)...)
}

// blankRecord returns a BLANK cell payload (formatted empty cell).
func blankRecord(row, col, xfIdx int) []byte {
	var b [6]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(col))
	binary.LittleEndian.PutUint16(b[4:6], uint16(xfIdx))
	return b[:]
}

// mulBlankRecord returns a MULBLANK payload for one row.
func mulBlankRecord(row, firstCol int, xfIndices []int) []byte {
	lastCol := firstCol + len(xfIndices) - 1
	b := make([]byte, 4+len(xfIndices)*2+2)
	binary.LittleEndian.PutUint16(b[0:2], uint16(row))
	binary.LittleEndian.PutUint16(b[2:4], uint16(firstCol))
	for i, xf := range xfIndices {
		binary.LittleEndian.PutUint16(b[4+i*2:], uint16(xf))
	}
	binary.LittleEndian.PutUint16(b[len(b)-2:], uint16(lastCol))
	return b
}

// formatRecord returns a FORMAT record payload: ifmt (uint16) + LongString.
func formatRecord(ifmt uint16, fmtStr string) []byte {
	var h [2]byte
	binary.LittleEndian.PutUint16(h[:], ifmt)
	return append(h[:], biff.EncodeLongString(fmtStr)...)
}

// dateModeRecord returns a DATEMODE record payload: 1 = 1904 system, 0 = 1900.
func dateModeRecord(mode uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], mode)
	return b[:]
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRead_FormulaCells verifies that formula cells of every result type
// are read back correctly with Type==CellTypeFormula and correct Value().
func TestRead_FormulaCells(t *testing.T) {
	xlsData := wrapCFBForStrings(t, buildBIFF8ForFormulas(t))
	wb, err := xls.Read(bytes.NewReader(xlsData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	row := wb.Sheet(0).Row(0)
	if row == nil {
		t.Fatal("Row(0) is nil")
	}

	cases := []struct {
		name        string
		col         int
		wantType    xls.CellType
		wantValue   string
		wantFormula string
	}{
		{"numeric", 0, xls.CellTypeFormula, "3.14", "0"},
		{"bool", 1, xls.CellTypeFormula, "TRUE", "0"},
		{"string", 2, xls.CellTypeFormula, "hello", "0"},
		{"error", 3, xls.CellTypeFormula, "#DIV/0!", "0"},
		{"uncached", 4, xls.CellTypeFormula, "", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := row.Cell(tc.col)
			if c == nil {
				t.Fatal("cell is nil")
			}
			if c.Type != tc.wantType {
				t.Errorf("Type = %v, want %v", c.Type, tc.wantType)
			}
			if got := c.Value(); got != tc.wantValue {
				t.Errorf("Value() = %q, want %q", got, tc.wantValue)
			}
			if got := c.Formula(); got != tc.wantFormula {
				t.Errorf("Formula() = %q, want %q", got, tc.wantFormula)
			}
		})
	}
}

func TestRead_SharedFutureFunctionFormulaIsPreservedWithoutEvaluation(t *testing.T) {
	xlsData := wrapCFBForStrings(t, buildBIFF8ForSharedFutureFormula(t))
	workbook, err := xls.Read(bytes.NewReader(xlsData))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []int{1, 2} {
		cell := workbook.Sheet(0).Row(row).Cell(0)
		if cell.Value() != "" || cell.Formula() != "XLOOKUP()" {
			t.Fatalf("row %d value=%q formula=%q", row, cell.Value(), cell.Formula())
		}
	}
}

// TestRead_LabelCell verifies that a LABEL record (inline string) is read back
// as a CellTypeString cell with the correct string value.
func TestRead_LabelCell(t *testing.T) {
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
	sw.WriteRecord(biff.RecLabel, labelRecord(0, 0, 0, "inline"))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	c := wb.Sheet(0).Row(0).Cell(0)
	if c == nil {
		t.Fatal("cell is nil")
	}
	if c.Type != xls.CellTypeString {
		t.Errorf("Type = %v, want CellTypeString", c.Type)
	}
	if got := c.String(); got != "inline" {
		t.Errorf("String() = %q, want inline", got)
	}
}

// TestRead_BlankCell verifies that a BLANK record creates a CellTypeEmpty cell.
func TestRead_BlankCell(t *testing.T) {
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
	sw.WriteRecord(biff.RecBlank, blankRecord(0, 0, 0))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	c := wb.Sheet(0).Row(0).Cell(0)
	if c == nil {
		t.Fatal("cell is nil")
	}
	if c.Type != xls.CellTypeEmpty {
		t.Errorf("Type = %v, want CellTypeEmpty", c.Type)
	}
}

// TestRead_MulBlankCells verifies that a MULBLANK record creates multiple
// CellTypeEmpty cells in the correct columns.
func TestRead_MulBlankCells(t *testing.T) {
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
	// Three consecutive blank cells in row 0, columns 0-2
	sw.WriteRecord(biff.RecMulBlank, mulBlankRecord(0, 0, []int{0, 0, 0}))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sh := wb.Sheet(0)
	for col := 0; col < 3; col++ {
		c := sh.Row(0).Cell(col)
		if c == nil {
			t.Fatalf("col %d: cell is nil", col)
		}
		if c.Type != xls.CellTypeEmpty {
			t.Errorf("col %d: Type = %v, want CellTypeEmpty", col, c.Type)
		}
	}
}

// TestRead_Open verifies that Open() reads a file from disk correctly.
func TestRead_Open(t *testing.T) {
	xlsBytes := wrapCFB(t, buildBIFF8(t))

	f, err := os.CreateTemp(t.TempDir(), "test-*.xls")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(xlsBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	wb, err := xls.Open(f.Name())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if wb.SheetCount() != 1 {
		t.Errorf("SheetCount = %d, want 1", wb.SheetCount())
	}
	if wb.Sheet(0).Name() != "Sheet1" {
		t.Errorf("sheet name = %q, want Sheet1", wb.Sheet(0).Name())
	}
}

// TestRead_SST_ContinueBoundary は SST レコードが文字列の途中で CONTINUE
// レコードに分割されるケースを検証する。
// 修正前は parseSST が "unexpected EOF" エラーを返しセルが空文字列になる。
func TestRead_SST_ContinueBoundary(t *testing.T) {
	// biff.EncodeLongString("foo") = [03 00][00][66 6F 6F] (6 bytes)
	// byte 0-1: cch=3, byte 2: grBit=0x00, bytes 3-5: 'f','o','o'
	foo := biff.EncodeLongString("foo")
	bar := biff.EncodeLongString("bar")
	baz := biff.EncodeLongString("baz")

	// SST ヘッダー (8 bytes)
	sstHeader := make([]byte, 8)
	binary.LittleEndian.PutUint32(sstHeader[0:4], 3) // cstTotal
	binary.LittleEndian.PutUint32(sstHeader[4:8], 3) // cstUnique

	// SST レコードに入れるデータ: ヘッダー + "foo" の先頭 4 bytes (cch=2B, grBit=1B, 'f'=1B)
	sstPart1 := append(sstHeader, foo[:4]...)

	// CONTINUE レコードデータ: 継続 grBit + "foo" の残り + "bar" + "baz"
	continuePart := []byte{foo[2]}                  // 継続 grBit (= 0x00, "foo" のエンコーディング継続)
	continuePart = append(continuePart, foo[4:]...) // 'o', 'o'
	continuePart = append(continuePart, bar...)
	continuePart = append(continuePart, baz...)

	// グローバルサブストリーム構築
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	gw.WriteRecord(biff.RecXF, xfRecord(0))
	gw.WriteRecord(biff.RecSST, sstPart1)
	gw.WriteRecord(biff.RecContinue, continuePart) // SST の続き
	bsOff := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))
	gw.WriteEmpty(biff.RecEOF)
	glob := globBuf.Bytes()

	// シートサブストリーム構築
	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 0, 0, 0)) // SST[0] = "foo"
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 1, 0, 1)) // SST[1] = "bar"
	sw.WriteRecord(biff.RecLabelSST, labelSSTRecord(0, 2, 0, 2)) // SST[2] = "baz"
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	row := wb.Sheet(0).Row(0)
	for i, want := range []string{"foo", "bar", "baz"} {
		c := row.Cell(i)
		if c == nil {
			t.Fatalf("col %d: cell is nil", i)
		}
		if got := c.String(); got != want {
			t.Errorf("col %d: got %q, want %q", i, got, want)
		}
	}
}

// TestRead_Open_Error verifies that Open() returns an error for missing files.
func TestRead_Open_Error(t *testing.T) {
	_, err := xls.Open("/nonexistent/path/that/does/not/exist.xls")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// TestRead_DateMode1904 verifies that date serials are correctly interpreted
// when the workbook uses the 1904 date system.
func TestRead_DateMode1904(t *testing.T) {
	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	// XF[0]: format 14 (built-in date)
	gw.WriteRecord(biff.RecXF, xfRecord(14))
	// DATEMODE = 1 (1904 date system)
	gw.WriteRecord(biff.RecDateMode, dateModeRecord(1))
	gw.WriteRecord(biff.RecSST, sstRecord(nil))
	bsOff := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))
	gw.WriteEmpty(biff.RecEOF)
	glob := globBuf.Bytes()

	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	// Serial 1 in 1904 system = January 2, 1904
	sw.WriteRecord(biff.RecNumber, numberRecord(0, 0, 0, 1.0))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	c := wb.Sheet(0).Row(0).Cell(0)
	if c == nil {
		t.Fatal("cell is nil")
	}
	if c.Type != xls.CellTypeDate {
		t.Errorf("Type = %v, want CellTypeDate", c.Type)
	}
	want := time.Date(1904, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := c.Time(); !got.Equal(want) {
		t.Errorf("Time() = %v, want %v", got, want)
	}
}

// TestRead_CustomDateFormat verifies that a FORMAT record with a date-like
// format string causes numeric cells to be treated as CellTypeDate.
func TestRead_CustomDateFormat(t *testing.T) {
	const customFmtIdx = uint16(164)

	var globBuf bytes.Buffer
	gw := biff.NewWriter(&globBuf)
	gw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook))
	// FORMAT record: index 164, format "yyyy-mm-dd"
	gw.WriteRecord(biff.RecFormat, formatRecord(customFmtIdx, "yyyy-mm-dd"))
	// XF[0]: references custom format 164
	gw.WriteRecord(biff.RecXF, xfRecord(customFmtIdx))
	gw.WriteRecord(biff.RecSST, sstRecord(nil))
	bsOff := globBuf.Len() + 4
	gw.WriteRecord(biff.RecBoundSheet, biff.AppendBoundSheet(nil, 0, "Sheet1", 0))
	gw.WriteEmpty(biff.RecEOF)
	glob := globBuf.Bytes()

	var shBuf bytes.Buffer
	sw := biff.NewWriter(&shBuf)
	sw.WriteRecord(biff.RecBOF, biff.AppendBOF(nil, biff.BOFSheet))
	// Serial 44927 = 2023-01-01 in 1900 system
	sw.WriteRecord(biff.RecNumber, numberRecord(0, 0, 0, 44927.0))
	sw.WriteEmpty(biff.RecEOF)

	binary.LittleEndian.PutUint32(glob[bsOff:], uint32(len(glob)))
	data := wrapCFB(t, append(glob, shBuf.Bytes()...))

	wb, err := xls.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	c := wb.Sheet(0).Row(0).Cell(0)
	if c == nil {
		t.Fatal("cell is nil")
	}
	if c.Type != xls.CellTypeDate {
		t.Errorf("Type = %v, want CellTypeDate", c.Type)
	}
	want := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := c.Time(); !got.Equal(want) {
		t.Errorf("Time() = %v, want %v", got, want)
	}
}

package xls

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/nkiri/xls/internal/biff"
	"github.com/nkiri/xls/internal/cfb"
	"github.com/nkiri/xls/internal/formula"
)

// Open opens an XLS file from the given path and returns a Workbook.
func Open(path string) (*Workbook, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

// Read parses an XLS workbook from r.
// It supports BIFF8 format (Excel 97–2003, .xls).
func Read(r io.ReadSeeker) (*Workbook, error) {
	cr, err := cfb.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("xls: reading OLE2 container: %w", err)
	}

	// BIFF8 names the stream "Workbook"; older BIFF5/7 used "Book".
	data, err := cr.OpenStream("Workbook")
	if err != nil {
		data, err = cr.OpenStream("Book")
		if err != nil {
			return nil, fmt.Errorf("xls: Workbook stream not found")
		}
	}

	return parseWorkbook(data)
}

// ── workbookDecoder ───────────────────────────────────────────────────────────

// workbookDecoder accumulates the workbook-level tables needed to interpret
// cell values (SST, XF, number formats, date system).
type workbookDecoder struct {
	date1904      bool              // true = 1904 date system
	sst           []string          // shared string table (indexed by LABELSST)
	xfFmts        []uint16          // xfFmts[xfIdx] = number-format index
	fmts          map[uint16]string // number-format index → format string
	sheets        []boundSheetDesc
	addinSupBook  bool
	externalNames []string
	definedNames  []string
}

type boundSheetDesc struct {
	name       string
	bofOffset  uint32
	visibility byte
}

// parseWorkbook processes the workbook globals sub-stream, then delegates each
// worksheet sub-stream to parseSheet.
func parseWorkbook(data []byte) (*Workbook, error) {
	if err := validateBIFF8Stream(data, biff.BOFWorkbook); err != nil {
		return nil, err
	}
	dec := &workbookDecoder{fmts: make(map[uint16]string)}

	br := biff.NewReader(bytes.NewReader(data))
globalsLoop:
	for {
		rec, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xls: reading workbook record: %w", err)
		}
		switch rec.Type {
		case biff.RecEOF:
			break globalsLoop
		case biff.RecDateMode:
			dec.parseDateMode(rec.Data)
		case biff.RecXF:
			dec.parseXF(rec.Data)
		case biff.RecFormat:
			dec.parseFormat(rec.Data)
		case biff.RecSST:
			if err := dec.parseSST(rec.Data, rec.ContinueOffsets); err != nil {
				return nil, fmt.Errorf("xls: invalid SST: %w", err)
			}
		case biff.RecBoundSheet:
			dec.parseBoundSheet(rec.Data)
		case biff.RecSupBook:
			dec.parseSupBook(rec.Data)
		case biff.RecExternName, biff.RecExternName2:
			dec.parseExternName(rec.Data)
		case biff.RecName:
			dec.parseName(rec.Data)
		}
	}

	wb := &Workbook{}
	for _, bs := range dec.sheets {
		if int(bs.bofOffset) >= len(data) {
			continue
		}
		sh, err := dec.parseSheet(data[bs.bofOffset:])
		if err != nil {
			// Return partial workbook rather than an error.
			sh = &Sheet{}
		}
		sh.name = bs.name
		wb.Sheets = append(wb.Sheets, sh)
	}
	return wb, nil
}

func (dec *workbookDecoder) parseSupBook(b []byte) {
	dec.addinSupBook = len(b) >= 4 && binary.LittleEndian.Uint16(b[2:4]) == 0x3A01
}

func (dec *workbookDecoder) parseExternName(b []byte) {
	if !dec.addinSupBook {
		return
	}
	name := ""
	if len(b) >= 8 {
		decoded, _, err := biff.DecodeShortString(b[6:])
		if err == nil {
			name = strings.TrimPrefix(decoded, "_xlfn.")
		}
	}
	dec.externalNames = append(dec.externalNames, name)
}

func (dec *workbookDecoder) parseName(b []byte) {
	// BIFF8 NAME: flags(2), key(1), cch(1), cce(2), metadata(8),
	// followed by XLUnicodeStringNoCch for the name text.
	if len(b) < 15 {
		dec.definedNames = append(dec.definedNames, "")
		return
	}
	characterCount := int(b[3])
	options := b[14]
	wide := options&0x01 != 0
	bytesPerCharacter := 1
	if wide {
		bytesPerCharacter = 2
	}
	end := 15 + characterCount*bytesPerCharacter
	if characterCount <= 0 || end > len(b) {
		dec.definedNames = append(dec.definedNames, "")
		return
	}
	name := ""
	if wide {
		units := make([]uint16, characterCount)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(b[15+index*2:])
		}
		name = string(utf16.Decode(units))
	} else {
		name = string(b[15:end])
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "_xlfn."), "_xlws.")
	dec.definedNames = append(dec.definedNames, name)
}

// ── Globals record parsers ────────────────────────────────────────────────────

func (dec *workbookDecoder) parseDateMode(b []byte) {
	if len(b) >= 2 {
		dec.date1904 = binary.LittleEndian.Uint16(b[0:2]) != 0
	}
}

// parseXF extracts only the number-format index (bytes 2-3) from an XF record.
// We do not need the rest of the 20-byte record for basic cell-value decoding.
func (dec *workbookDecoder) parseXF(b []byte) {
	if len(b) < 4 {
		dec.xfFmts = append(dec.xfFmts, 0)
		return
	}
	dec.xfFmts = append(dec.xfFmts, binary.LittleEndian.Uint16(b[2:4]))
}

// parseFormat stores a custom number-format string (BIFF8 FORMAT record).
func (dec *workbookDecoder) parseFormat(b []byte) {
	if len(b) < 3 {
		return
	}
	ifmt := binary.LittleEndian.Uint16(b[0:2])
	s, _, err := biff.DecodeLongString(b[2:])
	if err != nil {
		return
	}
	dec.fmts[ifmt] = s
}

// parseSST decodes the Shared String Table.
// NOTE: When the SST spans CONTINUE records and a string crosses the record
// boundary, our BIFF reader naively concatenates the bytes.  The CONTINUE
// record's leading grBit byte (MS-XLS §2.4.58) is then included as character
// data, potentially corrupting that one string.  This is an acceptable
// limitation for the initial implementation.
func (dec *workbookDecoder) parseSST(b []byte, boundaries []int) error {
	if len(b) < 8 {
		return fmt.Errorf("SST too short (%d bytes)", len(b))
	}
	// cstTotal (4 bytes) – total string references in the workbook.
	// cstUnique (4 bytes) – number of unique strings that follow.
	cstUnique := int(binary.LittleEndian.Uint32(b[4:8]))
	maximumByPayload := (len(b)-8)/3 + 1
	if cstUnique < 0 || cstUnique > maximumByPayload || cstUnique > 1_000_000 {
		return fmt.Errorf("SST unique string count %d exceeds bounded payload capacity %d", cstUnique, maximumByPayload)
	}
	dec.sst = make([]string, 0, cstUnique)

	off := 8
	for i := 0; i < cstUnique; i++ {
		if off >= len(b) {
			break
		}
		s, n, err := biff.DecodeSSTString(b, off, boundaries)
		if err != nil {
			return fmt.Errorf("SST string %d at offset %d: %w", i, off, err)
		}
		dec.sst = append(dec.sst, s)
		off += n
	}
	return nil
}

// parseBoundSheet decodes a BOUNDSHEET record and appends a worksheet
// descriptor.  Non-worksheet entries (charts, VBA modules) are silently
// skipped.
func (dec *workbookDecoder) parseBoundSheet(b []byte) {
	if len(b) < 6 {
		return
	}
	bofOffset := binary.LittleEndian.Uint32(b[0:4])
	visibility := b[4]
	sheetType := b[5] // 0x00=worksheet, 0x02=chart, 0x06=macro/VBA
	if sheetType != 0x00 {
		return
	}
	name, _, err := biff.DecodeShortString(b[6:])
	if err != nil {
		return
	}
	dec.sheets = append(dec.sheets, boundSheetDesc{
		name:       name,
		bofOffset:  bofOffset,
		visibility: visibility,
	})
}

// ── Worksheet parser ──────────────────────────────────────────────────────────

// parseSheet processes one worksheet sub-stream starting with its BOF record.
func (dec *workbookDecoder) parseSheet(data []byte) (*Sheet, error) {
	if err := validateBIFF8Stream(data, biff.BOFSheet); err != nil {
		return nil, err
	}
	sh := &Sheet{}
	br := biff.NewReader(bytes.NewReader(data))

	// formulaStrPending is set when a FORMULA cell holds a string result;
	// it is cleared after the following STRING record fills the value.
	var formulaStrPending *Cell
	sharedFormulas := map[[2]int][]byte{}
	lastFormulaCell := [2]int{-1, -1}

sheetLoop:
	for {
		rec, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sh, nil // return what we have
		}

		// Any non-STRING record clears the pending formula-string cell.
		if rec.Type != biff.RecString {
			formulaStrPending = nil
		}

		switch rec.Type {
		case biff.RecEOF:
			break sheetLoop

		// ── Cell records ─────────────────────────────────────────────────────

		case biff.RecNumber:
			dec.cellNumber(sh, rec.Data)

		case biff.RecRK:
			dec.cellRK(sh, rec.Data)

		case biff.RecMulRK:
			dec.cellMulRK(sh, rec.Data)

		case biff.RecLabelSST:
			dec.cellLabelSST(sh, rec.Data)

		case biff.RecLabel:
			dec.cellLabel(sh, rec.Data)

		case biff.RecBlank:
			dec.cellBlank(sh, rec.Data)

		case biff.RecMulBlank:
			dec.cellMulBlank(sh, rec.Data)

		case biff.RecBoolErr:
			dec.cellBoolErr(sh, rec.Data)

		case biff.RecFormula:
			if len(rec.Data) >= 4 {
				lastFormulaCell = [2]int{int(binary.LittleEndian.Uint16(rec.Data[0:2])), int(binary.LittleEndian.Uint16(rec.Data[2:4]))}
			}
			formulaStrPending = dec.cellFormula(sh, rec.Data)
			continue // skip the formulaStrPending = nil at the top

		case biff.RecShrFmla:
			if len(rec.Data) >= 10 {
				cce := int(binary.LittleEndian.Uint16(rec.Data[8:10]))
				if cce > 0 && 10+cce <= len(rec.Data) {
					anchor := lastFormulaCell
					if anchor[0] < 0 {
						anchor = [2]int{int(binary.LittleEndian.Uint16(rec.Data[0:2])), int(rec.Data[4])}
					}
					sharedFormulas[anchor] = append([]byte(nil), rec.Data[10:10+cce]...)
				}
			}

		case biff.RecString:
			// Cached string result for the immediately preceding FORMULA cell.
			// The cell already has Type == CellTypeFormula; we only fill value.
			if formulaStrPending != nil {
				s, _, err := biff.DecodeLongString(rec.Data)
				if err == nil {
					formulaStrPending.value = s
				} else {
					formulaStrPending.value = ""
				}
				formulaStrPending = nil
			}
		}
	}
	for _, row := range sh.rows {
		if row == nil {
			continue
		}
		for _, cell := range row.cells {
			if cell == nil || !cell.sharedFormula {
				continue
			}
			tokens := sharedFormulas[[2]int{cell.sharedRow, cell.sharedCol}]
			if len(tokens) == 0 {
				cell.formula = "BIFF_TOKEN_HEX(" + hex.EncodeToString(cell.formulaTokens) + ")"
				continue
			}
			cell.formula = dec.decodeFormulaTokenBytes(tokens, cell.Row, cell.Col)
		}
	}
	return sh, nil
}

func validateBIFF8Stream(data []byte, expectedType biff.BOFType) error {
	if len(data) < 8 || biff.RecordType(binary.LittleEndian.Uint16(data[0:2])) != biff.RecBOF || int(binary.LittleEndian.Uint16(data[2:4])) < 4 {
		return fmt.Errorf("xls: missing BIFF BOF record")
	}
	version := binary.LittleEndian.Uint16(data[4:6])
	streamType := biff.BOFType(binary.LittleEndian.Uint16(data[6:8]))
	if version != 0x0600 || streamType != expectedType {
		return fmt.Errorf("xls: unsupported BIFF stream version/type 0x%04X/0x%04X; only BIFF8 is supported", version, streamType)
	}
	return nil
}

// ── Cell-level decoders ───────────────────────────────────────────────────────

// cellBase decodes the common 6-byte prefix shared by most cell records:
//
//	[0:2] row index
//	[2:4] column index
//	[4:6] XF (style) index
func cellBase(b []byte) (row, col, xfIdx int, ok bool) {
	if len(b) < 6 {
		return 0, 0, 0, false
	}
	return int(binary.LittleEndian.Uint16(b[0:2])),
		int(binary.LittleEndian.Uint16(b[2:4])),
		int(binary.LittleEndian.Uint16(b[4:6])),
		true
}

// cellNumber handles a NUMBER record: 6-byte prefix + 8-byte IEEE 754 double.
func (dec *workbookDecoder) cellNumber(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 14 {
		return
	}
	val := math.Float64frombits(binary.LittleEndian.Uint64(b[6:14]))
	c := &Cell{Row: row, Col: col, Style: &Style{XFIndex: xfIdx}}
	if dec.isDateFmt(xfIdx) {
		c.Type = CellTypeDate
		c.value = dec.serialToTime(val)
	} else {
		c.Type = CellTypeNumber
		c.value = val
	}
	sh.setCell(row, col, c)
}

// cellRK handles an RK record: 6-byte prefix + 4-byte RK value.
func (dec *workbookDecoder) cellRK(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 10 {
		return
	}
	val := biff.DecodeRK(binary.LittleEndian.Uint32(b[6:10]))
	c := &Cell{Row: row, Col: col, Style: &Style{XFIndex: xfIdx}}
	if dec.isDateFmt(xfIdx) {
		c.Type = CellTypeDate
		c.value = dec.serialToTime(val)
	} else {
		c.Type = CellTypeNumber
		c.value = val
	}
	sh.setCell(row, col, c)
}

// cellMulRK handles a MULRK record: multiple RK cells in one row.
//
//	[0:2]  row
//	[2:4]  firstCol
//	[4 .. len-2]  (xfIdx uint16 + rkVal uint32) × N
//	[len-2:len]  lastCol
func (dec *workbookDecoder) cellMulRK(sh *Sheet, b []byte) {
	if len(b) < 6 {
		return
	}
	row := int(binary.LittleEndian.Uint16(b[0:2]))
	firstCol := int(binary.LittleEndian.Uint16(b[2:4]))
	lastCol := int(binary.LittleEndian.Uint16(b[len(b)-2:]))

	off := 4
	for col := firstCol; col <= lastCol; col++ {
		if off+6 > len(b)-2 {
			break
		}
		xfIdx := int(binary.LittleEndian.Uint16(b[off:]))
		val := biff.DecodeRK(binary.LittleEndian.Uint32(b[off+2:]))
		c := &Cell{Row: row, Col: col, Style: &Style{XFIndex: xfIdx}}
		if dec.isDateFmt(xfIdx) {
			c.Type = CellTypeDate
			c.value = dec.serialToTime(val)
		} else {
			c.Type = CellTypeNumber
			c.value = val
		}
		sh.setCell(row, col, c)
		off += 6
	}
}

// cellLabelSST handles a LABELSST record: string referenced by SST index.
func (dec *workbookDecoder) cellLabelSST(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 10 {
		return
	}
	sstIdx := int(binary.LittleEndian.Uint32(b[6:10]))
	var s string
	if sstIdx >= 0 && sstIdx < len(dec.sst) {
		s = dec.sst[sstIdx]
	}
	sh.setCell(row, col, &Cell{
		Row:   row,
		Col:   col,
		Type:  CellTypeString,
		value: s,
		Style: &Style{XFIndex: xfIdx},
	})
}

// cellLabel handles a LABEL record (inline string, used in BIFF5/7 and
// occasionally in BIFF8 for compatibility).
func (dec *workbookDecoder) cellLabel(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 8 {
		return
	}
	s, _, err := biff.DecodeLongString(b[6:])
	if err != nil {
		return
	}
	sh.setCell(row, col, &Cell{
		Row:   row,
		Col:   col,
		Type:  CellTypeString,
		value: s,
		Style: &Style{XFIndex: xfIdx},
	})
}

// cellBlank handles a BLANK record (formatted empty cell).
func (dec *workbookDecoder) cellBlank(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok {
		return
	}
	sh.setCell(row, col, &Cell{
		Row:   row,
		Col:   col,
		Type:  CellTypeEmpty,
		Style: &Style{XFIndex: xfIdx},
	})
}

// cellMulBlank handles a MULBLANK record: multiple blank cells in one row.
func (dec *workbookDecoder) cellMulBlank(sh *Sheet, b []byte) {
	if len(b) < 6 {
		return
	}
	row := int(binary.LittleEndian.Uint16(b[0:2]))
	firstCol := int(binary.LittleEndian.Uint16(b[2:4]))
	lastCol := int(binary.LittleEndian.Uint16(b[len(b)-2:]))

	off := 4
	for col := firstCol; col <= lastCol; col++ {
		if off+2 > len(b)-2 {
			break
		}
		xfIdx := int(binary.LittleEndian.Uint16(b[off:]))
		sh.setCell(row, col, &Cell{
			Row:   row,
			Col:   col,
			Type:  CellTypeEmpty,
			Style: &Style{XFIndex: xfIdx},
		})
		off += 2
	}
}

// cellBoolErr handles a BOOLERR record.
//
//	[6]  value  (boolean 0/1 or error code)
//	[7]  type   (0 = bool, 1 = error)
func (dec *workbookDecoder) cellBoolErr(sh *Sheet, b []byte) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 8 {
		return
	}
	val, kind := b[6], b[7]
	c := &Cell{Row: row, Col: col, Style: &Style{XFIndex: xfIdx}}
	if kind == 0 {
		c.Type = CellTypeBool
		c.value = val != 0
	} else {
		c.Type = CellTypeError
		c.value = val
	}
	sh.setCell(row, col, c)
}

// cellFormula handles a FORMULA record.
// All formula cells are stored with Type == CellTypeFormula; the cached
// calculation result is placed in the value field as the appropriate Go type:
//
//   - float64   for numeric results (including date serials)
//   - time.Time for numeric results with a date/time number format
//   - string    for string results (filled later from the STRING record)
//   - bool      for boolean results
//   - byte      for error-code results
//
// Returns a non-nil *Cell only when the result is a string (type indicator 0),
// so that the caller can fill the value from the following STRING record.
//
// FORMULA record layout:
//
//	[0:2]   row
//	[2:4]   col
//	[4:6]   ixfe (XF index)
//	[6:14]  result value (8 bytes)
//	  If byte[12]==0xFF && byte[13]==0xFF → special result:
//	    byte[6]: 0=string  1=bool  2=error  3=blank/unevaluated
//	    byte[8]: value (for bool/error)
//	  Otherwise: IEEE 754 double (little-endian)
//	[14:16] grbit
//	[16:20] chn (reserved)
//	[20:22] cce (formula token byte count)
//	[22:]   rgce (formula token stream)
func (dec *workbookDecoder) cellFormula(sh *Sheet, b []byte) (pendingStr *Cell) {
	row, col, xfIdx, ok := cellBase(b)
	if !ok || len(b) < 14 {
		return nil
	}
	c := &Cell{Row: row, Col: col, Type: CellTypeFormula, Style: &Style{XFIndex: xfIdx}}
	c.formulaTokens = formulaTokenBytes(b)
	if sharedRow, sharedCol, ok := sharedFormulaAnchor(c.formulaTokens); ok {
		c.sharedFormula, c.sharedRow, c.sharedCol = true, sharedRow, sharedCol
	} else {
		c.formula = dec.decodeFormulaTokenBytes(c.formulaTokens, row, col)
	}

	if b[12] == 0xFF && b[13] == 0xFF {
		switch b[6] {
		case 0: // string – value comes in the next STRING record
			// c.value stays nil until the STRING record fills it.
			sh.setCell(row, col, c)
			return c
		case 1: // boolean
			c.value = b[8] != 0
		case 2: // error code
			c.value = b[8]
		default:
			// type=3 (blank) or any unrecognised value means the formula result
			// has not been pre-calculated. Keep the cached value empty: this
			// read-only parser deliberately never executes or recalculates formulas.
		}
	} else {
		val := math.Float64frombits(binary.LittleEndian.Uint64(b[6:14]))
		if dec.isDateFmt(xfIdx) {
			c.value = dec.serialToTime(val)
		} else {
			c.value = val
		}
	}
	sh.setCell(row, col, c)
	return nil
}

// decodeFormulaTokens extracts the Ptg token stream from a raw FORMULA record
// payload and decodes it for search evidence without evaluating it.
func formulaTokenBytes(b []byte) []byte {
	if len(b) < 22 {
		return nil
	}
	cce := int(binary.LittleEndian.Uint16(b[20:22]))
	if cce == 0 || len(b) < 22+cce {
		return nil
	}
	return append([]byte(nil), b[22:22+cce]...)
}

func sharedFormulaAnchor(tokens []byte) (row, col int, ok bool) {
	if len(tokens) == 5 && tokens[0] == 0x01 {
		return int(binary.LittleEndian.Uint16(tokens[1:3])), int(binary.LittleEndian.Uint16(tokens[3:5])), true
	}
	return 0, 0, false
}

func (dec *workbookDecoder) decodeFormulaTokenBytes(tokens []byte, row, col int) string {
	if len(tokens) == 0 {
		return ""
	}
	sheetNames := make([]string, 0, len(dec.sheets))
	for _, sheet := range dec.sheets {
		sheetNames = append(sheetNames, sheet.name)
	}
	decoded, err := formula.DecodeWithNames(tokens, row, col, sheetNames, dec.definedNames, dec.externalNames)
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "BIFF_TOKEN_HEX(" + hex.EncodeToString(tokens) + ")"
	}
	return decoded
}

// ── Date helpers ──────────────────────────────────────────────────────────────

// isDateFmt reports whether the XF at xfIdx uses a date/time number format.
func (dec *workbookDecoder) isDateFmt(xfIdx int) bool {
	if xfIdx < 0 || xfIdx >= len(dec.xfFmts) {
		return false
	}
	return isDateFormatIndex(dec.xfFmts[xfIdx], dec.fmts)
}

// isDateFormatIndex returns true for built-in and custom date/time formats.
// Built-in date indices: 14-22 (date), 45-47 (time).
func isDateFormatIndex(idx uint16, custom map[uint16]string) bool {
	if (idx >= 14 && idx <= 22) || (idx >= 45 && idx <= 47) {
		return true
	}
	if s, ok := custom[idx]; ok {
		return looksLikeDateFmt(s)
	}
	return false
}

// looksLikeDateFmt returns true if the format string s looks like a date or
// time format, by searching for the characters 'y', 'Y', 'd', 'D' outside of
// bracket sequences ([…]) and double-quoted literals.
//
// Note: 'm'/'M' alone can mean both "month" and "minute"; we require 'y' or
// 'd' to be present to avoid false positives like "mm:ss".  Built-in time
// formats (indices 45-47) are already handled by isDateFormatIndex.
func looksLikeDateFmt(s string) bool {
	lower := strings.ToLower(s)
	inBracket, inQuote := false, false
	for _, ch := range lower {
		switch {
		case inQuote:
			if ch == '"' {
				inQuote = false
			}
		case inBracket:
			if ch == ']' {
				inBracket = false
			}
		case ch == '"':
			inQuote = true
		case ch == '[':
			inBracket = true
		case ch == 'y' || ch == 'd':
			return true
		}
	}
	return false
}

// serialToTime converts an Excel serial date number to a UTC time.Time.
//
// 1900 date system (default):
//
//	Excel erroneously treats 1900 as a leap year, so the actual epoch is
//	December 30, 1899, making serial 1 = January 1, 1900.
//
// 1904 date system:
//
//	Serial 0 = January 1, 1904.
func (dec *workbookDecoder) serialToTime(serial float64) time.Time {
	var epoch time.Time
	if dec.date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else {
		epoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	wholeDays := math.Floor(serial)
	frac := serial - wholeDays
	t := epoch.AddDate(0, 0, int(wholeDays))
	t = t.Add(time.Duration(frac * float64(24*time.Hour)))
	return t
}

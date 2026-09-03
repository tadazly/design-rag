package formula

import (
	"fmt"
	"strings"
)

// colToA1 converts a 0-based column index to an Excel column label (A, B, …, Z, AA, …).
func colToA1(col int) string {
	result := ""
	for col >= 0 {
		result = string(rune('A'+col%26)) + result
		col = col/26 - 1
	}
	return result
}

// cellRefA1 returns the A1 notation for a cell reference.
//
//   - rowAbs / colAbs: true means absolute (prefixed with $)
//   - row / col: 0-based
func cellRefA1(row, col int, rowAbs, colAbs bool) string {
	colStr := colToA1(col)
	if colAbs {
		colStr = "$" + colStr
	}
	rowStr := fmt.Sprintf("%d", row+1)
	if rowAbs {
		rowStr = "$" + rowStr
	}
	return colStr + rowStr
}

// rangeA1 returns the A1 notation for a cell range (e.g. A1:C3).
func rangeA1(r1, c1, r2, c2 int, r1Abs, c1Abs, r2Abs, c2Abs bool) string {
	return cellRefA1(r1, c1, r1Abs, c1Abs) + ":" + cellRefA1(r2, c2, r2Abs, c2Abs)
}

// decodeRef decodes a PtgRef or PtgRefN cell reference from 4 bytes.
//
// BIFF8 PtgRef layout (4 bytes):
//
//	bytes 0-1: row (0-based, 16-bit unsigned)
//	bytes 2-3: column + flags
//	  bit 15: fRowRel (row is relative)
//	  bit 14: fColRel (col is relative)
//	  bits 13-0: column (0-based, 14-bit)
//
// When a reference is relative the stored value is a signed offset from
// baseRow / baseCol; when absolute it is the real 0-based index.
func decodeRef(data []byte, baseRow, baseCol int) (ref string, ok bool) {
	if len(data) < 4 {
		return "", false
	}
	row := int(uint16(data[0]) | uint16(data[1])<<8)
	colWord := uint16(data[2]) | uint16(data[3])<<8
	fRowRel := colWord&0x8000 != 0
	fColRel := colWord&0x4000 != 0
	col := int(colWord & 0x3FFF)

	absRow := row
	absCol := col
	if fRowRel {
		// Treat stored row as signed 16-bit offset
		offset := int(int16(row))
		absRow = baseRow + offset
	}
	if fColRel {
		// Treat stored col as signed 14-bit offset (sign-extend from bit 13)
		if col&0x2000 != 0 {
			col |= -0x4000 // sign-extend to int
		}
		absCol = baseCol + col
	}

	return cellRefA1(absRow, absCol, !fRowRel, !fColRel), true
}

// decodeArea decodes a PtgArea cell range from 8 bytes.
//
// BIFF8 PtgArea layout (8 bytes):
//
//	bytes 0-1: first row
//	bytes 2-3: last row
//	bytes 4-5: first col + flags (same layout as PtgRef colWord)
//	bytes 6-7: last col + flags
func decodeArea(data []byte, baseRow, baseCol int) (ref string, ok bool) {
	if len(data) < 8 {
		return "", false
	}
	r1 := int(uint16(data[0]) | uint16(data[1])<<8)
	r2 := int(uint16(data[2]) | uint16(data[3])<<8)
	cw1 := uint16(data[4]) | uint16(data[5])<<8
	cw2 := uint16(data[6]) | uint16(data[7])<<8

	r1RowRel := cw1&0x8000 != 0
	r1ColRel := cw1&0x4000 != 0
	r2RowRel := cw2&0x8000 != 0
	r2ColRel := cw2&0x4000 != 0
	c1 := int(cw1 & 0x3FFF)
	c2 := int(cw2 & 0x3FFF)

	absR1, absC1 := r1, c1
	absR2, absC2 := r2, c2

	if r1RowRel {
		absR1 = baseRow + int(int16(r1))
	}
	if r1ColRel {
		if c1&0x2000 != 0 {
			c1 |= -0x4000
		}
		absC1 = baseCol + c1
	}
	if r2RowRel {
		absR2 = baseRow + int(int16(r2))
	}
	if r2ColRel {
		if c2&0x2000 != 0 {
			c2 |= -0x4000
		}
		absC2 = baseCol + c2
	}

	return rangeA1(absR1, absC1, absR2, absC2,
		!r1RowRel, !r1ColRel, !r2RowRel, !r2ColRel), true
}

// decode3DRef decodes a PtgRef3D or PtgArea3D sheet-index prefix (2 bytes).
// Returns the "Sheet1!" prefix if sheetNames is provided, or "Sheet<n>!" otherwise.
func sheetPrefix(ixti uint16, sheetNames []string) string {
	if int(ixti) < len(sheetNames) {
		name := sheetNames[ixti]
		// Quote names that contain spaces or special chars
		if needsQuote(name) {
			name = "'" + strings.ReplaceAll(name, "'", "''") + "'"
		}
		return name + "!"
	}
	return fmt.Sprintf("Sheet%d!", ixti+1)
}

// needsQuote reports whether a sheet name needs single-quote wrapping in A1 notation.
func needsQuote(name string) bool {
	for _, r := range name {
		if r == ' ' || r == '\'' || r == '!' || r == '[' || r == ']' {
			return true
		}
	}
	return len(name) == 0
}

// Package formula handles encoding and decoding of BIFF8 formula token streams.
//
// A BIFF8 formula is stored as a sequence of "Ptg" (parsed thing) tokens that
// form a Reverse Polish Notation expression.  This package converts between
// that binary representation and a human-readable infix string.
//
// Entry points:
//
//	Decode(tokens []byte, baseRow, baseCol int, sheetNames []string) (string, error)
//	Encode(formula string, baseRow, baseCol int) ([]byte, error)
//
// Decode is fully implemented.  Encode is a best-effort implementation that
// handles the common subset of formulas produced and consumed by Excel 97-2003.
package formula

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Encode converts a human-readable infix formula string to a BIFF8 token byte
// stream.  The leading "=" is optional and is stripped if present.
//
// baseRow and baseCol are the 0-based cell coordinates; they are used to
// encode relative cell references.
//
// The encoder supports:
//   - Numeric literals (integer → PtgInt, float → PtgNum)
//   - String literals (PtgStr)
//   - Boolean literals TRUE / FALSE (PtgBool)
//   - Error literals #NULL! … #N/A (PtgErr)
//   - Cell references A1, $A1, A$1, $A$1 (PtgRef)
//   - Range references A1:B2 (PtgArea)
//   - Built-in functions by name (PtgFunc / PtgFuncVar)
//   - Arithmetic / comparison operators
//   - Unary plus, minus, percent
//   - Parenthesised sub-expressions
//
// Returns ErrUnsupported for constructs the encoder cannot handle.
func Encode(formula string, baseRow, baseCol int) ([]byte, error) {
	expr := strings.TrimPrefix(strings.TrimSpace(formula), "=")
	p := &parser{src: expr, baseRow: baseRow, baseCol: baseCol}
	return p.parse()
}

// ErrUnsupported is returned by Encode when the formula contains a construct
// that the encoder does not handle.
var ErrUnsupported = fmt.Errorf("formula: unsupported construct")

// ── Minimal recursive-descent encoder ────────────────────────────────────────

type parser struct {
	src     string
	pos     int
	baseRow int
	baseCol int
}

func (p *parser) parse() ([]byte, error) {
	out, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("formula: unexpected %q at pos %d", p.src[p.pos:], p.pos)
	}
	return out, nil
}

// Operator precedence table (higher = tighter binding).
var precedence = map[string]int{
	"<": 1, "<=": 1, "=": 1, ">=": 1, ">": 1, "<>": 1,
	"&": 2,
	"+": 3, "-": 3,
	"*": 4, "/": 4,
	"^": 5,
}

var opPtg = map[string]byte{
	"+": byte(PtgAdd), "-": byte(PtgSub), "*": byte(PtgMul), "/": byte(PtgDiv),
	"^": byte(PtgPower), "&": byte(PtgConcat),
	"<": byte(PtgLT), "<=": byte(PtgLE), "=": byte(PtgEQ),
	">=": byte(PtgGE), ">": byte(PtgGT), "<>": byte(PtgNE),
}

func (p *parser) parseExpr(minPrec int) ([]byte, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	for {
		p.skipWS()
		op, opLen := p.peekOp()
		if op == "" {
			break
		}
		prec, ok := precedence[op]
		if !ok || prec <= minPrec {
			break
		}
		p.pos += opLen

		rhs, err := p.parseExpr(prec)
		if err != nil {
			return nil, err
		}
		ptgByte, ok := opPtg[op]
		if !ok {
			return nil, fmt.Errorf("formula: unknown op %q", op)
		}
		lhs = append(lhs, rhs...)
		lhs = append(lhs, ptgByte)
	}
	return lhs, nil
}

func (p *parser) parseUnary() ([]byte, error) {
	p.skipWS()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("formula: unexpected end of expression")
	}
	ch := p.src[p.pos]
	if ch == '+' {
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return append(inner, byte(PtgUplus)), nil
	}
	if ch == '-' {
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return append(inner, byte(PtgUminus)), nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() ([]byte, error) {
	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == '%' {
		p.pos++
		atom = append(atom, byte(PtgPercent))
	}
	return atom, nil
}

func (p *parser) parseAtom() ([]byte, error) {
	p.skipWS()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("formula: unexpected end")
	}

	// Parenthesised expression
	if p.src[p.pos] == '(' {
		p.pos++
		inner, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		p.skipWS()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return nil, fmt.Errorf("formula: missing closing ')'")
		}
		p.pos++
		return append(inner, byte(PtgParen)), nil
	}

	// String literal
	if p.src[p.pos] == '"' {
		return p.encodeString()
	}

	// Number literal
	if ch := p.src[p.pos]; ch >= '0' && ch <= '9' || ch == '.' {
		return p.encodeNumber()
	}

	// Error literals
	if p.src[p.pos] == '#' {
		return p.encodeError()
	}

	// Boolean / function / name / reference
	if isAlpha(rune(p.src[p.pos])) || p.src[p.pos] == '$' {
		return p.encodeNameOrRef()
	}

	return nil, fmt.Errorf("formula: cannot parse %q at pos %d", p.src[p.pos:], p.pos)
}

func (p *parser) encodeString() ([]byte, error) {
	p.pos++ // consume opening "
	var sb strings.Builder
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		p.pos++
		if ch == '"' {
			if p.pos < len(p.src) && p.src[p.pos] == '"' {
				sb.WriteByte('"')
				p.pos++
				continue
			}
			break
		}
		sb.WriteByte(ch)
	}
	s := sb.String()
	// PtgStr followed by BIFF8 short string
	encoded, err := encodeShortStringBytes(s)
	if err != nil {
		return nil, err
	}
	return append([]byte{byte(PtgStr)}, encoded...), nil
}

func (p *parser) encodeNumber() ([]byte, error) {
	start := p.pos
	hasDot := false
	hasExp := false
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch >= '0' && ch <= '9' {
			p.pos++
		} else if ch == '.' && !hasDot {
			hasDot = true
			p.pos++
		} else if (ch == 'e' || ch == 'E') && !hasExp {
			hasExp = true
			p.pos++
			if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
				p.pos++
			}
		} else {
			break
		}
	}
	s := p.src[start:p.pos]
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("formula: bad number %q", s)
	}
	// Use PtgInt for integers in [0, 65535]
	if !hasDot && !hasExp && f >= 0 && f <= 65535 {
		n := uint16(f)
		b := []byte{byte(PtgInt), 0, 0}
		binary.LittleEndian.PutUint16(b[1:], n)
		return b, nil
	}
	b := []byte{byte(PtgNum), 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint64(b[1:], math.Float64bits(f))
	return b, nil
}

func (p *parser) encodeError() ([]byte, error) {
	for code, s := range errString {
		if strings.HasPrefix(p.src[p.pos:], s) {
			p.pos += len(s)
			return []byte{byte(PtgErr), code}, nil
		}
	}
	return nil, fmt.Errorf("formula: unknown error literal at pos %d", p.pos)
}

func (p *parser) encodeNameOrRef() ([]byte, error) {
	// Collect the token (identifier, possibly with $ and digits)
	start := p.pos
	for p.pos < len(p.src) {
		ch := rune(p.src[p.pos])
		if isAlpha(ch) || (ch >= '0' && ch <= '9') || ch == '$' || ch == '_' {
			p.pos++
		} else {
			break
		}
	}
	token := p.src[start:p.pos]
	upper := strings.ToUpper(token)

	// Boolean literals
	if upper == "TRUE" {
		return []byte{byte(PtgBool), 1}, nil
	}
	if upper == "FALSE" {
		return []byte{byte(PtgBool), 0}, nil
	}

	// Check for function call
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		return p.encodeFunc(upper)
	}

	// Check for range A1:B2 — check if next token (after optional ':') is also a ref
	// Try to parse as cell reference
	if ref, ok := parseCellRef(token); ok {
		p.skipWS()
		if p.pos < len(p.src) && p.src[p.pos] == ':' {
			p.pos++
			// Read second cell ref
			start2 := p.pos
			for p.pos < len(p.src) {
				ch := rune(p.src[p.pos])
				if isAlpha(ch) || (ch >= '0' && ch <= '9') || ch == '$' {
					p.pos++
				} else {
					break
				}
			}
			token2 := p.src[start2:p.pos]
			if ref2, ok2 := parseCellRef(token2); ok2 {
				return encodeArea(ref, ref2, p.baseRow, p.baseCol), nil
			}
			// Not a valid range end — backtrack
			p.pos = start2 - 1
		}
		return encodeRef(ref, p.baseRow, p.baseCol), nil
	}

	return nil, fmt.Errorf("formula: unsupported token %q", token)
}

func (p *parser) encodeFunc(name string) ([]byte, error) {
	p.pos++ // consume '('
	var argBytes [][]byte
	for {
		p.skipWS()
		if p.pos < len(p.src) && p.src[p.pos] == ')' {
			p.pos++
			break
		}
		// Missing argument (comma at start or consecutive commas)
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			argBytes = append(argBytes, []byte{byte(PtgMissArg)})
			p.pos++
			continue
		}
		arg, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		argBytes = append(argBytes, arg)
		p.skipWS()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		}
	}

	// Find function ID
	funcID := uint16(0xFFFF)
	for id, n := range funcNames {
		if strings.ToUpper(n) == name {
			funcID = id
			break
		}
	}

	if funcID == 0xFFFF {
		return nil, fmt.Errorf("formula: unknown function %q", name)
	}

	// Build token bytes: all arg tokens concatenated, then function token
	var out []byte
	for _, a := range argBytes {
		out = append(out, a...)
	}

	nargs := len(argBytes)
	fixedN := fixedArgCount(funcID)
	if nargs != fixedN {
		// Use PtgFuncVar
		out = append(out, byte(PtgFuncVar), byte(nargs))
		out = append(out, 0, 0)
		binary.LittleEndian.PutUint16(out[len(out)-2:], funcID)
	} else {
		// Use PtgFunc
		out = append(out, byte(PtgFunc), 0, 0)
		binary.LittleEndian.PutUint16(out[len(out)-2:], funcID)
	}
	return out, nil
}

func (p *parser) skipWS() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

// peekOp returns the operator string and its length at the current position.
func (p *parser) peekOp() (string, int) {
	if p.pos >= len(p.src) {
		return "", 0
	}
	// Two-char ops first
	if p.pos+1 < len(p.src) {
		s := p.src[p.pos : p.pos+2]
		switch s {
		case "<=", ">=", "<>":
			return s, 2
		}
	}
	switch p.src[p.pos] {
	case '+', '-', '*', '/', '^', '&', '<', '>', '=':
		return string(p.src[p.pos]), 1
	}
	return "", 0
}

// ── Cell reference parsing and encoding ───────────────────────────────────────

type cellRef struct {
	row, col       int
	rowAbs, colAbs bool
}

// parseCellRef attempts to parse a string like A1, $B$2, C3 into a cellRef.
func parseCellRef(s string) (cellRef, bool) {
	i := 0
	colAbs := false
	if i < len(s) && s[i] == '$' {
		colAbs = true
		i++
	}
	// Column letters
	colStart := i
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i == colStart {
		return cellRef{}, false
	}
	colStr := s[colStart:i]

	rowAbs := false
	if i < len(s) && s[i] == '$' {
		rowAbs = true
		i++
	}
	// Row digits
	rowStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == rowStart || i != len(s) {
		return cellRef{}, false
	}
	rowN, err := strconv.Atoi(s[rowStart:])
	if err != nil || rowN < 1 {
		return cellRef{}, false
	}

	col := a1ToCol(colStr)
	if col < 0 {
		return cellRef{}, false
	}
	return cellRef{row: rowN - 1, col: col, rowAbs: rowAbs, colAbs: colAbs}, true
}

// a1ToCol converts an Excel column label (A, B, …, Z, AA, …) to a 0-based index.
func a1ToCol(s string) int {
	result := -1
	for _, ch := range s {
		result = (result+1)*26 + int(ch-'A')
	}
	return result
}

// encodeRef encodes a PtgRef token (4 data bytes + 1 opcode = 5 bytes total).
func encodeRef(ref cellRef, baseRow, baseCol int) []byte {
	row := ref.row
	col := ref.col
	var colWord uint16
	colWord = uint16(col & 0x3FFF)
	if !ref.rowAbs {
		colWord |= 0x8000 // fRowRel
		row = row - baseRow
	}
	if !ref.colAbs {
		colWord |= 0x4000 // fColRel
		col = col - baseCol
		colWord = uint16(col&0x3FFF) | (colWord & 0xC000)
	}
	b := make([]byte, 5)
	b[0] = byte(PtgRef)
	binary.LittleEndian.PutUint16(b[1:], uint16(row))
	binary.LittleEndian.PutUint16(b[3:], colWord)
	return b
}

// encodeArea encodes a PtgArea token (8 data bytes + 1 opcode = 9 bytes total).
func encodeArea(r1, r2 cellRef, baseRow, baseCol int) []byte {
	row1, row2 := r1.row, r2.row
	col1, col2 := r1.col, r2.col

	var cw1, cw2 uint16
	cw1 = uint16(col1 & 0x3FFF)
	cw2 = uint16(col2 & 0x3FFF)
	if !r1.rowAbs {
		cw1 |= 0x8000
		row1 -= baseRow
	}
	if !r1.colAbs {
		cw1 |= 0x4000
		rel1 := col1 - baseCol
		cw1 = uint16(rel1&0x3FFF) | (cw1 & 0xC000)
	}
	if !r2.rowAbs {
		cw2 |= 0x8000
		row2 -= baseRow
	}
	if !r2.colAbs {
		cw2 |= 0x4000
		rel2 := col2 - baseCol
		cw2 = uint16(rel2&0x3FFF) | (cw2 & 0xC000)
	}

	b := make([]byte, 9)
	b[0] = byte(PtgArea)
	binary.LittleEndian.PutUint16(b[1:], uint16(row1))
	binary.LittleEndian.PutUint16(b[3:], uint16(row2))
	binary.LittleEndian.PutUint16(b[5:], cw1)
	binary.LittleEndian.PutUint16(b[7:], cw2)
	return b
}

// encodeShortStringBytes encodes a string as a BIFF8 ShortXLUnicodeString.
func encodeShortStringBytes(s string) ([]byte, error) {
	runes := []rune(s)
	if len(runes) > 255 {
		return nil, fmt.Errorf("formula: string too long (%d chars)", len(runes))
	}
	// Determine if Latin-1 compression is possible
	latin1 := true
	for _, r := range runes {
		if r > 0xFF {
			latin1 = false
			break
		}
	}
	if latin1 {
		b := make([]byte, 2+len(runes))
		b[0] = byte(len(runes))
		b[1] = 0x00 // fHighByte = 0 (compressed)
		for i, r := range runes {
			b[2+i] = byte(r)
		}
		return b, nil
	}
	b := make([]byte, 2+2*len(runes))
	b[0] = byte(len(runes))
	b[1] = 0x01 // fHighByte = 1 (uncompressed UTF-16LE)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(b[2+2*i:], uint16(r))
	}
	return b, nil
}

func isAlpha(r rune) bool {
	return unicode.IsLetter(r)
}

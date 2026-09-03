package formula

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nkiri/xls/internal/biff"
)

// Decode converts a BIFF8 formula token byte stream into a human-readable
// formula string such as "=SUM(A1:B2)+1".
//
//   - tokens: raw Ptg bytes from a FORMULA record (the rgce field).
//   - baseRow, baseCol: 0-based position of the cell containing the formula
//     (needed to resolve relative references).
//   - sheetNames: optional slice of worksheet names for 3-D references.
//
// The returned string does NOT include the leading "=".
// Returns an error only for malformed token streams; unknown tokens are
// rendered as "_UNK(0xNN)".
func Decode(tokens []byte, baseRow, baseCol int, sheetNames []string) (string, error) {
	return DecodeWithNames(tokens, baseRow, baseCol, sheetNames, nil, nil)
}

// DecodeWithNames additionally resolves workbook-defined and add-in external
// names. It only renders the token stream and never evaluates the formula.
func DecodeWithNames(tokens []byte, baseRow, baseCol int, sheetNames, definedNames, externalNames []string) (string, error) {
	d := &decoder{
		data:          tokens,
		baseRow:       baseRow,
		baseCol:       baseCol,
		sheetNames:    sheetNames,
		definedNames:  definedNames,
		externalNames: externalNames,
	}
	return d.decode()
}

// decoder holds state while walking the Ptg stream.
type decoder struct {
	data          []byte
	pos           int
	baseRow       int
	baseCol       int
	sheetNames    []string
	definedNames  []string
	externalNames []string
	stack         []string
}

func (d *decoder) decode() (string, error) {
	for d.pos < len(d.data) {
		raw := d.data[d.pos]
		ptg := ptgClass(raw)
		d.pos++

		switch ptg {
		// ── Unary operators ──────────────────────────────────────────────
		case PtgUplus:
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push("+" + a)

		case PtgUminus:
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push("-" + a)

		case PtgPercent:
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push(a + "%")

		// ── Binary infix operators ────────────────────────────────────────
		case PtgAdd, PtgSub, PtgMul, PtgDiv, PtgPower,
			PtgConcat, PtgLT, PtgLE, PtgEQ, PtgGE, PtgGT, PtgNE:
			b, err := d.pop()
			if err != nil {
				return "", err
			}
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			op := ptgInfix(ptg)
			d.push(a + op + b)

		// ── Reference operators ────────────────────────────────────────────
		case PtgIsect:
			b, err := d.pop()
			if err != nil {
				return "", err
			}
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push(a + " " + b)

		case PtgUnion:
			b, err := d.pop()
			if err != nil {
				return "", err
			}
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push(a + "," + b)

		case PtgRange:
			b, err := d.pop()
			if err != nil {
				return "", err
			}
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push(a + ":" + b)

		// ── PtgParen ──────────────────────────────────────────────────────
		case PtgParen:
			a, err := d.pop()
			if err != nil {
				return "", err
			}
			d.push("(" + a + ")")

		// ── PtgMissArg ────────────────────────────────────────────────────
		case PtgMissArg:
			d.push("")

		// ── PtgAttr ───────────────────────────────────────────────────────
		case PtgAttr:
			if d.pos+3 > len(d.data) {
				return "", fmt.Errorf("PtgAttr: truncated")
			}
			grbit := d.data[d.pos]
			d.pos += 3 // grbit(1) + w(2)
			// PtgAttrSum is a unary SUM shortcut
			if grbit&PtgAttrSum != 0 {
				a, err := d.pop()
				if err != nil {
					return "", err
				}
				d.push("SUM(" + a + ")")
			}
			// PtgAttrIf / PtgAttrGoto / PtgAttrChoose / PtgAttrSpace:
			// we skip the jump data; the actual IF logic is already encoded
			// as normal PtgFunc tokens in well-formed formulas.
			// PtgAttrSpace just inserts whitespace — ignore.

		// ── Constant operands ──────────────────────────────────────────────
		case PtgStr:
			s, _, err := d.readBIFFString()
			if err != nil {
				return "", fmt.Errorf("PtgStr: %w", err)
			}
			d.push(`"` + strings.ReplaceAll(s, `"`, `""`) + `"`)

		case PtgErr:
			if d.pos >= len(d.data) {
				return "", fmt.Errorf("PtgErr: truncated")
			}
			code := d.data[d.pos]
			d.pos++
			if s, ok := errString[code]; ok {
				d.push(s)
			} else {
				d.push(fmt.Sprintf("#ERR!%02X", code))
			}

		case PtgBool:
			if d.pos >= len(d.data) {
				return "", fmt.Errorf("PtgBool: truncated")
			}
			v := d.data[d.pos]
			d.pos++
			if v != 0 {
				d.push("TRUE")
			} else {
				d.push("FALSE")
			}

		case PtgInt:
			if d.pos+2 > len(d.data) {
				return "", fmt.Errorf("PtgInt: truncated")
			}
			v := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 2
			d.push(strconv.FormatUint(uint64(v), 10))

		case PtgNum:
			if d.pos+8 > len(d.data) {
				return "", fmt.Errorf("PtgNum: truncated")
			}
			bits := binary.LittleEndian.Uint64(d.data[d.pos:])
			d.pos += 8
			f := math.Float64frombits(bits)
			d.push(strconv.FormatFloat(f, 'f', -1, 64))

		// ── Cell / range references ────────────────────────────────────────
		case PtgRef, PtgRefN:
			if d.pos+4 > len(d.data) {
				return "", fmt.Errorf("PtgRef: truncated")
			}
			ref, ok := decodeRef(d.data[d.pos:], d.baseRow, d.baseCol)
			d.pos += 4
			if !ok {
				d.push("!REF")
			} else {
				d.push(ref)
			}

		case PtgArea, PtgAreaN:
			if d.pos+8 > len(d.data) {
				return "", fmt.Errorf("PtgArea: truncated")
			}
			ref, ok := decodeArea(d.data[d.pos:], d.baseRow, d.baseCol)
			d.pos += 8
			if !ok {
				d.push("!REF")
			} else {
				d.push(ref)
			}

		case PtgRefErr, PtgAreaErr:
			// Deleted / invalid reference
			size := 4
			if ptg == PtgAreaErr {
				size = 8
			}
			d.pos += size
			d.push("#REF!")

		case PtgRef3D:
			if d.pos+6 > len(d.data) {
				return "", fmt.Errorf("PtgRef3D: truncated")
			}
			ixti := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 2
			ref, ok := decodeRef(d.data[d.pos:], d.baseRow, d.baseCol)
			d.pos += 4
			prefix := sheetPrefix(ixti, d.sheetNames)
			if !ok {
				d.push(prefix + "!REF")
			} else {
				d.push(prefix + ref)
			}

		case PtgArea3D:
			if d.pos+10 > len(d.data) {
				return "", fmt.Errorf("PtgArea3D: truncated")
			}
			ixti := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 2
			ref, ok := decodeArea(d.data[d.pos:], d.baseRow, d.baseCol)
			d.pos += 8
			prefix := sheetPrefix(ixti, d.sheetNames)
			if !ok {
				d.push(prefix + "!REF")
			} else {
				d.push(prefix + ref)
			}

		case PtgRefErr3D, PtgAreaErr3D:
			size := 6
			if ptg == PtgAreaErr3D {
				size = 10
			}
			d.pos += size
			d.push("#REF!")

		// ── Name references ───────────────────────────────────────────────
		case PtgName:
			// 4 bytes: nameIndex (2) + reserved (2)
			if d.pos+4 > len(d.data) {
				return "", fmt.Errorf("PtgName: truncated")
			}
			idx := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 4
			if idx > 0 && int(idx) <= len(d.definedNames) && d.definedNames[idx-1] != "" {
				d.push(d.definedNames[idx-1])
			} else {
				d.push(fmt.Sprintf("_NAME_%d", idx))
			}

		case PtgNameX:
			// 6 bytes: ixti (2) + nameIndex (2) + reserved (2)
			if d.pos+6 > len(d.data) {
				return "", fmt.Errorf("PtgNameX: truncated")
			}
			idx := binary.LittleEndian.Uint32(d.data[d.pos+2:])
			d.pos += 6
			if idx > 0 && int(idx) <= len(d.externalNames) && d.externalNames[idx-1] != "" {
				d.push(d.externalNames[idx-1])
			} else {
				d.push(fmt.Sprintf("_NAMEX_%d", idx))
			}

		// ── Array constant ────────────────────────────────────────────────
		case PtgArray:
			// 7 reserved bytes follow the opcode in the token stream;
			// the actual array data comes after the token stream (not handled here).
			if d.pos+7 > len(d.data) {
				return "", fmt.Errorf("PtgArray: truncated")
			}
			d.pos += 7
			d.push("{array}")

		// ── Memory tokens (sub-expression bookkeeping) ────────────────────
		case PtgMemArea:
			// 6 bytes: reserved(4) + cce(2)
			if d.pos+6 > len(d.data) {
				return "", fmt.Errorf("PtgMemArea: truncated")
			}
			d.pos += 6

		case PtgMemNoMem, PtgMemFunc:
			// 2 bytes: cce
			if d.pos+2 > len(d.data) {
				return "", fmt.Errorf("PtgMemNoMem/Func: truncated")
			}
			d.pos += 2

		case PtgMemErr:
			// 6 bytes: reserved(4) + cce(2)
			if d.pos+6 > len(d.data) {
				return "", fmt.Errorf("PtgMemErr: truncated")
			}
			d.pos += 6

		// ── Function call tokens ──────────────────────────────────────────
		case PtgFunc:
			// 2 bytes: function index
			if d.pos+2 > len(d.data) {
				return "", fmt.Errorf("PtgFunc: truncated")
			}
			funcID := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 2
			name := funcName(funcID)
			nargs := fixedArgCount(funcID)
			args, err := d.popN(nargs)
			if err != nil {
				return "", err
			}
			d.push(name + "(" + strings.Join(args, ",") + ")")

		case PtgFuncVar:
			// 3 bytes: cargs(1) + funcIndex(2)
			if d.pos+3 > len(d.data) {
				return "", fmt.Errorf("PtgFuncVar: truncated")
			}
			cargs := int(d.data[d.pos] & 0x7F) // bit 7 = prompt flag
			d.pos++
			funcID := binary.LittleEndian.Uint16(d.data[d.pos:])
			d.pos += 2
			args, err := d.popN(cargs)
			if err != nil {
				return "", err
			}
			name := funcName(funcID)
			if funcID == 255 && len(args) > 0 {
				name, args = args[0], args[1:]
			}
			d.push(name + "(" + strings.Join(args, ",") + ")")

		default:
			// Unknown / unhandled token — skip payload if we can guess size,
			// otherwise bail to avoid producing nonsense.
			d.push(fmt.Sprintf("_UNK(0x%02X)", raw))
		}
	}

	if len(d.stack) == 0 {
		return "", nil
	}
	return d.stack[len(d.stack)-1], nil
}

// ── Stack helpers ─────────────────────────────────────────────────────────────

func (d *decoder) push(s string) {
	d.stack = append(d.stack, s)
}

func (d *decoder) pop() (string, error) {
	if len(d.stack) == 0 {
		return "", fmt.Errorf("formula stack underflow")
	}
	s := d.stack[len(d.stack)-1]
	d.stack = d.stack[:len(d.stack)-1]
	return s, nil
}

// popN pops n items and returns them in left-to-right order.
func (d *decoder) popN(n int) ([]string, error) {
	if n == 0 {
		return nil, nil
	}
	if len(d.stack) < n {
		// Best-effort: return what we have
		n = len(d.stack)
	}
	args := make([]string, n)
	for i := n - 1; i >= 0; i-- {
		s, err := d.pop()
		if err != nil {
			return nil, err
		}
		args[i] = s
	}
	return args, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// ptgInfix maps a binary operator Ptg to its infix string.
func ptgInfix(p Ptg) string {
	switch p {
	case PtgAdd:
		return "+"
	case PtgSub:
		return "-"
	case PtgMul:
		return "*"
	case PtgDiv:
		return "/"
	case PtgPower:
		return "^"
	case PtgConcat:
		return "&"
	case PtgLT:
		return "<"
	case PtgLE:
		return "<="
	case PtgEQ:
		return "="
	case PtgGE:
		return ">="
	case PtgGT:
		return ">"
	case PtgNE:
		return "<>"
	}
	return "?"
}

// readBIFFString reads a BIFF8 short string (1-byte cch) at the current
// position and advances d.pos.  Returns the Go string and bytes consumed.
func (d *decoder) readBIFFString() (string, int, error) {
	s, n, err := biff.DecodeShortString(d.data[d.pos:])
	if err != nil {
		return "", 0, err
	}
	d.pos += n
	return s, n, nil
}

// fixedArgCount returns the number of arguments for a fixed-argument function.
// This is a small subset; for unknown IDs we return 0 and let the formula
// still render with no args (the formula stream is the authoritative source
// of argument count for PtgFuncVar).
func fixedArgCount(id uint16) int {
	counts := map[uint16]int{
		2: 1, 3: 1, // ISNA, ISERROR
		8: 1, 9: 1, // ROW, COLUMN (0 or 1, but common 1-arg form)
		10: 0,                      // NA()
		15: 1, 16: 1, 17: 1, 18: 1, // SIN COS TAN ATAN
		19: 0,                                           // PI()
		20: 1, 21: 1, 22: 1, 23: 1, 24: 1, 25: 1, 26: 1, // SQRT EXP LN LOG10 ABS INT SIGN
		27: 2,        // ROUND
		30: 2,        // REPT
		31: 3,        // MID
		32: 1,        // LEN
		33: 1,        // VALUE
		34: 0, 35: 0, // TRUE FALSE
		38: 1,        // NOT
		39: 2,        // MOD
		63: 0,        // RAND
		65: 3, 66: 3, // DATE TIME
		67: 1, 68: 1, 69: 1, 70: 1, 71: 1, 72: 1, 73: 1, // DAY MONTH YEAR WEEKDAY HOUR MINUTE SECOND
		74: 0,        // NOW
		97: 2,        // ATAN2
		98: 1, 99: 1, // ASIN ACOS
		105: 1,                         // ISREF
		109: 2,                         // LOG
		111: 1, 112: 1, 113: 1, 114: 1, // CHAR LOWER UPPER PROPER
		115: 2, 116: 2, // LEFT RIGHT
		117: 2,                         // EXACT
		118: 1,                         // TRIM
		119: 4,                         // REPLACE
		121: 1,                         // CODE
		126: 1, 127: 1, 128: 1, 129: 1, // ISERR ISTEXT ISNUMBER ISBLANK
		130: 1, 131: 1, // T N
		140: 1, 141: 1, // DATEVALUE TIMEVALUE
		142: 3, 143: 3, 144: 4, // SLN SYD DDB
		163: 1, 164: 1, // MDETERM MINVERSE
		165: 2,         // MMULT
		180: 1,         // FACT
		186: 1,         // ISNONTEXT
		189: 1, 190: 1, // STDEVP VARP
		193: 1, 194: 1, // TRUNC ISLOGICAL
		208: 2, 209: 2, // ROUNDUP ROUNDDOWN
		218: 0,                                         // TODAY
		222: 1, 223: 1, 224: 1, 225: 1, 226: 1, 227: 1, // SINH COSH TANH ASINH ACOSH ATANH
		256: 1,         // ERROR.TYPE
		266: 1,         // GAMMALN
		271: 2,         // COMBIN
		274: 1,         // EVEN
		280: 2,         // FLOOR
		283: 2,         // CEILING
		293: 1,         // ODD
		294: 2,         // PERMUT
		331: 2,         // POWER
		336: 1, 337: 1, // RADIANS DEGREES
		341: 1, // COUNTBLANK
		354: 1, // PHONETIC
	}
	if n, ok := counts[id]; ok {
		return n
	}
	return 1 // safe fallback
}

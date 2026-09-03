package formula

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/nkiri/xls/internal/biff"
)

// ErrEval is returned when the formula cannot be evaluated (e.g., it references
// a cell or contains an unsupported function).
var ErrEval = fmt.Errorf("formula: cannot evaluate")

// evalValue is a value on the evaluation stack.
type evalValue struct {
	f    float64
	s    string
	b    bool
	err  byte
	kind evalKind
}

type evalKind int8

const (
	kindFloat evalKind = iota
	kindString
	kindBool
	kindErr
)

func floatVal(f float64) evalValue { return evalValue{f: f, kind: kindFloat} }
func strVal(s string) evalValue    { return evalValue{s: s, kind: kindString} }
func boolVal(b bool) evalValue     { return evalValue{b: b, kind: kindBool} }
func errVal(e byte) evalValue      { return evalValue{err: e, kind: kindErr} }

func (v evalValue) toFloat() (float64, bool) {
	switch v.kind {
	case kindFloat:
		return v.f, true
	case kindBool:
		if v.b {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// CellLookup is an optional callback that returns the value of a cell at the
// given 0-based (row, col).  Return (nil, false) if the cell is unknown.
type CellLookup func(row, col int) (any, bool)

// Eval evaluates a BIFF8 formula token stream and returns the result as a Go
// native value (float64, string, bool, or byte for error codes).
//
//   - baseRow, baseCol are the 0-based position of the formula cell.
//   - lookup may be nil; it is called for PtgRef / PtgArea tokens.
//
// Returns ErrEval if the formula contains constructs that cannot be evaluated
// (unsupported functions, external references, etc.).
func Eval(tokens []byte, baseRow, baseCol int, lookup CellLookup) (any, error) {
	ev := &evaluator{
		data:    tokens,
		baseRow: baseRow,
		baseCol: baseCol,
		lookup:  lookup,
	}
	return ev.eval()
}

type evaluator struct {
	data    []byte
	pos     int
	baseRow int
	baseCol int
	lookup  CellLookup
	stack   []evalValue
}

func (ev *evaluator) eval() (any, error) {
	for ev.pos < len(ev.data) {
		raw := ev.data[ev.pos]
		ptg := ptgClass(raw)
		ev.pos++

		switch ptg {
		// ── Constants ────────────────────────────────────────────────────────
		case PtgInt:
			if ev.pos+2 > len(ev.data) {
				return nil, ErrEval
			}
			n := binary.LittleEndian.Uint16(ev.data[ev.pos:])
			ev.pos += 2
			ev.push(floatVal(float64(n)))

		case PtgNum:
			if ev.pos+8 > len(ev.data) {
				return nil, ErrEval
			}
			f := math.Float64frombits(binary.LittleEndian.Uint64(ev.data[ev.pos:]))
			ev.pos += 8
			ev.push(floatVal(f))

		case PtgBool:
			if ev.pos >= len(ev.data) {
				return nil, ErrEval
			}
			ev.push(boolVal(ev.data[ev.pos] != 0))
			ev.pos++

		case PtgStr:
			s, n, err := biff.DecodeShortString(ev.data[ev.pos:])
			if err != nil {
				return nil, ErrEval
			}
			ev.pos += n
			ev.push(strVal(s))

		case PtgErr:
			if ev.pos >= len(ev.data) {
				return nil, ErrEval
			}
			ev.push(errVal(ev.data[ev.pos]))
			ev.pos++

		case PtgMissArg:
			ev.push(floatVal(0))

		// ── Unary operators ───────────────────────────────────────────────────
		case PtgUplus:
			// no-op

		case PtgUminus:
			a, ok := ev.popFloat()
			if !ok {
				return nil, ErrEval
			}
			ev.push(floatVal(-a))

		case PtgPercent:
			a, ok := ev.popFloat()
			if !ok {
				return nil, ErrEval
			}
			ev.push(floatVal(a / 100))

		// ── Binary arithmetic operators ───────────────────────────────────────
		case PtgAdd:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				// String concatenation fallback
				bs, bsok := ev.popString()
				as, asok := ev.popString()
				if asok && bsok {
					ev.push(strVal(as + bs))
					continue
				}
				return nil, ErrEval
			}
			ev.push(floatVal(a + b))

		case PtgSub:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(floatVal(a - b))

		case PtgMul:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(floatVal(a * b))

		case PtgDiv:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			if b == 0 {
				ev.push(errVal(0x07)) // #DIV/0!
			} else {
				ev.push(floatVal(a / b))
			}

		case PtgPower:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(floatVal(math.Pow(a, b)))

		case PtgConcat:
			b, bok := ev.popString()
			a, aok := ev.popString()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(strVal(a + b))

		// ── Comparison operators (return bool) ────────────────────────────────
		case PtgLT:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a < b))

		case PtgLE:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a <= b))

		case PtgEQ:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a == b))

		case PtgGE:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a >= b))

		case PtgGT:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a > b))

		case PtgNE:
			b, bok := ev.popFloat()
			a, aok := ev.popFloat()
			if !aok || !bok {
				return nil, ErrEval
			}
			ev.push(boolVal(a != b))

		// ── PtgParen / no-op tokens ───────────────────────────────────────────
		case PtgParen:
			// parentheses: no-op on the value stack

		// ── PtgAttr ───────────────────────────────────────────────────────────
		case PtgAttr:
			if ev.pos+3 > len(ev.data) {
				return nil, ErrEval
			}
			grbit := ev.data[ev.pos]
			ev.pos += 4 // grbit(1) + w(2) + unused(1)

			if grbit&PtgAttrSum != 0 {
				// SUM shortcut: acts like SUM of the single argument
				// already on the stack → no extra work needed (scalar sum = identity)
			}

		// ── Cell / range references ───────────────────────────────────────────
		case PtgRef, PtgRefN:
			if ev.pos+4 > len(ev.data) {
				return nil, ErrEval
			}
			if ev.lookup == nil {
				ev.pos += 4
				return nil, ErrEval
			}
			refStr, ok := decodeRef(ev.data[ev.pos:], ev.baseRow, ev.baseCol)
			ev.pos += 4
			if !ok {
				return nil, ErrEval
			}
			// Parse A1 notation back to row/col for the lookup
			row, col, parseOK := a1ToRowCol(refStr)
			if !parseOK {
				return nil, ErrEval
			}
			val, found := ev.lookup(row, col)
			if !found {
				return nil, ErrEval
			}
			ev.push(anyToEvalValue(val))

		// ── Function calls ────────────────────────────────────────────────────
		case PtgFunc:
			if ev.pos+2 > len(ev.data) {
				return nil, ErrEval
			}
			funcID := binary.LittleEndian.Uint16(ev.data[ev.pos:])
			ev.pos += 2
			nargs := fixedArgCount(funcID)
			args, ok := ev.popN(nargs)
			if !ok {
				return nil, ErrEval
			}
			result, err := evalFunc(funcID, args, ev.baseRow, ev.baseCol)
			if err != nil {
				return nil, err
			}
			ev.push(result)

		case PtgFuncVar:
			if ev.pos+3 > len(ev.data) {
				return nil, ErrEval
			}
			cargs := int(ev.data[ev.pos] & 0x7F)
			ev.pos++
			funcID := binary.LittleEndian.Uint16(ev.data[ev.pos:])
			ev.pos += 2
			args, ok := ev.popN(cargs)
			if !ok {
				return nil, ErrEval
			}
			result, err := evalFunc(funcID, args, ev.baseRow, ev.baseCol)
			if err != nil {
				return nil, err
			}
			ev.push(result)

		// ── Memory / bookkeeping tokens ───────────────────────────────────────
		case PtgMemArea:
			if ev.pos+6 > len(ev.data) {
				return nil, ErrEval
			}
			ev.pos += 6

		case PtgMemNoMem, PtgMemFunc:
			if ev.pos+2 > len(ev.data) {
				return nil, ErrEval
			}
			ev.pos += 2

		case PtgMemErr:
			if ev.pos+6 > len(ev.data) {
				return nil, ErrEval
			}
			ev.pos += 6

		// ── Array constants ───────────────────────────────────────────────────
		case PtgArray:
			if ev.pos+7 > len(ev.data) {
				return nil, ErrEval
			}
			ev.pos += 7
			return nil, ErrEval // array formulas not supported

		// ── Unsupported ───────────────────────────────────────────────────────
		default:
			return nil, fmt.Errorf("formula eval: unsupported Ptg 0x%02X", raw)
		}
	}

	if len(ev.stack) == 0 {
		return nil, ErrEval
	}
	top := ev.stack[len(ev.stack)-1]
	return evalValueToAny(top), nil
}

// ── Stack helpers ─────────────────────────────────────────────────────────────

func (ev *evaluator) push(v evalValue) {
	ev.stack = append(ev.stack, v)
}

func (ev *evaluator) pop() (evalValue, bool) {
	if len(ev.stack) == 0 {
		return evalValue{}, false
	}
	v := ev.stack[len(ev.stack)-1]
	ev.stack = ev.stack[:len(ev.stack)-1]
	return v, true
}

func (ev *evaluator) popFloat() (float64, bool) {
	v, ok := ev.pop()
	if !ok {
		return 0, false
	}
	return v.toFloat()
}

func (ev *evaluator) popString() (string, bool) {
	v, ok := ev.pop()
	if !ok {
		return "", false
	}
	if v.kind == kindString {
		return v.s, true
	}
	return "", false
}

// popN pops n items and returns them in argument order (left to right).
func (ev *evaluator) popN(n int) ([]evalValue, bool) {
	if n > len(ev.stack) {
		return nil, false
	}
	args := make([]evalValue, n)
	for i := n - 1; i >= 0; i-- {
		v, ok := ev.pop()
		if !ok {
			return nil, false
		}
		args[i] = v
	}
	return args, true
}

// ── Function evaluation table ─────────────────────────────────────────────────

// evalFunc evaluates a built-in function with the given args.
// baseRow and baseCol are 0-based and needed by ROW() / COLUMN().
func evalFunc(id uint16, args []evalValue, baseRow, baseCol int) (evalValue, error) {
	switch id {
	case 8: // ROW()
		if len(args) == 0 {
			return floatVal(float64(baseRow + 1)), nil // 1-based
		}
		return floatVal(float64(baseRow + 1)), nil

	case 9: // COLUMN()
		if len(args) == 0 {
			return floatVal(float64(baseCol + 1)), nil // 1-based
		}
		return floatVal(float64(baseCol + 1)), nil

	case 10: // NA()
		return errVal(0x2A), nil

	case 15: // SIN
		return mathFunc1(args, math.Sin)
	case 16: // COS
		return mathFunc1(args, math.Cos)
	case 17: // TAN
		return mathFunc1(args, math.Tan)
	case 18: // ATAN
		return mathFunc1(args, math.Atan)
	case 19: // PI()
		return floatVal(math.Pi), nil
	case 20: // SQRT
		return mathFunc1(args, math.Sqrt)
	case 21: // EXP
		return mathFunc1(args, math.Exp)
	case 22: // LN
		return mathFunc1(args, math.Log)
	case 23: // LOG10
		return mathFunc1(args, math.Log10)
	case 24: // ABS
		return mathFunc1(args, math.Abs)
	case 25: // INT
		return mathFunc1(args, math.Floor)
	case 26: // SIGN
		return mathFunc1(args, func(x float64) float64 {
			if x > 0 {
				return 1
			} else if x < 0 {
				return -1
			}
			return 0
		})
	case 27: // ROUND(number, digits)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		num, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		digits, ok := args[1].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		factor := math.Pow(10, digits)
		return floatVal(math.Round(num*factor) / factor), nil

	case 32: // LEN
		if len(args) < 1 || args[0].kind != kindString {
			return floatVal(0), ErrEval
		}
		return floatVal(float64(len([]rune(args[0].s)))), nil

	case 34: // TRUE()
		return boolVal(true), nil
	case 35: // FALSE()
		return boolVal(false), nil

	case 38: // NOT
		if len(args) < 1 {
			return boolVal(false), ErrEval
		}
		f, ok := args[0].toFloat()
		if !ok {
			return boolVal(false), ErrEval
		}
		return boolVal(f == 0), nil

	case 63: // RAND()
		// Return 0 for deterministic behaviour in a reader context.
		return floatVal(0), nil

	case 74: // NOW()
		return floatVal(0), nil // cannot evaluate without real-time clock context

	case 97: // ATAN2(x, y)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		x, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		y, ok := args[1].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		return floatVal(math.Atan2(y, x)), nil

	case 98: // ASIN
		return mathFunc1(args, math.Asin)
	case 99: // ACOS
		return mathFunc1(args, math.Acos)

	case 109: // LOG(number[, base])
		if len(args) < 1 {
			return floatVal(0), ErrEval
		}
		n, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		if len(args) >= 2 {
			base, ok := args[1].toFloat()
			if !ok {
				return floatVal(0), ErrEval
			}
			return floatVal(math.Log(n) / math.Log(base)), nil
		}
		return floatVal(math.Log10(n)), nil

	case 112: // LOWER
		if len(args) < 1 || args[0].kind != kindString {
			return strVal(""), ErrEval
		}
		return strVal(strings.ToLower(args[0].s)), nil

	case 113: // UPPER
		if len(args) < 1 || args[0].kind != kindString {
			return strVal(""), ErrEval
		}
		return strVal(strings.ToUpper(args[0].s)), nil

	case 118: // TRIM
		if len(args) < 1 || args[0].kind != kindString {
			return strVal(""), ErrEval
		}
		return strVal(strings.TrimSpace(args[0].s)), nil

	case 180: // FACT
		return mathFunc1(args, func(x float64) float64 {
			n := int(x)
			r := 1.0
			for i := 2; i <= n; i++ {
				r *= float64(i)
			}
			return r
		})

	case 193: // TRUNC
		return mathFunc1(args, math.Trunc)

	case 208: // ROUNDUP
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		num, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		digits, ok := args[1].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		factor := math.Pow(10, digits)
		if num >= 0 {
			return floatVal(math.Ceil(num*factor) / factor), nil
		}
		return floatVal(math.Floor(num*factor) / factor), nil

	case 209: // ROUNDDOWN
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		num, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		digits, ok := args[1].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		factor := math.Pow(10, digits)
		if num >= 0 {
			return floatVal(math.Floor(num*factor) / factor), nil
		}
		return floatVal(math.Ceil(num*factor) / factor), nil

	case 218: // TODAY()
		return floatVal(0), nil // cannot evaluate without real-time context

	case 222: // SINH
		return mathFunc1(args, math.Sinh)
	case 223: // COSH
		return mathFunc1(args, math.Cosh)
	case 224: // TANH
		return mathFunc1(args, math.Tanh)
	case 225: // ASINH
		return mathFunc1(args, math.Asinh)
	case 226: // ACOSH
		return mathFunc1(args, math.Acosh)
	case 227: // ATANH
		return mathFunc1(args, math.Atanh)

	case 274: // EVEN
		return mathFunc1(args, func(x float64) float64 {
			n := math.Ceil(math.Abs(x)/2) * 2
			if x < 0 {
				return -n
			}
			return n
		})

	case 280: // FLOOR(number, significance)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		num, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		sig, ok := args[1].toFloat()
		if !ok || sig == 0 {
			return floatVal(0), ErrEval
		}
		return floatVal(math.Floor(num/sig) * sig), nil

	case 283: // CEILING(number, significance)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		num, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		sig, ok := args[1].toFloat()
		if !ok || sig == 0 {
			return floatVal(0), ErrEval
		}
		return floatVal(math.Ceil(num/sig) * sig), nil

	case 293: // ODD
		return mathFunc1(args, func(x float64) float64 {
			n := math.Ceil(math.Abs(x))
			if int(n)%2 == 0 {
				n++
			}
			if x < 0 {
				return -n
			}
			return n
		})

	case 331: // POWER(number, power)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		base, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		exp, ok := args[1].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		return floatVal(math.Pow(base, exp)), nil

	case 336: // RADIANS
		return mathFunc1(args, func(x float64) float64 { return x * math.Pi / 180 })

	case 337: // DEGREES
		return mathFunc1(args, func(x float64) float64 { return x * 180 / math.Pi })

	case 4: // SUM (variable-arg)
		sum := 0.0
		for _, a := range args {
			f, ok := a.toFloat()
			if !ok {
				return floatVal(0), ErrEval
			}
			sum += f
		}
		return floatVal(sum), nil

	case 5: // AVERAGE
		if len(args) == 0 {
			return floatVal(0), ErrEval
		}
		sum := 0.0
		for _, a := range args {
			f, ok := a.toFloat()
			if !ok {
				return floatVal(0), ErrEval
			}
			sum += f
		}
		return floatVal(sum / float64(len(args))), nil

	case 6: // MIN
		if len(args) == 0 {
			return floatVal(0), ErrEval
		}
		m, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		for _, a := range args[1:] {
			f, ok := a.toFloat()
			if !ok {
				return floatVal(0), ErrEval
			}
			if f < m {
				m = f
			}
		}
		return floatVal(m), nil

	case 7: // MAX
		if len(args) == 0 {
			return floatVal(0), ErrEval
		}
		m, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		for _, a := range args[1:] {
			f, ok := a.toFloat()
			if !ok {
				return floatVal(0), ErrEval
			}
			if f > m {
				m = f
			}
		}
		return floatVal(m), nil

	case 36: // AND
		for _, a := range args {
			f, ok := a.toFloat()
			if !ok {
				return boolVal(false), ErrEval
			}
			if f == 0 {
				return boolVal(false), nil
			}
		}
		return boolVal(true), nil

	case 37: // OR
		for _, a := range args {
			f, ok := a.toFloat()
			if !ok {
				return boolVal(false), ErrEval
			}
			if f != 0 {
				return boolVal(true), nil
			}
		}
		return boolVal(false), nil

	case 1: // IF(test, true_val, false_val)
		if len(args) < 2 {
			return floatVal(0), ErrEval
		}
		f, ok := args[0].toFloat()
		if !ok {
			return floatVal(0), ErrEval
		}
		if f != 0 {
			return args[1], nil
		}
		if len(args) >= 3 {
			return args[2], nil
		}
		return boolVal(false), nil
	}

	return floatVal(0), fmt.Errorf("formula eval: unsupported function %d", id)
}

// mathFunc1 applies a single-argument math function.
func mathFunc1(args []evalValue, fn func(float64) float64) (evalValue, error) {
	if len(args) < 1 {
		return floatVal(0), ErrEval
	}
	f, ok := args[0].toFloat()
	if !ok {
		return floatVal(0), ErrEval
	}
	return floatVal(fn(f)), nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func evalValueToAny(v evalValue) any {
	switch v.kind {
	case kindFloat:
		return v.f
	case kindString:
		return v.s
	case kindBool:
		return v.b
	case kindErr:
		return v.err
	}
	return nil
}

func anyToEvalValue(v any) evalValue {
	switch x := v.(type) {
	case float64:
		return floatVal(x)
	case string:
		return strVal(x)
	case bool:
		return boolVal(x)
	case byte:
		return errVal(x)
	}
	return floatVal(0)
}

// a1ToRowCol converts an A1-notation cell reference (like "$A$1" or "B3")
// back to 0-based (row, col).  Returns ok=false if the string cannot be parsed.
func a1ToRowCol(ref string) (row, col int, ok bool) {
	// Strip leading sheet prefix "Sheet1!"
	if idx := strings.LastIndex(ref, "!"); idx >= 0 {
		ref = ref[idx+1:]
	}
	cr, parseOK := parseCellRef(strings.ToUpper(ref))
	if !parseOK {
		return 0, 0, false
	}
	return cr.row, cr.col, true
}

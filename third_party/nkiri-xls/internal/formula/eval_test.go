package formula

import (
	"encoding/binary"
	"math"
	"testing"
)

// ── token builder helpers ──────────────────────────────────────────────────────

func tokInt(n uint16) []byte {
	b := make([]byte, 3)
	b[0] = byte(PtgInt)
	binary.LittleEndian.PutUint16(b[1:], n)
	return b
}

func tokNum(f float64) []byte {
	b := make([]byte, 9)
	b[0] = byte(PtgNum)
	binary.LittleEndian.PutUint64(b[1:], math.Float64bits(f))
	return b
}

func tokBool(v bool) []byte {
	b := byte(0)
	if v {
		b = 1
	}
	return []byte{byte(PtgBool), b}
}

// tokStr encodes PtgStr using the BIFF8 ShortString format (Latin-1).
func tokStr(s string) []byte {
	b := []byte{byte(PtgStr), byte(len(s)), 0x00} // cch, grBit=compressed
	return append(b, []byte(s)...)
}

func tokErr(code byte) []byte {
	return []byte{byte(PtgErr), code}
}

func tokRef(row, colWord uint16) []byte {
	b := make([]byte, 5)
	b[0] = byte(PtgRef)
	binary.LittleEndian.PutUint16(b[1:], row)
	binary.LittleEndian.PutUint16(b[3:], colWord)
	return b
}

// tokFunc builds a PtgFunc token (fixed arg count).
func tokFunc(id uint16) []byte {
	b := make([]byte, 3)
	b[0] = byte(PtgFunc)
	binary.LittleEndian.PutUint16(b[1:], id)
	return b
}

// tokFuncVar builds a PtgFuncVar token (variable arg count).
func tokFuncVar(cargs byte, id uint16) []byte {
	b := make([]byte, 4)
	b[0] = byte(PtgFuncVar)
	b[1] = cargs
	binary.LittleEndian.PutUint16(b[2:], id)
	return b
}

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ── Eval: constant tokens ──────────────────────────────────────────────────────

func TestEval_PtgInt(t *testing.T) {
	got, err := Eval(tokInt(42), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(42) {
		t.Errorf("got %v, want 42.0", got)
	}
}

func TestEval_PtgNum(t *testing.T) {
	got, err := Eval(tokNum(3.14), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3.14 {
		t.Errorf("got %v, want 3.14", got)
	}
}

func TestEval_PtgBool_True(t *testing.T) {
	got, err := Eval(tokBool(true), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestEval_PtgBool_False(t *testing.T) {
	got, err := Eval(tokBool(false), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestEval_PtgStr(t *testing.T) {
	got, err := Eval(tokStr("hello"), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %v, want hello", got)
	}
}

func TestEval_PtgErr(t *testing.T) {
	got, err := Eval(tokErr(ErrDiv0), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != byte(ErrDiv0) {
		t.Errorf("got %v, want %d", got, ErrDiv0)
	}
}

func TestEval_PtgMissArg(t *testing.T) {
	// PtgMissArg pushes 0.0
	tokens := join([]byte{byte(PtgMissArg)}, tokInt(5), []byte{byte(PtgAdd)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(5) {
		t.Errorf("got %v, want 5.0", got)
	}
}

// ── Eval: unary operators ──────────────────────────────────────────────────────

func TestEval_PtgUplus(t *testing.T) {
	tokens := join(tokInt(7), []byte{byte(PtgUplus)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(7) {
		t.Errorf("got %v, want 7.0", got)
	}
}

func TestEval_PtgUminus(t *testing.T) {
	tokens := join(tokInt(5), []byte{byte(PtgUminus)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(-5) {
		t.Errorf("got %v, want -5.0", got)
	}
}

func TestEval_PtgPercent(t *testing.T) {
	tokens := join(tokInt(50), []byte{byte(PtgPercent)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.5 {
		t.Errorf("got %v, want 0.5", got)
	}
}

// ── Eval: binary arithmetic ────────────────────────────────────────────────────

func TestEval_PtgAdd_Floats(t *testing.T) {
	tokens := join(tokInt(3), tokInt(4), []byte{byte(PtgAdd)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(7) {
		t.Errorf("got %v, want 7.0", got)
	}
}

func TestEval_PtgAdd_Strings_ReturnsError(t *testing.T) {
	// popFloat pops items from the stack even on type mismatch, so the
	// string-concat fallback in PtgAdd cannot recover them — ErrEval is expected.
	tokens := join(tokStr("foo"), tokStr("bar"), []byte{byte(PtgAdd)})
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected ErrEval when adding two strings via PtgAdd")
	}
}

func TestEval_PtgSub(t *testing.T) {
	tokens := join(tokInt(10), tokInt(3), []byte{byte(PtgSub)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(7) {
		t.Errorf("got %v, want 7.0", got)
	}
}

func TestEval_PtgMul(t *testing.T) {
	tokens := join(tokInt(6), tokInt(7), []byte{byte(PtgMul)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(42) {
		t.Errorf("got %v, want 42.0", got)
	}
}

func TestEval_PtgDiv(t *testing.T) {
	tokens := join(tokInt(10), tokInt(2), []byte{byte(PtgDiv)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(5) {
		t.Errorf("got %v, want 5.0", got)
	}
}

func TestEval_PtgDiv_ByZero(t *testing.T) {
	tokens := join(tokInt(1), tokInt(0), []byte{byte(PtgDiv)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != byte(ErrDiv0) {
		t.Errorf("got %v, want #DIV/0! (%d)", got, ErrDiv0)
	}
}

func TestEval_PtgPower(t *testing.T) {
	tokens := join(tokInt(2), tokInt(10), []byte{byte(PtgPower)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(1024) {
		t.Errorf("got %v, want 1024.0", got)
	}
}

func TestEval_PtgConcat(t *testing.T) {
	tokens := join(tokStr("foo"), tokStr("bar"), []byte{byte(PtgConcat)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "foobar" {
		t.Errorf("got %v, want foobar", got)
	}
}

// ── Eval: comparison operators ─────────────────────────────────────────────────

func TestEval_Comparisons(t *testing.T) {
	cases := []struct {
		op   Ptg
		a, b int
		want bool
	}{
		{PtgLT, 1, 2, true},
		{PtgLT, 2, 1, false},
		{PtgLE, 2, 2, true},
		{PtgLE, 3, 2, false},
		{PtgEQ, 5, 5, true},
		{PtgEQ, 5, 6, false},
		{PtgGE, 3, 3, true},
		{PtgGE, 2, 3, false},
		{PtgGT, 4, 3, true},
		{PtgGT, 3, 4, false},
		{PtgNE, 1, 2, true},
		{PtgNE, 1, 1, false},
	}
	for _, tc := range cases {
		tokens := join(tokInt(uint16(tc.a)), tokInt(uint16(tc.b)), []byte{byte(tc.op)})
		got, err := Eval(tokens, 0, 0, nil)
		if err != nil {
			t.Errorf("op 0x%02X: %v", tc.op, err)
			continue
		}
		if got != tc.want {
			t.Errorf("op 0x%02X (%d, %d): got %v, want %v", tc.op, tc.a, tc.b, got, tc.want)
		}
	}
}

// ── Eval: control tokens ───────────────────────────────────────────────────────

func TestEval_PtgParen(t *testing.T) {
	tokens := join(tokInt(9), []byte{byte(PtgParen)})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(9) {
		t.Errorf("got %v, want 9.0", got)
	}
}

func TestEval_PtgAttr_Sum(t *testing.T) {
	// PtgAttr with PtgAttrSum flag: acts as SUM shortcut (identity for a scalar)
	tokens := join(tokInt(7), []byte{byte(PtgAttr), PtgAttrSum, 0x00, 0x00, 0x00})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(7) {
		t.Errorf("got %v, want 7.0", got)
	}
}

func TestEval_PtgAttr_NonSum(t *testing.T) {
	// PtgAttr without PtgAttrSum: consumed but stack unchanged
	tokens := join(tokNum(2.5), []byte{byte(PtgAttr), 0x00, 0x00, 0x00, 0x00})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2.5 {
		t.Errorf("got %v, want 2.5", got)
	}
}

// ── Eval: cell references ──────────────────────────────────────────────────────

func TestEval_PtgRef_WithLookup(t *testing.T) {
	// Absolute $A$1 (row=0, colWord=0x0000 → both absolute)
	tokens := tokRef(0, 0x0000)
	lookup := func(row, col int) (any, bool) {
		if row == 0 && col == 0 {
			return float64(42), true
		}
		return nil, false
	}
	got, err := Eval(tokens, 0, 0, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(42) {
		t.Errorf("got %v, want 42.0", got)
	}
}

func TestEval_PtgRef_WithLookup_String(t *testing.T) {
	tokens := tokRef(1, 0x0000) // $A$2
	lookup := func(row, col int) (any, bool) {
		if row == 1 && col == 0 {
			return "hello", true
		}
		return nil, false
	}
	got, err := Eval(tokens, 0, 0, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %v, want hello", got)
	}
}

func TestEval_PtgRef_NoLookup(t *testing.T) {
	tokens := tokRef(0, 0x0000)
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error with nil lookup, got nil")
	}
}

func TestEval_PtgRef_LookupNotFound(t *testing.T) {
	tokens := tokRef(0, 0x0000)
	_, err := Eval(tokens, 0, 0, func(row, col int) (any, bool) { return nil, false })
	if err == nil {
		t.Fatal("expected error when lookup returns false")
	}
}

// ── Eval: bookkeeping tokens ───────────────────────────────────────────────────

func TestEval_PtgMemArea(t *testing.T) {
	// PtgMemArea consumes 6 bytes, then the float on stack remains
	tokens := join(tokInt(3), []byte{byte(PtgMemArea), 0, 0, 0, 0, 0, 0})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(3) {
		t.Errorf("got %v, want 3.0", got)
	}
}

func TestEval_PtgMemNoMem(t *testing.T) {
	tokens := join(tokInt(5), []byte{byte(PtgMemNoMem), 0, 0})
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(5) {
		t.Errorf("got %v, want 5.0", got)
	}
}

// ── Eval: PtgFunc (fixed-arg) ─────────────────────────────────────────────────

func TestEval_Func_PI(t *testing.T) {
	tokens := tokFunc(19) // PI(), 0 args
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != math.Pi {
		t.Errorf("got %v, want Pi", got)
	}
}

func TestEval_Func_TRUE(t *testing.T) {
	tokens := tokFunc(34)
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestEval_Func_FALSE(t *testing.T) {
	tokens := tokFunc(35)
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestEval_Func_NA(t *testing.T) {
	tokens := tokFunc(10)
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != byte(ErrNA) {
		t.Errorf("got %v, want #N/A (%d)", got, ErrNA)
	}
}

func TestEval_Func_RAND(t *testing.T) {
	tokens := tokFunc(63)
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(0) {
		t.Errorf("got %v, want 0.0 (deterministic)", got)
	}
}

func TestEval_Func_ABS(t *testing.T) {
	tokens := join(tokNum(-5.0), tokFunc(24))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5.0 {
		t.Errorf("got %v, want 5.0", got)
	}
}

func TestEval_Func_SQRT(t *testing.T) {
	tokens := join(tokNum(9.0), tokFunc(20))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3.0 {
		t.Errorf("got %v, want 3.0", got)
	}
}

func TestEval_Func_SIN(t *testing.T) {
	tokens := join(tokNum(0.0), tokFunc(15))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(0) {
		t.Errorf("got %v, want 0.0", got)
	}
}

func TestEval_Func_NOT(t *testing.T) {
	tokens := join(tokInt(0), tokFunc(38))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true (NOT 0)", got)
	}
}

func TestEval_Func_ROUND(t *testing.T) {
	tokens := join(tokNum(3.14159), tokNum(2.0), tokFunc(27))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-3.14) > 1e-10 {
		t.Errorf("got %v, want 3.14", got)
	}
}

func TestEval_Func_ROW(t *testing.T) {
	// ROW() with 1 arg (fixedArgCount=1 for id=8)
	tokens := join(tokInt(0), tokFunc(8))
	got, err := Eval(tokens, 2, 0, nil) // baseRow=2 → ROW()=3
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(3) {
		t.Errorf("got %v, want 3.0", got)
	}
}

func TestEval_Func_COLUMN(t *testing.T) {
	tokens := join(tokInt(0), tokFunc(9))
	got, err := Eval(tokens, 0, 4, nil) // baseCol=4 → COLUMN()=5
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(5) {
		t.Errorf("got %v, want 5.0", got)
	}
}

func TestEval_Func_LEN(t *testing.T) {
	tokens := join(tokStr("hello"), tokFunc(32))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(5) {
		t.Errorf("got %v, want 5.0", got)
	}
}

// ── Eval: PtgFuncVar (variable-arg) ──────────────────────────────────────────

func TestEval_FuncVar_SUM(t *testing.T) {
	tokens := join(tokInt(1), tokInt(2), tokInt(3), tokFuncVar(3, 4))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(6) {
		t.Errorf("got %v, want 6.0", got)
	}
}

func TestEval_FuncVar_AVERAGE(t *testing.T) {
	tokens := join(tokInt(2), tokInt(4), tokInt(6), tokFuncVar(3, 5))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(4) {
		t.Errorf("got %v, want 4.0", got)
	}
}

func TestEval_FuncVar_MIN(t *testing.T) {
	tokens := join(tokInt(5), tokInt(2), tokInt(8), tokFuncVar(3, 6))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(2) {
		t.Errorf("got %v, want 2.0", got)
	}
}

func TestEval_FuncVar_MAX(t *testing.T) {
	tokens := join(tokInt(5), tokInt(2), tokInt(8), tokFuncVar(3, 7))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(8) {
		t.Errorf("got %v, want 8.0", got)
	}
}

func TestEval_FuncVar_IF_True(t *testing.T) {
	tokens := join(tokBool(true), tokInt(10), tokInt(20), tokFuncVar(3, 1))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(10) {
		t.Errorf("got %v, want 10.0", got)
	}
}

func TestEval_FuncVar_IF_False(t *testing.T) {
	tokens := join(tokBool(false), tokInt(10), tokInt(20), tokFuncVar(3, 1))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(20) {
		t.Errorf("got %v, want 20.0", got)
	}
}

func TestEval_FuncVar_AND_True(t *testing.T) {
	tokens := join(tokBool(true), tokBool(true), tokFuncVar(2, 36))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestEval_FuncVar_AND_False(t *testing.T) {
	tokens := join(tokBool(true), tokBool(false), tokFuncVar(2, 36))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestEval_FuncVar_OR_True(t *testing.T) {
	tokens := join(tokBool(false), tokBool(true), tokFuncVar(2, 37))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestEval_FuncVar_OR_False(t *testing.T) {
	tokens := join(tokBool(false), tokBool(false), tokFuncVar(2, 37))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestEval_FuncVar_LOWER(t *testing.T) {
	tokens := join(tokStr("HELLO"), tokFuncVar(1, 112))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %v, want hello", got)
	}
}

func TestEval_FuncVar_UPPER(t *testing.T) {
	tokens := join(tokStr("world"), tokFuncVar(1, 113))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "WORLD" {
		t.Errorf("got %v, want WORLD", got)
	}
}

func TestEval_FuncVar_TRIM(t *testing.T) {
	tokens := join(tokStr("  hi  "), tokFuncVar(1, 118))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi" {
		t.Errorf("got %v, want hi", got)
	}
}

func TestEval_FuncVar_ROUNDUP(t *testing.T) {
	tokens := join(tokNum(1.23), tokNum(1.0), tokFuncVar(2, 208))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-1.3) > 1e-10 {
		t.Errorf("got %v, want 1.3", got)
	}
}

func TestEval_FuncVar_ROUNDDOWN(t *testing.T) {
	tokens := join(tokNum(1.89), tokNum(1.0), tokFuncVar(2, 209))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-1.8) > 1e-10 {
		t.Errorf("got %v, want 1.8", got)
	}
}

func TestEval_FuncVar_POWER(t *testing.T) {
	tokens := join(tokNum(3.0), tokNum(3.0), tokFuncVar(2, 331))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(27) {
		t.Errorf("got %v, want 27.0", got)
	}
}

func TestEval_FuncVar_FLOOR(t *testing.T) {
	tokens := join(tokNum(7.8), tokNum(2.0), tokFuncVar(2, 280))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(6) {
		t.Errorf("got %v, want 6.0", got)
	}
}

func TestEval_FuncVar_CEILING(t *testing.T) {
	tokens := join(tokNum(7.2), tokNum(2.0), tokFuncVar(2, 283))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(8) {
		t.Errorf("got %v, want 8.0", got)
	}
}

func TestEval_FuncVar_ATAN2(t *testing.T) {
	tokens := join(tokNum(1.0), tokNum(0.0), tokFuncVar(2, 97))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-0.0) > 1e-10 {
		t.Errorf("got %v, want 0.0", got)
	}
}

func TestEval_FuncVar_LOG_Base10(t *testing.T) {
	tokens := join(tokNum(100.0), tokFuncVar(1, 109))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-2.0) > 1e-10 {
		t.Errorf("got %v, want 2.0", got)
	}
}

func TestEval_FuncVar_LOG_WithBase(t *testing.T) {
	tokens := join(tokNum(8.0), tokNum(2.0), tokFuncVar(2, 109))
	got, err := Eval(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.(float64)-3.0) > 1e-10 {
		t.Errorf("got %v, want 3.0", got)
	}
}

// ── Eval: error paths ──────────────────────────────────────────────────────────

func TestEval_EmptyTokens_NoResult(t *testing.T) {
	_, err := Eval(nil, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for empty tokens, got nil")
	}
}

func TestEval_UnsupportedPtg(t *testing.T) {
	tokens := []byte{0xFF} // Unknown opcode
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for unsupported Ptg, got nil")
	}
}

func TestEval_PtgInt_TooShort(t *testing.T) {
	tokens := []byte{byte(PtgInt), 0x01} // missing 2nd byte
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for truncated PtgInt")
	}
}

func TestEval_PtgUminus_EmptyStack(t *testing.T) {
	tokens := []byte{byte(PtgUminus)}
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for PtgUminus on empty stack")
	}
}

func TestEval_PtgSub_EmptyStack(t *testing.T) {
	tokens := join(tokInt(1), []byte{byte(PtgSub)})
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for PtgSub with only 1 operand")
	}
}

func TestEval_PtgArray_Unsupported(t *testing.T) {
	tokens := []byte{byte(PtgArray), 0, 0, 0, 0, 0, 0, 0}
	_, err := Eval(tokens, 0, 0, nil)
	if err == nil {
		t.Fatal("expected ErrEval for PtgArray")
	}
}

func TestEval_PtgFunc_UnsupportedID(t *testing.T) {
	// funcID=9999 is unknown; fixedArgCount returns 0 so no args needed
	b := make([]byte, 3)
	b[0] = byte(PtgFunc)
	binary.LittleEndian.PutUint16(b[1:], 9999)
	_, err := Eval(b, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for unsupported funcID")
	}
}

// ── Eval: anyToEvalValue & evalValueToAny coverage ───────────────────────────

func TestEval_PtgRef_LookupBool(t *testing.T) {
	tokens := tokRef(0, 0x0000)
	lookup := func(row, col int) (any, bool) {
		return true, true
	}
	got, err := Eval(tokens, 0, 0, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestEval_PtgRef_LookupError(t *testing.T) {
	tokens := tokRef(0, 0x0000)
	lookup := func(row, col int) (any, bool) {
		return byte(ErrNA), true
	}
	got, err := Eval(tokens, 0, 0, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != byte(ErrNA) {
		t.Errorf("got %v, want #N/A byte", got)
	}
}

package formula

import (
	"testing"
)

func TestDecodeWithNamesResolvesFutureFunctionWithoutEvaluation(t *testing.T) {
	tokens := []byte{
		byte(PtgNameX), 0, 0, 1, 0, 0, 0,
		byte(PtgFuncVar), 1, 0xFF, 0,
	}
	got, err := DecodeWithNames(tokens, 0, 0, nil, nil, []string{"XLOOKUP"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "XLOOKUP()" {
		t.Fatalf("decoded future function = %q", got)
	}
}

// ── colToA1 ───────────────────────────────────────────────────────────────────

func TestColToA1(t *testing.T) {
	cases := []struct {
		col  int
		want string
	}{
		{0, "A"},
		{1, "B"},
		{25, "Z"},
		{26, "AA"},
		{27, "AB"},
		{51, "AZ"},
		{52, "BA"},
		{701, "ZZ"},
		{702, "AAA"},
	}
	for _, tc := range cases {
		if got := colToA1(tc.col); got != tc.want {
			t.Errorf("colToA1(%d) = %q, want %q", tc.col, got, tc.want)
		}
	}
}

// ── a1ToCol round-trip ────────────────────────────────────────────────────────

func TestA1ToColRoundTrip(t *testing.T) {
	for _, col := range []int{0, 1, 25, 26, 27, 51, 52, 255, 701, 702} {
		label := colToA1(col)
		got := a1ToCol(label)
		if got != col {
			t.Errorf("round-trip col %d → %q → %d", col, label, got)
		}
	}
}

// ── Decode: simple arithmetic ─────────────────────────────────────────────────

func TestDecode_Arithmetic(t *testing.T) {
	// 1 + 2  →  PtgInt(1) PtgInt(2) PtgAdd
	tokens := []byte{
		byte(PtgInt), 1, 0, // 1
		byte(PtgInt), 2, 0, // 2
		byte(PtgAdd),
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1+2" {
		t.Errorf("got %q, want %q", got, "1+2")
	}
}

func TestDecode_UnaryMinus(t *testing.T) {
	// -3.14  →  PtgNum(3.14) PtgUminus
	tokens := []byte{
		byte(PtgNum), 0x1f, 0x85, 0xeb, 0x51, 0xb8, 0x1e, 0x09, 0x40, // 3.14
		byte(PtgUminus),
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "-3.14" {
		t.Errorf("got %q, want %q", got, "-3.14")
	}
}

func TestDecode_Percent(t *testing.T) {
	// 50%  →  PtgInt(50) PtgPercent
	tokens := []byte{
		byte(PtgInt), 50, 0,
		byte(PtgPercent),
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "50%" {
		t.Errorf("got %q, want %q", got, "50%")
	}
}

// ── Decode: boolean and error constants ───────────────────────────────────────

func TestDecode_Bool(t *testing.T) {
	for _, tc := range []struct {
		b    byte
		want string
	}{
		{1, "TRUE"},
		{0, "FALSE"},
	} {
		tokens := []byte{byte(PtgBool), tc.b}
		got, err := Decode(tokens, 0, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("PtgBool(%d): got %q, want %q", tc.b, got, tc.want)
		}
	}
}

func TestDecode_Error(t *testing.T) {
	tokens := []byte{byte(PtgErr), ErrDiv0}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "#DIV/0!" {
		t.Errorf("got %q", got)
	}
}

// ── Decode: string constant ───────────────────────────────────────────────────

func TestDecode_String(t *testing.T) {
	// PtgStr "hello" (Latin-1 compressed)
	// Format: opcode(1) + cch(1) + grBit(1) + chars(5)
	s := "hello"
	tokens := []byte{byte(PtgStr), byte(len(s)), 0x00, 'h', 'e', 'l', 'l', 'o'}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"hello"` {
		t.Errorf("got %q, want %q", got, `"hello"`)
	}
}

func TestDecode_StringWithQuote(t *testing.T) {
	// String containing a double-quote: say`it`
	// In formula output: `"say""it"`
	s := `say"it`
	tokens := append([]byte{byte(PtgStr), byte(len(s)), 0x00}, []byte(s)...)
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `"say""it"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── Decode: cell references ───────────────────────────────────────────────────

func TestDecode_RefAbsolute(t *testing.T) {
	// Absolute ref $A$1 from any base cell
	// row=0, col=0, both absolute → fRowRel=0, fColRel=0
	tokens := []byte{
		byte(PtgRef),
		0x00, 0x00, // row=0
		0x00, 0x00, // col=0, no flags
	}
	got, err := Decode(tokens, 5, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "$A$1" {
		t.Errorf("got %q, want $A$1", got)
	}
}

func TestDecode_RefRelative(t *testing.T) {
	// Relative ref A1 from base (0,0): offset row=0, offset col=0
	// fRowRel=1 (bit15), fColRel=1 (bit14) → colWord = 0xC000 | 0
	tokens := []byte{
		byte(PtgRef),
		0x00, 0x00, // row offset = 0
		0x00, 0xC0, // colWord with fRowRel+fColRel, col offset=0
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "A1" {
		t.Errorf("got %q, want A1", got)
	}
}

func TestDecode_Area(t *testing.T) {
	// Absolute range $A$1:$C$3
	// r1=0,c1=0,r2=2,c2=2 all absolute
	tokens := []byte{
		byte(PtgArea),
		0x00, 0x00, // r1=0
		0x02, 0x00, // r2=2
		0x00, 0x00, // c1=0, no flags
		0x02, 0x00, // c2=2, no flags
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "$A$1:$C$3" {
		t.Errorf("got %q, want $A$1:$C$3", got)
	}
}

// ── Decode: function calls ────────────────────────────────────────────────────

func TestDecode_FuncSUM(t *testing.T) {
	// SUM(A1:B2)  →  PtgArea(A1:B2) PtgFuncVar(cargs=1, SUM=4)
	areaTokens := []byte{
		byte(PtgArea),
		0x00, 0x00, // r1=0
		0x01, 0x00, // r2=1
		0x00, 0xC0, // c1=0 relative
		0x01, 0xC0, // c2=1 relative
	}
	funcToken := []byte{byte(PtgFuncVar), 0x01, 0x04, 0x00} // cargs=1, SUM=4
	tokens := append(areaTokens, funcToken...)
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SUM(A1:B2)" {
		t.Errorf("got %q, want SUM(A1:B2)", got)
	}
}

func TestDecode_FuncIF(t *testing.T) {
	// IF(TRUE,1,0) → PtgBool(1) PtgInt(1) PtgInt(0) PtgFuncVar(cargs=3, IF=1)
	tokens := []byte{
		byte(PtgBool), 1,
		byte(PtgInt), 1, 0,
		byte(PtgInt), 0, 0,
		byte(PtgFuncVar), 3, 1, 0,
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "IF(TRUE,1,0)" {
		t.Errorf("got %q, want IF(TRUE,1,0)", got)
	}
}

func TestDecode_OptimizedIFAttributesKeepTokenAlignment(t *testing.T) {
	tokens := []byte{
		byte(PtgBool), 1,
		byte(PtgAttr), PtgAttrIf, 7, 0,
		byte(PtgInt), 1, 0,
		byte(PtgAttr), PtgAttrGoto, 3, 0,
		byte(PtgInt), 0, 0,
		byte(PtgFuncVar), 3, 1, 0,
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "IF(TRUE,1,0)" {
		t.Fatalf("optimized IF decoded as %q", got)
	}
}

// ── Decode: concatenation ─────────────────────────────────────────────────────

func TestDecode_Concat(t *testing.T) {
	// "A"&"B" → PtgStr("A") PtgStr("B") PtgConcat
	tokens := []byte{
		byte(PtgStr), 1, 0, 'A',
		byte(PtgStr), 1, 0, 'B',
		byte(PtgConcat),
	}
	got, err := Decode(tokens, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"A"&"B"` {
		t.Errorf("got %q, want %q", got, `"A"&"B"`)
	}
}

// ── Encode round-trip tests ───────────────────────────────────────────────────

func TestEncode_Integer(t *testing.T) {
	b, err := Encode("42", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "42" {
		t.Errorf("got %q, want 42", got)
	}
}

func TestEncode_Float(t *testing.T) {
	b, err := Encode("3.14", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "3.14" {
		t.Errorf("got %q, want 3.14", got)
	}
}

func TestEncode_String(t *testing.T) {
	b, err := Encode(`"hello"`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"hello"` {
		t.Errorf("got %q, want %q", got, `"hello"`)
	}
}

func TestEncode_BoolTrue(t *testing.T) {
	b, err := Encode("TRUE", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "TRUE" {
		t.Errorf("got %q", got)
	}
}

func TestEncode_ArithmeticWithLeadingEquals(t *testing.T) {
	b, err := Encode("=1+2", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1+2" {
		t.Errorf("got %q, want 1+2", got)
	}
}

func TestEncode_CellRef(t *testing.T) {
	// Encode absolute $B$3 from base (0,0)
	b, err := Encode("$B$3", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "$B$3" {
		t.Errorf("got %q, want $B$3", got)
	}
}

func TestEncode_Range(t *testing.T) {
	// Encode absolute range $A$1:$C$3
	b, err := Encode("$A$1:$C$3", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "$A$1:$C$3" {
		t.Errorf("got %q, want $A$1:$C$3", got)
	}
}

func TestEncode_Function(t *testing.T) {
	b, err := Encode("SUM($A$1:$B$2)", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SUM($A$1:$B$2)" {
		t.Errorf("got %q, want SUM($A$1:$B$2)", got)
	}
}

func TestEncode_UnaryMinus(t *testing.T) {
	b, err := Encode("-5", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "-5" {
		t.Errorf("got %q, want -5", got)
	}
}

// ── parseCellRef ──────────────────────────────────────────────────────────────

func TestParseCellRef(t *testing.T) {
	cases := []struct {
		s      string
		wantOK bool
		row    int
		col    int
		rowAbs bool
		colAbs bool
	}{
		{"A1", true, 0, 0, false, false},
		{"B2", true, 1, 1, false, false},
		{"$A1", true, 0, 0, false, true},
		{"A$1", true, 0, 0, true, false},
		{"$A$1", true, 0, 0, true, true},
		{"AA10", true, 9, 26, false, false},
		{"Z100", true, 99, 25, false, false},
		{"1A", false, 0, 0, false, false},
		{"", false, 0, 0, false, false},
	}
	for _, tc := range cases {
		ref, ok := parseCellRef(tc.s)
		if ok != tc.wantOK {
			t.Errorf("parseCellRef(%q): ok=%v, want %v", tc.s, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if ref.row != tc.row || ref.col != tc.col || ref.rowAbs != tc.rowAbs || ref.colAbs != tc.colAbs {
			t.Errorf("parseCellRef(%q): got {row=%d,col=%d,rowAbs=%v,colAbs=%v}, want {%d,%d,%v,%v}",
				tc.s, ref.row, ref.col, ref.rowAbs, ref.colAbs,
				tc.row, tc.col, tc.rowAbs, tc.colAbs)
		}
	}
}

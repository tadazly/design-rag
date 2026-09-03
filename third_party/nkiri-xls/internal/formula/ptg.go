package formula

// Ptg (parsed thing) opcode constants for BIFF8 formula token streams.
//
// Each Ptg is one byte. Operand tokens carry data; operator tokens consume
// operands from the evaluation stack. The high two bits of many operand
// tokens encode the "Ptg class" (reference / value / array), which we strip
// when classifying tokens.
//
// Reference: [MS-XLS] §2.5.198

// Ptg is a parsed-thing opcode.
type Ptg byte

const (
	// ── Arithmetic / comparison operators ─────────────────────────────────
	PtgAdd    Ptg = 0x03 // infix +
	PtgSub    Ptg = 0x04 // infix -
	PtgMul    Ptg = 0x05 // infix *
	PtgDiv    Ptg = 0x06 // infix /
	PtgPower  Ptg = 0x07 // infix ^
	PtgConcat Ptg = 0x08 // infix &
	PtgLT     Ptg = 0x09 // infix <
	PtgLE     Ptg = 0x0A // infix <=
	PtgEQ     Ptg = 0x0B // infix =
	PtgGE     Ptg = 0x0C // infix >=
	PtgGT     Ptg = 0x0D // infix >
	PtgNE     Ptg = 0x0E // infix <>

	// ── Unary operators ───────────────────────────────────────────────────
	PtgUplus   Ptg = 0x12 // unary +
	PtgUminus  Ptg = 0x13 // unary -
	PtgPercent Ptg = 0x14 // postfix %

	// ── Reference operators ───────────────────────────────────────────────
	PtgUnion Ptg = 0x10 // union (comma in arg list)
	PtgIsect Ptg = 0x0F // intersection (space operator)
	PtgRange Ptg = 0x11 // range (colon operator)

	// ── Control ───────────────────────────────────────────────────────────
	PtgParen   Ptg = 0x15 // parentheses (no-op)
	PtgMissArg Ptg = 0x16 // missing argument
	PtgAttr    Ptg = 0x19 // special attribute (IF, GOTO, SUM shortcut, SPACE)

	// ── Operand tokens ────────────────────────────────────────────────────
	// These appear in three "class" variants: Ref (base), Val (+0x20), Array (+0x40).
	// We always normalise to the base (Ref) value for identification.

	PtgStr  Ptg = 0x17 // string constant
	PtgErr  Ptg = 0x1C // error constant
	PtgBool Ptg = 0x1D // boolean constant
	PtgInt  Ptg = 0x1E // integer constant (unsigned 16-bit)
	PtgNum  Ptg = 0x1F // floating-point constant (IEEE 754 64-bit)

	// Reference tokens (base / Ref class)
	PtgArray    Ptg = 0x20 // array constant
	PtgFunc     Ptg = 0x21 // fixed-argument built-in function
	PtgFuncVar  Ptg = 0x22 // variable-argument function
	PtgName     Ptg = 0x23 // defined name
	PtgRef      Ptg = 0x24 // single-cell reference
	PtgArea     Ptg = 0x25 // rectangular cell range
	PtgMemArea  Ptg = 0x26 // constant reference sub-expression
	PtgMemErr   Ptg = 0x27 // erroneous constant reference sub-expression
	PtgMemNoMem Ptg = 0x28 // non-constant reference sub-expression
	PtgMemFunc  Ptg = 0x29 // function in reference sub-expression
	PtgRefErr   Ptg = 0x2A // deleted reference
	PtgAreaErr  Ptg = 0x2B // deleted range
	PtgRefN     Ptg = 0x2C // relative reference (shared formula)
	PtgAreaN    Ptg = 0x2D // relative range (shared formula)

	// 3-D reference tokens
	PtgNameX     Ptg = 0x39 // 3-D defined name
	PtgRef3D     Ptg = 0x3A // 3-D single-cell reference
	PtgArea3D    Ptg = 0x3B // 3-D range reference
	PtgRefErr3D  Ptg = 0x3C // 3-D deleted reference
	PtgAreaErr3D Ptg = 0x3D // 3-D deleted range
)

// ptgClass strips the class bits (bits 5-6) so a Ref/Val/Array variant all map
// to the same base opcode.
func ptgClass(b byte) Ptg {
	// Operand Ptgs in the range 0x20-0x3F appear as:
	//   Ref   = base
	//   Val   = base | 0x20
	//   Array = base | 0x40
	// Normalise to Ref (strip bit 5 for Val, bit 6 for Array).
	if b >= 0x60 && b <= 0x7F {
		return Ptg(b - 0x40)
	}
	if b >= 0x40 && b <= 0x5F {
		return Ptg(b - 0x20)
	}
	return Ptg(b)
}

// PtgAttr sub-type bits (byte following the PtgAttr opcode)
const (
	PtgAttrSemi   = 0x01 // volatile / optimised-IF
	PtgAttrIf     = 0x02 // IF branch jump
	PtgAttrChoose = 0x04 // CHOOSE jump table
	PtgAttrGoto   = 0x08 // unconditional GOTO
	PtgAttrSum    = 0x10 // SUM shortcut (unary)
	PtgAttrBaxcel = 0x20 // assignment-style formula
	PtgAttrSpace  = 0x40 // white-space annotation
)

// Error byte values (same as BOOLERR record error codes)
const (
	ErrNull  = 0x00 // #NULL!
	ErrDiv0  = 0x07 // #DIV/0!
	ErrValue = 0x0F // #VALUE!
	ErrRef   = 0x17 // #REF!
	ErrName  = 0x1D // #NAME?
	ErrNum   = 0x24 // #NUM!
	ErrNA    = 0x2A // #N/A
)

// errString maps an error byte to its display string.
var errString = map[byte]string{
	ErrNull:  "#NULL!",
	ErrDiv0:  "#DIV/0!",
	ErrValue: "#VALUE!",
	ErrRef:   "#REF!",
	ErrName:  "#NAME?",
	ErrNum:   "#NUM!",
	ErrNA:    "#N/A",
}

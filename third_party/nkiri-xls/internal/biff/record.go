package biff

// RecordType is the 2-byte opcode that identifies a BIFF8 record.
type RecordType uint16

// Record holds a single BIFF8 record.  If the original stream contained
// CONTINUE records following this one, their payloads are already appended
// to Data by [Reader].
//
// ContinueOffsets lists the byte offsets within Data where each CONTINUE
// record's payload begins.  This is needed by the SST parser to correctly
// handle the grBit byte that BIFF8 places at the start of a CONTINUE payload
// when a string straddles the record boundary (MS-XLS §2.4.58).
type Record struct {
	Type            RecordType
	Data            []byte
	ContinueOffsets []int
}

// BIFF8 record opcodes (MS-XLS §2.3).
// Only opcodes that are used or parsed by this library are listed here.
const (
	// ── Workbook globals ─────────────────────────────────────────────────────
	RecBOF         RecordType = 0x0809 // Beginning of File
	RecEOF         RecordType = 0x000A // End of File
	RecContinue    RecordType = 0x003C // Continuation of a previous record
	RecCodePage    RecordType = 0x0042 // Code page used for BIFF7 strings
	RecDateMode    RecordType = 0x0022 // Date system (1900 or 1904)
	RecBoundSheet  RecordType = 0x0085 // Sheet descriptor (name + BOF offset)
	RecSST         RecordType = 0x00FC // Shared String Table
	RecExtSST      RecordType = 0x00FF // Shared String Table index
	RecSupBook     RecordType = 0x01AE // Supporting workbook for external names
	RecExternName  RecordType = 0x0023 // External/add-in name
	RecExternName2 RecordType = 0x0223 // Alternate BIFF external name record
	RecName        RecordType = 0x0018 // Workbook defined name

	// ── Style / formatting ───────────────────────────────────────────────────
	RecFont    RecordType = 0x0031 // Font record
	RecFormat  RecordType = 0x041E // Number format string
	RecXF      RecordType = 0x00E0 // Extended format (cell style)
	RecStyle   RecordType = 0x0293 // Built-in / user-defined style
	RecPalette RecordType = 0x0092 // Colour palette

	// ── Sheet data ───────────────────────────────────────────────────────────
	RecDimensions  RecordType = 0x0200 // Usable dimensions of the sheet
	RecRow         RecordType = 0x0208 // Row descriptor
	RecBlank       RecordType = 0x0201 // Empty cell (formatted)
	RecMulBlank    RecordType = 0x00BE // Multiple consecutive blank cells
	RecNumber      RecordType = 0x0203 // Cell with IEEE 754 double
	RecRK          RecordType = 0x027E // Cell with RK-encoded number
	RecMulRK       RecordType = 0x00BD // Multiple consecutive RK cells
	RecLabel       RecordType = 0x0204 // Cell with inline string (BIFF5/7)
	RecLabelSST    RecordType = 0x00FD // Cell with SST string reference
	RecBoolErr     RecordType = 0x0205 // Cell with boolean or error value
	RecFormula     RecordType = 0x0006 // Cell with a formula
	RecShrFmla     RecordType = 0x04BC // Shared formula definition
	RecString      RecordType = 0x0207 // Cached string result of a formula
	RecArray       RecordType = 0x0221 // Array formula
	RecMergedCells RecordType = 0x00E5 // Merged cell ranges

	// ── Sheet settings ───────────────────────────────────────────────────────
	RecIndex                RecordType = 0x020B // Row index for fast seek
	RecColInfo              RecordType = 0x007D // Column width / format
	RecDefColWidth          RecordType = 0x0055 // Default column width
	RecDefRowHeight         RecordType = 0x0225 // Default row height
	RecWindow1              RecordType = 0x003D // Workbook window settings
	RecWindow2              RecordType = 0x023E // Sheet window settings
	RecPane                 RecordType = 0x0041 // Freeze / split panes
	RecSelection            RecordType = 0x001D // Selected cell range
	RecSetup                RecordType = 0x00A1 // Page setup
	RecPrintGrid            RecordType = 0x002B // Print gridlines flag
	RecHorizontalPageBreaks RecordType = 0x001B
	RecVerticalPageBreaks   RecordType = 0x001A
)

// BOFType identifies the sub-stream type in a BOF record.
type BOFType uint16

const (
	BOFWorkbook  BOFType = 0x0005 // Workbook globals sub-stream
	BOFSheet     BOFType = 0x0010 // Worksheet sub-stream
	BOFChart     BOFType = 0x0020 // Chart sub-stream
	BOFMacro     BOFType = 0x0040 // Macro sub-stream
	BOFWorkspace BOFType = 0x0100 // Workspace file globals
)

// BoolErrType distinguishes boolean from error values in a BOOLERR cell.
type BoolErrType uint8

const (
	BoolErrBool  BoolErrType = 0
	BoolErrError BoolErrType = 1
)

// Error codes that can appear in BOOLERR cells.
const (
	ErrNull  = 0x00 // #NULL!
	ErrDiv0  = 0x07 // #DIV/0!
	ErrValue = 0x0F // #VALUE!
	ErrRef   = 0x17 // #REF!
	ErrName  = 0x1D // #NAME?
	ErrNum   = 0x24 // #NUM!
	ErrNA    = 0x2A // #N/A
)

// SheetVisibility values used in BOUNDSHEET records.
const (
	SheetVisible    = 0x00
	SheetHidden     = 0x01
	SheetVeryHidden = 0x02
)

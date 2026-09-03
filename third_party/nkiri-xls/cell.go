package xls

import (
	"fmt"
	"strconv"
	"time"
)

// CellType represents the data type of a cell value.
type CellType int

const (
	CellTypeEmpty   CellType = iota
	CellTypeString           // String (label or SST entry)
	CellTypeNumber           // Floating-point number
	CellTypeBool             // Boolean
	CellTypeError            // Formula error
	CellTypeFormula          // Formula result
	CellTypeDate             // Date/time stored as a number
)

// Cell represents a single cell in a worksheet.
type Cell struct {
	Row, Col      int
	Type          CellType
	value         any // string | float64 | bool | time.Time
	formula       string
	formulaTokens []byte
	sharedFormula bool
	sharedRow     int
	sharedCol     int
	Style         *Style
}

// Formula returns the decoded BIFF formula expression without a leading '='.
// It never evaluates the expression. An empty string means the cell is not a
// formula or the stored token stream could not be decoded.
func (c *Cell) Formula() string {
	if c == nil || c.Type != CellTypeFormula {
		return ""
	}
	return c.formula
}

// String returns the cell value as a string.
func (c *Cell) String() string {
	if c == nil {
		return ""
	}
	if v, ok := c.value.(string); ok {
		return v
	}
	return ""
}

// Float returns the cell value as a float64.
func (c *Cell) Float() float64 {
	if c == nil {
		return 0
	}
	if v, ok := c.value.(float64); ok {
		return v
	}
	return 0
}

// Bool returns the cell value as a bool.
func (c *Cell) Bool() bool {
	if c == nil {
		return false
	}
	if v, ok := c.value.(bool); ok {
		return v
	}
	return false
}

// Time returns the cell value as a time.Time.
func (c *Cell) Time() time.Time {
	if c == nil {
		return time.Time{}
	}
	if v, ok := c.value.(time.Time); ok {
		return v
	}
	return time.Time{}
}

// Value returns the cell content as a human-readable string regardless of type:
//
//   - Empty   → ""
//   - String  → the string value
//   - Number  → decimal representation (no trailing zeros)
//   - Bool    → "TRUE" or "FALSE"
//   - Date    → "2006-01-02" (date-only) or "2006-01-02T15:04:05Z" when
//     the time component is non-zero
//   - Error   → "#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?",
//     "#NUM!", "#N/A", or "#ERR!<code>" for unknown codes
//   - Formula → the cached calculation result, formatted by the same rules
//     as the concrete types above (string/number/bool/date/error)
func (c *Cell) Value() string {
	if c == nil {
		return ""
	}
	switch c.Type {
	case CellTypeEmpty:
		return ""
	case CellTypeString:
		return c.String()
	case CellTypeNumber:
		return strconv.FormatFloat(c.Float(), 'f', -1, 64)
	case CellTypeBool:
		if c.Bool() {
			return "TRUE"
		}
		return "FALSE"
	case CellTypeDate:
		return formatTime(c.Time())
	case CellTypeError:
		return formatErrorCode(c.value)
	case CellTypeFormula:
		// Formula cells store the cached calculation result in c.value.
		// Dispatch on the Go type of the result to produce the same
		// human-readable representation as the concrete type cases above.
		return valueFromNative(c.value)
	default:
		return ""
	}
}

// formatTime converts a time.Time to an Excel-style human-readable string.
// Pure dates (midnight UTC) are formatted as "2006-01-02"; values with a
// time component are formatted as RFC3339.
func formatTime(t time.Time) string {
	h, m, s := t.Clock()
	ns := t.Nanosecond()
	if h == 0 && m == 0 && s == 0 && ns == 0 {
		return t.Format("2006-01-02")
	}
	return t.UTC().Format(time.RFC3339)
}

// formatErrorCode converts an Excel error-code byte to its display string.
func formatErrorCode(v any) string {
	code, ok := v.(byte)
	if !ok {
		return "#ERROR!"
	}
	switch code {
	case 0x00:
		return "#NULL!"
	case 0x07:
		return "#DIV/0!"
	case 0x0F:
		return "#VALUE!"
	case 0x17:
		return "#REF!"
	case 0x1D:
		return "#NAME?"
	case 0x24:
		return "#NUM!"
	case 0x2A:
		return "#N/A"
	default:
		return fmt.Sprintf("#ERR!%02X", code)
	}
}

// valueFromNative converts the raw Go value stored in a formula cell to a
// human-readable string, applying the same rules as the concrete CellType
// cases in Value().
func valueFromNative(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	case time.Time:
		return formatTime(val)
	case byte:
		return formatErrorCode(val)
	}
	return ""
}

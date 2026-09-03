package xls

import (
	"testing"
	"time"
)

// ── nil receiver ──────────────────────────────────────────────────────────────

func TestCell_NilReceiver(t *testing.T) {
	var c *Cell

	if got := c.String(); got != "" {
		t.Errorf("nil.String() = %q, want empty", got)
	}
	if got := c.Float(); got != 0 {
		t.Errorf("nil.Float() = %v, want 0", got)
	}
	if got := c.Bool(); got != false {
		t.Errorf("nil.Bool() = %v, want false", got)
	}
	if !c.Time().IsZero() {
		t.Errorf("nil.Time() = %v, want zero", c.Time())
	}
	if got := c.Value(); got != "" {
		t.Errorf("nil.Value() = %q, want empty", got)
	}
}

// ── String() ──────────────────────────────────────────────────────────────────

func TestCell_String_WrongType(t *testing.T) {
	c := &Cell{value: float64(3.14)}
	if got := c.String(); got != "" {
		t.Errorf("String() on float cell = %q, want empty", got)
	}
}

// ── Float() ───────────────────────────────────────────────────────────────────

func TestCell_Float_WrongType(t *testing.T) {
	c := &Cell{value: "not a float"}
	if got := c.Float(); got != 0 {
		t.Errorf("Float() on string cell = %v, want 0", got)
	}
}

// ── Bool() ────────────────────────────────────────────────────────────────────

func TestCell_Bool_WrongType(t *testing.T) {
	c := &Cell{value: float64(1)}
	if got := c.Bool(); got != false {
		t.Errorf("Bool() on float cell = %v, want false", got)
	}
}

// ── Time() ────────────────────────────────────────────────────────────────────

func TestCell_Time_WrongType(t *testing.T) {
	c := &Cell{value: "2023-01-01"}
	if !c.Time().IsZero() {
		t.Errorf("Time() on string cell = %v, want zero", c.Time())
	}
}

// ── Value() ───────────────────────────────────────────────────────────────────

func TestCell_Value_Empty(t *testing.T) {
	c := &Cell{Type: CellTypeEmpty}
	if got := c.Value(); got != "" {
		t.Errorf("Value() empty = %q, want empty", got)
	}
}

func TestCell_Value_String(t *testing.T) {
	c := &Cell{Type: CellTypeString, value: "hello"}
	if got := c.Value(); got != "hello" {
		t.Errorf("Value() string = %q, want hello", got)
	}
}

func TestCell_Value_Number(t *testing.T) {
	c := &Cell{Type: CellTypeNumber, value: float64(42)}
	if got := c.Value(); got != "42" {
		t.Errorf("Value() number = %q, want 42", got)
	}
}

func TestCell_Value_Number_Float(t *testing.T) {
	c := &Cell{Type: CellTypeNumber, value: 1.5}
	if got := c.Value(); got != "1.5" {
		t.Errorf("Value() float = %q, want 1.5", got)
	}
}

func TestCell_Value_BoolTrue(t *testing.T) {
	c := &Cell{Type: CellTypeBool, value: true}
	if got := c.Value(); got != "TRUE" {
		t.Errorf("Value() bool true = %q, want TRUE", got)
	}
}

func TestCell_Value_BoolFalse(t *testing.T) {
	c := &Cell{Type: CellTypeBool, value: false}
	if got := c.Value(); got != "FALSE" {
		t.Errorf("Value() bool false = %q, want FALSE", got)
	}
}

func TestCell_Value_Date_DateOnly(t *testing.T) {
	d := time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)
	c := &Cell{Type: CellTypeDate, value: d}
	if got := c.Value(); got != "2023-06-15" {
		t.Errorf("Value() date-only = %q, want 2023-06-15", got)
	}
}

func TestCell_Value_Date_WithTime(t *testing.T) {
	d := time.Date(2023, 6, 15, 14, 30, 0, 0, time.UTC)
	c := &Cell{Type: CellTypeDate, value: d}
	want := "2023-06-15T14:30:00Z"
	if got := c.Value(); got != want {
		t.Errorf("Value() datetime = %q, want %q", got, want)
	}
}

func TestCell_Value_Unknown(t *testing.T) {
	c := &Cell{Type: CellType(99)}
	if got := c.Value(); got != "" {
		t.Errorf("Value() unknown type = %q, want empty", got)
	}
}

// ── formatErrorCode ───────────────────────────────────────────────────────────

func TestCell_Value_Error_AllCodes(t *testing.T) {
	cases := []struct {
		code byte
		want string
	}{
		{0x00, "#NULL!"},
		{0x07, "#DIV/0!"},
		{0x0F, "#VALUE!"},
		{0x17, "#REF!"},
		{0x1D, "#NAME?"},
		{0x24, "#NUM!"},
		{0x2A, "#N/A"},
		{0xFF, "#ERR!FF"},
	}
	for _, tc := range cases {
		c := &Cell{Type: CellTypeError, value: tc.code}
		if got := c.Value(); got != tc.want {
			t.Errorf("error code 0x%02X: got %q, want %q", tc.code, got, tc.want)
		}
	}
}

func TestCell_Value_Error_NonByte(t *testing.T) {
	// formatErrorCode with non-byte value returns "#ERROR!"
	c := &Cell{Type: CellTypeError, value: "not a byte"}
	if got := c.Value(); got != "#ERROR!" {
		t.Errorf("non-byte error value: got %q, want #ERROR!", got)
	}
}

// ── formatTime ────────────────────────────────────────────────────────────────

func TestFormatTime_DateOnly(t *testing.T) {
	cases := []time.Time{
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC),
	}
	for _, d := range cases {
		got := formatTime(d)
		want := d.Format("2006-01-02")
		if got != want {
			t.Errorf("formatTime(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFormatTime_WithTimeComponent(t *testing.T) {
	cases := []time.Time{
		time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2023, 1, 1, 0, 0, 1, 0, time.UTC),
		time.Date(2023, 1, 1, 0, 1, 0, 0, time.UTC),
	}
	for _, d := range cases {
		got := formatTime(d)
		want := d.UTC().Format("2006-01-02T15:04:05Z07:00")
		if got != want {
			t.Errorf("formatTime(%v) = %q, want %q", d, got, want)
		}
	}
}

// ── valueFromNative ───────────────────────────────────────────────────────────

func TestCell_Value_Formula_AllNativeTypes(t *testing.T) {
	d := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		value any
		want  string
	}{
		{"hello", "hello"},
		{float64(3.14), "3.14"},
		{true, "TRUE"},
		{false, "FALSE"},
		{d, "2023-01-01"},
		{byte(0x07), "#DIV/0!"},
		{nil, ""},
	}
	for _, tc := range cases {
		c := &Cell{Type: CellTypeFormula, value: tc.value}
		if got := c.Value(); got != tc.want {
			t.Errorf("formula value %T(%v): got %q, want %q", tc.value, tc.value, got, tc.want)
		}
	}
}

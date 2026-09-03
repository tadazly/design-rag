package biff_test

import (
	"bytes"
	"io"
	"math"
	"testing"

	"github.com/nkiri/xls/internal/biff"
)

// ── Reader / Writer round-trip ────────────────────────────────────────────────

func TestRoundTrip_SimpleRecords(t *testing.T) {
	var buf bytes.Buffer
	w := biff.NewWriter(&buf)

	records := []struct {
		t    biff.RecordType
		data []byte
	}{
		{biff.RecBOF, biff.AppendBOF(nil, biff.BOFWorkbook)},
		{biff.RecCodePage, []byte{0xE4, 0x04}}, // 1252
		{biff.RecEOF, nil},
	}
	for _, rec := range records {
		if err := w.WriteRecord(rec.t, rec.data); err != nil {
			t.Fatalf("WriteRecord 0x%04X: %v", rec.t, err)
		}
	}

	r := biff.NewReader(&buf)
	for i, want := range records {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("record %d: Next: %v", i, err)
		}
		if got.Type != want.t {
			t.Errorf("record %d: type got 0x%04X, want 0x%04X", i, got.Type, want.t)
		}
		if !bytes.Equal(got.Data, want.data) {
			t.Errorf("record %d: data mismatch\n got  %v\n want %v", i, got.Data, want.data)
		}
	}

	_, err := r.Next()
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// TestContinue_Split checks that payloads > maxDataSize are split into
// CONTINUE records and re-assembled correctly by the Reader.
func TestContinue_Split(t *testing.T) {
	// Build a payload that is 2.5 × maxDataSize.
	const maxData = 8224
	payload := bytes.Repeat([]byte{0xAB}, maxData*2+100)

	var buf bytes.Buffer
	w := biff.NewWriter(&buf)
	if err := w.WriteRecord(biff.RecSST, payload); err != nil {
		t.Fatal(err)
	}

	r := biff.NewReader(&buf)
	got, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != biff.RecSST {
		t.Fatalf("type got 0x%04X, want 0x%04X", got.Type, biff.RecSST)
	}
	if !bytes.Equal(got.Data, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got.Data), len(payload))
	}
}

// TestContinue_ExactBoundary tests a payload that is exactly maxDataSize bytes.
func TestContinue_ExactBoundary(t *testing.T) {
	const maxData = 8224
	payload := bytes.Repeat([]byte{0x42}, maxData)

	var buf bytes.Buffer
	w := biff.NewWriter(&buf)
	if err := w.WriteRecord(biff.RecNumber, payload); err != nil {
		t.Fatal(err)
	}

	r := biff.NewReader(&buf)
	got, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, payload) {
		t.Fatalf("payload mismatch (len got=%d want=%d)", len(got.Data), len(payload))
	}
}

// TestEmptyRecord ensures a zero-payload record is written and read correctly.
func TestEmptyRecord(t *testing.T) {
	var buf bytes.Buffer
	w := biff.NewWriter(&buf)
	if err := w.WriteEmpty(biff.RecEOF); err != nil {
		t.Fatal(err)
	}
	r := biff.NewReader(&buf)
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Type != biff.RecEOF {
		t.Fatalf("got 0x%04X, want RecEOF", rec.Type)
	}
	if len(rec.Data) != 0 {
		t.Fatalf("expected empty data, got %d bytes", len(rec.Data))
	}
}

// ── String codec ─────────────────────────────────────────────────────────────

var stringTests = []struct {
	name string
	s    string
}{
	{"empty", ""},
	{"ascii", "Hello, World!"},
	{"latin1", "caf\u00e9"},   // 'é' = U+00E9 (fits in Latin-1)
	{"unicode", "日本語テスト"},     // non-Latin-1, must use UTF-16LE
	{"mixed", "abc\u0100xyz"}, // U+0100 forces UTF-16LE
}

func TestLongString_RoundTrip(t *testing.T) {
	for _, tt := range stringTests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := biff.EncodeLongString(tt.s)
			got, n, err := biff.DecodeLongString(encoded)
			if err != nil {
				t.Fatalf("DecodeLongString: %v", err)
			}
			if got != tt.s {
				t.Errorf("got %q, want %q", got, tt.s)
			}
			if n != len(encoded) {
				t.Errorf("consumed %d bytes, want %d", n, len(encoded))
			}
		})
	}
}

func TestShortString_RoundTrip(t *testing.T) {
	for _, tt := range stringTests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := biff.EncodeShortString(tt.s)
			got, n, err := biff.DecodeShortString(encoded)
			if err != nil {
				t.Fatalf("DecodeShortString: %v", err)
			}
			if got != tt.s {
				t.Errorf("got %q, want %q", got, tt.s)
			}
			if n != len(encoded) {
				t.Errorf("consumed %d bytes, want %d", n, len(encoded))
			}
		})
	}
}

func TestShortString_Truncation(t *testing.T) {
	// Strings longer than 255 runes must be truncated to 255.
	long := string(bytes.Repeat([]byte("X"), 300))
	enc := biff.EncodeShortString(long)
	// First byte (cch) must be 255.
	if enc[0] != 255 {
		t.Fatalf("cch = %d, want 255", enc[0])
	}
}

// ── RK codec ─────────────────────────────────────────────────────────────────

func TestDecodeRK_Integer(t *testing.T) {
	// fInt=1, fX100=0 → value = bits[31:2] as signed integer
	// Encode 42: 42 << 2 | 0x02 = 0xAA
	rk := uint32(42<<2) | 0x02
	if got := biff.DecodeRK(rk); got != 42 {
		t.Fatalf("got %v, want 42", got)
	}
}

func TestDecodeRK_IntegerDivBy100(t *testing.T) {
	// fInt=1, fX100=1 → value = bits[31:2] / 100
	rk := uint32(4200<<2) | 0x03 // 4200/100 = 42
	if got := biff.DecodeRK(rk); got != 42 {
		t.Fatalf("got %v, want 42", got)
	}
}

func TestDecodeRK_NegativeInteger(t *testing.T) {
	// int32(-1)<<2 = -4; converting a negative constant to uint32 is not
	// allowed at compile time in Go, so we use a variable.
	v := int32(-1)
	rk := uint32(v<<2) | 0x02
	if got := biff.DecodeRK(rk); got != -1 {
		t.Fatalf("got %v, want -1", got)
	}
}

func TestDecodeRK_Float(t *testing.T) {
	// Build an RK for the double 1.0:
	// bits of 1.0 = 0x3FF0000000000000; high 30 bits into RK
	bits := math.Float64bits(1.0)       // 0x3FF0000000000000
	rk := uint32(bits>>32) & 0xFFFFFFFC // = 0x3FF00000, bits 1:0 cleared
	if got := biff.DecodeRK(rk); got != 1.0 {
		t.Fatalf("got %v, want 1.0", got)
	}
}

func TestEncodeDecodeRK_RoundTrip(t *testing.T) {
	values := []float64{0, 1, -1, 42, -100, 536870911, -536870912, 0.01}
	for _, v := range values {
		rk, ok := biff.EncodeRK(v)
		if !ok {
			// Not all values are encodable; skip silently.
			continue
		}
		got := biff.DecodeRK(rk)
		if math.Abs(got-v) > 1e-10 {
			t.Errorf("value %v: encode→decode got %v", v, got)
		}
	}
}

func TestEncodeRK_LargeValueNotEncodable(t *testing.T) {
	// π cannot be represented as an RK float (low 34 bits of double are non-zero).
	_, ok := biff.EncodeRK(math.Pi)
	if ok {
		t.Fatal("expected EncodeRK(π) = false")
	}
}

// ── Writer helpers ────────────────────────────────────────────────────────────

func TestWriteUint16_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := biff.NewWriter(&buf)
	const val = uint16(0x04E4) // code page 1252
	if err := w.WriteUint16(biff.RecCodePage, val); err != nil {
		t.Fatalf("WriteUint16: %v", err)
	}
	r := biff.NewReader(&buf)
	rec, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Type != biff.RecCodePage {
		t.Errorf("type = 0x%04X, want RecCodePage", rec.Type)
	}
	if len(rec.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(rec.Data))
	}
	got := uint16(rec.Data[0]) | uint16(rec.Data[1])<<8
	if got != val {
		t.Errorf("value = %d, want %d", got, val)
	}
}

func TestWriter_Err_NoError(t *testing.T) {
	var buf bytes.Buffer
	w := biff.NewWriter(&buf)
	_ = w.WriteEmpty(biff.RecEOF)
	if err := w.Err(); err != nil {
		t.Errorf("Err() = %v, want nil after successful write", err)
	}
}

func TestAppendBoundSheet_Format(t *testing.T) {
	// AppendBoundSheet should encode offset + visibility + sheet type + name.
	name := "MySheet"
	b := biff.AppendBoundSheet(nil, 0xDEADBEEF, name, 0)

	// First 4 bytes: bofOffset
	off := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	if off != 0xDEADBEEF {
		t.Errorf("bofOffset = 0x%08X, want 0xDEADBEEF", off)
	}
	// Byte 4: visibility, byte 5: sheet type (0x00 = worksheet)
	if b[4] != 0 {
		t.Errorf("visibility = %d, want 0", b[4])
	}
	if b[5] != 0x00 {
		t.Errorf("sheet type = 0x%02X, want 0x00", b[5])
	}
	// Remaining bytes: encoded short string — first byte is cch (length)
	if b[6] != byte(len(name)) {
		t.Errorf("cch = %d, want %d", b[6], len(name))
	}
}

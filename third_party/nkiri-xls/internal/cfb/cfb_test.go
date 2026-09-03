package cfb_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nkiri/xls/internal/cfb"
)

// roundTrip writes a CFB file with the given streams and then reads it back.
func roundTrip(t *testing.T, streams map[string][]byte) map[string][]byte {
	t.Helper()

	w := cfb.NewWriter()
	for name, data := range streams {
		w.AddStream(name, data)
	}

	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	result := make(map[string][]byte)
	for _, e := range r.Entries() {
		if !e.IsStream {
			continue
		}
		data, err := r.OpenStream(e.Name)
		if err != nil {
			t.Fatalf("OpenStream(%q): %v", e.Name, err)
		}
		result[e.Name] = data
	}
	return result
}

func TestRoundTrip_SmallStream(t *testing.T) {
	// Small stream → goes into the mini stream (< 4096 bytes).
	want := []byte("Hello, CFB mini stream!")
	got := roundTrip(t, map[string][]byte{"TestStream": want})
	if !bytes.Equal(got["TestStream"], want) {
		t.Fatalf("got %q, want %q", got["TestStream"], want)
	}
}

func TestRoundTrip_LargeStream(t *testing.T) {
	// Large stream → stored in regular sectors.
	want := bytes.Repeat([]byte("BIFF"), 2048) // 8192 bytes
	got := roundTrip(t, map[string][]byte{"Workbook": want})
	if !bytes.Equal(got["Workbook"], want) {
		t.Fatalf("large stream mismatch (len got=%d, want=%d)", len(got["Workbook"]), len(want))
	}
}

func TestRoundTrip_MultipleStreams(t *testing.T) {
	streams := map[string][]byte{
		"Workbook": bytes.Repeat([]byte{0xAB, 0xCD}, 4096), // 8192 bytes, regular
		"Summary":  []byte("small summary data"),           // mini stream
	}
	got := roundTrip(t, streams)

	for name, want := range streams {
		if !bytes.Equal(got[name], want) {
			t.Fatalf("stream %q: got %d bytes, want %d bytes", name, len(got[name]), len(want))
		}
	}
}

func TestRoundTrip_EmptyStream(t *testing.T) {
	want := []byte{}
	got := roundTrip(t, map[string][]byte{"Empty": want})
	if !bytes.Equal(got["Empty"], want) {
		t.Fatalf("got %v, want empty", got["Empty"])
	}
}

func TestOpenStream_CaseInsensitive(t *testing.T) {
	w := cfb.NewWriter()
	w.AddStream("Workbook", []byte("data"))
	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatal(err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Workbook", "workbook", "WORKBOOK"} {
		if _, err := r.OpenStream(name); err != nil {
			t.Errorf("OpenStream(%q) failed: %v", name, err)
		}
	}
}

func TestOpenStream_NotFound(t *testing.T) {
	w := cfb.NewWriter()
	w.AddStream("Workbook", []byte("data"))
	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatal(err)
	}
	r, err := cfb.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.OpenStream("NoSuchStream")
	if err != cfb.ErrStreamNotFound {
		t.Fatalf("got %v, want ErrStreamNotFound", err)
	}
}

func TestInvalidSignature(t *testing.T) {
	bad := bytes.Repeat([]byte{0x00}, 512)
	_, err := cfb.NewReader(bytes.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestEntries_ContainsRoot(t *testing.T) {
	w := cfb.NewWriter()
	w.AddStream("Workbook", []byte("x"))
	var buf bytes.Buffer
	if err := w.Write(&buf); err != nil {
		t.Fatal(err)
	}
	r, err := cfb.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	hasRoot := false
	for _, e := range r.Entries() {
		if e.IsRoot {
			hasRoot = true
		}
	}
	if !hasRoot {
		t.Fatal("expected root entry in Entries()")
	}
}

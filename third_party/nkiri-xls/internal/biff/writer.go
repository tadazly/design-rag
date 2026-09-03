package biff

import (
	"encoding/binary"
	"io"
)

// Writer writes BIFF8 records to an io.Writer.
//
// Payloads larger than [maxDataSize] are automatically split into the original
// record type followed by one or more [RecContinue] records.
type Writer struct {
	w   io.Writer
	err error // sticky error
}

// NewWriter returns a Writer that writes to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteRecord writes t with the given data payload, splitting into CONTINUE
// records as necessary.  Returns the first write error encountered; subsequent
// calls are no-ops after an error.
func (w *Writer) WriteRecord(t RecordType, data []byte) error {
	if w.err != nil {
		return w.err
	}

	// First chunk uses the original type; remaining chunks use RecContinue.
	first := true
	for {
		chunk := data
		if len(chunk) > maxDataSize {
			chunk = data[:maxDataSize]
		}
		recType := t
		if !first {
			recType = RecContinue
		}
		if err := w.writeRaw(recType, chunk); err != nil {
			w.err = err
			return err
		}
		data = data[len(chunk):]
		first = false
		if len(data) == 0 {
			break
		}
	}
	return nil
}

// Err returns the first error encountered during writing, if any.
func (w *Writer) Err() error { return w.err }

// writeRaw writes a single record header + payload to the underlying writer.
// data must be ≤ maxDataSize bytes.
func (w *Writer) writeRaw(t RecordType, data []byte) error {
	var hdr [4]byte
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(t))
	binary.LittleEndian.PutUint16(hdr[2:4], uint16(len(data)))
	if _, err := w.w.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := w.w.Write(data)
		return err
	}
	return nil
}

// ── Convenience helpers ───────────────────────────────────────────────────────

// WriteEmpty writes a zero-payload record (e.g. EOF).
func (w *Writer) WriteEmpty(t RecordType) error {
	return w.WriteRecord(t, nil)
}

// WriteUint16 writes a record whose entire payload is a single little-endian
// uint16 (e.g. CODEPAGE, DATEMODE).
func (w *Writer) WriteUint16(t RecordType, v uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	return w.WriteRecord(t, buf[:])
}

// AppendBOF appends a BIFF8 BOF record payload for the given sheet type to b
// and returns the extended slice.
//
// The caller must pass the result to [Writer.WriteRecord] with [RecBOF].
func AppendBOF(b []byte, sheetType BOFType) []byte {
	var buf [16]byte
	binary.LittleEndian.PutUint16(buf[0:2], 0x0600)            // Vers = BIFF8
	binary.LittleEndian.PutUint16(buf[2:4], uint16(sheetType)) // Type
	binary.LittleEndian.PutUint16(buf[4:6], 0x0DBB)            // BID (build ID)
	binary.LittleEndian.PutUint16(buf[6:8], 0x07CC)            // BYear = 1996
	binary.LittleEndian.PutUint32(buf[8:12], 0x00000041)       // FileHistory
	binary.LittleEndian.PutUint32(buf[12:16], 0x00000006)      // LowestBIFF = BIFF8
	return append(b, buf[:]...)
}

// AppendBoundSheet appends a BOUNDSHEET record payload for a worksheet with
// the given name and the given byte offset of its BOF record within the
// Workbook stream.
//
// The caller must pass the result to [Writer.WriteRecord] with [RecBoundSheet].
func AppendBoundSheet(b []byte, bofOffset uint32, name string, visibility byte) []byte {
	nameBytes := EncodeShortString(name)
	var hdr [6]byte
	binary.LittleEndian.PutUint32(hdr[0:4], bofOffset)
	hdr[4] = visibility               // grbit
	hdr[5] = byte(sheetTypeWorksheet) // sheet type
	b = append(b, hdr[:]...)
	b = append(b, nameBytes...)
	return b
}

const sheetTypeWorksheet = 0x00

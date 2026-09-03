package biff

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Reader reads BIFF8 records from an io.Reader sequentially.
//
// CONTINUE records are merged automatically: when [Next] reads a record that
// is immediately followed by one or more CONTINUE records, their payloads are
// appended to the current record's Data before it is returned.
//
// See the package-level note about SST CONTINUE records.
type Reader struct {
	r      io.Reader
	peeked *Record // record read ahead for CONTINUE lookahead
	err    error   // sticky error (once set, always returned)
}

// NewReader returns a Reader that reads from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// Next returns the next logical record.  CONTINUE records are merged into
// their predecessor and never returned as standalone records.
//
// Returns (nil, io.EOF) after the last record.
func (r *Reader) Next() (*Record, error) {
	if r.err != nil {
		return nil, r.err
	}

	// Obtain the current record (from lookahead buffer or wire).
	var rec *Record
	if r.peeked != nil {
		rec = r.peeked
		r.peeked = nil
	} else {
		var err error
		rec, err = r.readRaw()
		if err != nil {
			r.err = err
			return nil, err
		}
	}

	// Merge any immediately following CONTINUE records.
	for {
		next, err := r.readRaw()
		if err != nil {
			// EOF or read error: no more CONTINUEs possible.
			r.err = err
			break
		}
		if next.Type != RecContinue {
			r.peeked = next
			break
		}
		rec.ContinueOffsets = append(rec.ContinueOffsets, len(rec.Data))
		rec.Data = append(rec.Data, next.Data...)
	}

	return rec, nil
}

// readRaw reads exactly one record header + payload from the wire,
// without any CONTINUE merging.
func (r *Reader) readRaw() (*Record, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF // treat a partial header as clean EOF
		}
		return nil, err
	}

	recType := RecordType(binary.LittleEndian.Uint16(hdr[0:2]))
	dataLen := int(binary.LittleEndian.Uint16(hdr[2:4]))

	if dataLen > maxDataSize {
		return nil, fmt.Errorf("biff: record type 0x%04X claims data length %d (max %d)",
			recType, dataLen, maxDataSize)
	}

	var data []byte
	if dataLen > 0 {
		data = make([]byte, dataLen)
		if _, err := io.ReadFull(r.r, data); err != nil {
			return nil, fmt.Errorf("biff: reading data for record type 0x%04X: %w", recType, err)
		}
	}

	return &Record{Type: recType, Data: data}, nil
}

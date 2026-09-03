package cfb

import (
	"encoding/binary"
	"io"
)

// cfbMagic is the 8-byte signature at the very start of every CFB file.
var cfbMagic = [8]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// rawHeader is the 512-byte header located at byte offset 0 of every CFB file.
// Field names and offsets follow the MS-CFB specification §2.2.
//
//	Offset  Size  Field
//	     0     8  Sig
//	     8    16  CLSID
//	    24     2  MinorVersion
//	    26     2  MajorVersion   (3 = 512-byte sectors, 4 = 4096-byte sectors)
//	    28     2  ByteOrder      (always 0xFFFE = little-endian)
//	    30     2  SectorSizePow  (9 → 512 bytes, 12 → 4096 bytes)
//	    32     2  MiniSizePow    (6 → 64 bytes)
//	    34     6  Reserved
//	    40     4  NumDirSects    (v4 only; 0 for v3)
//	    44     4  NumFATSects
//	    48     4  FirstDirSect
//	    52     4  TxSigCount
//	    56     4  MiniCutoff     (default 4096)
//	    60     4  FirstMiniFAT
//	    64     4  NumMiniFAT
//	    68     4  FirstDIFAT
//	    72     4  NumDIFAT
//	    76   436  DIFAT[109]
type rawHeader struct {
	Sig           [8]byte
	CLSID         [16]byte
	MinorVersion  uint16
	MajorVersion  uint16
	ByteOrder     uint16
	SectorSizePow uint16
	MiniSizePow   uint16
	Reserved      [6]byte
	NumDirSects   uint32
	NumFATSects   uint32
	FirstDirSect  uint32
	TxSigCount    uint32
	MiniCutoff    uint32
	FirstMiniFAT  uint32
	NumMiniFAT    uint32
	FirstDIFAT    uint32
	NumDIFAT      uint32
	DIFAT         [109]uint32
}

// parseHeader reads and validates the 512-byte header from r.
func parseHeader(r io.Reader) (rawHeader, error) {
	var h rawHeader
	if err := binary.Read(r, binary.LittleEndian, &h); err != nil {
		return h, err
	}
	if h.Sig != cfbMagic {
		return h, ErrInvalidSignature
	}
	if h.ByteOrder != 0xFFFE {
		return h, ErrInvalidHeader
	}
	if h.MajorVersion != 3 && h.MajorVersion != 4 {
		return h, ErrInvalidHeader
	}
	if (h.MajorVersion == 3 && (h.SectorSizePow != 9 || h.NumDirSects != 0)) || (h.MajorVersion == 4 && h.SectorSizePow != 12) || h.MiniSizePow != 6 || h.MiniCutoff != miniStreamCutoff {
		return h, ErrInvalidHeader
	}
	return h, nil
}

// marshalHeader encodes h into the 512-byte buf.
func marshalHeader(h rawHeader, buf *[headerSize]byte) {
	b := buf[:]
	copy(b[0:8], h.Sig[:])
	copy(b[8:24], h.CLSID[:])
	binary.LittleEndian.PutUint16(b[24:], h.MinorVersion)
	binary.LittleEndian.PutUint16(b[26:], h.MajorVersion)
	binary.LittleEndian.PutUint16(b[28:], h.ByteOrder)
	binary.LittleEndian.PutUint16(b[30:], h.SectorSizePow)
	binary.LittleEndian.PutUint16(b[32:], h.MiniSizePow)
	// Reserved [6]byte at b[34:40] stays zero
	binary.LittleEndian.PutUint32(b[40:], h.NumDirSects)
	binary.LittleEndian.PutUint32(b[44:], h.NumFATSects)
	binary.LittleEndian.PutUint32(b[48:], h.FirstDirSect)
	binary.LittleEndian.PutUint32(b[52:], h.TxSigCount)
	binary.LittleEndian.PutUint32(b[56:], h.MiniCutoff)
	binary.LittleEndian.PutUint32(b[60:], h.FirstMiniFAT)
	binary.LittleEndian.PutUint32(b[64:], h.NumMiniFAT)
	binary.LittleEndian.PutUint32(b[68:], h.FirstDIFAT)
	binary.LittleEndian.PutUint32(b[72:], h.NumDIFAT)
	for i, sid := range h.DIFAT {
		binary.LittleEndian.PutUint32(b[76+i*4:], sid)
	}
}

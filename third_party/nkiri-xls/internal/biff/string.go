package biff

import (
	"encoding/binary"
	"io"
	"unicode/utf16"
)

// BIFF8 defines two string types (MS-XLS §2.5.3 – XLUnicodeString):
//
//	Short string (cch uint8)   – used in BOUNDSHEET, FONT name, …
//	Long  string (cch uint16)  – used in SST, LABEL, FORMAT, …
//
// Both share the same 1-byte grBit flag after cch:
//
//	bit 0 fHighByte  0 = compressed Latin-1, 1 = UTF-16LE
//	bit 3 fRichSt    1 = followed by cRun uint16 + 4*cRun formatting bytes
//	bit 2 fExtSt     1 = followed by cbExtRst uint32 + cbExtRst phonetic bytes
//
// This package always writes strings either compressed (pure ASCII/Latin-1) or
// UTF-16LE, without rich-text or extended-string sections.

// DecodeLongString decodes a BIFF8 XLUnicodeString (2-byte cch) from b.
// Returns the decoded string and the number of bytes consumed, or an error.
func DecodeLongString(b []byte) (s string, n int, err error) {
	if len(b) < 3 {
		return "", 0, io.ErrUnexpectedEOF
	}
	cch := int(binary.LittleEndian.Uint16(b[0:2]))
	grBit := b[2]
	off := 3

	fHighByte := grBit&0x01 != 0
	fRichSt := grBit&0x08 != 0
	fExtSt := grBit&0x04 != 0

	// Optional header counts.
	var cRun, cbExtRst int
	if fRichSt {
		if off+2 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		cRun = int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
	}
	if fExtSt {
		if off+4 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		cbExtRst = int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
	}

	// Character data.
	s, charBytes, err := decodeChars(b[off:], cch, fHighByte)
	if err != nil {
		return "", 0, err
	}
	off += charBytes

	// Skip trailing rich-text and phonetic sections.
	off += cRun * 4
	off += cbExtRst

	return s, off, nil
}

// DecodeShortString decodes a BIFF8 XLUnicodeStringNoCch (1-byte cch) from b.
// Returns the decoded string and the number of bytes consumed, or an error.
// This format is used in BOUNDSHEET and FONT records.
func DecodeShortString(b []byte) (s string, n int, err error) {
	if len(b) < 2 {
		return "", 0, io.ErrUnexpectedEOF
	}
	cch := int(b[0])
	fHighByte := b[1]&0x01 != 0
	off := 2

	s, charBytes, err := decodeChars(b[off:], cch, fHighByte)
	if err != nil {
		return "", 0, err
	}
	return s, off + charBytes, nil
}

// decodeChars decodes cch characters from b starting at b[0].
// If fHighByte is false, each character is one Latin-1 byte;
// if true, each character is two bytes (UTF-16LE).
// Returns the decoded string and the number of bytes consumed.
func decodeChars(b []byte, cch int, fHighByte bool) (string, int, error) {
	if fHighByte {
		byteLen := cch * 2
		if byteLen > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		words := make([]uint16, cch)
		for i := range words {
			words[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
		return string(utf16.Decode(words)), byteLen, nil
	}
	// Compressed (Latin-1): each byte maps directly to a Unicode codepoint.
	if cch > len(b) {
		return "", 0, io.ErrUnexpectedEOF
	}
	runes := make([]rune, cch)
	for i := 0; i < cch; i++ {
		runes[i] = rune(b[i])
	}
	return string(runes), cch, nil
}

// DecodeSSTString is like DecodeLongString but handles CONTINUE record
// boundaries within the SST payload.
//
// Per MS-XLS §2.4.58, when a string straddles a CONTINUE record boundary the
// first byte of the CONTINUE payload is a new grBit for the remaining
// characters (encoding may switch between Latin-1 and UTF-16LE).
//
// b is the full SST Record.Data (including all merged CONTINUE payloads).
// off is the byte offset of the string's first byte (cch low byte) within b.
// boundaries lists the absolute offsets within b where CONTINUE payloads begin.
func DecodeSSTString(b []byte, off int, boundaries []int) (s string, consumed int, err error) {
	start := off
	if off+3 > len(b) {
		return "", 0, io.ErrUnexpectedEOF
	}
	cch := int(binary.LittleEndian.Uint16(b[off : off+2]))
	grBit := b[off+2]
	off += 3

	fHighByte := grBit&0x01 != 0
	fRichSt := grBit&0x08 != 0
	fExtSt := grBit&0x04 != 0

	var cRun, cbExtRst int
	if fRichSt {
		if off+2 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		cRun = int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
	}
	if fExtSt {
		if off+4 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		cbExtRst = int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
	}

	// Read cch characters one at a time, handling CONTINUE boundary grBit bytes.
	runes := make([]rune, 0, cch)
	for i := 0; i < cch; i++ {
		// At a CONTINUE boundary the next byte is a new grBit, not character data.
		if isBoundaryOffset(off, boundaries) {
			if off >= len(b) {
				return "", 0, io.ErrUnexpectedEOF
			}
			grBit = b[off]
			fHighByte = grBit&0x01 != 0
			off++
		}
		if fHighByte {
			if off+2 > len(b) {
				return "", 0, io.ErrUnexpectedEOF
			}
			// UTF-16LE: decode the code unit (surrogate pairs are rare in SST
			// but handled via utf16.Decode at the end via uint16 accumulation).
			runes = append(runes, rune(binary.LittleEndian.Uint16(b[off:])))
			off += 2
		} else {
			if off >= len(b) {
				return "", 0, io.ErrUnexpectedEOF
			}
			runes = append(runes, rune(b[off]))
			off++
		}
	}

	// Convert runes to string, decoding any UTF-16 surrogate pairs.
	words := make([]uint16, len(runes))
	for i, r := range runes {
		words[i] = uint16(r)
	}
	s = string(utf16.Decode(words))

	// Skip trailing rich-text run table and extended (phonetic) data.
	off += cRun * 4
	off += cbExtRst
	return s, off - start, nil
}

// isBoundaryOffset reports whether off is one of the CONTINUE boundary offsets.
func isBoundaryOffset(off int, boundaries []int) bool {
	for _, b := range boundaries {
		if b == off {
			return true
		}
	}
	return false
}

// EncodeLongString encodes s as a BIFF8 XLUnicodeString (2-byte cch prefix).
// The string is written compressed (1 byte/char) if all code points fit in
// Latin-1; otherwise UTF-16LE (2 bytes/char) is used.
func EncodeLongString(s string) []byte {
	runes := []rune(s)
	cch := len(runes)

	compressed := isLatin1(runes)
	var grBit byte
	if !compressed {
		grBit = 0x01
	}

	size := 3 // cch(2) + grBit(1)
	if compressed {
		size += cch
	} else {
		size += cch * 2
	}

	b := make([]byte, size)
	binary.LittleEndian.PutUint16(b[0:2], uint16(cch))
	b[2] = grBit

	encodeCharsInto(b[3:], runes, compressed)
	return b
}

// EncodeShortString encodes s as a BIFF8 XLUnicodeStringNoCch (1-byte cch).
// The string is truncated to 255 characters if longer.
// This format is used in BOUNDSHEET and FONT records.
func EncodeShortString(s string) []byte {
	runes := []rune(s)
	if len(runes) > 255 {
		runes = runes[:255]
	}
	cch := len(runes)
	compressed := isLatin1(runes)

	var grBit byte
	if !compressed {
		grBit = 0x01
	}

	size := 2 // cch(1) + grBit(1)
	if compressed {
		size += cch
	} else {
		size += cch * 2
	}

	b := make([]byte, size)
	b[0] = byte(cch)
	b[1] = grBit
	encodeCharsInto(b[2:], runes, compressed)
	return b
}

// isLatin1 reports whether every rune in rs fits in one byte (Latin-1).
func isLatin1(rs []rune) bool {
	for _, r := range rs {
		if r > 0xFF {
			return false
		}
	}
	return true
}

// encodeCharsInto writes runes into dst.
// If compressed, each rune occupies 1 byte; otherwise 2 bytes (UTF-16LE).
// dst must be large enough.
func encodeCharsInto(dst []byte, runes []rune, compressed bool) {
	if compressed {
		for i, r := range runes {
			dst[i] = byte(r)
		}
		return
	}
	words := utf16.Encode(runes)
	for i, w := range words {
		binary.LittleEndian.PutUint16(dst[i*2:], w)
	}
}

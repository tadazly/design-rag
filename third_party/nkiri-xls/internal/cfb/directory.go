package cfb

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"
)

// rawDirEntry is the 128-byte on-disk representation of a directory entry.
// Field layout per MS-CFB §2.6.
//
//	Offset  Size  Field
//	     0    64  DirectoryEntryName   (UTF-16LE, null-terminated, max 31+1 chars)
//	    64     2  DirectoryEntryNameLength  (bytes incl. null terminator)
//	    66     1  ObjectType           (0=empty, 1=storage, 2=stream, 5=root)
//	    67     1  ColorFlag            (0=red, 1=black)
//	    68     4  LeftSiblingID
//	    72     4  RightSiblingID
//	    76     4  ChildID
//	    80    16  CLSID
//	    96     4  StateBits
//	   100     8  CreatedTime
//	   108     8  ModifiedTime
//	   116     4  StartingSectorLocation
//	   120     4  SizeLow
//	   124     4  SizeHigh             (reserved / 0 for v3 streams)
type rawDirEntry struct {
	Name      [32]uint16
	NameLen   uint16
	ObjType   byte
	ColorFlag byte
	LeftSib   uint32
	RightSib  uint32
	Child     uint32
	CLSID     [16]byte
	StateBits uint32
	Created   [8]byte
	Modified  [8]byte
	StartSect uint32
	SizeLow   uint32
	SizeHigh  uint32
}

// Entry is the public representation of a CFB directory entry.
type Entry struct {
	Name      string
	IsRoot    bool
	IsStorage bool
	IsStream  bool
	// Size is the stream length in bytes (0 for storage / root).
	Size      int64
	startSect uint32
	child     uint32 // only used internally during writing
}

// parseDirEntries decodes all directory entries found in data.
// data must be a multiple of dirEntrySize bytes.
func parseDirEntries(data []byte) ([]Entry, error) {
	n := len(data) / dirEntrySize
	entries := make([]Entry, n)
	for i := 0; i < n; i++ {
		chunk := data[i*dirEntrySize : (i+1)*dirEntrySize]
		var raw rawDirEntry
		if err := binary.Read(bytes.NewReader(chunk), binary.LittleEndian, &raw); err != nil {
			return nil, err
		}

		// Decode UTF-16LE name.
		nameWords := int(raw.NameLen) / 2
		if nameWords > 32 {
			nameWords = 32
		}
		words := raw.Name[:nameWords]
		// Strip null terminator if present.
		for len(words) > 0 && words[len(words)-1] == 0 {
			words = words[:len(words)-1]
		}
		name := string(utf16.Decode(words))

		entries[i] = Entry{
			Name:      name,
			IsRoot:    raw.ObjType == objRoot,
			IsStorage: raw.ObjType == objStorage || raw.ObjType == objRoot,
			IsStream:  raw.ObjType == objStream,
			Size:      int64(raw.SizeLow) | int64(raw.SizeHigh)<<32,
			startSect: raw.StartSect,
			child:     raw.Child,
		}
	}
	return entries, nil
}

// marshalDirEntry encodes one directory entry into a 128-byte array.
func marshalDirEntry(
	name string,
	objType byte,
	leftSib, rightSib, child uint32,
	startSect, sizeLow uint32,
) [dirEntrySize]byte {
	var buf [dirEntrySize]byte

	if objType == objEmpty {
		// Empty slots: all-zero name, freeSect for sibling/child fields.
		binary.LittleEndian.PutUint32(buf[68:], freeSect)
		binary.LittleEndian.PutUint32(buf[72:], freeSect)
		binary.LittleEndian.PutUint32(buf[76:], freeSect)
		binary.LittleEndian.PutUint32(buf[116:], endOfChain)
		return buf
	}

	// Encode name as UTF-16LE.
	runes := []rune(name)
	if len(runes) > 31 {
		runes = runes[:31]
	}
	words := utf16.Encode(runes)
	for i, w := range words {
		if i >= 32 {
			break
		}
		binary.LittleEndian.PutUint16(buf[i*2:], w)
	}
	// null terminator at position len(words) is already zero
	// NameLen = (len(words) + 1) * 2, including null terminator
	nameLen := uint16((len(words) + 1) * 2)
	binary.LittleEndian.PutUint16(buf[64:], nameLen)

	buf[66] = objType
	buf[67] = 1 // black node (color flag)

	binary.LittleEndian.PutUint32(buf[68:], leftSib)
	binary.LittleEndian.PutUint32(buf[72:], rightSib)
	binary.LittleEndian.PutUint32(buf[76:], child)
	// CLSID (buf[80:96]) stays zero
	// StateBits (buf[96:100]) stays zero
	// Created / Modified (buf[100:116]) stay zero
	binary.LittleEndian.PutUint32(buf[116:], startSect)
	binary.LittleEndian.PutUint32(buf[120:], sizeLow)
	// SizeHigh (buf[124:128]) stays zero (v3)

	return buf
}

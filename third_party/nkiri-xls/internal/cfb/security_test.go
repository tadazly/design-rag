package cfb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func validCFB(t *testing.T, content []byte) []byte {
	t.Helper()
	writer := NewWriter()
	writer.AddStream("Workbook", content)
	var output bytes.Buffer
	if err := writer.Write(&output); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestReaderRejectsInvalidSectorShift(t *testing.T) {
	data := append([]byte(nil), validCFB(t, bytes.Repeat([]byte("x"), 8192))...)
	binary.LittleEndian.PutUint16(data[30:32], 31)
	if _, err := NewReader(bytes.NewReader(data)); err == nil {
		t.Fatal("invalid sector shift was accepted")
	}
}

func TestReaderRejectsCyclicRegularFATChain(t *testing.T) {
	data := append([]byte(nil), validCFB(t, bytes.Repeat([]byte("x"), 8192))...)
	directorySector := binary.LittleEndian.Uint32(data[48:52])
	fatSector := binary.LittleEndian.Uint32(data[76:80])
	fatOffset := headerSize + int(fatSector)*defaultSectorSize + int(directorySector)*4
	if fatOffset+4 > len(data) {
		t.Fatalf("invalid test fixture offsets: fat=%d directory=%d", fatSector, directorySector)
	}
	binary.LittleEndian.PutUint32(data[fatOffset:fatOffset+4], directorySector)
	if _, err := NewReader(bytes.NewReader(data)); err == nil {
		t.Fatal("cyclic FAT chain was accepted")
	}
}

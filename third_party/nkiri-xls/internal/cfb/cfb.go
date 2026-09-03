// Package cfb implements reading and writing of Microsoft Compound File Binary
// (CFB) files, also known as OLE2 / Structured Storage.
//
// The format is used as the outer container for XLS workbooks.
// The on-disk layout is:
//
//	Header (512 bytes)
//	Sector 0 … N-1  (each sectorSize bytes, default 512 for BIFF8)
//
// Streams (like the "Workbook" stream inside an XLS file) are stored as chains
// of sectors described by the File Allocation Table (FAT).  Small streams
// (< miniStreamCutoff bytes) are packed into a single "mini stream container"
// whose sectors are described by the Mini FAT.
package cfb

import "errors"

// Sentinel values that appear in FAT chains and directory entries.
const (
	freeSect   uint32 = 0xFFFFFFFF // unallocated sector
	endOfChain uint32 = 0xFFFFFFFE // last sector in a chain
	fatSect    uint32 = 0xFFFFFFFD // sector occupied by the FAT itself
	difatSect  uint32 = 0xFFFFFFFC // sector occupied by a DIFAT chain sector
)

// Directory entry object types.
const (
	objEmpty   byte = 0
	objStorage byte = 1
	objStream  byte = 2
	objRoot    byte = 5
)

const (
	// miniStreamCutoff is the default size threshold.  Streams whose size is
	// strictly less than this value are stored in the mini stream.
	miniStreamCutoff uint32 = 4096

	// headerSize is the fixed size of the CFB header (always 512 bytes,
	// independent of sector size).
	headerSize = 512

	// dirEntrySize is the fixed size of each directory entry in bytes.
	dirEntrySize = 128

	// defaultSectorSize is the sector size used by version-3 CFB files (BIFF8).
	defaultSectorSize = 512

	// defaultMiniSectorSize is the mini-sector size used by all CFB versions.
	defaultMiniSectorSize = 64
)

var (
	// ErrInvalidSignature is returned when the file does not start with the
	// expected CFB magic bytes.
	ErrInvalidSignature = errors.New("cfb: invalid file signature")

	// ErrInvalidHeader is returned when header fields contain unexpected values.
	ErrInvalidHeader = errors.New("cfb: invalid header")

	// ErrStreamNotFound is returned by OpenStream when no matching stream exists.
	ErrStreamNotFound = errors.New("cfb: stream not found")
)

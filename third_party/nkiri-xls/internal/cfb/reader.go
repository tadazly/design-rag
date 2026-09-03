package cfb

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Reader reads streams from a CFB file.
type Reader struct {
	r          io.ReadSeeker
	sectorSize int
	miniSize   int
	miniCutoff uint32
	fat        []uint32
	miniFAT    []uint32
	entries    []Entry
	miniData   []byte // contents of the root entry's data (mini stream container)
	fileSize   int64
	maxSectors uint32
}

// NewReader parses the CFB header, FAT, and directory of r and returns a
// Reader ready to open individual streams.
func NewReader(r io.ReadSeeker) (*Reader, error) {
	hdr, err := parseHeader(r)
	if err != nil {
		return nil, err
	}

	cr := &Reader{
		r:          r,
		sectorSize: 1 << hdr.SectorSizePow,
		miniSize:   1 << hdr.MiniSizePow,
		miniCutoff: hdr.MiniCutoff,
	}
	position, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	fileSize, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(position, io.SeekStart); err != nil {
		return nil, err
	}
	if fileSize < headerSize || (fileSize-headerSize)%int64(cr.sectorSize) != 0 {
		return nil, ErrInvalidHeader
	}
	sectors := (fileSize - headerSize) / int64(cr.sectorSize)
	if sectors <= 0 || sectors > int64(^uint32(0)) {
		return nil, ErrInvalidHeader
	}
	cr.fileSize = fileSize
	cr.maxSectors = uint32(sectors)
	if hdr.NumFATSects > cr.maxSectors || hdr.NumDIFAT > cr.maxSectors || hdr.NumMiniFAT > cr.maxSectors {
		return nil, ErrInvalidHeader
	}

	if err := cr.buildFAT(hdr); err != nil {
		return nil, fmt.Errorf("cfb: building FAT: %w", err)
	}
	if err := cr.readDirectory(hdr.FirstDirSect); err != nil {
		return nil, fmt.Errorf("cfb: reading directory: %w", err)
	}
	if err := cr.buildMiniFAT(hdr.FirstMiniFAT); err != nil {
		return nil, fmt.Errorf("cfb: building mini FAT: %w", err)
	}

	// Read the mini stream container from the root entry's data chain.
	if len(cr.entries) > 0 && cr.entries[0].IsRoot {
		root := cr.entries[0]
		if root.startSect != endOfChain && root.startSect != freeSect && root.Size > 0 {
			cr.miniData, err = cr.readRegularChain(root.startSect, int(root.Size))
			if err != nil {
				return nil, fmt.Errorf("cfb: reading mini stream container: %w", err)
			}
		}
	}

	return cr, nil
}

// OpenStream returns the raw bytes of the named stream (case-insensitive).
// It returns ErrStreamNotFound if no matching stream exists.
func (cr *Reader) OpenStream(name string) ([]byte, error) {
	for _, e := range cr.entries {
		if e.IsStream && strings.EqualFold(e.Name, name) {
			return cr.readStreamData(e)
		}
	}
	return nil, ErrStreamNotFound
}

// Entries returns all non-empty directory entries in the file.
func (cr *Reader) Entries() []Entry {
	var out []Entry
	for _, e := range cr.entries {
		if e.Name != "" {
			out = append(out, e)
		}
	}
	return out
}

// ─── internal helpers ────────────────────────────────────────────────────────

// sectorOffset returns the byte offset in the file of the first byte of sector sid.
// The 512-byte header is always at offset 0; sector 0 starts immediately after.
func (cr *Reader) sectorOffset(sid uint32) int64 {
	return int64(headerSize) + int64(sid)*int64(cr.sectorSize)
}

// readSector fills buf with the contents of sector sid.
// buf must be exactly sectorSize bytes long.
func (cr *Reader) readSector(sid uint32, buf []byte) error {
	if sid >= cr.maxSectors || len(buf) != cr.sectorSize {
		return fmt.Errorf("cfb: sector %d out of range (count=%d)", sid, cr.maxSectors)
	}
	if _, err := cr.r.Seek(cr.sectorOffset(sid), io.SeekStart); err != nil {
		return err
	}
	_, err := io.ReadFull(cr.r, buf)
	return err
}

// buildFAT reads all FAT sectors, following the DIFAT chain when necessary,
// and stores the concatenated FAT in cr.fat.
func (cr *Reader) buildFAT(hdr rawHeader) error {
	buf := make([]byte, cr.sectorSize)

	// Collect FAT sector IDs: first 109 are in the header's DIFAT array.
	fatCapacity := int(hdr.NumFATSects)
	if fatCapacity > int(cr.maxSectors) {
		fatCapacity = int(cr.maxSectors)
	}
	fatIDs := make([]uint32, 0, fatCapacity)
	for _, sid := range hdr.DIFAT {
		if sid == freeSect || sid == endOfChain {
			break
		}
		if sid >= cr.maxSectors || len(fatIDs) >= int(hdr.NumFATSects) {
			return ErrInvalidHeader
		}
		fatIDs = append(fatIDs, sid)
	}

	// Follow the DIFAT chain for files with more than 109 FAT sectors.
	difat := hdr.FirstDIFAT
	visitedDIFAT := map[uint32]bool{}
	difatSteps := uint32(0)
	for difat != endOfChain && difat != freeSect {
		if difat >= cr.maxSectors || visitedDIFAT[difat] || difatSteps >= hdr.NumDIFAT {
			return fmt.Errorf("cfb: invalid or cyclic DIFAT chain at sector %d", difat)
		}
		visitedDIFAT[difat] = true
		difatSteps++
		if err := cr.readSector(difat, buf); err != nil {
			return err
		}
		// Each DIFAT sector holds (sectorSize/4 - 1) FAT sector IDs.
		// The last uint32 is the next DIFAT sector.
		n := cr.sectorSize/4 - 1
		done := false
		for i := 0; i < n; i++ {
			sid := binary.LittleEndian.Uint32(buf[i*4:])
			if sid == freeSect || sid == endOfChain {
				done = true
				break
			}
			if sid >= cr.maxSectors || len(fatIDs) >= int(hdr.NumFATSects) {
				return ErrInvalidHeader
			}
			fatIDs = append(fatIDs, sid)
		}
		if done {
			break
		}
		difat = binary.LittleEndian.Uint32(buf[cr.sectorSize-4:])
	}
	if len(fatIDs) != int(hdr.NumFATSects) {
		return fmt.Errorf("cfb: FAT sector count mismatch: got %d want %d", len(fatIDs), hdr.NumFATSects)
	}

	// Read the FAT sectors and concatenate them.
	fat := make([]uint32, 0, len(fatIDs)*cr.sectorSize/4)
	for _, sid := range fatIDs {
		if err := cr.readSector(sid, buf); err != nil {
			return err
		}
		for i := 0; i < cr.sectorSize; i += 4 {
			fat = append(fat, binary.LittleEndian.Uint32(buf[i:]))
		}
	}
	cr.fat = fat
	return nil
}

// nextInFAT returns the sector ID that follows sid in the FAT chain.
func (cr *Reader) nextInFAT(sid uint32) uint32 {
	if int(sid) >= len(cr.fat) {
		return endOfChain
	}
	return cr.fat[sid]
}

// readRegularChain reads and concatenates all sectors in the FAT chain that
// starts at startSect, then trims the result to size bytes.
// Pass size = -1 to return all data without trimming.
func (cr *Reader) readRegularChain(startSect uint32, size int) ([]byte, error) {
	buf := make([]byte, cr.sectorSize)
	if size < -1 || size > int(cr.fileSize) {
		return nil, fmt.Errorf("cfb: invalid stream size %d", size)
	}
	capacity := int(cr.fileSize - headerSize)
	if size >= 0 {
		capacity = size
	}
	result := make([]byte, 0, capacity)
	visited := map[uint32]bool{}
	// Regular sector IDs are 0..0xFFFFFFFA.  All values above that are
	// sentinel values (endOfChain, freeSect, fatSect, difatSect) and should
	// terminate the chain.
	for sid := startSect; sid <= 0xFFFFFFFA; sid = cr.nextInFAT(sid) {
		if sid >= cr.maxSectors || visited[sid] || len(visited) >= int(cr.maxSectors) {
			return nil, fmt.Errorf("cfb: invalid or cyclic FAT chain at sector %d", sid)
		}
		visited[sid] = true
		if err := cr.readSector(sid, buf); err != nil {
			return nil, err
		}
		result = append(result, buf...)
	}
	if size >= 0 && len(result) > size {
		result = result[:size]
	}
	return result, nil
}

// buildMiniFAT reads the mini FAT chain starting at firstSect.
func (cr *Reader) buildMiniFAT(firstSect uint32) error {
	if firstSect == endOfChain || firstSect == freeSect {
		return nil
	}
	data, err := cr.readRegularChain(firstSect, -1)
	if err != nil {
		return err
	}
	miniFAT := make([]uint32, len(data)/4)
	for i := range miniFAT {
		miniFAT[i] = binary.LittleEndian.Uint32(data[i*4:])
	}
	cr.miniFAT = miniFAT
	return nil
}

// readDirectory reads all directory sectors into cr.entries.
func (cr *Reader) readDirectory(firstSect uint32) error {
	data, err := cr.readRegularChain(firstSect, -1)
	if err != nil {
		return err
	}
	entries, err := parseDirEntries(data)
	if err != nil {
		return err
	}
	cr.entries = entries
	return nil
}

// readStreamData returns the bytes of entry e.
// Streams below miniCutoff are read from the mini stream; larger ones from
// regular sectors.
func (cr *Reader) readStreamData(e Entry) ([]byte, error) {
	if e.Size < 0 || e.Size > cr.fileSize || uint64(e.Size) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("cfb: invalid stream size %d", e.Size)
	}
	if e.Size < int64(cr.miniCutoff) && len(cr.miniFAT) > 0 {
		return cr.readMiniChain(e.startSect, int(e.Size))
	}
	return cr.readRegularChain(e.startSect, int(e.Size))
}

// readMiniChain reads size bytes from the mini stream chain starting at
// startSect using the mini FAT.
func (cr *Reader) readMiniChain(startSect uint32, size int) ([]byte, error) {
	if size < 0 || size > len(cr.miniData) {
		return nil, fmt.Errorf("cfb: invalid mini stream size %d", size)
	}
	result := make([]byte, 0, size)
	visited := map[uint32]bool{}
	for sid := startSect; sid <= 0xFFFFFFFA; {
		if int(sid) >= len(cr.miniFAT) || visited[sid] || len(visited) >= len(cr.miniFAT) {
			return nil, fmt.Errorf("cfb: invalid or cyclic mini FAT chain at sector %d", sid)
		}
		visited[sid] = true
		offset := int(sid) * cr.miniSize
		end := offset + cr.miniSize
		if end > len(cr.miniData) {
			return nil, fmt.Errorf("cfb: mini sector %d out of range (mini stream is %d bytes)", sid, len(cr.miniData))
		}
		result = append(result, cr.miniData[offset:end]...)
		sid = cr.miniFAT[sid]
	}
	if len(result) > size {
		result = result[:size]
	}
	return result, nil
}

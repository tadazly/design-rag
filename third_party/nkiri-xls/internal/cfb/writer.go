package cfb

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Writer accumulates named streams and writes them as a CFB file.
//
// Usage:
//
//	w := cfb.NewWriter()
//	w.AddStream("Workbook", biffData)
//	err := w.Write(dst)
type Writer struct {
	streams []streamEntry
}

type streamEntry struct {
	name string
	data []byte
}

// NewWriter returns an empty Writer.
func NewWriter() *Writer { return &Writer{} }

// AddStream registers name with the given data.  Streams are written in the
// order they are added.  The name must be at most 31 UTF-16 code units.
func (w *Writer) AddStream(name string, data []byte) {
	w.streams = append(w.streams, streamEntry{name: name, data: data})
}

// Write writes a complete, well-formed CFB file to dst.
//
// Streams whose size is < miniStreamCutoff (4096 bytes) are stored in the
// mini stream; larger streams occupy regular sectors directly.
func (w *Writer) Write(dst io.Writer) error {
	const ss = defaultSectorSize     // 512
	const ms = defaultMiniSectorSize // 64
	mc := miniStreamCutoff           // 4096

	// ── Phase 1: classify streams and build mini stream ──────────────────────

	type streamLayout struct {
		name      string
		data      []byte
		isMini    bool
		startSect uint32 // regular: first regular sector; mini: first mini sector
	}

	layouts := make([]streamLayout, len(w.streams))
	var miniStreamData []byte
	var miniFATEntries []uint32
	nextMiniSect := uint32(0)

	for i, s := range w.streams {
		layouts[i].name = s.name
		layouts[i].data = s.data
		if uint32(len(s.data)) < mc {
			layouts[i].isMini = true
			if len(s.data) == 0 {
				layouts[i].startSect = endOfChain
				continue
			}
			layouts[i].startSect = nextMiniSect
			// Pack stream data into mini sectors (64 bytes each), zero-padded.
			nMiniSects := (len(s.data) + ms - 1) / ms
			for j := 0; j < nMiniSects; j++ {
				var sector [ms]byte
				start := j * ms
				copy(sector[:], s.data[start:])
				miniStreamData = append(miniStreamData, sector[:]...)
				if j < nMiniSects-1 {
					miniFATEntries = append(miniFATEntries, nextMiniSect+1)
				} else {
					miniFATEntries = append(miniFATEntries, endOfChain)
				}
				nextMiniSect++
			}
		}
	}

	// ── Phase 2: compute sector counts ───────────────────────────────────────

	// Number of directory sectors (4 entries per 512-byte sector).
	// Entries: root + one per stream.
	numDirEntries := 1 + len(w.streams)
	numDirSectors := (numDirEntries + 3) / 4

	// Mini FAT sectors.
	numMiniFATSectors := 0
	if len(miniFATEntries) > 0 {
		numMiniFATSectors = (len(miniFATEntries)*4 + ss - 1) / ss
	}

	// Mini stream container sectors (root's data chain).
	miniContainerSectors := (len(miniStreamData) + ss - 1) / ss

	// Regular stream sectors.
	regularSectors := 0
	for _, l := range layouts {
		if !l.isMini {
			regularSectors += (len(l.data) + ss - 1) / ss
		}
	}

	// Payload = everything except the FAT sectors themselves.
	payload := numDirSectors + numMiniFATSectors + miniContainerSectors + regularSectors

	// Number of FAT sectors (iterate to convergence: FAT sectors consume entries too).
	numFATSectors := 0
	for {
		needed := (payload + numFATSectors + ss/4 - 1) / (ss / 4)
		if needed == numFATSectors {
			break
		}
		numFATSectors = needed
	}

	// ── Phase 3: assign sector IDs ───────────────────────────────────────────

	// Layout (in order):
	//   [FAT sectors 0..numFATSectors-1]
	//   [Directory sectors]
	//   [Mini FAT sectors]
	//   [Mini container sectors]
	//   [Regular stream sectors]

	nextSect := uint32(numFATSectors)

	firstDirSect := nextSect
	nextSect += uint32(numDirSectors)

	firstMiniFATSect := endOfChain
	if numMiniFATSectors > 0 {
		firstMiniFATSect = nextSect
		nextSect += uint32(numMiniFATSectors)
	}

	firstMiniContainerSect := endOfChain
	if miniContainerSectors > 0 {
		firstMiniContainerSect = nextSect
		nextSect += uint32(miniContainerSectors)
	}

	for i := range layouts {
		if !layouts[i].isMini {
			layouts[i].startSect = nextSect
			nextSect += uint32((len(layouts[i].data) + ss - 1) / ss)
		}
	}

	totalSectors := int(nextSect)

	// ── Phase 4: build FAT array ─────────────────────────────────────────────

	fat := make([]uint32, numFATSectors*(ss/4))
	for i := range fat {
		fat[i] = freeSect
	}
	// FAT sectors mark themselves.
	for i := 0; i < numFATSectors; i++ {
		fat[i] = fatSect
	}
	// Directory sector chain.
	for i := 0; i < numDirSectors; i++ {
		sid := uint32(firstDirSect) + uint32(i)
		if i < numDirSectors-1 {
			fat[sid] = sid + 1
		} else {
			fat[sid] = endOfChain
		}
	}
	// Mini FAT chain.
	for i := 0; i < numMiniFATSectors; i++ {
		sid := firstMiniFATSect + uint32(i)
		if i < numMiniFATSectors-1 {
			fat[sid] = sid + 1
		} else {
			fat[sid] = endOfChain
		}
	}
	// Mini container chain.
	for i := 0; i < miniContainerSectors; i++ {
		sid := firstMiniContainerSect + uint32(i)
		if i < miniContainerSectors-1 {
			fat[sid] = sid + 1
		} else {
			fat[sid] = endOfChain
		}
	}
	// Regular stream chains.
	for _, l := range layouts {
		if l.isMini {
			continue
		}
		n := (len(l.data) + ss - 1) / ss
		for i := 0; i < n; i++ {
			sid := l.startSect + uint32(i)
			if i < n-1 {
				fat[sid] = sid + 1
			} else {
				fat[sid] = endOfChain
			}
		}
	}

	// Sanity check: we haven't exceeded what the FAT can describe.
	if totalSectors > len(fat) {
		return fmt.Errorf("cfb: internal error: totalSectors=%d exceeds FAT capacity=%d", totalSectors, len(fat))
	}

	// ── Phase 5: build DIFAT for header ──────────────────────────────────────

	if numFATSectors > 109 {
		// DIFAT sector chain would be needed; not yet implemented.
		return fmt.Errorf("cfb: file requires %d FAT sectors; DIFAT overflow not implemented", numFATSectors)
	}
	var difat [109]uint32
	for i := range difat {
		difat[i] = freeSect
	}
	for i := 0; i < numFATSectors; i++ {
		difat[i] = uint32(i) // FAT sector IDs are 0, 1, 2, …
	}

	// ── Phase 6: write header ─────────────────────────────────────────────────

	hdr := rawHeader{
		Sig:           cfbMagic,
		MinorVersion:  0x003E,
		MajorVersion:  0x0003,
		ByteOrder:     0xFFFE,
		SectorSizePow: 9, // 1<<9 = 512
		MiniSizePow:   6, // 1<<6 = 64
		NumFATSects:   uint32(numFATSectors),
		FirstDirSect:  firstDirSect,
		MiniCutoff:    mc,
		FirstMiniFAT:  firstMiniFATSect,
		NumMiniFAT:    uint32(numMiniFATSectors),
		FirstDIFAT:    endOfChain,
		NumDIFAT:      0,
		DIFAT:         difat,
	}
	var hdrBuf [headerSize]byte
	marshalHeader(hdr, &hdrBuf)
	if _, err := dst.Write(hdrBuf[:]); err != nil {
		return err
	}

	// ── Phase 7: write FAT sectors ───────────────────────────────────────────

	entriesPerSector := ss / 4
	for i := 0; i < numFATSectors; i++ {
		var sector [ss]byte
		base := i * entriesPerSector
		for j := 0; j < entriesPerSector; j++ {
			idx := base + j
			v := freeSect
			if idx < len(fat) {
				v = fat[idx]
			}
			binary.LittleEndian.PutUint32(sector[j*4:], v)
		}
		if _, err := dst.Write(sector[:]); err != nil {
			return err
		}
	}

	// ── Phase 8: write directory sectors ─────────────────────────────────────

	// Build flat list of directory entries.
	// Entry 0: Root Entry.
	// Entries 1…N: streams in addition order, forming a right-sibling chain.
	type dirSlot struct {
		name      string
		objType   byte
		leftSib   uint32
		rightSib  uint32
		child     uint32
		startSect uint32
		sizeLow   uint32
	}
	slots := make([]dirSlot, numDirSectors*4)
	for i := range slots {
		slots[i] = dirSlot{
			leftSib:   freeSect,
			rightSib:  freeSect,
			child:     freeSect,
			startSect: endOfChain,
		}
	}

	// Root entry.
	slots[0].name = "Root Entry"
	slots[0].objType = objRoot
	if len(layouts) > 0 {
		slots[0].child = 1
	}
	slots[0].startSect = firstMiniContainerSect
	if miniContainerSectors > 0 {
		slots[0].sizeLow = uint32(len(miniStreamData))
	}

	// Stream entries: simple right-sibling chain.
	for i, l := range layouts {
		idx := i + 1
		slots[idx].name = l.name
		slots[idx].objType = objStream
		if i+1 < len(layouts) {
			slots[idx].rightSib = uint32(idx + 1)
		}
		slots[idx].startSect = l.startSect
		slots[idx].sizeLow = uint32(len(l.data))
	}

	for s := 0; s < numDirSectors; s++ {
		var sector [ss]byte
		for e := 0; e < 4; e++ {
			idx := s*4 + e
			sl := slots[idx]
			entry := marshalDirEntry(sl.name, sl.objType, sl.leftSib, sl.rightSib, sl.child, sl.startSect, sl.sizeLow)
			copy(sector[e*dirEntrySize:], entry[:])
		}
		if _, err := dst.Write(sector[:]); err != nil {
			return err
		}
	}

	// ── Phase 9: write mini FAT sectors ──────────────────────────────────────

	if numMiniFATSectors > 0 {
		buf := make([]byte, numMiniFATSectors*ss)
		for i, v := range miniFATEntries {
			binary.LittleEndian.PutUint32(buf[i*4:], v)
		}
		// Remaining entries stay as freeSect (zero isn't freeSect, but we fill it).
		for i := len(miniFATEntries); i < numMiniFATSectors*ss/4; i++ {
			binary.LittleEndian.PutUint32(buf[i*4:], freeSect)
		}
		if _, err := dst.Write(buf); err != nil {
			return err
		}
	}

	// ── Phase 10: write mini stream container ────────────────────────────────

	if miniContainerSectors > 0 {
		padded := make([]byte, miniContainerSectors*ss)
		copy(padded, miniStreamData)
		if _, err := dst.Write(padded); err != nil {
			return err
		}
	}

	// ── Phase 11: write regular stream data ──────────────────────────────────

	for _, l := range layouts {
		if l.isMini {
			continue
		}
		n := (len(l.data) + ss - 1) / ss
		padded := make([]byte, n*ss)
		copy(padded, l.data)
		if _, err := dst.Write(padded); err != nil {
			return err
		}
	}

	return nil
}

// Package biff implements reading and writing of Binary Interchange File Format
// (BIFF8) record streams, as used inside the "Workbook" stream of an XLS file.
//
// # Record wire format
//
// A BIFF8 stream is a sequence of variable-length records.  Each record has:
//
//	2 bytes  record type (opcode)
//	2 bytes  data length  (0 … maxDataSize)
//	N bytes  payload
//
// Records whose payload would exceed [maxDataSize] are split: the first chunk
// uses the original opcode and subsequent chunks use the [RecContinue] opcode.
// [Reader] transparently reassembles these chunks; [Writer] automatically
// splits large payloads.
//
// # SST CONTINUE caveat
//
// The MS-XLS specification allows a CONTINUE record that follows an SST record
// to embed a 1-byte re-encoding flag when a shared string crosses the record
// boundary.  This package concatenates CONTINUE bytes verbatim; callers that
// parse SST records must handle that flag themselves.
package biff

// maxDataSize is the maximum payload in bytes that a single BIFF8 record
// may carry.  Payloads larger than this must be split across CONTINUE records.
const maxDataSize = 8224

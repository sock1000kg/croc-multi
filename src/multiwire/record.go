package multiwire

// A "record" is the plaintext that gets encrypted into ONE DATA frame
// (docs/contracts.md §4). The sender builds records (multisend), the receiver
// parses them (multirecv). The first byte says which kind of record it is:
//
//	kind 1  chunk     a piece of a file:      kind | fileIdx | offset | data
//	kind 2  file-end  "file N is finished":   kind | fileIdx | sha256
//	kind 3  all-end   "no more files":        kind
//
// Field sizes (this is where numbers like 1+2+8 come from):
//
//	kind     u8   = 1 byte
//	fileIdx  u16  = 2 bytes  (index into the session's file list; max 65535 files)
//	offset   u64  = 8 bytes  (byte position of this chunk inside the file)
//	sha256        = 32 bytes
//
// So a chunk header is 1+2+8 = 11 bytes and a file-end record is 1+2+32 = 35
// bytes.
//
// Byte layout of a chunk record:
//
//	index:  0      1   2   3 ............ 10   11 ...........
//	       [kind] [fileIdx] [   offset     ]  [ data ...    ]
//	        b[0]   b[1:3]       b[3:11]          b[11:]
//
// Why carry offset at all if chunks arrive in order? Because receivers write
// with WriteAt(offset), which keeps them correct even if chunks are replayed or
// (in Phase 2) re-sent out of order to fill gaps.

import (
	"encoding/binary"
	"fmt"
)

const (
	RecordKindChunk   uint8 = 1
	RecordKindFileEnd uint8 = 2
	RecordKindAllEnd  uint8 = 3

	ChunkHeaderSize = 1 + 2 + 8  // kind + fileIdx + offset
	FileEndSize     = 1 + 2 + 32 // kind + fileIdx + sha256
)

// EncodeChunk builds a kind-1 record. Layout: b[0]=kind, b[1:3]=fileIdx,
// b[3:11]=offset, b[11:]=data. The data is copied, so the caller may reuse
// its buffer afterwards.
func EncodeChunk(fileIdx uint16, offset uint64, data []byte) []byte {
	buf := make([]byte, ChunkHeaderSize+len(data))
	buf[0] = RecordKindChunk
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx)
	binary.LittleEndian.PutUint64(buf[3:11], offset)
	copy(buf[ChunkHeaderSize:], data)
	return buf
}

// EncodeFileEnd builds a kind-2 record telling receivers that file fileIdx is
// complete and what its SHA-256 must be. Layout: b[0]=kind, b[1:3]=fileIdx,
// b[3:35]=hash.
func EncodeFileEnd(fileIdx uint16, hash [32]byte) []byte {
	buf := make([]byte, FileEndSize)
	buf[0] = RecordKindFileEnd
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx)
	copy(buf[3:], hash[:])
	return buf
}

// EncodeAllEnd builds the 1-byte kind-3 record that ends the session. A
// receiver replies "done" only after it sees this and every file hash matched.
func EncodeAllEnd() []byte {
	return []byte{RecordKindAllEnd}
}

// ChunkRecord is a decoded kind-1 record.
type ChunkRecord struct {
	FileIdx uint16
	Offset  uint64
	Data    []byte // aliases the decoded buffer; copy it to keep it
}

// FileEndRecord is a decoded kind-2 record.
type FileEndRecord struct {
	FileIdx uint16
	Hash    [32]byte
}

// DecodeRecord parses a decrypted record (the inverse of the Encode* funcs).
// Exactly one of the results is meaningful, depending on kind:
//
//	kind 1 -> chunk != nil  (1..ChunkSize data bytes)
//	kind 2 -> fileEnd != nil
//	kind 3 -> both nil (all-end carries no data)
//
// Sizes are checked exactly: a record with trailing bytes is rejected.
// Chunk data is NOT copied (it is written straight to disk by the receiver),
// so b must stay untouched while chunk.Data is in use.
func DecodeRecord(b []byte) (kind uint8, chunk *ChunkRecord, fileEnd *FileEndRecord, err error) {
	if len(b) == 0 {
		return 0, nil, nil, fmt.Errorf("empty record")
	}
	kind = b[0]
	switch kind {
	case RecordKindChunk:
		if len(b) <= ChunkHeaderSize || len(b) > ChunkHeaderSize+ChunkSize {
			return 0, nil, nil, fmt.Errorf("invalid chunk record size %d", len(b))
		}
		return kind, &ChunkRecord{
			FileIdx: binary.LittleEndian.Uint16(b[1:3]),
			Offset:  binary.LittleEndian.Uint64(b[3:11]),
			Data:    b[ChunkHeaderSize:],
		}, nil, nil

	case RecordKindFileEnd:
		if len(b) != FileEndSize {
			return 0, nil, nil, fmt.Errorf("invalid file-end record size %d", len(b))
		}
		fe := &FileEndRecord{FileIdx: binary.LittleEndian.Uint16(b[1:3])}
		copy(fe.Hash[:], b[3:])
		return kind, nil, fe, nil

	case RecordKindAllEnd:
		if len(b) != 1 {
			return 0, nil, nil, fmt.Errorf("invalid all-end record size %d", len(b))
		}
		return kind, nil, nil, nil

	default:
		return 0, nil, nil, fmt.Errorf("unknown record kind %d", kind)
	}
}

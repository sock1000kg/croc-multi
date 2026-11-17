package multisend

// A "record" is the plaintext that gets encrypted into ONE DATA frame
// (docs/contracts.md §4). The first byte says which kind of record it is:
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
// bytes. All multi-byte integers are little-endian, matching src/comm.
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
	"errors"
	"fmt"
)

const (
	RecordKindChunk   uint8 = 1
	RecordKindFileEnd uint8 = 2
	RecordKindAllEnd  uint8 = 3

	// ChunkSize is the max file bytes per chunk record. 32 KB of data + 11-byte
	// header + 28 bytes of AES-GCM overhead + 6-byte hub header stays well under
	// the 64 KB frame limit (models.TCP_BUFFER_SIZE).
	ChunkSize = 32 * 1024
)

// EncodeChunk đóng gói bản ghi kind 1: kind u8 (1) | fileIdx u16 | offset u64 | data
//
// EncodeChunk builds a kind-1 record. Layout: b[0]=kind, b[1:3]=fileIdx,
// b[3:11]=offset, b[11:]=data (11 = 1+2+8 header bytes). The data is copied,
// so the caller may reuse its buffer afterwards.
func EncodeChunk(fileIdx uint16, offset uint64, data []byte) []byte {
	buf := make([]byte, 1+2+8+len(data)) // header (kind+fileIdx+offset) + data
	buf[0] = RecordKindChunk
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx) // bytes 1..2
	binary.LittleEndian.PutUint64(buf[3:11], offset) // bytes 3..10
	copy(buf[11:], data)                             // byte 11 onward
	return buf
}

// EncodeFileEnd đóng gói bản ghi kind 2: kind u8 (2) | fileIdx u16 | sha256 (32 byte)
//
// EncodeFileEnd builds a kind-2 record telling receivers that file fileIdx is
// complete and what its SHA-256 must be. Layout: b[0]=kind, b[1:3]=fileIdx,
// b[3:35]=hash (35 = 1+2+32 bytes total).
func EncodeFileEnd(fileIdx uint16, hash [32]byte) []byte {
	buf := make([]byte, 1+2+32) // kind + fileIdx + sha256
	buf[0] = RecordKindFileEnd
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx)
	copy(buf[3:35], hash[:])
	return buf
}

// EncodeAllEnd đóng gói bản ghi kind 3: kind u8 (3)
//
// EncodeAllEnd builds the 1-byte kind-3 record that ends the session. A
// receiver replies "done" only after it sees this and every file hash matched.
func EncodeAllEnd() []byte {
	return []byte{RecordKindAllEnd}
}

// ChunkRecord chứa thông tin khối dữ liệu giải mã từ kind 1
//
// ChunkRecord is a decoded kind-1 record.
type ChunkRecord struct {
	FileIdx uint16
	Offset  uint64
	Data    []byte
}

// FileEndRecord chứa thông tin kết thúc file giải mã từ kind 2
//
// FileEndRecord is a decoded kind-2 record.
type FileEndRecord struct {
	FileIdx uint16
	Hash    [32]byte
}

// DecodeRecord giải mã bản ghi thô (plaintext) thành loại tương ứng
//
// DecodeRecord parses a decrypted record (the inverse of the Encode* funcs).
// Exactly one of the results is meaningful, depending on kind:
//
//	kind 1 -> chunk != nil
//	kind 2 -> fileEnd != nil
//	kind 3 -> both nil (all-end carries no data)
//
// It is mainly for receivers (multirecv) and tests; the sender never decodes.
// Chunk data is copied out of b, so b may be reused after the call.
func DecodeRecord(b []byte) (kind uint8, chunk *ChunkRecord, fileEnd *FileEndRecord, err error) {
	if len(b) < 1 {
		return 0, nil, nil, errors.New("bản ghi rỗng") // empty record
	}
	kind = b[0]
	switch kind {
	case RecordKindChunk:
		if len(b) < 11 { // shorter than the 1+2+8 header
			return 0, nil, nil, fmt.Errorf("bản ghi chunk quá ngắn: %d byte", len(b))
		}
		fileIdx := binary.LittleEndian.Uint16(b[1:3])
		offset := binary.LittleEndian.Uint64(b[3:11])
		data := make([]byte, len(b)-11)
		copy(data, b[11:])
		return kind, &ChunkRecord{
			FileIdx: fileIdx,
			Offset:  offset,
			Data:    data,
		}, nil, nil

	case RecordKindFileEnd:
		if len(b) < 35 { // shorter than 1+2+32
			return 0, nil, nil, fmt.Errorf("bản ghi file-end quá ngắn: %d byte", len(b))
		}
		fileIdx := binary.LittleEndian.Uint16(b[1:3])
		var hash [32]byte
		copy(hash[:], b[3:35])
		return kind, nil, &FileEndRecord{
			FileIdx: fileIdx,
			Hash:    hash,
		}, nil

	case RecordKindAllEnd:
		return kind, nil, nil, nil

	default:
		return 0, nil, nil, fmt.Errorf("loại bản ghi không xác định: %d", kind) // unknown kind
	}
}

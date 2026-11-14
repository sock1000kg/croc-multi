package multisend

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	RecordKindChunk   uint8 = 1
	RecordKindFileEnd uint8 = 2
	RecordKindAllEnd  uint8 = 3
	ChunkSize               = 32 * 1024
)

// EncodeChunk đóng gói bản ghi kind 1: kind u8 (1) | fileIdx u16 | offset u64 | data
func EncodeChunk(fileIdx uint16, offset uint64, data []byte) []byte {
	buf := make([]byte, 1+2+8+len(data))
	buf[0] = RecordKindChunk
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx)
	binary.LittleEndian.PutUint64(buf[3:11], offset)
	copy(buf[11:], data)
	return buf
}

// EncodeFileEnd đóng gói bản ghi kind 2: kind u8 (2) | fileIdx u16 | sha256 (32 byte)
func EncodeFileEnd(fileIdx uint16, hash [32]byte) []byte {
	buf := make([]byte, 1+2+32)
	buf[0] = RecordKindFileEnd
	binary.LittleEndian.PutUint16(buf[1:3], fileIdx)
	copy(buf[3:35], hash[:])
	return buf
}

// EncodeAllEnd đóng gói bản ghi kind 3: kind u8 (3)
func EncodeAllEnd() []byte {
	return []byte{RecordKindAllEnd}
}

// ChunkRecord chứa thông tin khối dữ liệu giải mã từ kind 1
type ChunkRecord struct {
	FileIdx uint16
	Offset  uint64
	Data    []byte
}

// FileEndRecord chứa thông tin kết thúc file giải mã từ kind 2
type FileEndRecord struct {
	FileIdx uint16
	Hash    [32]byte
}

// DecodeRecord giải mã bản ghi thô (plaintext) thành loại tương ứng
func DecodeRecord(b []byte) (kind uint8, chunk *ChunkRecord, fileEnd *FileEndRecord, err error) {
	if len(b) < 1 {
		return 0, nil, nil, errors.New("bản ghi rỗng")
	}
	kind = b[0]
	switch kind {
	case RecordKindChunk:
		if len(b) < 11 {
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
		if len(b) < 35 {
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
		return 0, nil, nil, fmt.Errorf("loại bản ghi không xác định: %d", kind)
	}
}

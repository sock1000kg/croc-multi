package multisend

import (
	"context"
	"crypto/sha256"
	"io"
	"os"

	"github.com/schollz/croc/v11/src/crypt"
)

// StreamFiles đọc tuần tự từng file, cắt khối 32KB, mã hóa bằng dataKey và phát qua kênh emit
func StreamFiles(ctx context.Context, files []string, dataKey []byte, emit func(cipherChunk []byte) error) (int64, error) {
	aead, err := crypt.NewAESGCM(dataKey)
	if err != nil {
		return 0, err
	}

	var totalBytesSent int64
	buf := make([]byte, ChunkSize)
	var cipherBuf []byte

	for fileIdx, filePath := range files {
		f, err := os.Open(filePath)
		if err != nil {
			return totalBytesSent, err
		}

		hasher := sha256.New()
		tee := io.TeeReader(f, hasher)

		var offset uint64 = 0
		for {
			if ctx.Err() != nil {
				f.Close()
				return totalBytesSent, ctx.Err()
			}

			n, readErr := tee.Read(buf)
			if n > 0 {
				plainRecord := EncodeChunk(uint16(fileIdx), offset, buf[:n])
				offset += uint64(n)

				cipherChunk, err := crypt.EncryptAEADTo(cipherBuf, plainRecord, aead)
				if err != nil {
					f.Close()
					return totalBytesSent, err
				}
				cipherBuf = cipherChunk[:0] // tái sử dụng buffer

				if err := emit(cipherChunk); err != nil {
					f.Close()
					return totalBytesSent, err
				}
				totalBytesSent += int64(len(cipherChunk))
			}

			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				f.Close()
				return totalBytesSent, readErr
			}
		}
		f.Close()

		// Gửi file-end kèm sha256
		var hashArr [32]byte
		copy(hashArr[:], hasher.Sum(nil))
		fileEndRecord := EncodeFileEnd(uint16(fileIdx), hashArr)
		cipherEnd, err := crypt.EncryptAEADTo(cipherBuf, fileEndRecord, aead)
		if err != nil {
			return totalBytesSent, err
		}
		if err := emit(cipherEnd); err != nil {
			return totalBytesSent, err
		}
		totalBytesSent += int64(len(cipherEnd))
	}

	// Gửi all-end kết thúc toàn bộ phiên
	allEndRecord := EncodeAllEnd()
	cipherAllEnd, err := crypt.EncryptAEADTo(cipherBuf, allEndRecord, aead)
	if err != nil {
		return totalBytesSent, err
	}
	if err := emit(cipherAllEnd); err != nil {
		return totalBytesSent, err
	}
	totalBytesSent += int64(len(cipherAllEnd))

	return totalBytesSent, nil
}
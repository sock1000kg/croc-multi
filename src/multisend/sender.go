package multisend

import (
	"context"
	"crypto/sha256"
	"io"
	"os"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
)

// StreamFiles đọc tuần tự từng file, cắt khối 32KB, mã hóa bằng dataKey và phát qua kênh emit
//
// StreamFiles is the sender's data pump. For every file, in order, it emits:
//
//	chunk(fileIdx, 0, ≤32KB) chunk(fileIdx, 32K, …) … file-end(fileIdx, sha256)
//
// and after the last file one all-end record. Each record is encrypted ONCE
// with dataKey (AES-256-GCM) and passed to emit. In the real sender, emit wraps
// the ciphertext in a hub DATA frame and sends it; the hub fans it out to all N
// receivers. Tests pass an emit that copies into channels instead.
//
// fileIdx is the position in files, so files[i] must match the "idx" the
// receivers got in the session manifest.
//
// The returned int64 counts ciphertext bytes handed to emit (≈ file size plus
// ~39 bytes per 32 KB chunk). It is the "uploaded X MB" number and must NOT grow
// with the number of receivers; that is the whole point of multi-send.
//
// IMPORTANT for whoever writes emit: the cipherChunk slice is REUSED for the
// next record (see cipherBuf below). emit must finish with it before returning
// (e.g. write it to the network synchronously) or copy it. Queuing the slice
// itself would corrupt data.
//
// emit returning an error, ctx being cancelled, or a file read error stops the
// whole stream; there is no per-file retry.
func StreamFiles(ctx context.Context, files []string, dataKey []byte, emit func(cipherChunk []byte) error) (int64, error) {
	// One AEAD for the whole session; EncryptAEADTo picks a fresh random
	// 12-byte nonce for every record, so reusing the key is safe.
	aead, err := crypt.NewAESGCM(dataKey)
	if err != nil {
		return 0, err
	}

	var totalBytesSent int64
	buf := make([]byte, multiwire.ChunkSize) // plaintext read buffer, reused every chunk
	var cipherBuf []byte                     // ciphertext buffer, reused after the first record

	for fileIdx, filePath := range files {
		f, err := os.Open(filePath)
		if err != nil {
			return totalBytesSent, err
		}

		// TeeReader: every byte read from f is also written into hasher, so we
		// get the file's SHA-256 in the same pass instead of reading it twice.
		hasher := sha256.New()
		tee := io.TeeReader(f, hasher)

		var offset uint64 = 0
		for {
			if ctx.Err() != nil {
				f.Close()
				return totalBytesSent, ctx.Err()
			}

			// Read may return fewer than multiwire.ChunkSize bytes (always at EOF, sometimes
			// mid-file); chunks are therefore "up to" 32 KB, never exactly sized.
			// Per io.Reader rules, handle n > 0 BEFORE looking at readErr.
			n, readErr := tee.Read(buf)
			if n > 0 {
				// uint16(fileIdx) silently wraps past 65535 files (contract limit).
				plainRecord := multiwire.EncodeChunk(uint16(fileIdx), offset, buf[:n])
				offset += uint64(n)

				cipherChunk, err := crypt.EncryptAEADTo(cipherBuf, plainRecord, aead)
				if err != nil {
					f.Close()
					return totalBytesSent, err
				}
				// Keep the backing array so the next EncryptAEADTo writes into it
				// instead of allocating. This is why emit must not keep the slice.
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
		// The hasher has now seen every byte of the file (via the TeeReader), so
		// its sum is the file's SHA-256. Receivers compare it after writing.
		var hashArr [32]byte
		copy(hashArr[:], hasher.Sum(nil))
		fileEndRecord := multiwire.EncodeFileEnd(uint16(fileIdx), hashArr)
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
	allEndRecord := multiwire.EncodeAllEnd()
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

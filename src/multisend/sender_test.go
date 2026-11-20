package multisend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
)

// TestRecordEncodeDecode kiểm tra tính chính xác của các hàm đóng/mở gói bản ghi
func TestRecordEncodeDecode(t *testing.T) {
	// 1. Kiểm tra Chunk (kind 1)
	payload := []byte("hello network programming multi-send")
	chunkBytes := multiwire.EncodeChunk(1, 1024, payload)
	kind, chunk, fileEnd, err := multiwire.DecodeRecord(chunkBytes)
	if err != nil {
		t.Fatalf("multiwire.DecodeRecord chunk failed: %v", err)
	}
	if kind != multiwire.RecordKindChunk || chunk == nil || fileEnd != nil {
		t.Fatalf("expected RecordKindChunk, got %d", kind)
	}
	if chunk.FileIdx != 1 || chunk.Offset != 1024 || !bytes.Equal(chunk.Data, payload) {
		t.Fatalf("chunk data mismatch: %+v", chunk)
	}

	// 2. Kiểm tra FileEnd (kind 2)
	dummyHash := sha256.Sum256([]byte("dummy content"))
	fileEndBytes := multiwire.EncodeFileEnd(2, dummyHash)
	kind, chunk, fileEnd, err = multiwire.DecodeRecord(fileEndBytes)
	if err != nil {
		t.Fatalf("multiwire.DecodeRecord fileEnd failed: %v", err)
	}
	if kind != multiwire.RecordKindFileEnd || fileEnd == nil || chunk != nil {
		t.Fatalf("expected RecordKindFileEnd, got %d", kind)
	}
	if fileEnd.FileIdx != 2 || fileEnd.Hash != dummyHash {
		t.Fatalf("fileEnd data mismatch: %+v", fileEnd)
	}

	// 3. Kiểm tra AllEnd (kind 3)
	allEndBytes := multiwire.EncodeAllEnd()
	kind, chunk, fileEnd, err = multiwire.DecodeRecord(allEndBytes)
	if err != nil {
		t.Fatalf("multiwire.DecodeRecord allEnd failed: %v", err)
	}
	if kind != multiwire.RecordKindAllEnd || chunk != nil || fileEnd != nil {
		t.Fatalf("expected RecordKindAllEnd, got %d", kind)
	}
}

// TestM1c_SenderEngine chạy bài kiểm chứng mốc M1c theo docs/phan-cong.md:
// 1 người gửi + 3 người nhận trong bộ nhớ:
// 1. In "3/3 done".
// 2. Số byte đã gửi ≈ kích thước file (KHÔNG gấp 3).
// 3. sha256 của file ghi ra ở cả 3 nơi giống hệt file gốc.
func TestM1c_SenderEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Tạo file dữ liệu mẫu (100 KB để vượt qua nhiều chunk 32 KB)
	tempDir := t.TempDir()
	sourceFilePath := filepath.Join(tempDir, "test_source.bin")
	fileSize := 100 * 1024 // 100 KB

	sourceData := make([]byte, fileSize)
	if _, err := rand.Read(sourceData); err != nil {
		t.Fatalf("tạo dữ liệu ngẫu nhiên thất bại: %v", err)
	}
	if err := os.WriteFile(sourceFilePath, sourceData, 0o600); err != nil {
		t.Fatalf("ghi file nguồn thất bại: %v", err)
	}

	originalHash := sha256.Sum256(sourceData)
	originalHashHex := hex.EncodeToString(originalHash[:])
	t.Logf("File gốc: dung lượng = %d bytes, sha256 = %s", fileSize, originalHashHex)

	// 2. Tạo khóa ngẫu nhiên cho phiên (dataKey 32 bytes)
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		t.Fatalf("tạo dataKey thất bại: %v", err)
	}

	// 3. Giả lập Hub và 3 người nhận (Receivers)
	numReceivers := 3
	type receiverState struct {
		id       int
		outDir   string
		outFile  string
		done     bool
		hashHex  string
		bytesRec int64
	}

	receivers := make([]*receiverState, numReceivers)
	receiverChans := make([]chan []byte, numReceivers)

	for i := range numReceivers {
		rxDir := filepath.Join(tempDir, fmt.Sprintf("receiver_%d", i+1))
		if err := os.MkdirAll(rxDir, 0o700); err != nil {
			t.Fatalf("tạo thư mục receiver thất bại: %v", err)
		}
		receivers[i] = &receiverState{
			id:      i + 1,
			outDir:  rxDir,
			outFile: filepath.Join(rxDir, "received.bin"),
		}
		receiverChans[i] = make(chan []byte, 100)
	}

	// Hub in-memory chuyển tiếp: nhận 1 chunk từ sender, sao chép gửi cho cả 3 receivers
	emit := func(cipherChunk []byte) error {
		for i := range numReceivers {
			packet := make([]byte, len(cipherChunk))
			copy(packet, cipherChunk)
			receiverChans[i] <- packet
		}
		return nil
	}

	// Khởi chạy 3 goroutines người nhận
	var wg sync.WaitGroup
	receiverAead, err := crypt.NewAESGCM(dataKey)
	if err != nil {
		t.Fatalf("khởi tạo AES-GCM thất bại: %v", err)
	}

	for i := range numReceivers {
		wg.Add(1)
		go func(rx *receiverState, ch <-chan []byte) {
			defer wg.Done()

			outFile, err := os.OpenFile(rx.outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Errorf("[Receiver %d] mở file thất bại: %v", rx.id, err)
				return
			}
			defer outFile.Close()

			for {
				select {
				case <-ctx.Done():
					t.Errorf("[Receiver %d] timeout", rx.id)
					return
				case cipherChunk, ok := <-ch:
					if !ok {
						return
					}

					plainRecord, err := crypt.DecryptAEAD(cipherChunk, receiverAead)
					if err != nil {
						t.Errorf("[Receiver %d] giải mã thất bại: %v", rx.id, err)
						return
					}

					kind, chunk, fileEnd, err := multiwire.DecodeRecord(plainRecord)
					if err != nil {
						t.Errorf("[Receiver %d] giải mã bản ghi thất bại: %v", rx.id, err)
						return
					}

					switch kind {
					case multiwire.RecordKindChunk:
						if _, err := outFile.WriteAt(chunk.Data, int64(chunk.Offset)); err != nil {
							t.Errorf("[Receiver %d] ghi file thất bại: %v", rx.id, err)
							return
						}
						rx.bytesRec += int64(len(chunk.Data))

					case multiwire.RecordKindFileEnd:
						// Kiểm tra sha256 sau khi nhận hết file
						outFile.Sync()
						content, err := os.ReadFile(rx.outFile)
						if err != nil {
							t.Errorf("[Receiver %d] đọc file nhận thất bại: %v", rx.id, err)
							return
						}
						actualHash := sha256.Sum256(content)
						if actualHash != fileEnd.Hash {
							t.Errorf("[Receiver %d] sai băm: expected %x, got %x", rx.id, fileEnd.Hash, actualHash)
							return
						}
						rx.hashHex = hex.EncodeToString(actualHash[:])

					case multiwire.RecordKindAllEnd:
						rx.done = true
						return
					}
				}
			}
		}(receivers[i], receiverChans[i])
	}

	// 4. Người gửi bắt đầu phát dữ liệu qua Hub
	totalBytesSent, err := StreamFiles(ctx, []string{sourceFilePath}, dataKey, emit)
	if err != nil {
		t.Fatalf("StreamFiles thất bại: %v", err)
	}

	// Đóng các kênh sau khi phát xong để receiver dừng vòng lặp
	for i := range numReceivers {
		close(receiverChans[i])
	}

	// Chờ cả 3 receivers xử lý xong
	wg.Wait()

	// 5. Kiểm tra các tiêu chuẩn nghiệm thu M1c

	// Tiêu chuẩn 1: Cả 3 người nhận đều hoàn thành (3/3 done)
	doneCount := 0
	for _, rx := range receivers {
		if rx.done {
			doneCount++
		}
	}
	doneMessage := fmt.Sprintf("%d/%d done", doneCount, numReceivers)
	t.Log(doneMessage)
	fmt.Printf("\n====> KẾT QUẢ NGHIỆM THU: %s <====\n", doneMessage)
	if doneCount != numReceivers {
		t.Fatalf("Tiêu chuẩn 1 thất bại: mong đợi %d/3 done, thực tế %d/3 done", numReceivers, doneCount)
	}

	// Tiêu chuẩn 2: Số byte người gửi tải lên ≈ kích thước file (KHÔNG gấp 3)
	// (Chỉ tăng một chút do metadata kind/offset và AEAD overhead nonce 12B + tag 16B)
	// Real overhead per 32 KB chunk is 11 (record header) + 12 (nonce) + 16 (GCM
	// tag) = 39 bytes ≈ 0.12%, plus 63 bytes for file-end and 29 for all-end.
	// 15% is a loose bound: the point is to catch "uploaded N times" (≥ 200%).
	maxExpectedUpload := int64(float64(fileSize) * 1.15) // tối đa +15% cho overhead
	t.Logf("Tổng byte người gửi tải lên: %d bytes (File gốc: %d bytes)", totalBytesSent, fileSize)
	if totalBytesSent < int64(fileSize) {
		t.Fatalf("Tiêu chuẩn 2 thất bại: số byte gửi (%d) nhỏ hơn kích thước file (%d)", totalBytesSent, fileSize)
	}
	if totalBytesSent > maxExpectedUpload {
		t.Fatalf("Tiêu chuẩn 2 thất bại: số byte gửi (%d) bị vượt quá (nghi vấn bị nhân N)", totalBytesSent)
	}
	t.Logf("Tiêu chuẩn 2 ĐẠT: Số byte gửi %d byte không bị nhân 3!", totalBytesSent)

	// Tiêu chuẩn 3: sha256 ở cả 3 nơi nhận giống hệt file gốc
	for _, rx := range receivers {
		if rx.hashHex != originalHashHex {
			t.Fatalf("Tiêu chuẩn 3 thất bại: Receiver %d có hash %s không khớp hash gốc %s", rx.id, rx.hashHex, originalHashHex)
		}
		if rx.bytesRec != int64(fileSize) {
			t.Fatalf("Tiêu chuẩn 3 thất bại: Receiver %d nhận thiếu byte: %d / %d", rx.id, rx.bytesRec, fileSize)
		}
	}
	t.Logf("Tiêu chuẩn 3 ĐẠT: sha256 của cả 3 người nhận trùng khớp 100%% (%s)", originalHashHex)
}

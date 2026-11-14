package multisend

import (
	"context"
	"time"
)

type SendOptions struct {
	Files       []string
	Receivers   int
	Approve     bool
	Timeout     time.Duration
	Relay       string
	RelayPass   string
	Code        string             // trống thì tự sinh
	ThrottleBps int64              // 0 = không giới hạn
	Progress    func(PeerProgress) // gọi mỗi khi có thay đổi từ từng người nhận
}

type PeerProgress struct {
	ID     uint16
	Name   string
	Bytes  int64
	State  string // joined, ready, receiving, done, failed, left
	Reason string
}

// Send là hàm khởi động gửi multi-send (sẽ tích hợp multisession ở M2)
func Send(ctx context.Context, opts SendOptions) error {
	// Sẽ được hoàn thiện khi ghép nối relay thật ở M2
	return nil
}

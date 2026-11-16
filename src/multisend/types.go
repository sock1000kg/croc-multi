package multisend

import (
	"context"
	"time"
)

// SendOptions is everything the CLI (`croc send --receivers N ...`) passes to
// Send. Shape is frozen in docs/contracts.md §5.4.
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

// PeerProgress is one receiver's status, reported through SendOptions.Progress
// so the CLI can draw the per-receiver progress lines.
type PeerProgress struct {
	ID     uint16 // hub member ID: sender is always 1, receivers are 2, 3, 4, ...
	Name   string
	Bytes  int64
	State  string // joined, ready, receiving, done, failed, left
	Reason string
}

// Send là hàm khởi động gửi multi-send (sẽ tích hợp multisession ở M2)
//
// Send is the full sender flow. It is a STUB today; in M2 it will:
//  1. connect to the relay and JOIN the "multi:<room>" as sender (multisession.Dial),
//  2. run PAKE with each receiver as it joins and give it the dataKey
//     (multisession.AuthorizeReceiver),
//  3. call StreamFiles with an emit that sends DATA frames to the hub,
//  4. wait for "done"/"fail" from every receiver over DIRECT frames.
//
// Per contract it returns nil only if ALL receivers reported done.
func Send(ctx context.Context, opts SendOptions) error {
	// Sẽ được hoàn thiện khi ghép nối relay thật ở M2
	return nil
}

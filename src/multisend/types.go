package multisend

import (
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

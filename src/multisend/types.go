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

// PeerProgress is one event for the CLI's progress display, reported through
// SendOptions.Progress. Calls are serialized (never two at once), but may come
// from different goroutines, and a few may still arrive after Send returns.
//
// Per-receiver states (ID ≥ 2):
//
//	joined     the hub let it in; Addr is its IP (Name not known yet)
//	rejected   left the lobby without the dataKey (wrong code, refused,
//	           not ready in time); Reason says why
//	ready      authenticated and ready; Name is its sanitized --name
//	receiving  Bytes = plaintext bytes it has written so far
//	done       finished, every hash matched
//	failed     reported an error or its link broke; Reason says why
//	left       disconnected (MEMBER_LEFT) before it was done
//
// Session-wide states (ID 0):
//
//	sending    the upload starts now (the timing clock starts); Total is
//	           the manifest's TotalSize, Bytes is 0
//	uploaded   the upload finished; Bytes = ciphertext bytes sent to the hub
//	           (must stay ≈ Total, whatever the number of receivers)
type PeerProgress struct {
	ID     uint16 // hub member ID: sender is always 1, receivers are 2, 3, 4, ...; 0 = whole session
	Name   string
	Addr   string
	Bytes  int64
	Total  int64
	State  string
	Reason string
}

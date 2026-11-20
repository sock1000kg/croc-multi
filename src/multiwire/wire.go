// Package multiwire is the shared wire contract for croc's 1-to-N multi-send
// mode (docs/contracts.md §2 and §4). Every other multi* package imports it,
// so the hub, the sender, the receiver and the test tools cannot disagree
// about a byte value. Nothing here touches the network.
//
// Three layers, from outside in:
//
//	comm frame:  "croc" | len u32 | ...                         (src/comm)
//	 hub frame:  type u8 | flags u8 | from u16 | to u16 | payload  (this file)
//	  DATA payload: AES-GCM(record) with the session's dataKey   (record.go)
//
// The hub reads only the 6-byte hub-frame header and routes on it; it never
// decrypts a payload. All multi-byte integers are little-endian, like src/comm.
package multiwire

import (
	"encoding/binary"
	"errors"
)

// Version is the first byte of JOIN and JOIN_ACK. The hub refuses a JOIN with
// any other version (ErrBadVersion), so incompatible builds fail fast.
const Version = 1

const (
	// HeaderSize is the hub-frame header: type(1) + flags(1) + from(2) + to(2).
	HeaderSize = 6

	// MaxFrame is the largest encoded hub frame, header included.
	MaxFrame = 64 * 1024

	// ChunkSize is the most file bytes one DATA frame carries. With the record
	// header, AES-GCM overhead and hub header it stays well under MaxFrame.
	ChunkSize = 32 * 1024
)

// SenderID is the member ID the hub always gives the sender. Receivers get
// 2, 3, 4, ... in join order, and address every DIRECT frame To: SenderID.
const SenderID uint16 = 1

// Role is what a client declares in its JOIN.
type Role uint8

const (
	RoleSender   Role = 1
	RoleReceiver Role = 2
)

// Frame types (the first header byte). The values are the protocol: they
// are written out explicitly so that adding or reordering a line can never
// renumber an existing type.
const (
	TypeJoin         uint8 = 1 // client → hub: version u8 | role u8 | maxReceivers u8 (sender only)
	TypeJoinAck      uint8 = 2 // hub → client: version u8 | memberID u16 | maxReceivers u8
	TypeMemberJoined uint8 = 3 // hub → sender: memberID u16 | addrLen u8 | addr
	TypeMemberLeft   uint8 = 4 // hub → sender: memberID u16 | reason u8 (LeftQuit/LeftSlow/LeftNetwork)
	TypeDirect       uint8 = 5 // receiver ↔ sender: private control message (JSON or ciphertext)
	TypeData         uint8 = 6 // sender → hub → every receiver: one encrypted record
	TypePing         uint8 = 7 // both ways: keep-alive; a client answers every PING with a PING
	TypeError        uint8 = 8 // hub → client: code u8 | UTF-8 message (see HubError)
	TypeKick         uint8 = 9 // sender → hub: memberID u16 | reason u8; hub closes that receiver
)

// Error codes, the first payload byte of a TypeError frame.
const (
	ErrRoomLocked uint8 = 1 // the transfer already started; no late joiners
	ErrRoomFull   uint8 = 2 // the room already has the sender's requested receiver count
	ErrNoSender   uint8 = 3 // no sender in this room (yet); receivers retry this one
	ErrKickedSlow uint8 = 4 // reserved: a stalled receiver is closed without a frame
	ErrBadFrame   uint8 = 5 // malformed or not-allowed frame
	ErrBadVersion uint8 = 6 // JOIN version is not Version
	ErrSenderGone uint8 = 7 // the sender disconnected; the session is over
)

// MEMBER_LEFT reasons, the byte after memberID in a TypeMemberLeft frame.
const (
	LeftQuit    uint8 = 0 // the receiver disconnected on its own
	LeftSlow    uint8 = 1 // the hub dropped it for not reading (stall timeout)
	LeftNetwork uint8 = 2 // network error
)

// Frame is one decoded hub frame.
type Frame struct {
	Type  uint8
	Flags uint8 // unused, always 0
	// From and To are member IDs. The hub overwrites From/To on every frame it
	// forwards, so a client cannot impersonate another member.
	From    uint16
	To      uint16
	Payload []byte
}

// Encode serialises f. It fails only if the result would exceed MaxFrame.
func Encode(f Frame) ([]byte, error) {
	if len(f.Payload) > MaxFrame-HeaderSize {
		return nil, errors.New("multi frame too large")
	}
	b := make([]byte, HeaderSize+len(f.Payload))
	b[0] = f.Type
	b[1] = f.Flags
	binary.LittleEndian.PutUint16(b[2:4], f.From)
	binary.LittleEndian.PutUint16(b[4:6], f.To)
	copy(b[HeaderSize:], f.Payload)
	return b, nil
}

// Decode parses one hub frame. Payload aliases b (no copy).
func Decode(b []byte) (Frame, error) {
	if len(b) < HeaderSize || len(b) > MaxFrame {
		return Frame{}, errors.New("invalid multi frame size")
	}
	return Frame{
		Type:    b[0],
		Flags:   b[1],
		From:    binary.LittleEndian.Uint16(b[2:4]),
		To:      binary.LittleEndian.Uint16(b[4:6]),
		Payload: b[HeaderSize:],
	}, nil
}

// Package multiwire defines the multi-send hub frame format.
package multiwire

import (
	"encoding/binary"
	"errors"
)

const (
	Version   = 1
	MaxFrame  = 64 * 1024
	ChunkSize = 32 * 1024
)

type Role uint8

const (
	RoleSender   Role = 1
	RoleReceiver Role = 2
)
const (
	TypeJoin uint8 = 1 + iota
	TypeJoinAck
	TypeMemberJoined
	TypeMemberLeft
	TypeDirect
	TypeData
	TypePing
	TypeError
	TypeKick
)
const (
	ErrRoomLocked uint8 = 1 + iota
	ErrRoomFull
	ErrNoSender
	ErrKickedSlow
	ErrBadFrame
	ErrBadVersion
	ErrSenderGone
)

type Frame struct {
	Type    uint8
	Flags   uint8
	From    uint16
	To      uint16
	Payload []byte
}

func Encode(f Frame) ([]byte, error) {
	if len(f.Payload) > MaxFrame-6 {
		return nil, errors.New("multi frame too large")
	}
	b := make([]byte, 6+len(f.Payload))
	b[0] = f.Type
	b[1] = f.Flags
	binary.LittleEndian.PutUint16(b[2:4], f.From)
	binary.LittleEndian.PutUint16(b[4:6], f.To)
	copy(b[6:], f.Payload)
	return b, nil
}
func Decode(b []byte) (Frame, error) {
	if len(b) < 6 || len(b) > MaxFrame {
		return Frame{}, errors.New("invalid multi frame size")
	}
	return Frame{Type: b[0], Flags: b[1], From: binary.LittleEndian.Uint16(b[2:4]), To: binary.LittleEndian.Uint16(b[4:6]), Payload: b[6:]}, nil
}

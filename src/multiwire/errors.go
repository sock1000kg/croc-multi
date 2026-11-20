package multiwire

import "fmt"

// errorNames are the short names docs/contracts.md §2.2 uses for each code.
var errorNames = map[uint8]string{
	ErrRoomLocked: "room-locked",
	ErrRoomFull:   "room-full",
	ErrNoSender:   "no-sender",
	ErrKickedSlow: "kicked-slow",
	ErrBadFrame:   "bad-frame",
	ErrBadVersion: "bad-version",
	ErrSenderGone: "sender-gone",
}

// ErrorName returns the contract name of an error code, e.g. "room-full".
func ErrorName(code uint8) string {
	if name, ok := errorNames[code]; ok {
		return name
	}
	return fmt.Sprintf("error-%d", code)
}

// HubError is a TypeError frame turned into a Go error. Callers check the
// code with errors.As:
//
//	var he *multiwire.HubError
//	if errors.As(err, &he) && he.Code == multiwire.ErrNoSender { ... }
type HubError struct {
	Code uint8
	Msg  string // free text from the hub; may be empty
}

// ErrorFrame builds the payload of a TypeError frame: code u8 | message.
func ErrorFrame(code uint8, msg string) Frame {
	return Frame{Type: TypeError, Payload: append([]byte{code}, msg...)}
}

// ParseError reads a TypeError payload. An empty payload becomes ErrBadFrame.
func ParseError(payload []byte) *HubError {
	if len(payload) == 0 {
		return &HubError{Code: ErrBadFrame, Msg: "empty ERROR frame"}
	}
	return &HubError{Code: payload[0], Msg: string(payload[1:])}
}

// Error prints "hub: room-full", or "hub: sender-gone: sender disconnected"
// when the hub's message adds something beyond the code name.
func (e *HubError) Error() string {
	name := ErrorName(e.Code)
	if e.Msg == "" || e.Msg == name {
		return "hub: " + name
	}
	return "hub: " + name + ": " + e.Msg
}

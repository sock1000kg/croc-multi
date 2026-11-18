package multisession

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/schollz/croc/v11/src/codephrase"
	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
	"github.com/schollz/croc/v11/src/pakekey"
)

type FileEntry struct {
	Idx  int    `json:"idx"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type Session struct {
	DataKey   []byte      `json:"dataKey"`
	ChunkSize int         `json:"chunkSize"`
	Files     []FileEntry `json:"files"`
	EmptyDirs []string    `json:"emptyDirs"`
	TotalSize int64       `json:"totalSize"`
}
type ReceiverInfo struct {
	ID   uint16
	Addr string
	Name string
}
type message struct {
	Type    string `json:"t"`
	Version int    `json:"version,omitempty"`
	Curve   string `json:"curve,omitempty"`
	Bytes   []byte `json:"bytes,omitempty"`
	Bytes2  []byte `json:"bytes2,omitempty"`
	Tag     []byte `json:"tag,omitempty"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
type sessionMessage struct {
	Type    string `json:"t"`
	Confirm []byte `json:"confirm"`
	Session
}

func sendMessage(l *Link, id uint16, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return l.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: id, Payload: b})
}
func recvMessage(ctx context.Context, l *Link, id uint16) (message, error) {
	f, e := l.RecvPeer(ctx, id)
	if e != nil {
		return message{}, e
	}
	var m message
	if e = json.Unmarshal(f.Payload, &m); e != nil {
		return m, e
	}
	if m.Type == "reject" {
		return m, fmt.Errorf("session rejected: %s", m.Reason)
	}
	return m, nil
}
func roomKey(code string, id uint16) (string, string, error) {
	p, e := codephrase.Parse(code)
	if e != nil {
		return "", "", e
	}
	return p.PAKEPassphrase, fmt.Sprintf("multi:%s/%d", p.RoomName, id), nil
}
func AuthorizeReceiver(ctx context.Context, l *Link, code string, id uint16, session Session, approve func(ReceiverInfo) bool) error {
	pass, room, e := roomKey(code, id)
	if e != nil {
		return e
	}
	first, e := recvMessage(ctx, l, id)
	if e != nil {
		return e
	}
	if first.Type != "pake1" || first.Curve != "p256" || first.Version != pakekey.ProtocolVersion {
		return errors.New("invalid pake1")
	}
	p, e := pakekey.Init([]byte(pass), 1, "p256", pakekey.PurposeTransfer, room)
	if e != nil {
		return e
	}
	if e = p.Update(first.Bytes); e != nil {
		return e
	}
	shared, e := p.SessionKey()
	if e != nil {
		return e
	}
	salt := make([]byte, pakekey.SaltSize)
	if _, e = rand.Read(salt); e != nil {
		return e
	}
	response := p.Bytes()
	keys, e := pakekey.Derive(shared, pakekey.Context{Purpose: pakekey.PurposeTransfer, Room: room, Curve: "p256", Initiator: first.Bytes, Responder: response, Salt: salt})
	if e != nil {
		return e
	}
	if e = sendMessage(l, id, message{Type: "pake2", Bytes: response, Bytes2: salt}); e != nil {
		return e
	}
	confirmation, e := recvMessage(ctx, l, id)
	if e != nil {
		return e
	}
	if confirmation.Type != "pake-confirm" || !pakekey.Confirm(keys.ConfirmationA, confirmation.Tag) {
		_ = sendMessage(l, id, message{Type: "reject", Reason: "auth"})
		return errors.New("receiver authentication failed")
	}
	if approve != nil && !approve(ReceiverInfo{ID: id, Name: first.Name}) {
		_ = sendMessage(l, id, message{Type: "reject", Reason: "refused"})
		return errors.New("receiver refused")
	}
	plain, e := json.Marshal(sessionMessage{Type: "session", Confirm: keys.ConfirmationB, Session: session})
	if e != nil {
		return e
	}
	encrypted, e := crypt.Encrypt(plain, keys.EncryptionKey)
	if e != nil {
		return e
	}
	return l.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: id, Payload: encrypted})
}
func JoinSession(ctx context.Context, l *Link, code string) (Session, error) {
	var session Session
	pass, room, e := roomKey(code, l.id)
	if e != nil {
		return session, e
	}
	p, e := pakekey.Init([]byte(pass), 0, "p256", pakekey.PurposeTransfer, room)
	if e != nil {
		return session, e
	}
	initial := p.Bytes()
	name, _ := os.Hostname()
	if e = sendMessage(l, 1, message{Type: "pake1", Version: pakekey.ProtocolVersion, Curve: "p256", Bytes: initial, Name: name}); e != nil {
		return session, e
	}
	response, e := recvMessage(ctx, l, 1)
	if e != nil {
		return session, e
	}
	if response.Type != "pake2" {
		return session, errors.New("invalid pake2")
	}
	if e = p.Update(response.Bytes); e != nil {
		return session, e
	}
	shared, e := p.SessionKey()
	if e != nil {
		return session, e
	}
	keys, e := pakekey.Derive(shared, pakekey.Context{Purpose: pakekey.PurposeTransfer, Room: room, Curve: "p256", Initiator: initial, Responder: response.Bytes, Salt: response.Bytes2})
	if e != nil {
		return session, e
	}
	if e = sendMessage(l, 1, message{Type: "pake-confirm", Tag: keys.ConfirmationA}); e != nil {
		return session, e
	}
	frame, e := l.RecvPeer(ctx, 1)
	if e != nil {
		return session, e
	}
	var reject message
	if json.Unmarshal(frame.Payload, &reject) == nil && reject.Type == "reject" {
		return session, fmt.Errorf("session rejected: %s", reject.Reason)
	}
	plain, e := crypt.Decrypt(frame.Payload, keys.EncryptionKey)
	if e != nil {
		return session, errors.New("session authentication failed")
	}
	var v sessionMessage
	if e = json.Unmarshal(plain, &v); e != nil {
		return session, e
	}
	if v.Type != "session" || !pakekey.Confirm(keys.ConfirmationB, v.Confirm) {
		return session, errors.New("sender authentication failed")
	}
	return v.Session, nil
}

// Package multisession is the key exchange of croc's multi-send mode
// (contracts.md §3). Both engines use it: multisend calls
// AuthorizeReceiver once per receiver, multirecv calls JoinSession.
//
// The problem it solves: all receivers must decrypt the SAME DATA stream, so
// the sender picks one random dataKey per session. Each receiver proves it
// knows the code with its own PAKE run against the sender, and only then gets
// the dataKey, encrypted with the key that PAKE produced. The hub forwards
// these DIRECT messages but cannot read them.
//
//	receiver (PAKE side A)                       sender (PAKE side B)
//	  pake1 {pake bytes, hostname}     ──►
//	                                   ◄──  pake2 {pake bytes, salt}
//	  pake-confirm {tag A}             ──►      checks tag A (wrong code stops here)
//	                                   ◄──  AES-GCM(session {tag B, manifest, dataKey})
//	  checks tag B (proves the sender knew the code too)
//
// Each PAKE is bound to "multi:<RoomName>/<memberID>", so a transcript for
// one receiver cannot be replayed to another. link.go has the transport.
package multisession

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/schollz/croc/v11/src/codephrase"
	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
	"github.com/schollz/croc/v11/src/pakekey"
	"golang.org/x/crypto/hkdf"
)

// FileEntry is one file in the manifest. Idx is the fileIdx used in DATA
// records; Path is relative (the receiver validates it before writing).
type FileEntry struct {
	Idx  int    `json:"idx"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Session is what a receiver learns after authenticating: the manifest and
// the key that decrypts every DATA record. It never crosses the hub in clear.
type Session struct {
	DataKey   []byte      `json:"dataKey"`
	ChunkSize int         `json:"chunkSize"`
	Files     []FileEntry `json:"files"`
	EmptyDirs []string    `json:"emptyDirs"`
	TotalSize int64       `json:"totalSize"`
	// DoneToken is this receiver's own proof of completion: it echoes it in
	// "done", and the sender accepts "done" only with the right token. The
	// hub never sees it (the session is encrypted with the pair key), so it
	// cannot fake a "done" (docs/review-mat-ma.md, F2). Set per receiver by
	// AuthorizeReceiver; see DoneToken.
	DoneToken []byte `json:"doneToken"`
}

// doneTokenInfo separates the done-token key from the dataKey's use as the
// AES-GCM key for DATA.
const doneTokenInfo = "croc-multi done-token v1"

// DoneToken is receiver id's done token for a session with dataKey:
// HMAC-SHA256(HKDF-SHA256(dataKey, info=doneTokenInfo), id as u16 LE).
// The sender can recompute it at any time, so it keeps no per-receiver state.
func DoneToken(dataKey []byte, id uint16) []byte {
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, dataKey, nil, []byte(doneTokenInfo)), key); err != nil {
		panic(err) // cannot happen: 32 bytes is far below HKDF's limit
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(binary.LittleEndian.AppendUint16(nil, id))
	return mac.Sum(nil)
}

// CheckDoneToken reports, in constant time, whether token is receiver id's
// done token.
func CheckDoneToken(dataKey []byte, id uint16, token []byte) bool {
	return hmac.Equal(DoneToken(dataKey, id), token)
}

// ErrAuthFailed means a receiver's key confirmation did not match: it used a
// wrong code. Each one is an online guess at the code, so the sender counts
// them (multisend caps them per session); other lobby failures are not guesses.
var ErrAuthFailed = errors.New("receiver authentication failed")

// ErrRefused means the sender's approve callback said no.
var ErrRefused = errors.New("receiver refused")

// ReceiverInfo is shown to the sender's approve callback (Phase 2 approval).
type ReceiverInfo struct {
	ID   uint16
	Addr string
	Name string
}

// message is the JSON body of the clear-text DIRECT messages: pake1, pake2,
// pake-confirm and reject. Only the fields for its Type are set.
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

// sessionMessage is the final sender → receiver message. It is sent
// encrypted with the PAKE key, so it may carry the dataKey.
type sessionMessage struct {
	Type    string `json:"t"`
	Confirm []byte `json:"confirm"`
	Session
}

// sendMessage sends v as JSON in a DIRECT frame to member id.
func sendMessage(l *Link, id uint16, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return l.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: id, Payload: b})
}

// Reject tells receiver id that the sender will not run PAKE with it, e.g.
// "busy" when the sender is limiting wrong-code attempts. The receiver's
// JoinSession reads it as the answer to its pake1 and fails with
// "session rejected: <reason>".
func Reject(l *Link, id uint16, reason string) error {
	return sendMessage(l, id, message{Type: "reject", Reason: reason})
}

// recvMessage reads the next DIRECT from member id as a message, turning a
// "reject" into an error.
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

// roomKey splits code into the PAKE password and this member's PAKE room,
// "multi:<RoomName>/<memberID>".
func roomKey(code string, id uint16) (string, string, error) {
	p, e := codephrase.Parse(code)
	if e != nil {
		return "", "", e
	}
	return p.PAKEPassphrase, fmt.Sprintf("multi:%s/%d", p.RoomName, id), nil
}

// AuthorizeReceiver is the sender's side of the exchange with receiver id
// (call it in its own goroutine per receiver). It returns nil once the
// encrypted session is sent; the caller then waits for that receiver's "ready".
// approve, if non-nil, can refuse an authenticated receiver.
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
	// Role 1 = PAKE side B (responder). The receiver is side A.
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
	// Both sides derive the same keys from shared + this context. The salt is
	// fresh per run, so keys differ even if the same receiver joins twice.
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
	// A receiver with the wrong code derives different keys, so its tag fails.
	if confirmation.Type != "pake-confirm" || !pakekey.Confirm(keys.ConfirmationA, confirmation.Tag) {
		_ = sendMessage(l, id, message{Type: "reject", Reason: "auth"})
		return ErrAuthFailed
	}
	if approve != nil && !approve(ReceiverInfo{ID: id, Name: sanitizeName(first.Name)}) {
		_ = sendMessage(l, id, message{Type: "reject", Reason: "refused"})
		return ErrRefused
	}
	// session is our own copy, so the per-receiver token stays with this
	// receiver's message.
	session.DoneToken = DoneToken(session.DataKey, id)
	// Tag B lets the receiver check that we know the code too.
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

// JoinSession is the receiver's side: run PAKE with the sender over l and
// return the decrypted Session. A wrong code fails here, before any DATA.
// name is shown to the sender (unauthenticated); "" means the hostname.
func JoinSession(ctx context.Context, l *Link, code, name string) (Session, error) {
	var session Session
	pass, room, e := roomKey(code, l.id)
	if e != nil {
		return session, e
	}
	// Role 0 = PAKE side A (initiator).
	p, e := pakekey.Init([]byte(pass), 0, "p256", pakekey.PurposeTransfer, room)
	if e != nil {
		return session, e
	}
	initial := p.Bytes()
	if name == "" {
		name, _ = os.Hostname()
	}
	if e = sendMessage(l, multiwire.SenderID, message{Type: "pake1", Version: pakekey.ProtocolVersion, Curve: "p256", Bytes: initial, Name: name}); e != nil {
		return session, e
	}
	response, e := recvMessage(ctx, l, multiwire.SenderID)
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
	if e = sendMessage(l, multiwire.SenderID, message{Type: "pake-confirm", Tag: keys.ConfirmationA}); e != nil {
		return session, e
	}
	frame, e := l.RecvPeer(ctx, multiwire.SenderID)
	if e != nil {
		return session, e
	}
	// The reply is either a clear-text JSON reject or the encrypted session.
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

// sanitizeName makes a receiver's self-chosen name safe to print. The name is
// untrusted: it arrives in pake1, and the sender reads it in the terminal when
// deciding whether to approve that receiver.
func sanitizeName(s string) string {
	// Drop everything unprintable, so a name cannot control the terminal or
	// disguise the prompt
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)

	// Cap the length so a long name cannot flood the screen or push the rest
	// of the prompt out of sight. Count runes, not bytes, so a multi-byte
	// character is never cut in half.
	if r := []rune(s); len(r) > 32 {
		s = string(r[:32])
	}
	// Never return nothing: the prompt should still show there is a name here.
	if s == "" {
		return "?"
	}
	return s
}

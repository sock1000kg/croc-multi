package multisession_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
	"testing"
	"time"
)

func TestSessionAuthentication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub := multihub.New(multihub.Config{MaxReceivers: 3, QueueBytes: 8 << 20, StallTimeout: time.Second})
	connect := func(role multiwire.Role) *multisession.Link {
		t.Helper()
		c, s := multihub.Pipe()
		go hub.Serve("multi:test", s)
		l, e := multisession.NewLink(ctx, c, role, 3)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}
	sender := connect(multiwire.RoleSender)
	session := multisession.Session{DataKey: bytes.Repeat([]byte{7}, 32), ChunkSize: multiwire.ChunkSize}
	for _, test := range []struct {
		code string
		good bool
	}{{"room-correct-words", true}, {"room-wrong-words", false}, {"room-correct-words", true}} {
		r := connect(multiwire.RoleReceiver)
		joined, e := sender.RecvContext(ctx)
		if e != nil || joined.Type != multiwire.TypeMemberJoined {
			t.Fatalf("join: %v %v", joined, e)
		}
		id := binary.LittleEndian.Uint16(joined.Payload)
		sent := make(chan error, 1)
		var seen string
		approve := func(info multisession.ReceiverInfo) bool { seen = info.Name; return true }
		go func() {
			sent <- multisession.AuthorizeReceiver(ctx, sender, "room-correct-words", id, session, approve)
		}()
		got, e := multisession.JoinSession(ctx, r, test.code, "an-pc")
		sendErr := <-sent
		if test.good {
			if e != nil || sendErr != nil || !bytes.Equal(got.DataKey, session.DataKey) {
				t.Fatalf("good: %v %v", e, sendErr)
			}
			if seen != "an-pc" {
				t.Fatalf("sender saw name %q", seen)
			}
		} else if e == nil || sendErr == nil {
			t.Fatalf("wrong code accepted: %v %v", e, sendErr)
		}
		r.Close()
		_, e = sender.RecvContext(ctx)
		if e != nil {
			t.Fatal(e)
		}
	}
}

// The done token is per receiver and per session, so a "done" cannot be
// copied from one receiver to another or from another session.
func TestDoneToken(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	a, b := multisession.DoneToken(key, 2), multisession.DoneToken(key, 3)
	if len(a) != 32 || bytes.Equal(a, b) {
		t.Fatalf("tokens for two receivers: %x, %x", a, b)
	}
	if !multisession.CheckDoneToken(key, 2, a) || multisession.CheckDoneToken(key, 3, a) || multisession.CheckDoneToken(key, 2, nil) {
		t.Fatal("CheckDoneToken accepted a wrong token or refused the right one")
	}
	if multisession.CheckDoneToken(bytes.Repeat([]byte{2}, 32), 2, a) {
		t.Fatal("a token from another session was accepted")
	}
}

package multisend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multirecv"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

type receiverStream struct{ link *multisession.Link }

func (s receiverStream) ReceiveData(ctx context.Context) ([]byte, error) {
	f, e := s.link.RecvContext(ctx)
	if e != nil {
		return nil, e
	}
	if f.Type != multiwire.TypeData {
		return nil, fmt.Errorf("unexpected frame %d", f.Type)
	}
	return f.Payload, nil
}
func (s receiverStream) SendControl(ctx context.Context, b []byte) error {
	return s.link.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: 1, Payload: b})
}
func TestFullM1InMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub := multihub.New(multihub.Config{MaxReceivers: 2, QueueBytes: 8 << 20, StallTimeout: time.Second})
	connect := func(role multiwire.Role) *multisession.Link {
		t.Helper()
		c, s := multihub.Pipe()
		go hub.Serve("multi:test", s)
		l, e := multisession.NewLink(ctx, c, role, 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}
	sender := connect(multiwire.RoleSender)
	data := bytes.Repeat([]byte("M1 authenticated transfer\n"), 5000)
	source := filepath.Join(t.TempDir(), "source")
	if e := os.WriteFile(source, data, 0600); e != nil {
		t.Fatal(e)
	}
	key := bytes.Repeat([]byte{71}, 32)
	session := multisession.Session{DataKey: key, ChunkSize: multiwire.ChunkSize, Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, "room-correct-words", session, SendOptions{Files: []string{source}, Receivers: 2})
	}()
	results := make(chan error, 2)
	var outputs []string
	for range 2 {
		l := connect(multiwire.RoleReceiver)
		out := t.TempDir()
		outputs = append(outputs, filepath.Join(out, "file"))
		go func() {
			s, e := multisession.JoinSession(ctx, l, "room-correct-words")
			if e != nil {
				results <- e
				return
			}
			files := []multirecv.FileEntry{{Idx: s.Files[0].Idx, Path: s.Files[0].Path, Size: s.Files[0].Size}}
			e = multirecv.ReceiveStream(ctx, receiverStream{l}, out, multirecv.Session{DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: files, TotalSize: s.TotalSize}, nil)
			results <- e
		}()
	}
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	if e := <-sent; e != nil {
		t.Fatal(e)
	}
	for _, p := range outputs {
		got, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(got, data) {
			t.Fatalf("file mismatch: %v", e)
		}
	}
}

// --timeout only limits the lobby: with nobody joining, sendSession gives up
// after opts.Timeout with a "waiting for receivers" error, not a context error.
func TestLobbyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub := multihub.New(multihub.Config{})
	c, s := multihub.Pipe()
	go hub.Serve("multi:lobby-timeout", s)
	sender, e := multisession.NewLink(ctx, c, multiwire.RoleSender, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer sender.Close()

	session := multisession.Session{DataKey: bytes.Repeat([]byte{1}, 32), ChunkSize: multiwire.ChunkSize}
	start := time.Now()
	e = sendSession(ctx, sender, "room-correct-words", session, SendOptions{Receivers: 1, Timeout: 200 * time.Millisecond})
	if e == nil || errors.Is(e, context.DeadlineExceeded) || !strings.Contains(e.Error(), "waiting for receivers") {
		t.Fatalf("got %v, want a lobby timeout error", e)
	}
	if !strings.Contains(e.Error(), "0/1") {
		t.Fatalf("error %q should say how many receivers were ready (0/1)", e)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("lobby timeout took %v, want ~200ms", d)
	}
}

// slowStream delays the first DATA read, so the transfer runs well past
// opts.Timeout. The old code (timeout around the whole session) killed it.
type slowStream struct {
	receiverStream
	delay time.Duration
	once  *sync.Once
}

func (s slowStream) ReceiveData(ctx context.Context) ([]byte, error) {
	s.once.Do(func() { time.Sleep(s.delay) })
	return s.receiverStream.ReceiveData(ctx)
}

func TestTimeoutDoesNotCutTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hub := multihub.New(multihub.Config{MaxReceivers: 1})
	connect := func(role multiwire.Role) *multisession.Link {
		t.Helper()
		c, s := multihub.Pipe()
		go hub.Serve("multi:timeout-transfer", s)
		l, e := multisession.NewLink(ctx, c, role, 1)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}
	sender := connect(multiwire.RoleSender)
	data := bytes.Repeat([]byte("timeout covers the lobby only\n"), 10000)
	source := filepath.Join(t.TempDir(), "source")
	if e := os.WriteFile(source, data, 0600); e != nil {
		t.Fatal(e)
	}
	session := multisession.Session{DataKey: bytes.Repeat([]byte{7}, 32), ChunkSize: multiwire.ChunkSize, Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}

	const timeout = time.Second
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, "room-correct-words", session, SendOptions{Files: []string{source}, Receivers: 1, Timeout: timeout})
	}()

	l := connect(multiwire.RoleReceiver)
	out := t.TempDir()
	s, e := multisession.JoinSession(ctx, l, "room-correct-words")
	if e != nil {
		t.Fatal(e)
	}
	files := []multirecv.FileEntry{{Idx: s.Files[0].Idx, Path: s.Files[0].Path, Size: s.Files[0].Size}}
	stream := slowStream{receiverStream{l}, 2 * timeout, &sync.Once{}}
	if e := multirecv.ReceiveStream(ctx, stream, out, multirecv.Session{DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: files, TotalSize: s.TotalSize}, nil); e != nil {
		t.Fatal(e)
	}
	if e := <-sent; e != nil {
		t.Fatalf("sender failed after the lobby: %v", e)
	}
	if got, e := os.ReadFile(filepath.Join(out, "file")); e != nil || !bytes.Equal(got, data) {
		t.Fatalf("file mismatch: %v", e)
	}
}

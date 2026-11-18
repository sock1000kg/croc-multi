package multisend

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	session := multisession.Session{DataKey: key, ChunkSize: 32768, Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
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

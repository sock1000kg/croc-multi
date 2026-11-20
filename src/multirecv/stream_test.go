package multirecv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
)

type testStream struct {
	data     [][]byte
	controls []controlMessage
}

func (s *testStream) ReceiveData(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(s.data) == 0 {
		return nil, io.EOF
	}
	data := s.data[0]
	s.data = s.data[1:]
	return data, nil
}
func (s *testStream) SendControl(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var message controlMessage
	if err := json.Unmarshal(b, &message); err != nil {
		return err
	}
	s.controls = append(s.controls, message)
	return nil
}
func TestReceiveStreamControls(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	data := []byte("payload")
	sum := sha256.Sum256(data)
	chunk := append(make([]byte, 11), data...)
	chunk[0] = 1
	end := append([]byte{2, 0, 0}, sum[:]...)
	encrypt := func(b []byte) []byte {
		v, err := crypt.Encrypt(b, key)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	session := Session{DataKey: key, ChunkSize: multiwire.ChunkSize, Files: []FileEntry{{Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
	t.Run("ready progress done", func(t *testing.T) {
		dir := t.TempDir()
		s := &testStream{data: [][]byte{encrypt(chunk), encrypt(end), encrypt([]byte{3})}}
		if err := ReceiveStream(context.Background(), s, dir, session, nil); err != nil {
			t.Fatal(err)
		}
		if len(s.controls) != 3 || s.controls[0].Type != "ready" || s.controls[1].Type != "progress" || s.controls[2].Type != "done" || s.controls[2].OK == nil || !*s.controls[2].OK || *s.controls[2].Bytes != int64(len(data)) {
			t.Fatalf("controls: %+v", s.controls)
		}
		got, err := os.ReadFile(filepath.Join(dir, "file"))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q: %v", got, err)
		}
	})
	t.Run("hash failure", func(t *testing.T) {
		dir := t.TempDir()
		wrong := append([]byte(nil), end...)
		wrong[3] ^= 1
		s := &testStream{data: [][]byte{encrypt(chunk), encrypt(wrong)}}
		err := ReceiveStream(context.Background(), s, dir, session, nil)
		if !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("got %v", err)
		}
		last := s.controls[len(s.controls)-1]
		if last.Type != "fail" || last.Reason != "hash-mismatch" {
			t.Fatalf("control %+v", last)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries %v: %v", entries, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := &testStream{}
		if err := ReceiveStream(ctx, s, t.TempDir(), session, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

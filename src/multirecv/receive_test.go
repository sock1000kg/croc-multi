package multirecv

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schollz/croc/v11/src/crypt"
)

func TestReceiveRecords(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	data := []byte("multi-send payload")
	newSession := func(name string) Session {
		return Session{DataKey: key, ChunkSize: 32768, Files: []FileEntry{{Idx: 0, Path: name, Size: int64(len(data))}}, TotalSize: int64(len(data))}
	}
	encrypt := func(b []byte) []byte {
		t.Helper()
		result, err := crypt.Encrypt(b, key)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	chunk := make([]byte, 11+len(data))
	chunk[0] = 1
	copy(chunk[11:], data)
	hash := sha256.Sum256(data)
	end := append([]byte{2, 0, 0}, hash[:]...)
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		var progress int64
		r, err := New(dir, newSession("folder/file"), func(n, total int64) { progress = n })
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for _, record := range [][]byte{chunk, end, {3}} {
			done, err := r.Accept(encrypt(record))
			if err != nil {
				t.Fatal(err)
			}
			if done != (record[0] == 3) {
				t.Fatal("unexpected completion")
			}
		}
		got, err := os.ReadFile(filepath.Join(dir, "folder", "file"))
		if err != nil || !bytes.Equal(got, data) || progress != int64(len(data)) {
			t.Fatalf("data %q progress %d err %v", got, progress, err)
		}
	})
	t.Run("hash mismatch removes staged file", func(t *testing.T) {
		dir := t.TempDir()
		r, err := New(dir, newSession("file"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		altered := append([]byte(nil), chunk...)
		altered[11] ^= 1
		if _, err = r.Accept(encrypt(altered)); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Accept(encrypt(end)); err == nil || !strings.Contains(err.Error(), "hash-mismatch") {
			t.Fatalf("got %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries %v error %v", entries, err)
		}
	})
	t.Run("unsafe paths", func(t *testing.T) {
		for _, name := range []string{"../x", "/absolute", "C:/escape", "CON"} {
			if r, err := New(t.TempDir(), newSession(name), nil); err == nil {
				r.Close()
				t.Fatalf("accepted %q", name)
			}
		}
	})
	t.Run("tampered ciphertext", func(t *testing.T) {
		dir := t.TempDir()
		r, err := New(dir, newSession("file"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		record := encrypt(chunk)
		record[len(record)-1] ^= 1
		if _, err = r.Accept(record); err == nil {
			t.Fatal("accepted tampered ciphertext")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("created file")
		}
	})
	t.Run("gap", func(t *testing.T) {
		r, err := New(t.TempDir(), newSession("file"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		bad := append([]byte(nil), chunk...)
		binary.LittleEndian.PutUint64(bad[3:11], 1)
		if _, err = r.Accept(encrypt(bad)); err == nil {
			t.Fatal("accepted gap")
		}
	})
	t.Run("replayed chunk", func(t *testing.T) {
		r, err := New(t.TempDir(), newSession("file"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for _, record := range [][]byte{chunk, chunk, end, {3}} {
			if _, err := r.Accept(encrypt(record)); err != nil {
				t.Fatal(err)
			}
		}
	})
}

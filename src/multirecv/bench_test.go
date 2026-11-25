package multirecv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multiwire"
)

func TestReceiveStreamStats(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	data := []byte("payload")
	sum := sha256.Sum256(data)
	chunk := append(make([]byte, 11), data...)
	chunk[0] = 1
	enc := func(b []byte) []byte {
		v, err := crypt.Encrypt(b, key)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	session := Session{DataKey: key, ChunkSize: multiwire.ChunkSize, Files: []FileEntry{{Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
	s := &testStream{data: [][]byte{enc(chunk), enc(append([]byte{2, 0, 0}, sum[:]...)), enc([]byte{3})}}
	stats, err := receiveStream(context.Background(), s, t.TempDir(), session, nil)
	if err != nil || stats.Bytes != int64(len(data)) || stats.Elapsed <= 0 {
		t.Fatalf("stats %+v: %v", stats, err)
	}

	// No DATA at all: nothing timed.
	stats, err = receiveStream(context.Background(), &testStream{}, t.TempDir(), session, nil)
	if err == nil || stats.Bytes != 0 || stats.Elapsed != 0 {
		t.Fatalf("empty stream: %+v %v", stats, err)
	}
}

func TestBenchLine(t *testing.T) {
	got := benchLine("an pc\x1b[31m", Stats{Bytes: 1485169722, Elapsed: 61900 * time.Millisecond}, "done")
	want := "bench: role=recv name=an_pc_[31m bytes=1485169722 seconds=61.90 mbps=191.9 result=done"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := benchLine("", Stats{}, "failed"); got != "bench: role=recv name=? bytes=0 seconds=0.00 mbps=0.0 result=failed" {
		t.Fatal(got)
	}
	if got := formatBytes(1485169722); got != "1.38 GiB" {
		t.Fatal(got)
	}
}

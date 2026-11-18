package multirecv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multirecv"
	"github.com/schollz/croc/v11/src/multisend"
)

type hubStream struct{ conn multihub.Conn }

func (s hubStream) ReceiveData(ctx context.Context) ([]byte, error) {
	for {
		frame, err := s.conn.Receive()
		if err != nil {
			return nil, err
		}
		if len(frame) < 6 {
			return nil, fmt.Errorf("short frame")
		}
		if frame[0] == 6 {
			return frame[6:], nil
		}
		if frame[0] == 7 {
			if err = s.conn.Send([]byte{7, 0, 0, 0, 0, 0}); err != nil {
				return nil, err
			}
			continue
		}
		return nil, fmt.Errorf("unexpected frame %d", frame[0])
	}
}
func (s hubStream) SendControl(ctx context.Context, b []byte) error {
	return s.conn.Send(append([]byte{5, 0, 0, 0, 0, 0}, b...))
}

// Exercises the real sender and hub. Authentication awaits multisession.
func TestSenderHubTwoReceivers(t *testing.T) {
	hub := multihub.New(multihub.Config{MaxReceivers: 2, QueueBytes: 8 << 20, StallTimeout: time.Second})
	connect := func(role byte) multihub.Conn {
		t.Helper()
		client, server := multihub.Pipe()
		go hub.Serve("multi:test", server)
		t.Cleanup(func() { client.Close() })
		if err := client.Send([]byte{1, 0, 0, 0, 0, 0, 1, role, 2}); err != nil {
			t.Fatal(err)
		}
		ack, err := client.Receive()
		if err != nil || len(ack) < 10 || ack[0] != 2 {
			t.Fatalf("join: %x %v", ack, err)
		}
		return client
	}
	sender := connect(1)
	conns := []multihub.Conn{connect(2), connect(2)}
	for range conns {
		frame, err := sender.Receive()
		if err != nil || len(frame) < 6 || frame[0] != 3 {
			t.Fatalf("member: %x %v", frame, err)
		}
	}
	data := bytes.Repeat([]byte("sender through hub\n"), 6000)
	key := bytes.Repeat([]byte{42}, 32)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	controls := make(chan string, 16)
	senderErrors := make(chan error, 1)
	go func() {
		for {
			frame, err := sender.Receive()
			if err != nil {
				senderErrors <- err
				return
			}
			if len(frame) < 6 || frame[0] != 5 {
				senderErrors <- fmt.Errorf("unexpected frame %x", frame)
				return
			}
			var message struct {
				Type string `json:"t"`
			}
			if err = json.Unmarshal(frame[6:], &message); err != nil {
				senderErrors <- err
				return
			}
			controls <- message.Type
		}
	}()
	results := make(chan error, 2)
	var paths []string
	for _, conn := range conns {
		out := t.TempDir()
		paths = append(paths, filepath.Join(out, "file"))
		session := multirecv.Session{DataKey: key, ChunkSize: 32768, Files: []multirecv.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
		go func(c multihub.Conn) {
			results <- multirecv.ReceiveStream(context.Background(), hubStream{c}, out, session, nil)
		}(conn)
	}
	await := func(want string, count int) {
		t.Helper()
		for count > 0 {
			select {
			case got := <-controls:
				if got == want {
					count--
				}
			case err := <-senderErrors:
				t.Fatal(err)
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out waiting for %s", want)
			}
		}
	}
	await("ready", 2)
	_, err := multisend.StreamFiles(context.Background(), []string{source}, key, func(b []byte) error { return sender.Send(append([]byte{6, 0, 0, 0, 0, 0}, b...)) })
	if err != nil {
		t.Fatal(err)
	}
	await("done", 2)
	for range conns {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("receiver timeout")
		}
	}
	for _, file := range paths {
		got, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("file mismatch: %v", err)
		}
	}
}

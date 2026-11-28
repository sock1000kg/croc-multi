package multisend

import (
	"bytes"
	"context"
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

// memoryRoom is an in-memory hub room plus a helper to join it.
func memoryRoom(t *testing.T, ctx context.Context, maxReceivers int) func(multiwire.Role) (*multisession.Link, error) {
	t.Helper()
	hub := multihub.New(multihub.Config{MaxReceivers: maxReceivers, QueueBytes: 8 << 20, StallTimeout: time.Second})
	return func(role multiwire.Role) (*multisession.Link, error) {
		c, s := multihub.Pipe()
		go hub.Serve("multi:test", s)
		l, e := multisession.NewLink(ctx, c, role, maxReceivers)
		if e != nil {
			return nil, e
		}
		t.Cleanup(func() { l.Close() })
		return l, nil
	}
}

// The CLI's progress display depends on this event sequence: joined (with
// IP), ready (with the sanitized name), sending (with Total), receiving,
// done, and uploaded with a byte count that does not grow with N.
func TestSendReportsProgressEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connect := memoryRoom(t, ctx, 2)
	sender, err := connect(multiwire.RoleSender)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("progress events\n"), 20000)
	source := filepath.Join(t.TempDir(), "source")
	if err = os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	total := int64(len(data))
	session := multisession.Session{DataKey: bytes.Repeat([]byte{9}, 32), ChunkSize: multiwire.ChunkSize,
		Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: total}}, TotalSize: total}

	var mu sync.Mutex
	var events []PeerProgress
	progress := func(p PeerProgress) {
		mu.Lock()
		events = append(events, p)
		mu.Unlock()
	}
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, "room-correct-words", session, SendOptions{Files: []string{source}, Receivers: 2, Progress: progress})
	}()
	results := make(chan error, 2)
	for _, name := range []string{"an pc", "binh\x1b[31m"} {
		l, e := connect(multiwire.RoleReceiver)
		if e != nil {
			t.Fatal(e)
		}
		go func() {
			s, e := multisession.JoinSession(ctx, l, "room-correct-words", name)
			if e != nil {
				results <- e
				return
			}
			files := []multirecv.FileEntry{{Idx: 0, Path: s.Files[0].Path, Size: s.Files[0].Size}}
			results <- multirecv.ReceiveStream(ctx, receiverStream{l}, t.TempDir(), multirecv.Session{DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: files, TotalSize: s.TotalSize}, nil)
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

	mu.Lock()
	defer mu.Unlock()
	count := map[string]int{}
	names := map[string]bool{}
	for _, p := range events {
		count[p.State]++
		switch p.State {
		case "joined":
			if p.ID < 2 || p.Addr == "" {
				t.Errorf("joined without ID/Addr: %+v", p)
			}
		case "ready":
			names[p.Name] = true
		case "sending":
			if p.ID != 0 || p.Total != total {
				t.Errorf("sending = %+v, want ID 0 and Total %d", p, total)
			}
		case "uploaded":
			if p.Bytes < total || float64(p.Bytes) > 1.01*float64(total) {
				t.Errorf("uploaded %d bytes for a %d-byte file and 2 receivers", p.Bytes, total)
			}
		}
	}
	for state, want := range map[string]int{"joined": 2, "ready": 2, "sending": 1, "done": 2, "uploaded": 1} {
		if count[state] != want {
			t.Errorf("%d %q events, want %d (all: %v)", count[state], state, want, count)
		}
	}
	// The name reaches the CLI already sanitized: no escape sequences.
	if !names["an pc"] || !names["binh[31m"] {
		t.Errorf("ready names = %v", names)
	}
}

// A wrong code is an online guess, and a rejected receiver can rejoin at
// once, so the lobby must stop after maxAuthFailures of them.
func TestLobbyStopsAfterTooManyWrongCodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := memoryRoom(t, ctx, 2)
	sender, err := connect(multiwire.RoleSender)
	if err != nil {
		t.Fatal(err)
	}
	session := multisession.Session{DataKey: bytes.Repeat([]byte{3}, 32), ChunkSize: multiwire.ChunkSize}
	var mu sync.Mutex
	rejected := 0
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, "room-correct-words", session, SendOptions{Files: []string{"unused"}, Receivers: 2,
			Progress: func(p PeerProgress) {
				if p.State == "rejected" && p.Reason == "wrong code" {
					mu.Lock()
					rejected++
					mu.Unlock()
				}
			}})
	}()
	guesses := 0
	for {
		select {
		case e := <-sent:
			if e == nil || !strings.Contains(e.Error(), "wrong-code attempts") {
				t.Fatalf("sendSession = %v, want the wrong-code limit", e)
			}
			if guesses < maxAuthFailures {
				t.Fatalf("stopped after %d guesses, want %d", guesses, maxAuthFailures)
			}
			mu.Lock()
			defer mu.Unlock()
			// A guesser that hangs up at once may be reported as "left"
			// instead, so only check that rejections are reported at all.
			if rejected == 0 {
				t.Error("no rejected events")
			}
			return
		case <-ctx.Done():
			t.Fatalf("lobby still open after %d wrong codes", guesses)
		default:
		}
		l, e := connect(multiwire.RoleReceiver)
		if e != nil { // room-full while a kick is in flight
			time.Sleep(10 * time.Millisecond)
			continue
		}
		guessCtx, cancelGuess := context.WithTimeout(ctx, 2*time.Second)
		_, e = multisession.JoinSession(guessCtx, l, "room-wrong-guess", "guesser")
		cancelGuess()
		l.Close()
		if e == nil {
			t.Fatal("a wrong code joined the session")
		}
		if strings.Contains(e.Error(), "rejected: auth") {
			guesses++
		}
	}
}

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
// once. After guessBurst wrong codes, new joiners are turned away as "busy"
// without a PAKE (no guess), while a receiver that is already ready is not
// affected; once the bucket refills, a receiver with the right code gets in
// and the send completes.
func TestLobbyLimitsWrongCodes(t *testing.T) {
	oldRefill := guessRefill
	guessRefill = 1500 * time.Millisecond
	defer func() { guessRefill = oldRefill }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := memoryRoom(t, ctx, 2)
	sender, err := connect(multiwire.RoleSender)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("guess limit\n"), 1000)
	source := filepath.Join(t.TempDir(), "source")
	if err = os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	session := multisession.Session{DataKey: bytes.Repeat([]byte{3}, 32), ChunkSize: multiwire.ChunkSize,
		Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}

	firstReady := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	busy := 0
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, "room-correct-words", session, SendOptions{Files: []string{source}, Receivers: 2,
			Progress: func(p PeerProgress) {
				switch {
				case p.State == "ready":
					once.Do(func() { close(firstReady) })
				case p.State == "rejected" && p.Reason == "busy":
					mu.Lock()
					busy++
					mu.Unlock()
				}
			}})
	}()
	receive := func() <-chan error {
		done := make(chan error, 1)
		l, e := connect(multiwire.RoleReceiver)
		if e != nil {
			done <- e
			return done
		}
		go func() {
			s, e := multisession.JoinSession(ctx, l, "room-correct-words", "real")
			if e != nil {
				done <- e
				return
			}
			files := []multirecv.FileEntry{{Idx: 0, Path: s.Files[0].Path, Size: s.Files[0].Size}}
			done <- multirecv.ReceiveStream(ctx, receiverStream{l}, t.TempDir(), multirecv.Session{DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: files, TotalSize: s.TotalSize}, nil)
		}()
		return done
	}
	first := receive()
	select {
	case <-firstReady:
	case e := <-first:
		t.Fatalf("first receiver: %v", e)
	case <-ctx.Done():
		t.Fatal("first receiver never ready")
	}

	// Guess until the sender turns us away without a PAKE.
	guesses := 0
	for {
		if ctx.Err() != nil {
			t.Fatalf("never turned away after %d wrong codes", guesses)
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
		if strings.Contains(e.Error(), "rejected: busy") {
			break
		}
		if strings.Contains(e.Error(), "rejected: auth") {
			guesses++
		}
	}
	// The burst, plus at most one token refilled while guessing.
	if guesses < guessBurst || guesses > guessBurst+1 {
		t.Errorf("%d wrong codes before busy, want %d", guesses, guessBurst)
	}
	select {
	case e := <-sent:
		t.Fatalf("wrong codes ended the session: %v", e)
	default:
	}

	// After a refill, the right code gets in and everyone finishes.
	time.Sleep(guessRefill + 300*time.Millisecond)
	second := receive()
	for _, ch := range []<-chan error{first, second} {
		if e := <-ch; e != nil {
			t.Fatalf("receiver: %v", e)
		}
	}
	if e := <-sent; e != nil {
		t.Fatalf("sendSession = %v", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if busy == 0 {
		t.Error("no busy rejection reported")
	}
}

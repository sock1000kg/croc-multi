package multisend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multirecv"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

// newTestApprover answers every prompt with answer(prompt) and counts calls.
func newTestApprover(ctx context.Context, answer func(prompt string) (string, error)) (*approver, *atomic.Int32) {
	var calls atomic.Int32
	return &approver{
		ctx: ctx,
		ask: func(_ context.Context, prompt string) (string, error) {
			calls.Add(1)
			return answer(prompt)
		},
		refused: map[string]bool{},
	}, &calls
}

func TestApproverAnswers(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{"y", true}, {"Y", true}, {"yes", true}, {" YES ", true},
		{"", false}, {"n", false}, {"no", false}, {"yep", false},
	}
	for _, tt := range tests {
		a, _ := newTestApprover(context.Background(), func(string) (string, error) { return tt.answer, nil })
		if got := a.approve(multisession.ReceiverInfo{Name: "x", Addr: "1.2.3.4"}); got != tt.want {
			t.Errorf("answer %q: got %v, want %v", tt.answer, got, tt.want)
		}
	}
}

func TestApproverPromptShowsNameAndAddr(t *testing.T) {
	var prompt string
	a, _ := newTestApprover(context.Background(), func(p string) (string, error) { prompt = p; return "y", nil })
	a.approve(multisession.ReceiverInfo{Name: "an-pc", Addr: "1.2.3.4"})
	if !strings.Contains(prompt, `"an-pc"`) || !strings.Contains(prompt, "1.2.3.4") {
		t.Fatalf("prompt %q should show the quoted name and the address", prompt)
	}
}

// A refused name+host is refused again without asking; another name or
// another host from the same receiver is asked about.
func TestApproverRemembersRefused(t *testing.T) {
	a, calls := newTestApprover(context.Background(), func(string) (string, error) { return "n", nil })
	bad := multisession.ReceiverInfo{Name: "bad", Addr: "1.2.3.4"}
	a.approve(bad)
	a.lastEnd = time.Time{} // skip the gap: this test is about memory only
	for i := 0; i < 5; i++ {
		if a.approve(bad) {
			t.Fatal("refused receiver was approved")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("asked %d times, want 1", n)
	}
	a.lastEnd = time.Time{}
	a.approve(multisession.ReceiverInfo{Name: "bad", Addr: "5.6.7.8"})
	a.lastEnd = time.Time{}
	a.approve(multisession.ReceiverInfo{Name: "other", Addr: "1.2.3.4"})
	if n := calls.Load(); n != 3 {
		t.Fatalf("asked %d times, want 3 (new host and new name are asked)", n)
	}
}

// A failed read (Ctrl+C, lobby over) refuses but does not remember: the
// person was never actually refused by the sender.
func TestApproverErrorNotRemembered(t *testing.T) {
	fail := true
	a, calls := newTestApprover(context.Background(), func(string) (string, error) {
		if fail {
			return "", errors.New("stdin closed")
		}
		return "y", nil
	})
	info := multisession.ReceiverInfo{Name: "x", Addr: "1.2.3.4"}
	if a.approve(info) {
		t.Fatal("approved on read error")
	}
	fail = false
	a.lastEnd = time.Time{}
	if !a.approve(info) || calls.Load() != 2 {
		t.Fatalf("after an error the receiver should be asked again (calls=%d)", calls.Load())
	}
}

// Concurrent receivers get one prompt at a time, at least promptGap apart.
func TestApproverSerializesAndSpacesPrompts(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		inFlight int
		overlap  bool
		times    []time.Time
	)
	a, _ := newTestApprover(context.Background(), func(string) (string, error) {
		mu.Lock()
		inFlight++
		overlap = overlap || inFlight > 1
		times = append(times, time.Now())
		mu.Unlock()
		time.Sleep(50 * time.Millisecond) // the "user" thinking
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "y", nil
	})
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.approve(multisession.ReceiverInfo{Name: name, Addr: "1.2.3.4"})
		}()
	}
	wg.Wait()
	if overlap {
		t.Fatal("two prompts were shown at the same time")
	}
	if len(times) != 2 {
		t.Fatalf("got %d prompts, want 2", len(times))
	}
	// The gap counts from the end of the first answer (~50 ms after it began).
	if gap := times[1].Sub(times[0]); gap < promptGap {
		t.Fatalf("second prompt came %v after the first, want ≥ %v", gap, promptGap)
	}
}

// A context that ends during the gap refuses at once without asking.
func TestApproverCancelledDuringGap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, calls := newTestApprover(ctx, func(string) (string, error) { return "y", nil })
	a.lastEnd = time.Now() // a prompt just ended, so the next one must wait
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if a.approve(multisession.ReceiverInfo{Name: "x", Addr: "1.2.3.4"}) {
		t.Fatal("approved after cancel")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("approve took %v after cancel, want it to return promptly", d)
	}
	if calls.Load() != 0 {
		t.Fatal("asked after the context ended")
	}
}

// End to end in memory with --approve and one receiver slot: "bad" joins and
// is refused, joins again and is refused without a prompt, then "good" joins,
// is approved and gets the file.
func TestApproveInMemory(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var prompts sync.Map // name → number of prompts
	orig := askApproval
	askApproval = func(_ context.Context, prompt string) (string, error) {
		for _, name := range []string{"bad", "good"} {
			if strings.Contains(prompt, `"`+name+`"`) {
				n, _ := prompts.LoadOrStore(name, new(atomic.Int32))
				n.(*atomic.Int32).Add(1)
				if name == "good" {
					return "y", nil
				}
			}
		}
		return "n", nil
	}
	t.Cleanup(func() { askApproval = orig })

	hub := multihub.New(multihub.Config{MaxReceivers: 1})
	const room = "multi:approve"
	sender := func() *multisession.Link {
		c, s := multihub.Pipe()
		go hub.Serve(room, s)
		l, e := multisession.NewLink(ctx, c, multiwire.RoleSender, 1)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}()
	// A refused receiver's slot frees only once the sender's KICK reaches the
	// hub, so retry room-full for a moment.
	receiver := func() *multisession.Link {
		for {
			c, s := multihub.Pipe()
			go hub.Serve(room, s)
			l, e := multisession.NewLink(ctx, c, multiwire.RoleReceiver, 0)
			if e == nil {
				t.Cleanup(func() { l.Close() })
				return l
			}
			var he *multiwire.HubError
			if !errors.As(e, &he) || he.Code != multiwire.ErrRoomFull || ctx.Err() != nil {
				t.Fatal(e)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	data := bytes.Repeat([]byte("approved receivers only\n"), 2000)
	source := filepath.Join(t.TempDir(), "source")
	if e := os.WriteFile(source, data, 0o600); e != nil {
		t.Fatal(e)
	}
	session := multisession.Session{DataKey: bytes.Repeat([]byte{9}, 32), ChunkSize: multiwire.ChunkSize,
		Files: []multisession.FileEntry{{Idx: 0, Path: "file", Size: int64(len(data))}}, TotalSize: int64(len(data))}
	const code = "room-correct-words"
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, code, session, SendOptions{Files: []string{source}, Receivers: 1, Approve: true})
	}()

	for i := 0; i < 2; i++ {
		_, e := multisession.JoinSession(ctx, receiver(), code, "bad")
		if e == nil || !strings.Contains(e.Error(), "refused") {
			t.Fatalf("bad attempt %d: got %v, want a refusal", i+1, e)
		}
	}

	l := receiver()
	s, e := multisession.JoinSession(ctx, l, code, "good")
	if e != nil {
		t.Fatalf("good receiver: %v", e)
	}
	out := t.TempDir()
	files := []multirecv.FileEntry{{Idx: s.Files[0].Idx, Path: s.Files[0].Path, Size: s.Files[0].Size}}
	if e = multirecv.ReceiveStream(ctx, receiverStream{l}, out, multirecv.Session{DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: files, TotalSize: s.TotalSize}, nil); e != nil {
		t.Fatal(e)
	}
	if e = <-sent; e != nil {
		t.Fatalf("sender: %v", e)
	}
	if got, e := os.ReadFile(filepath.Join(out, "file")); e != nil || !bytes.Equal(got, data) {
		t.Fatalf("file mismatch: %v", e)
	}
	for name, want := range map[string]int32{"bad": 1, "good": 1} {
		n, ok := prompts.Load(name)
		if !ok || n.(*atomic.Int32).Load() != want {
			t.Fatalf("%s was prompted %v times, want %d", name, n, want)
		}
	}
}

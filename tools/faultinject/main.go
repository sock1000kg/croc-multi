// Command faultinject checks that a relay's multi-send hub survives receivers
// that misbehave (owner: D). It runs a complete scenario on its own, so it
// needs only a relay, not the real croc sender or receivers:
//
//   - one fake sender, which pushes --size MB of random DATA frames,
//   - --receivers well-behaved receivers, which read everything,
//   - --faulty receivers that misbehave according to --mode.
//
// It passes (exit 0) when every well-behaved receiver got all the data and,
// for --mode stall or silent, the hub dropped every faulty receiver.
//
//	go run ./tools/faultinject --help
//	croc relay &                                   # in another terminal
//	go run ./tools/faultinject --mode stall        # dropped after ~30s
//	go run ./tools/faultinject --mode slow --slow-rate 1024
//	go run ./tools/faultinject --mode silent       # dropped after ~60s (PING timeout)
//	go run ./tools/faultinject --relay <azure-ip>:9009 --pass <relay pass> --mode stall
//
// The data is random bytes, not real encrypted chunks: the hub never looks
// inside DATA payloads, so it cannot tell the difference.
//
// For anyone new to Go: "package main" + "func main()" makes this folder a
// program. `go run ./tools/faultinject` compiles and runs it; it is not part
// of the croc binary. Flags use the standard library "flag" package, which
// accepts -flag and --flag and handles -h/--help (exit status 0).
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/croc/v11/src/comm"
	"github.com/schollz/croc/v11/src/tcp"
)

// Faulty receiver behaviours.
const (
	modeStall  = "stall"  // join, then never read again: the hub must drop it (stall timeout, 30s)
	modeSlow   = "slow"   // read at --slow-rate bytes/s: report what the hub does with it
	modeSilent = "silent" // read everything but never send, not even PING replies: dropped by idle timeout (60s)
)

// Hub frame types and roles (docs/contracts.md §2). Every frame is a 6-byte
// header, type u8 | flags u8 | from u16 | to u16, followed by the payload.
// TODO: use the src/multiwire constants once T's package exists.
const (
	tJoin        = 1
	tJoinAck     = 2
	tMemberLeft  = 4
	tData        = 6
	tPing        = 7
	tError       = 8
	roleSender   = 1
	roleReceiver = 2
	chunkSize    = 32 * 1024
	joinAckWait  = 5 * time.Second // contracts.md §1
)

type options struct {
	relay     string
	pass      string
	receivers int
	faulty    int
	mode      string
	sizeMB    int
	slowRate  int
	timeout   time.Duration
}

func main() {
	// os.Exit skips deferred calls, so all real work lives in run().
	os.Exit(run(os.Args[1:]))
}

// run returns the exit code: 0 pass, 1 fail, 2 bad usage.
func run(args []string) int {
	fs := flag.NewFlagSet("faultinject", flag.ExitOnError)
	var o options
	fs.StringVar(&o.relay, "relay", "localhost:9009", "relay address host:port")
	fs.StringVar(&o.pass, "pass", "pass123", "relay password (croc relay --pass)")
	fs.IntVar(&o.receivers, "receivers", 2, "number of well-behaved receivers")
	fs.IntVar(&o.faulty, "faulty", 1, "number of misbehaving receivers")
	fs.StringVar(&o.mode, "mode", modeStall, "how faulty receivers misbehave: stall, slow or silent")
	fs.IntVar(&o.sizeMB, "size", 32, "MB of data the sender pushes; keep it well above the relay's --multi-queue-mb")
	fs.IntVar(&o.slowRate, "slow-rate", 1024, "read speed in bytes/s for --mode slow")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "give up after this long")
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "faultinject: check that a relay's multi-send hub copes with misbehaving receivers.")
		fmt.Fprintln(out, "It plays the sender, the normal receivers and the faulty receivers itself.")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintln(out, "  go run ./tools/faultinject [flags]")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Examples:")
		fmt.Fprintln(out, "  go run ./tools/faultinject --mode stall     # faulty receiver dropped after ~30s")
		fmt.Fprintln(out, "  go run ./tools/faultinject --mode silent    # faulty receiver dropped after ~60s")
		fmt.Fprintln(out, "  go run ./tools/faultinject --mode slow --faulty 2 --slow-rate 1024")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if err := o.validate(); err != nil {
		fmt.Fprintln(os.Stderr, "faultinject:", err)
		fs.Usage()
		return 2
	}

	ok, err := scenario(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "faultinject:", err)
		return 1
	}
	if !ok {
		fmt.Println("FAIL")
		return 1
	}
	fmt.Println("PASS")
	return 0
}

func (o options) validate() error {
	switch {
	case o.receivers < 1:
		return errors.New("--receivers must be at least 1")
	case o.faulty < 0:
		return errors.New("--faulty must not be negative")
	case o.receivers+o.faulty > 255:
		return errors.New("--receivers + --faulty must be at most 255")
	case o.mode != modeStall && o.mode != modeSlow && o.mode != modeSilent:
		return fmt.Errorf("--mode must be stall, slow or silent, got %q", o.mode)
	case o.sizeMB < 1:
		return errors.New("--size must be at least 1")
	case o.slowRate < 1:
		return errors.New("--slow-rate must be at least 1")
	case o.timeout <= 0:
		return errors.New("--timeout must be positive")
	}
	return nil
}

// peer is one receiver as the report sees it.
type peer struct {
	id     uint16
	faulty bool
	conn   *comm.Comm
	bytes  atomic.Int64 // DATA payload bytes received (normal receivers)
	done   chan struct{} // closed when a normal receiver has every byte
}

// leave is one MEMBER_LEFT the sender saw.
type leave struct {
	reason byte
	after  time.Duration
}

func scenario(o options) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()

	room := tcp.MultiRoomPrefix + "faultinject-" + randomHex(6)
	total := int64(o.sizeMB) << 20
	fmt.Printf("room %s on %s: %d MB, %d normal + %d %s receiver(s)\n",
		room, o.relay, o.sizeMB, o.receivers, o.faulty, o.mode)

	// 1. The sender joins first; receivers that come earlier get no-sender.
	sender, _, err := join(o, room, roleSender, byte(o.receivers+o.faulty))
	if err != nil {
		return false, fmt.Errorf("sender: %w", err)
	}
	defer sender.Close()

	// 2. Receivers join. Normal ones read in the background straight away.
	var peers []*peer
	for i := 0; i < o.receivers+o.faulty; i++ {
		c, id, err := join(o, room, roleReceiver, 0)
		if err != nil {
			return false, fmt.Errorf("receiver %d: %w", i+1, err)
		}
		defer c.Close()
		p := &peer{id: id, faulty: i >= o.receivers, conn: c, done: make(chan struct{})}
		peers = append(peers, p)
		if p.faulty {
			go misbehave(o, c)
		} else {
			go readAll(p, total)
		}
	}

	// Close every connection when the timeout hits, so nothing blocks forever.
	stop := context.AfterFunc(ctx, func() {
		sender.Close()
		for _, p := range peers {
			p.conn.Close()
		}
	})
	defer stop()

	// 3. The sender's own reader: answer PINGs, record MEMBER_LEFTs.
	start := time.Now()
	var mu sync.Mutex
	left := map[uint16]leave{}
	go func() {
		for {
			f, err := sender.Receive()
			if err != nil || len(f) < 6 {
				return
			}
			switch f[0] {
			case tPing:
				_ = sender.Send(header(tPing, 0))
			case tMemberLeft:
				if len(f) >= 9 {
					mu.Lock()
					left[binary.LittleEndian.Uint16(f[6:8])] = leave{reason: f[8], after: time.Since(start)}
					mu.Unlock()
				}
			}
		}
	}()

	// 4. Push the data. A stalled receiver blocks this until the hub drops it.
	chunk := make([]byte, chunkSize)
	_, _ = rand.Read(chunk)
	frame := append(header(tData, 0), chunk...)
	for sent := int64(0); sent < total; sent += chunkSize {
		n := min(int64(chunkSize), total-sent)
		if err := sender.Send(frame[:6+n]); err != nil {
			return false, fmt.Errorf("sending DATA after %.1f MB: %w", float64(sent)/(1<<20), err)
		}
	}
	fmt.Printf("sent %d MB in %s\n", o.sizeMB, time.Since(start).Round(100*time.Millisecond))

	// 5. Wait for normal receivers to finish and (stall/silent) faulty ones to
	//    be dropped, or for the timeout.
	for _, p := range peers {
		if !p.faulty {
			select {
			case <-p.done:
			case <-ctx.Done():
			}
		}
	}
	expectDrop := o.mode != modeSlow
	for expectDrop && ctx.Err() == nil {
		mu.Lock()
		all := true
		for _, p := range peers {
			if _, gone := left[p.id]; p.faulty && !gone {
				all = false
			}
		}
		mu.Unlock()
		if all {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 6. Report.
	mu.Lock()
	defer mu.Unlock()
	pass := true
	for _, p := range peers {
		l, gone := left[p.id]
		switch {
		case !p.faulty:
			got := p.bytes.Load()
			ok := got == total && !gone
			pass = pass && ok
			fmt.Printf("[%d] normal  got %.1f/%d MB  %s\n", p.id, float64(got)/(1<<20), o.sizeMB, verdict(ok))
		case gone:
			fmt.Printf("[%d] %-6s  dropped after %s (MEMBER_LEFT reason %d)  %s\n",
				p.id, o.mode, l.after.Round(100*time.Millisecond), l.reason, verdict(true))
		default:
			pass = pass && !expectDrop
			fmt.Printf("[%d] %-6s  still connected  %s\n", p.id, o.mode, verdict(!expectDrop))
		}
	}
	return pass, nil
}

// join does the relay handshake into room, sends a hub JOIN and waits for the
// JOIN_ACK. It returns the connection and the member ID the hub assigned.
func join(o options, room string, role, maxReceivers byte) (*comm.Comm, uint16, error) {
	c, _, _, err := tcp.ConnectToTCPServer(o.relay, o.pass, room)
	if err != nil {
		return nil, 0, err
	}
	// JOIN payload: version u8 | role u8 | maxReceivers u8
	if err := c.Send(append(header(tJoin, 0), 1, role, maxReceivers)); err != nil {
		c.Close()
		return nil, 0, err
	}
	reply, err := c.ReceiveWithDeadline(time.Now().Add(joinAckWait))
	if err != nil {
		c.Close()
		return nil, 0, fmt.Errorf("no JOIN_ACK within %s; this relay does not support multi-send: %w", joinAckWait, err)
	}
	switch {
	case len(reply) >= 9 && reply[0] == tJoinAck:
		// JOIN_ACK payload: version u8 | memberID u16 | maxReceivers u8
		return c, binary.LittleEndian.Uint16(reply[7:9]), nil
	case len(reply) >= 7 && reply[0] == tError:
		c.Close()
		return nil, 0, fmt.Errorf("hub refused JOIN: error %d %q", reply[6], reply[7:])
	default:
		c.Close()
		return nil, 0, fmt.Errorf("unexpected reply to JOIN: %x", reply)
	}
}

// readAll is a well-behaved receiver: count DATA bytes, answer every PING.
func readAll(p *peer, total int64) {
	for {
		f, err := p.conn.Receive()
		if err != nil {
			return
		}
		if len(f) < 6 {
			continue
		}
		switch f[0] {
		case tPing:
			_ = p.conn.Send(header(tPing, 0))
		case tData:
			if p.bytes.Add(int64(len(f)-6)) == total {
				close(p.done)
			}
		}
	}
}

// misbehave runs one faulty receiver until its connection closes.
func misbehave(o options, c *comm.Comm) {
	switch o.mode {
	case modeStall:
		// Never read: the hub's writes to us block, our queue fills up.
		return
	case modeSilent:
		// Read every frame, never send anything back.
		for {
			if _, err := c.Receive(); err != nil {
				return
			}
		}
	case modeSlow:
		// Read raw bytes off the socket at slowRate bytes/s, ignoring framing.
		buf := make([]byte, o.slowRate)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for range tick.C {
			if _, err := c.Connection().Read(buf); err != nil {
				return
			}
		}
	}
}

// header builds a 6-byte hub frame header with from = 0 (the hub fills it in).
func header(typ byte, to uint16) []byte {
	h := make([]byte, 6, 6+chunkSize)
	h[0] = typ
	binary.LittleEndian.PutUint16(h[4:6], to)
	return h
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func verdict(ok bool) string {
	if ok {
		return "OK"
	}
	return "FAIL"
}

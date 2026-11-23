package multisession

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/schollz/croc/v11/src/codephrase"
	"github.com/schollz/croc/v11/src/multiwire"
	"github.com/schollz/croc/v11/src/tcp"
)

// joinAckTimeout is how long Dial waits for JOIN_ACK. A relay without the
// multi-send hub never answers JOIN, so this is how an old relay is detected
// (docs/plan.md §2.6).
const joinAckTimeout = 5 * time.Second

// consumerStallTimeout: if the code using a Link stops reading one of its
// queues for this long, readLoop gives up and closes the Link instead of
// blocking forever (and with it, PING replies to the hub).
const consumerStallTimeout = 30 * time.Second

// IsNoSender reports whether err is the hub's "no sender in this room" refusal.
// A receiver that starts before the sender gets it and retries.
func IsNoSender(err error) bool {
	var he *multiwire.HubError
	return errors.As(err, &he) && he.Code == multiwire.ErrNoSender
}

// Link is a client's (sender's or receiver's) joined connection to the hub.
//
// One background goroutine (readLoop) reads every frame and sorts it:
//
//	PING                answered right away, never delivered
//	DIRECT from member X    → that member's queue      (read with RecvPeer(X))
//	MEMBER_LEFT about X     → X's queue AND the events queue
//	everything else         → the events queue          (read with Recv)
//
// Per-member queues let the sender run one goroutine per receiver (PAKE,
// waiting for "done") without them stealing each other's messages. A receiver
// only talks to the sender, so it reads DIRECT with RecvPeer(SenderID) and
// DATA/ERROR with Recv.
//
// Send is safe from many goroutines. Close may be called more than once.
type Link struct {
	c      multiwire.Conn
	id     uint16     // our member ID from JOIN_ACK
	sendMu sync.Mutex // one Send at a time: frames must not interleave

	mu    sync.Mutex                      // guards peers and err
	peers map[uint16]chan multiwire.Frame // per-member DIRECT/MEMBER_LEFT queues, created on first use
	err   error                           // why readLoop stopped; returned to readers after done closes

	events chan multiwire.Frame // all other frames
	done   chan struct{}        // closed when readLoop exits
	once   sync.Once            // makes Close idempotent
}

// MemberID is the ID the hub assigned us (SenderID for the sender).
func (l *Link) MemberID() uint16 { return l.id }

// Send encodes and writes one frame. The hub overwrites From, so only Type,
// To and Payload matter.
func (l *Link) Send(f multiwire.Frame) error {
	b, e := multiwire.Encode(f)
	if e != nil {
		return e
	}
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.c.Send(b)
}

// Close closes the connection. readLoop then fails its Receive and exits,
// which wakes every pending Recv/RecvPeer.
func (l *Link) Close() error {
	var e error
	l.once.Do(func() { e = l.c.Close() })
	return e
}

// peer returns member id's queue, creating it on first use. Either side may
// create it first: readLoop when a frame arrives, or a reader calling
// RecvPeer before anything arrived.
func (l *Link) peer(id uint16) chan multiwire.Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := l.peers[id]
	if ch == nil {
		ch = make(chan multiwire.Frame, 32)
		l.peers[id] = ch
	}
	return ch
}

// fail records why readLoop stopped. Readers see it once done is closed.
func (l *Link) fail(e error) {
	l.mu.Lock()
	l.err = e
	l.mu.Unlock()
}

// deliver puts f on ch. If nobody drains ch for consumerStallTimeout the Link
// is closed: a stuck consumer must not silently stall the whole connection.
func (l *Link) deliver(ch chan multiwire.Frame, f multiwire.Frame) bool {
	select {
	case ch <- f:
		return true
	case <-time.After(consumerStallTimeout):
		l.fail(errors.New("multi session consumer stalled"))
		l.Close()
		return false
	}
}

// readLoop is the only goroutine that calls c.Receive. See the Link doc for
// where each frame type goes.
func (l *Link) readLoop() {
	defer close(l.done)
	for {
		b, e := l.c.Receive()
		if e != nil {
			l.fail(e)
			return
		}
		f, e := multiwire.Decode(b)
		if e != nil {
			l.fail(e)
			return
		}
		switch {
		case f.Type == multiwire.TypePing:
			// Contract: answer every PING, or the hub's idle timeout drops us.
			if e = l.Send(multiwire.Frame{Type: multiwire.TypePing}); e != nil {
				l.fail(e)
				return
			}
			continue
		case f.Type == multiwire.TypeMemberLeft && len(f.Payload) >= 2:
			// Wake whoever is talking to that member (it would otherwise wait
			// for a reply that never comes); the events copy follows below.
			id := binary.LittleEndian.Uint16(f.Payload)
			if !l.deliver(l.peer(id), f) {
				return
			}
		}
		target := l.events
		if f.Type == multiwire.TypeDirect {
			target = l.peer(f.From)
		}
		if !l.deliver(target, f) {
			return
		}
	}
}

// get waits for the next frame on ch, the Link dying, or ctx.
func (l *Link) get(ctx context.Context, ch <-chan multiwire.Frame) (multiwire.Frame, error) {
	select {
	case f := <-ch:
		return f, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return multiwire.Frame{}, l.err
	case <-ctx.Done():
		return multiwire.Frame{}, ctx.Err()
	}
}

// RecvContext returns the next frame that is not DIRECT or PING
// (DATA, ERROR, MEMBER_JOINED, MEMBER_LEFT).
func (l *Link) RecvContext(ctx context.Context) (multiwire.Frame, error) {
	return l.get(ctx, l.events)
}

// Recv is RecvContext without a deadline.
func (l *Link) Recv() (multiwire.Frame, error) { return l.RecvContext(context.Background()) }

// RecvPeer returns the next DIRECT (or MEMBER_LEFT) frame from member id.
func (l *Link) RecvPeer(ctx context.Context, id uint16) (multiwire.Frame, error) {
	return l.get(ctx, l.peer(id))
}

// NewLink sends JOIN on an already-connected Conn and waits for JOIN_ACK
// (until ctx ends). maxReceivers is the sender's receiver count; receivers
// pass 0. On success the Link owns c and starts reading it.
func NewLink(ctx context.Context, c multiwire.Conn, role multiwire.Role, maxReceivers int) (*Link, error) {
	if maxReceivers < 0 || maxReceivers > 255 {
		return nil, errors.New("invalid receiver limit")
	}
	// Receive has no deadline of its own: closing c on ctx end unblocks it.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	// JOIN payload: version u8 | role u8 | maxReceivers u8
	b, _ := multiwire.Encode(multiwire.Frame{Type: multiwire.TypeJoin, Payload: []byte{multiwire.Version, byte(role), byte(maxReceivers)}})
	if e := c.Send(b); e != nil {
		return nil, e
	}
	b, e := c.Receive()
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, e
	}
	f, e := multiwire.Decode(b)
	if e != nil {
		return nil, ErrNotMultiRelay
	}
	if f.Type == multiwire.TypeError {
		return nil, multiwire.ParseError(f.Payload)
	}
	// JOIN_ACK payload: version u8 | memberID u16 | maxReceivers u8
	if f.Type != multiwire.TypeJoinAck || len(f.Payload) != 4 || f.Payload[0] != multiwire.Version {
		return nil, ErrNotMultiRelay
	}
	// The hub lowers a sender's request to its own --multi-max-receivers;
	// fail now rather than wait forever for receivers that cannot get in.
	if role == multiwire.RoleSender && maxReceivers > int(f.Payload[3]) {
		return nil, errors.New("receiver limit exceeds hub maximum")
	}
	l := &Link{
		c:      c,
		id:     binary.LittleEndian.Uint16(f.Payload[1:3]),
		events: make(chan multiwire.Frame, 128),
		peers:  make(map[uint16]chan multiwire.Frame),
		done:   make(chan struct{}),
	}
	go l.readLoop()
	return l, nil
}

// ErrNotMultiRelay means the relay answered JOIN with something that is not a
// hub frame (or not at all): it is a plain croc relay without multi-send.
var ErrNotMultiRelay = errors.New("relay này không hỗ trợ multi-send; dùng croc relay của bạn")

// Dial connects to the relay at addr, enters room "multi:<RoomName of code>"
// and JOINs with the given role. It is the real-network counterpart of
// NewLink(multihub.Pipe()).
func Dial(ctx context.Context, addr, pass, code string, role multiwire.Role, maxReceivers int) (*Link, error) {
	parts, e := codephrase.Parse(code)
	if e != nil {
		return nil, e
	}
	// The ordinary croc relay handshake (relay PAKE, password, room name).
	// The "multi:" prefix makes the relay hand us to its hub.
	c, _, _, _, e := tcp.ConnectToTCPServerControlContext(ctx, addr, pass, "multi:"+parts.RoomName)
	if e != nil {
		return nil, e
	}
	deadline, cancel := context.WithTimeout(ctx, joinAckTimeout)
	defer cancel()
	l, e := NewLink(deadline, multiwire.FromComm(c), role, maxReceivers)
	if e != nil {
		c.Close()
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, ErrNotMultiRelay) {
			return nil, errors.New("relay này không hỗ trợ multi-send; dùng croc relay của bạn")
		}
		return nil, e
	}
	return l, nil
}

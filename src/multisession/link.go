package multisession

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/schollz/croc/v11/src/codephrase"
	"github.com/schollz/croc/v11/src/comm"
	"github.com/schollz/croc/v11/src/multiwire"
	"github.com/schollz/croc/v11/src/tcp"
)

var ErrNoSender = errors.New("no sender in multi room")

func IsNoSender(err error) bool { return errors.Is(err, ErrNoSender) }

type conn interface {
	Send([]byte) error
	Receive() ([]byte, error)
	Close() error
}
type commConn struct{ *comm.Comm }

func (c commConn) Close() error { c.Comm.Close(); return nil }

type Link struct {
	c      conn
	id     uint16
	sendMu sync.Mutex
	mu     sync.Mutex
	peers  map[uint16]chan multiwire.Frame
	events chan multiwire.Frame
	done   chan struct{}
	err    error
	once   sync.Once
}

func (l *Link) MemberID() uint16 { return l.id }
func (l *Link) Send(f multiwire.Frame) error {
	b, e := multiwire.Encode(f)
	if e != nil {
		return e
	}
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.c.Send(b)
}
func (l *Link) Close() error { var e error; l.once.Do(func() { e = l.c.Close() }); return e }
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
func (l *Link) readLoop() {
	defer close(l.done)
	for {
		b, e := l.c.Receive()
		if e != nil {
			l.mu.Lock()
			l.err = e
			l.mu.Unlock()
			return
		}
		f, e := multiwire.Decode(b)
		if e != nil {
			l.mu.Lock()
			l.err = e
			l.mu.Unlock()
			return
		}
		if f.Type == multiwire.TypePing {
			if e = l.Send(multiwire.Frame{Type: multiwire.TypePing}); e != nil {
				l.mu.Lock()
				l.err = e
				l.mu.Unlock()
				return
			}
			continue
		}
		if f.Type == multiwire.TypeMemberLeft && len(f.Payload) >= 2 {
			id := binary.LittleEndian.Uint16(f.Payload)
			select {
			case l.peer(id) <- f:
			case <-time.After(30 * time.Second):
				l.mu.Lock()
				l.err = errors.New("multi session consumer stalled")
				l.mu.Unlock()
				l.Close()
				return
			}
		}
		target := l.events
		if f.Type == multiwire.TypeDirect {
			target = l.peer(f.From)
		}
		select {
		case target <- f:
		case <-time.After(30 * time.Second):
			l.mu.Lock()
			l.err = errors.New("multi session consumer stalled")
			l.mu.Unlock()
			l.Close()
			return
		}
	}
}
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
func (l *Link) RecvContext(ctx context.Context) (multiwire.Frame, error) { return l.get(ctx, l.events) }
func (l *Link) Recv() (multiwire.Frame, error)                           { return l.RecvContext(context.Background()) }
func (l *Link) RecvPeer(ctx context.Context, id uint16) (multiwire.Frame, error) {
	return l.get(ctx, l.peer(id))
}
func NewLink(ctx context.Context, c conn, role multiwire.Role, max int) (*Link, error) {
	if max < 0 || max > 255 {
		return nil, errors.New("invalid receiver limit")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	b, _ := multiwire.Encode(multiwire.Frame{Type: multiwire.TypeJoin, Payload: []byte{multiwire.Version, byte(role), byte(max)}})
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
		return nil, e
	}
	if f.Type == multiwire.TypeError && len(f.Payload) > 0 {
		if f.Payload[0] == multiwire.ErrNoSender {
			return nil, ErrNoSender
		}
		return nil, fmt.Errorf("hub error %d", f.Payload[0])
	}
	if f.Type != multiwire.TypeJoinAck || len(f.Payload) != 4 || f.Payload[0] != multiwire.Version {
		return nil, errors.New("invalid JOIN_ACK")
	}
	if role == multiwire.RoleSender && max > int(f.Payload[3]) {
		return nil, errors.New("receiver limit exceeds hub maximum")
	}
	l := &Link{c: c, id: binary.LittleEndian.Uint16(f.Payload[1:3]), events: make(chan multiwire.Frame, 128), peers: make(map[uint16]chan multiwire.Frame), done: make(chan struct{})}
	go l.readLoop()
	return l, nil
}
func Dial(ctx context.Context, addr, pass, code string, role multiwire.Role, max int) (*Link, error) {
	parts, e := codephrase.Parse(code)
	if e != nil {
		return nil, e
	}
	c, _, _, _, e := tcp.ConnectToTCPServerControlContext(ctx, addr, pass, "multi:"+parts.RoomName)
	if e != nil {
		return nil, e
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	l, e := NewLink(deadline, commConn{c}, role, max)
	if e != nil {
		c.Close()
		if errors.Is(e, context.DeadlineExceeded) {
			return nil, errors.New("relay này không hỗ trợ multi-send; dùng croc relay của bạn")
		}
		return nil, e
	}
	return l, nil
}

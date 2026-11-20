package multihub_test

// Behavior tests for the hub (milestone M1a, docs/phan-cong.md). They use only
// the public API from docs/contracts.md §5.2 (New, Serve, Pipe, Conn) and the
// wire format from §2, so they keep passing however the hub is built inside.

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multiwire"
)

// Short names for the src/multiwire contract values, to keep the tests terse.
const (
	tJoin         = multiwire.TypeJoin
	tJoinAck      = multiwire.TypeJoinAck
	tMemberJoined = multiwire.TypeMemberJoined
	tMemberLeft   = multiwire.TypeMemberLeft
	tDirect       = multiwire.TypeDirect
	tData         = multiwire.TypeData
	tPing         = multiwire.TypePing
	tError        = multiwire.TypeError
	tKick         = multiwire.TypeKick

	errRoomLocked = multiwire.ErrRoomLocked
	errRoomFull   = multiwire.ErrRoomFull
	errNoSender   = multiwire.ErrNoSender
	errBadFrame   = multiwire.ErrBadFrame
	errSenderGone = multiwire.ErrSenderGone

	roleSender   = uint8(multiwire.RoleSender)
	roleReceiver = uint8(multiwire.RoleReceiver)
	senderID     = multiwire.SenderID
)

const wait = 2 * time.Second

// frame is the hub header (type u8 | flags u8 | from u16 | to u16) + payload,
// with short field names for the tests.
type frame struct {
	typ      uint8
	from, to uint16
	payload  []byte
}

func encode(f frame) []byte {
	b, err := multiwire.Encode(multiwire.Frame{Type: f.typ, From: f.from, To: f.to, Payload: f.payload})
	if err != nil {
		panic(err)
	}
	return b
}

func decode(b []byte) frame {
	f, err := multiwire.Decode(b)
	if err != nil {
		return frame{} // typ 0: never valid, so any expect() on it fails
	}
	return frame{typ: f.Type, from: f.From, to: f.To, payload: f.Payload}
}

func defaultConfig() multihub.Config {
	return multihub.Config{MaxReceivers: 16, QueueBytes: 8 << 20, StallTimeout: 30 * time.Second}
}

// client is one well-behaved participant: its frames are read in the
// background, so the hub never sees it as a slow reader, and it answers every
// PING with a PING, as contracts.md §2.3 requires.
type client struct {
	t      *testing.T
	conn   multihub.Conn
	frames chan frame // closed when the hub closes the connection
}

// dial connects a raw Conn to the hub. Nothing reads from it.
func dial(t *testing.T, h *multihub.Hub, room string) multihub.Conn {
	t.Helper()
	clientSide, hubSide := multihub.Pipe()
	go h.Serve(room, hubSide)
	t.Cleanup(func() { clientSide.Close() })
	return clientSide
}

func connect(t *testing.T, h *multihub.Hub, room string) *client {
	t.Helper()
	c := &client{t: t, conn: dial(t, h, room), frames: make(chan frame, 4096)}
	go func() {
		defer close(c.frames)
		for {
			raw, err := c.conn.Receive()
			if err != nil {
				return
			}
			f := decode(raw)
			if f.typ == tPing {
				_ = c.conn.Send(encode(frame{typ: tPing}))
				continue
			}
			c.frames <- f
		}
	}()
	return c
}

func (c *client) send(f frame) {
	c.t.Helper()
	if err := c.conn.Send(encode(f)); err != nil {
		c.t.Fatalf("send type %d: %v", f.typ, err)
	}
}

func (c *client) expect(typ uint8) frame {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatalf("connection closed while waiting for frame type %d", typ)
		}
		if f.typ != typ {
			c.t.Fatalf("got frame type %d (payload %x), want %d", f.typ, f.payload, typ)
		}
		return f
	case <-time.After(wait):
		c.t.Fatalf("timed out waiting for frame type %d", typ)
	}
	return frame{}
}

func (c *client) expectError(code uint8) {
	c.t.Helper()
	if f := c.expect(tError); len(f.payload) == 0 || f.payload[0] != code {
		c.t.Fatalf("got ERROR %x, want code %d", f.payload, code)
	}
}

// expectClosed waits for the hub to close this connection, ignoring frames
// that were already in flight.
func (c *client) expectClosed() {
	c.t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return
			}
		case <-deadline:
			c.t.Fatal("hub did not close the connection")
		}
	}
}

func joinPayload(role, maxReceivers uint8) []byte { return []byte{1, role, maxReceivers} }

func joinSender(t *testing.T, h *multihub.Hub, room string, maxReceivers uint8) *client {
	t.Helper()
	c := connect(t, h, room)
	c.send(frame{typ: tJoin, payload: joinPayload(roleSender, maxReceivers)})
	if id := ackID(t, c.expect(tJoinAck)); id != senderID {
		t.Fatalf("sender got member ID %d, want %d", id, senderID)
	}
	return c
}

func joinReceiver(t *testing.T, h *multihub.Hub, room string) (*client, uint16) {
	t.Helper()
	c := connect(t, h, room)
	c.send(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})
	return c, ackID(t, c.expect(tJoinAck))
}

// ackID reads memberID from a JOIN_ACK payload: version u8 | memberID u16 | max u8.
func ackID(t *testing.T, f frame) uint16 {
	t.Helper()
	if len(f.payload) < 4 {
		t.Fatalf("JOIN_ACK payload too short: %x", f.payload)
	}
	return binary.LittleEndian.Uint16(f.payload[1:3])
}

func u16(id uint16) []byte { return binary.LittleEndian.AppendUint16(nil, id) }

func roomName(t *testing.T) string { return "multi:" + t.Name() }

func TestDataReachesEveryReceiver(t *testing.T) {
	h := multihub.New(defaultConfig())
	room := roomName(t)
	sender := joinSender(t, h, room, 3)

	var receivers []*client
	for range 3 {
		r, id := joinReceiver(t, h, room)
		if id == senderID {
			t.Fatalf("receiver got the sender's member ID")
		}
		joined := sender.expect(tMemberJoined)
		if got := binary.LittleEndian.Uint16(joined.payload[:2]); got != id {
			t.Fatalf("MEMBER_JOINED for %d, want %d", got, id)
		}
		receivers = append(receivers, r)
	}

	payload := []byte("ciphertext the hub must not touch")
	sender.send(frame{typ: tData, payload: payload})
	for i, r := range receivers {
		f := r.expect(tData)
		if f.from != senderID || !bytes.Equal(f.payload, payload) {
			t.Fatalf("receiver %d got from=%d payload=%q", i, f.from, f.payload)
		}
	}
}

func TestDirectGoesOnlyToItsTarget(t *testing.T) {
	h := multihub.New(defaultConfig())
	room := roomName(t)
	sender := joinSender(t, h, room, 2)
	a, aID := joinReceiver(t, h, room)
	b, bID := joinReceiver(t, h, room)
	sender.expect(tMemberJoined)
	sender.expect(tMemberJoined)

	// Receiver -> sender: the hub fills in from, and to is always the sender.
	a.send(frame{typ: tDirect, to: bID, payload: []byte("done")}) // tries to reach b; must not
	if f := sender.expect(tDirect); f.from != aID || string(f.payload) != "done" {
		t.Fatalf("sender got DIRECT from=%d payload=%q", f.from, f.payload)
	}

	// Sender -> one receiver.
	sender.send(frame{typ: tDirect, to: bID, payload: []byte("session")})
	if f := b.expect(tDirect); string(f.payload) != "session" {
		t.Fatalf("b got DIRECT %q", f.payload)
	}

	// a must see neither DIRECT: its next frame is the DATA sent after them.
	sender.send(frame{typ: tData, payload: []byte("x")})
	a.expect(tData)
	b.expect(tData)
}

func TestReceiverBeforeSenderGetsNoSender(t *testing.T) {
	h := multihub.New(defaultConfig())
	r := connect(t, h, roomName(t))
	r.send(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})
	r.expectError(errNoSender)
}

func TestRoomFull(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxReceivers = 2
	h := multihub.New(cfg)
	room := roomName(t)
	joinSender(t, h, room, 2)
	joinReceiver(t, h, room)
	joinReceiver(t, h, room)

	extra := connect(t, h, room)
	extra.send(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})
	extra.expectError(errRoomFull)
}

func TestRoomLocksAfterFirstData(t *testing.T) {
	h := multihub.New(defaultConfig())
	room := roomName(t)
	sender := joinSender(t, h, room, 3)
	r, _ := joinReceiver(t, h, room)

	sender.send(frame{typ: tData, payload: []byte("first chunk")})
	r.expect(tData) // the hub has certainly processed DATA by now

	late := connect(t, h, room)
	late.send(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})
	late.expectError(errRoomLocked)
}

func TestKickClosesConnectionAndFreesSlot(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxReceivers = 1
	h := multihub.New(cfg)
	room := roomName(t)
	sender := joinSender(t, h, room, 1)
	r, id := joinReceiver(t, h, room)
	sender.expect(tMemberJoined)

	// KICK payload: memberID u16 | reason u8 (0 = refused).
	sender.send(frame{typ: tKick, payload: append(u16(id), 0)})
	r.expectClosed()

	// The only slot is free again, and the new member's DATA arrives normally.
	// (All Pipe connections share one "address", like users behind one NAT.)
	again, _ := joinReceiver(t, h, room)
	sender.send(frame{typ: tData, payload: []byte("x")})
	again.expect(tData)
}

func TestReceiverCannotActAsSender(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    frame
	}{
		{"DATA", frame{typ: tData, payload: []byte("x")}},
		{"KICK", frame{typ: tKick, payload: append(u16(senderID), 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := multihub.New(defaultConfig())
			room := roomName(t)
			joinSender(t, h, room, 2)
			r, _ := joinReceiver(t, h, room)
			r.send(tc.f)
			r.expectError(errBadFrame)
		})
	}
}

func TestSenderLeavingClosesRoom(t *testing.T) {
	h := multihub.New(defaultConfig())
	room := roomName(t)
	sender := joinSender(t, h, room, 2)
	r, _ := joinReceiver(t, h, room)
	sender.expect(tMemberJoined)

	sender.conn.Close()
	r.expectError(errSenderGone)
	r.expectClosed()
}

func TestStalledReceiverIsDroppedOthersFinish(t *testing.T) {
	cfg := defaultConfig()
	cfg.QueueBytes = 64 << 10
	cfg.StallTimeout = 300 * time.Millisecond
	h := multihub.New(cfg)
	room := roomName(t)
	sender := joinSender(t, h, room, 3)
	good1, _ := joinReceiver(t, h, room)
	good2, _ := joinReceiver(t, h, room)

	// The stalled receiver reads its JOIN_ACK and then never reads again.
	stuck := dial(t, h, room)
	if err := stuck.Send(encode(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})); err != nil {
		t.Fatal(err)
	}
	raw, err := stuck.Receive()
	if err != nil {
		t.Fatal(err)
	}
	stuckID := ackID(t, decode(raw))
	for range 3 {
		sender.expect(tMemberJoined)
	}

	// Send ~3x the queue size. Without the stall kick this blocks forever.
	const n = 200
	sent := make(chan error, 1)
	go func() {
		chunk := make([]byte, 1024)
		for range n {
			if err := sender.conn.Send(encode(frame{typ: tData, payload: chunk})); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * cfg.StallTimeout):
		t.Fatal("sender stayed blocked: stalled receiver was never dropped")
	}

	// MEMBER_LEFT payload: memberID u16 | reason u8 (1 = kicked for being slow).
	left := sender.expect(tMemberLeft)
	if want := append(u16(stuckID), 1); !bytes.Equal(left.payload, want) {
		t.Fatalf("MEMBER_LEFT payload %x, want %x", left.payload, want)
	}
	for _, r := range []*client{good1, good2} {
		for range n {
			r.expect(tData)
		}
	}
}

func TestSilentClientIsDroppedAnsweringClientStays(t *testing.T) {
	cfg := defaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.IdleTimeout = 300 * time.Millisecond
	h := multihub.New(cfg)
	room := roomName(t)
	sender := joinSender(t, h, room, 2)
	good, _ := joinReceiver(t, h, room)
	sender.expect(tMemberJoined)

	// The silent receiver reads everything (so it is not "slow") but never
	// sends anything back, like a client that ignores PING or has gone dead.
	silent := dial(t, h, room)
	if err := silent.Send(encode(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})); err != nil {
		t.Fatal(err)
	}
	raw, err := silent.Receive()
	if err != nil {
		t.Fatal(err)
	}
	silentID := ackID(t, decode(raw))
	sender.expect(tMemberJoined)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, err := silent.Receive(); err != nil {
				return
			}
		}
	}()

	// MEMBER_LEFT payload: memberID u16 | reason u8 (0 = left or connection lost).
	left := sender.expect(tMemberLeft)
	if want := append(u16(silentID), 0); !bytes.Equal(left.payload, want) {
		t.Fatalf("MEMBER_LEFT payload %x, want %x", left.payload, want)
	}
	select {
	case <-closed:
	case <-time.After(wait):
		t.Fatal("hub did not close the silent connection")
	}

	// Several idle timeouts have passed, yet the sender and the receiver that
	// answer PINGs are still connected.
	time.Sleep(2 * cfg.IdleTimeout)
	sender.send(frame{typ: tData, payload: []byte("still here")})
	good.expect(tData)
}

func TestReceiverTakingDataIsNotIdle(t *testing.T) {
	cfg := defaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.IdleTimeout = 300 * time.Millisecond
	h := multihub.New(cfg)
	room := roomName(t)
	sender := joinSender(t, h, room, 1)

	// This receiver reads everything but never sends a byte back, not even
	// PING replies. While DATA keeps reaching it, it is clearly alive.
	r := dial(t, h, room)
	if err := r.Send(encode(frame{typ: tJoin, payload: joinPayload(roleReceiver, 0)})); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, err := r.Receive(); err != nil {
				return
			}
		}
	}()
	sender.expect(tMemberJoined)

	// Stream DATA for several idle timeouts.
	for end := time.Now().Add(4 * cfg.IdleTimeout); time.Now().Before(end); {
		sender.send(frame{typ: tData, payload: []byte("chunk")})
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case f := <-sender.frames:
		t.Fatalf("receiver taking DATA was treated as idle: sender got frame type %d", f.typ)
	default:
	}
}

package multihub

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

var (
	errStalled    = errors.New("member stalled")
	errMemberGone = errors.New("member gone")
)

// Frame types for the hub to route traffic
const (
	tJoin         = 1
	tJoinAck      = 2
	tMemberJoined = 3
	tMemberLeft   = 4
	tDirect       = 5
	tData         = 6
	tPing         = 7
	tError        = 8
	tKick         = 9

	errRoomLocked = 1
	errRoomFull   = 2
	errNoSender   = 3
	errKickedSlow = 4
	errBadFrame   = 5
	errBadVersion = 6
	errSenderGone = 7

	roleSender   = 1
	roleReceiver = 2

	senderID = 1

	leftQuit    uint8 = 0 // receiver disconnected on its own
	leftSlow    uint8 = 1 // hub kicked it for stalling
	leftNetwork uint8 = 2 // network error
)

type frame struct {
	typ     uint8
	flag    uint8
	from    uint16
	to      uint16
	payload []byte
}

// Frame helper functions
func encodeFrame(f frame) []byte {
	// 6 = typ + flag + from + to
	b := make([]byte, 6+len(f.payload))
	b[0] = f.typ
	b[1] = 0 // flag = 0 for now
	binary.LittleEndian.PutUint16(b[2:4], f.from)
	binary.LittleEndian.PutUint16(b[4:6], f.to)
	copy(b[6:], f.payload)

	return b
}

func decodeFrame(b []byte) (frame, error) {
	if len(b) < 6 {
		return frame{}, errors.New("frame too short")
	}

	return frame{
		typ:     b[0],
		flag:    b[1],
		from:    binary.LittleEndian.Uint16(b[2:4]),
		to:      binary.LittleEndian.Uint16(b[4:6]),
		payload: b[6:],
	}, nil
}

func makeErrorFrame(code uint8, msg string) []byte {
	// msg is converted into a byte slice and spread out into single bytes
	payload := append([]byte{code}, []byte(msg)...)
	return encodeFrame(frame{typ: tError, payload: payload})
}

// One connected receiver
type member struct {
	id   uint16
	conn Conn

	// When the sender broadcasts data,
	// the hub pushes the data into each member's queue.
	// A dedicated background writer goroutine pulls from queue and writes to conn
	queue chan []byte

	// closed when the writer goroutine exits, signaling that member is disconnected
	gone chan struct{}
}

type room struct {
	// The host
	sender Conn

	members map[uint16]*member

	// Sender alwasy has ID = 1, so this starts at 2
	nextID uint16

	// When the first DATA frame is sent, room is locked from entering
	locked bool

	maxReceivers uint8
}

// Default limits, from docs/contracts.md §2.3. They become flags on
// `croc relay` in M2 (--multi-max-receivers, --multi-queue-mb, --multi-stall-timeout).
const (
	DefaultMaxReceivers = 16
	DefaultQueueBytes   = 8 << 20 // 8 MB
	DefaultStallTimeout = 30 * time.Second
)

// Config holds the hub's limits. A zero field means "use the default".
type Config struct {
	// MaxReceivers caps receivers per room. A sender may ask for fewer in its
	// JOIN, never more. At most 255, because JOIN_ACK carries it in one byte.
	MaxReceivers int

	// QueueBytes is the outgoing buffer per receiver. When any receiver's
	// buffer is full the hub stops reading from the sender (backpressure).
	QueueBytes int

	// StallTimeout is how long a receiver's buffer may stay full before the
	// hub drops that receiver.
	StallTimeout time.Duration
}

type Hub struct {
	cfg   Config
	mu    sync.Mutex
	rooms map[string]*room
}

// New returns a hub with no rooms. Zero Config fields get the defaults above.
func New(cfg Config) *Hub {
	if cfg.MaxReceivers <= 0 {
		cfg.MaxReceivers = DefaultMaxReceivers
	}
	if cfg.MaxReceivers > 255 {
		cfg.MaxReceivers = 255
	}
	if cfg.QueueBytes <= 0 {
		cfg.QueueBytes = DefaultQueueBytes
	}
	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = DefaultStallTimeout
	}
	return &Hub{cfg: cfg, rooms: make(map[string]*room)}
}

func (hub *Hub) Serve(roomName string, c Conn) error {
	// auto close Conn when Serve returns
	defer c.Close()

	// First frame must be JOIN
	raw, err := c.Receive()
	if err != nil {
		return err
	}

	joinFrame, err := decodeFrame(raw)
	// Every payload type needs at least 2 byte
	if err != nil || joinFrame.typ != tJoin || len(joinFrame.payload) < 2 {
		_ = c.Send(makeErrorFrame(errBadFrame, "expected JOIN frame"))
		return errors.New("invalid initial frame")
	}
	if joinFrame.payload[0] != 1 {
		_ = c.Send(makeErrorFrame(errBadVersion, "version not supported"))
		return errors.New("invalid frame version")
	}

	role := joinFrame.payload[1]
	switch role {
	case roleSender:
		// Check if sender's requested receiver count fits with hub's configs, and is a valid join frame
		var maxReceivers uint8 = uint8(hub.cfg.MaxReceivers)
		if len(joinFrame.payload) >= 3 && joinFrame.payload[2] > 0 && int(joinFrame.payload[2]) < hub.cfg.MaxReceivers {
			maxReceivers = joinFrame.payload[2]
		}

		return hub.serveSender(roomName, c, maxReceivers)

	case roleReceiver:
		return hub.serveRecceiver(roomName, c)

	default:
		_ = c.Send(makeErrorFrame(errBadFrame, "unknown role"))
		return errors.New("unknown role")
	}
}

// Serve a sender's connection to the hub
func (hub *Hub) serveSender(roomName string, c Conn, maxReceivers uint8) error {
	hubRoom := &room{
		sender:       c,
		members:      make(map[uint16]*member),
		nextID:       2,
		maxReceivers: maxReceivers,
	}

	// Update the hub's room state
	hub.mu.Lock()
	if _, exists := hub.rooms[roomName]; exists {
		hub.mu.Unlock()
		_ = c.Send(makeErrorFrame(errBadFrame, "room already has a sender"))
		return errors.New("room already has a sender")
	}
	hub.rooms[roomName] = hubRoom
	hub.mu.Unlock()

	// When sender leaves, notify all receivers and delete room
	defer func() {
		hub.mu.Lock()
		delete(hub.rooms, roomName)

		// Copy current room localMembers to local slice
		localMembers := make([]*member, 0, len(hubRoom.members))
		for _, m := range hubRoom.members {
			localMembers = append(localMembers, m)
		}

		// delete all from hub
		hubRoom.members = nil
		hub.mu.Unlock()

		// send msg to room members before closing connection
		for _, m := range localMembers {
			_ = hub.enqueue(m, makeErrorFrame(errSenderGone, "sender disconnected"))
			_ = hub.enqueue(m, nil) // nil = close after this
		}
	}()

	// Reply sender's JOIN_ACK
	ack := encodeFrame(frame{
		typ: tJoinAck,

		// JOIN payload contract: version u8 | memberID u16 | maxReceivers u8
		payload: []byte{1, 1, 0, maxReceivers},
	})
	if err := c.Send(ack); err != nil {
		return err
	}

	// Read loop from Sender
	for {
		raw, err := c.Receive()
		if err != nil {
			return err
		}

		f, err := decodeFrame(raw)
		if err != nil {
			return err
		}

		switch f.typ {
		case tData:
			//Lock room when the first DATA arrive to copy member conns to local
			hub.mu.Lock()
			hubRoom.locked = true
			localMembers := make([]*member, 0, len(hubRoom.members))

			for _, m := range hubRoom.members {
				localMembers = append(localMembers, m)
			}
			hub.mu.Unlock()

			// Fan out DATA to all members
			dataFrame := encodeFrame(frame{typ: tData, from: senderID, payload: f.payload})
			for _, m := range localMembers {
				if err := hub.enqueue(m, dataFrame); errors.Is(err, errStalled) {
					hub.dropMember(hubRoom, m.id, 1, true) // 1 = kicked for being slow
				}
			}

		case tKick:
			// payload: memberID u16 | reason u8
			if len(f.payload) < 2 {
				_ = c.Send(makeErrorFrame(errBadFrame, "bad-frame"))
				continue
			}

			id := binary.LittleEndian.Uint16(f.payload[:2])

			hub.dropMember(hubRoom, id, leftQuit, false) // reason is unused when nofifySender = false

		case tDirect:
			hub.mu.Lock()
			m := hubRoom.members[f.to]
			hub.mu.Unlock()

			if m != nil {
				_ = hub.enqueue(
					m,
					encodeFrame(
						frame{
							typ:     tDirect,
							from:    senderID,
							to:      m.id,
							payload: f.payload,
						},
					))
			}

		case tPing:
			// ignore
		}
	}
}

// Serve a receiver's connection to the hub
func (hub *Hub) serveRecceiver(roomName string, c Conn) error {
	// Admission. Do every check AND the insert under ONE lock, so two
	// receivers racing for the last slot can't both get in, and the sender
	// can't leave halfway through.
	hub.mu.Lock()
	hubRoom := hub.rooms[roomName]
	var code uint8
	switch {
	case hubRoom == nil:
		code = errNoSender
	case hubRoom.locked:
		code = errRoomLocked
	case len(hubRoom.members) >= int(hubRoom.maxReceivers):
		code = errRoomFull
	}

	if code != 0 {
		hub.mu.Unlock()
		_ = c.Send(makeErrorFrame(code, "join refused"))
		return errors.New("join refused")
	}

	m := &member{
		id:    hubRoom.nextID,
		conn:  c,
		queue: make(chan []byte, queueLen(hub.cfg)),
		gone:  make(chan struct{}),
	}
	hubRoom.nextID++
	hubRoom.members[m.id] = m
	hub.mu.Unlock()

	// Writer goroutine: the ONLY place that calls m.conn.Send.
	// nil in the queue = "close after this" (the sender-gone cleanup in serveSender uses it).
	go func() {
		defer close(m.gone)
		defer m.conn.Close()
		for f := range m.queue {
			if f == nil || m.conn.Send(f) != nil {
				return
			}
		}
	}()

	// JOIN_ACK goes through the queue too, so it always arrives before any
	// DATA/DIRECT. Payload: version u8 | memberID u16 | maxReceivers u8
	ack := []byte{1, 0, 0, hubRoom.maxReceivers}
	binary.LittleEndian.PutUint16(ack[1:3], m.id)
	_ = hub.enqueue(m, encodeFrame(frame{typ: tJoinAck, payload: ack}))

	// 4. Tell the sender. Payload: memberID u16 | addrLen u8 | addr
	addr := c.RemoteAddr()
	if len(addr) > 255 {
		addr = addr[:255] // length must fit in one byte
	}
	joined := binary.LittleEndian.AppendUint16(nil, m.id)
	joined = append(joined, byte(len(addr)))
	joined = append(joined, addr...)
	_ = hubRoom.sender.Send(encodeFrame(frame{typ: tMemberJoined, to: senderID, payload: joined}))

	// 5. Read loop: receivers may only send DIRECT (to the sender) and PING.
	for {
		raw, err := c.Receive()
		if err != nil {
			// No-op if a KICK or a stall kick already dropped this member.
			hub.dropMember(hubRoom, m.id, leftQuit, true)
			return err
		}
		f, err := decodeFrame(raw)
		if err != nil {
			_ = hub.enqueue(m, makeErrorFrame(errBadFrame, "frame too short"))
			continue
		}
		switch f.typ {
		case tDirect:
			// The hub sets from/to itself and ignores what the client wrote,
			// so a receiver can't impersonate someone or message another receiver.
			_ = hubRoom.sender.Send(encodeFrame(frame{typ: tDirect, from: m.id, to: senderID, payload: f.payload}))
		case tPing:
			// keep-alive, nothing to do
		default: // DATA, KICK, a second JOIN, unknown types
			_ = hub.enqueue(m, makeErrorFrame(errBadFrame, "not allowed for receivers"))
		}
	}
}

// queueLen turns the byte budget into a channel capacity. DATA frames are
// ~32 KB, so 8 MB ≈ 256 frames.
func queueLen(cfg Config) int {
	n := max(cfg.QueueBytes/(32*1024), 1)
	return n
}

// Func to add encoded Frame, which is []byte, into a member's queue
func (hub *Hub) enqueue(member *member, data []byte) error {
	select {
	case member.queue <- data:
		return nil
	case <-member.gone:
		return errMemberGone
	default:
		// If queue is full, make a timer for stall
		timer := time.NewTimer(hub.cfg.StallTimeout)

		defer timer.Stop()

		select {
		case member.queue <- data:
			return nil
		case <-timer.C:
			return errStalled
		case <-member.gone:
			return errMemberGone
		}
	}
}

// notifySender = false for a sender's KICK: the sender already knows, so no MEMBER_LEFT goes back for it.
//
// For cases: Slow kick (reason 1) nad Receiver disconnected on its own (reason 0)
// We notify the sender with a reason
//
// Deletes m from the room. Only the first caller for a given member gets past
// the check below; later callers return without doing anything.
func (hub *Hub) dropMember(r *room, id uint16, reason uint8, notifySender bool) {
	hub.mu.Lock()
	m, ok := r.members[id]
	if !ok {
		// do nothing if member no longer exists
		// (receiver's read loop or a KICK got there first)
		hub.mu.Unlock()
		return
	}
	delete(r.members, id)
	hub.mu.Unlock()

	m.conn.Close() // makes a blocked m.conn.Send fail, so the writer goroutine exits and closes m.gone

	if !notifySender {
		return
	}
	payload := binary.LittleEndian.AppendUint16(nil, m.id)
	_ = r.sender.Send(encodeFrame(frame{typ: tMemberLeft, to: senderID, payload: append(payload, reason)}))
}

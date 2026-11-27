// Package multihub is the relay side of croc's 1-to-N multi-send mode (owner:
// D, milestone M1a). The relay (src/tcp/multi_room.go) hands every connection
// that asked for a "multi:" room to Hub.Serve.
//
// A room holds one sender (member ID 1) and up to N receivers (IDs 2, 3, ...).
// The hub only routes; it never decrypts anything:
//
//	DATA    sender → every receiver (one upload, N copies)
//	DIRECT  sender ↔ one receiver   (PAKE, session key, ready/done/fail)
//	KICK    sender → hub            (close one receiver)
//	MEMBER_JOINED / MEMBER_LEFT     hub → sender
//
// Every receiver has a bounded outgoing queue drained by its own writer
// goroutine. When a queue is full the hub stops reading from the sender, so
// TCP backpressure slows the sender to the slowest receiver; a receiver whose
// queue stays full for StallTimeout is dropped. Frame layouts live in
// src/multiwire.
package multihub

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"

	log "github.com/schollz/croc/v11/src/logger"
	"github.com/schollz/croc/v11/src/multiwire"
)

var (
	errStalled    = errors.New("member stalled")
	errMemberGone = errors.New("member gone")
)

// The hub's frame codec, frame types, error codes and roles all come from
// src/multiwire, the contract shared with the clients.

// encode wraps multiwire.Encode for frames the hub builds. Each one is either
// tiny (ACK, PING, ERROR, MEMBER_*) or re-wraps a payload that already passed
// multiwire.Decode's MaxFrame check with the same 6-byte header, so Encode
// cannot fail here and the error is safe to drop.
func encode(f multiwire.Frame) []byte {
	b, _ := multiwire.Encode(f)
	return b
}

// errorFrame builds an encoded ERROR frame: code u8 | message.
func errorFrame(code uint8, msg string) []byte {
	return encode(multiwire.ErrorFrame(code, msg))
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
	// Full room name ("multi:<hex>"); log only shortRoom(name).
	name string

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
	DefaultPingInterval = 15 * time.Second
	DefaultIdleTimeout  = 60 * time.Second
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

	// PingInterval is how often the hub sends PING to every client. Clients
	// answer each PING with a PING (contracts.md §2.3), which keeps idle
	// connections alive through NAT and proves the client is still there.
	PingInterval time.Duration

	// IdleTimeout closes a connection the hub has heard nothing from for this
	// long (laptop asleep, Wi-Fi gone: TCP often never reports these).
	// Must be longer than PingInterval.
	IdleTimeout time.Duration
}

// Hub is the set of live multi-send rooms on one relay. It is safe for
// concurrent use: the relay calls Serve once per connection, each in its own
// goroutine.
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
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = DefaultPingInterval
	}
	if cfg.PingInterval >= cfg.IdleTimeout {
		// Otherwise a client that answers every PING could still time out.
		cfg.PingInterval = cfg.IdleTimeout / 4
	}
	return &Hub{cfg: cfg, rooms: make(map[string]*room)}
}

// Serve runs one client connection until it disconnects. The first frame
// must be a JOIN: a sender creates roomName, a receiver joins it. Serve closes
// c before returning; the error says why the connection ended.
func (hub *Hub) Serve(roomName string, c Conn) error {
	// auto close Conn when Serve returns
	defer c.Close()

	// Idle timeout: close c if nothing arrives for IdleTimeout. idleConn
	// resets the timer on every successful Receive, and the close makes the
	// read loops below fail, so their normal cleanup runs.
	timer := time.AfterFunc(hub.cfg.IdleTimeout, func() { c.Close() })
	defer timer.Stop()
	c = idleConn{Conn: c, timer: timer, d: hub.cfg.IdleTimeout}

	// First frame must be JOIN
	raw, err := c.Receive()
	if err != nil {
		return err
	}

	joinFrame, err := multiwire.Decode(raw)
	// Every payload type needs at least 2 byte
	if err != nil || joinFrame.Type != multiwire.TypeJoin || len(joinFrame.Payload) < 2 {
		log.Debugf("multi room %s: join refused from %s: first frame is not a JOIN", shortRoom(roomName), c.RemoteAddr())
		_ = c.Send(errorFrame(multiwire.ErrBadFrame, "expected JOIN frame"))
		return errors.New("invalid initial frame")
	}
	if joinFrame.Payload[0] != multiwire.Version {
		log.Debugf("multi room %s: join refused from %s: version %d", shortRoom(roomName), c.RemoteAddr(), joinFrame.Payload[0])
		_ = c.Send(errorFrame(multiwire.ErrBadVersion, "version not supported"))
		return errors.New("invalid frame version")
	}

	role := multiwire.Role(joinFrame.Payload[1])
	switch role {
	case multiwire.RoleSender:
		// Check if sender's requested receiver count fits with hub's configs, and is a valid join frame
		var maxReceivers uint8 = uint8(hub.cfg.MaxReceivers)
		if len(joinFrame.Payload) >= 3 && joinFrame.Payload[2] > 0 && int(joinFrame.Payload[2]) < hub.cfg.MaxReceivers {
			maxReceivers = joinFrame.Payload[2]
		}

		return hub.serveSender(roomName, c, maxReceivers)

	case multiwire.RoleReceiver:
		return hub.serveReceiver(roomName, c)

	default:
		log.Debugf("multi room %s: join refused from %s: unknown role %d", shortRoom(roomName), c.RemoteAddr(), role)
		_ = c.Send(errorFrame(multiwire.ErrBadFrame, "unknown role"))
		return errors.New("unknown role")
	}
}

// serveSender creates the room, ACKs the sender, then loops reading its
// frames: DATA is fanned out, DIRECT forwarded to one receiver, KICK drops a
// receiver. When the sender leaves, every receiver gets ERROR sender-gone and
// the room is deleted.
func (hub *Hub) serveSender(roomName string, c Conn, maxReceivers uint8) error {
	hubRoom := &room{
		name:         roomName,
		sender:       c,
		members:      make(map[uint16]*member),
		nextID:       2,
		maxReceivers: maxReceivers,
	}

	// Update the hub's room state
	hub.mu.Lock()
	if _, exists := hub.rooms[roomName]; exists {
		hub.mu.Unlock()
		log.Debugf("multi room %s: join refused from %s: room already has a sender", shortRoom(roomName), c.RemoteAddr())
		_ = c.Send(errorFrame(multiwire.ErrBadFrame, "room already has a sender"))
		return errors.New("room already has a sender")
	}
	hub.rooms[roomName] = hubRoom
	hub.mu.Unlock()
	log.Infof("multi room %s created by %s (max %d receivers)", shortRoom(roomName), c.RemoteAddr(), maxReceivers)

	// When sender leaves, notify all receivers and delete room
	defer func() {
		hub.mu.Lock()
		delete(hub.rooms, roomName)
		log.Infof("multi room %s closed (sender left, %d receivers still connected)", shortRoom(roomName), len(hubRoom.members))

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
			_ = hub.enqueue(m, errorFrame(multiwire.ErrSenderGone, "sender disconnected"))
			_ = hub.enqueue(m, nil) // nil = close after this
		}
	}()

	// Reply sender's JOIN_ACK
	ack := encode(multiwire.Frame{
		Type: multiwire.TypeJoinAck,

		// JOIN_ACK payload: version u8 | memberID u16 (= SenderID, little-endian) | maxReceivers u8
		Payload: []byte{multiwire.Version, byte(multiwire.SenderID), byte(multiwire.SenderID >> 8), maxReceivers},
	})
	if err := c.Send(ack); err != nil {
		return err
	}

	// PING the sender every PingInterval until serveSender returns.
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		tick := time.NewTicker(hub.cfg.PingInterval)
		defer tick.Stop()
		ping := encode(multiwire.Frame{Type: multiwire.TypePing})
		for {
			select {
			case <-stopPing:
				return
			case <-tick.C:
				if c.Send(ping) != nil {
					return
				}
			}
		}
	}()

	// Read loop from Sender
	for {
		raw, err := c.Receive()
		if err != nil {
			return err
		}

		f, err := multiwire.Decode(raw)
		if err != nil {
			log.Debugf("multi room %s: bad frame from sender (%d bytes), closing room", shortRoom(roomName), len(raw))
			_ = c.Send(errorFrame(multiwire.ErrBadFrame, "invalid frame size"))
			return err
		}

		switch f.Type {
		case multiwire.TypeData:
			//Lock room when the first DATA arrive to copy member conns to local
			hub.mu.Lock()
			hubRoom.locked = true
			localMembers := make([]*member, 0, len(hubRoom.members))

			for _, m := range hubRoom.members {
				localMembers = append(localMembers, m)
			}
			hub.mu.Unlock()

			// Fan out DATA to all members
			dataFrame := encode(multiwire.Frame{Type: multiwire.TypeData, From: multiwire.SenderID, Payload: f.Payload})
			for _, m := range localMembers {
				if err := hub.enqueue(m, dataFrame); errors.Is(err, errStalled) {
					hub.dropMember(hubRoom, m.id, multiwire.LeftSlow, true)
				}
			}

		case multiwire.TypeKick:
			// Payload: memberID u16 | reason u8
			if len(f.Payload) < 2 {
				_ = c.Send(errorFrame(multiwire.ErrBadFrame, "bad-frame"))
				continue
			}

			id := binary.LittleEndian.Uint16(f.Payload[:2])
			log.Debugf("multi room %s: sender kicked member %d", shortRoom(roomName), id)

			m := hub.removeMember(hubRoom, id)
			if m != nil {
				select {
				// let the receiver read what's left in queue, then close
				case m.queue <- nil:
				//if queue full, kick immediately
				default:
					m.conn.Close()
				}
			}

		case multiwire.TypeDirect:
			hub.mu.Lock()
			m := hubRoom.members[f.To]
			hub.mu.Unlock()

			if m != nil {
				_ = hub.enqueue(
					m,
					encode(
						multiwire.Frame{
							Type:    multiwire.TypeDirect,
							From:    multiwire.SenderID,
							To:      m.id,
							Payload: f.Payload,
						},
					))
			}

		case multiwire.TypePing:
			// ignore
		}
	}
}

// serveReceiver admits a receiver into an existing room, starts its writer
// and PING goroutines, tells the sender (MEMBER_JOINED), then forwards the
// receiver's DIRECT frames to the sender until it disconnects.
func (hub *Hub) serveReceiver(roomName string, c Conn) error {
	// Admission. Do every check AND the insert under ONE lock, so two
	// receivers racing for the last slot can't both get in, and the sender
	// can't leave halfway through.
	hub.mu.Lock()
	hubRoom := hub.rooms[roomName]

	switch {
	case hubRoom == nil:
		hub.mu.Unlock()
		log.Debugf("multi room %s: join refused from %s: no-sender", shortRoom(roomName), c.RemoteAddr())
		_ = c.Send(errorFrame(multiwire.ErrNoSender, "no sender in this room yet"))
		return errors.New("no-sender")
	case hubRoom.locked:
		hub.mu.Unlock()
		log.Debugf("multi room %s: join refused from %s: room-locked", shortRoom(roomName), c.RemoteAddr())
		_ = c.Send(errorFrame(multiwire.ErrRoomLocked, "the transfer already started"))
		return errors.New("room-locked")
	case len(hubRoom.members) >= int(hubRoom.maxReceivers):
		hub.mu.Unlock()
		log.Debugf("multi room %s: join refused from %s: room-full", shortRoom(roomName), c.RemoteAddr())
		_ = c.Send(errorFrame(multiwire.ErrRoomFull, "the sender's receiver count is reached"))
		return errors.New("room-full")
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
	log.Debugf("multi room %s: member %d joined from %s", shortRoom(roomName), m.id, c.RemoteAddr())

	// A delivered DATA frame proves the receiver is alive (see idleConn.touch).
	// Only DATA counts: a dead peer's kernel buffer swallows tiny PINGs for a
	// long time, but DATA fills it quickly and then the stall check takes over.
	touch := func() {}
	if ic, ok := c.(idleConn); ok {
		touch = ic.touch
	}

	// Writer goroutine: the ONLY place that calls m.conn.Send.
	// nil in the queue = "close after this" (the sender-gone cleanup in serveSender uses it).
	go func() {
		defer close(m.gone)
		defer m.conn.Close()
		for f := range m.queue {
			if f == nil || m.conn.Send(f) != nil {
				return
			}
			if f[0] == multiwire.TypeData {
				touch()
			}
		}
	}()

	// JOIN_ACK goes through the queue too, so it always arrives before any
	// DATA/DIRECT. Payload: version u8 | memberID u16 | maxReceivers u8
	ack := []byte{multiwire.Version, 0, 0, hubRoom.maxReceivers}
	binary.LittleEndian.PutUint16(ack[1:3], m.id)
	_ = hub.enqueue(m, encode(multiwire.Frame{Type: multiwire.TypeJoinAck, Payload: ack}))

	// PING this receiver every PingInterval through its queue, until its
	// writer goroutine exits (m.gone). If the queue is full, DATA is flowing
	// and a PING adds nothing, so skip it instead of blocking.
	go func() {
		tick := time.NewTicker(hub.cfg.PingInterval)
		defer tick.Stop()
		ping := encode(multiwire.Frame{Type: multiwire.TypePing})
		for {
			select {
			case <-m.gone:
				return
			case <-tick.C:
				select {
				case m.queue <- ping: // try to put a PING in this receiver's queue
				default: // if the queue is full, give up right now
				}
			}
		}
	}()

	// Tell the sender. Payload: memberID u16 | addrLen u8 | addr
	addr := c.RemoteAddr()
	if len(addr) > 255 {
		addr = addr[:255] // length must fit in one byte
	}
	joined := binary.LittleEndian.AppendUint16(nil, m.id)
	joined = append(joined, byte(len(addr)))
	joined = append(joined, addr...)
	_ = hubRoom.sender.Send(encode(multiwire.Frame{Type: multiwire.TypeMemberJoined, To: multiwire.SenderID, Payload: joined}))

	// Read loop: receivers may only send DIRECT (to the sender) and PING.
	for {
		raw, err := c.Receive()
		if err != nil {
			// No-op if a KICK or a stall kick already dropped this member.
			hub.dropMember(hubRoom, m.id, multiwire.LeftQuit, true)
			// Wake the writer so it exits and closes m.gone. Without this, a
			// writer waiting on an empty queue would wait forever (goroutine
			// leak). If the queue is full, the writer is mid-Send on the
			// closed conn and exits on its own.
			select {
			case m.queue <- nil:
			default:
			}
			return err
		}
		f, err := multiwire.Decode(raw)
		if err != nil {
			log.Debugf("multi room %s: bad frame from member %d (%d bytes), dropping it", shortRoom(roomName), m.id, len(raw))
			_ = hub.enqueue(m, errorFrame(multiwire.ErrBadFrame, "invalid frame size"))

			// We can't use dropMember here cus we want the receiver to receive their error reason first
			// dropMember is used for cases where the receiver is cut (KICK, STALL)
			// -> cant change its code to enqueue
			_ = hub.enqueue(m, nil)
			<-m.gone
			hub.dropMember(hubRoom, m.id, multiwire.LeftNetwork, true)
		}
		switch f.Type {
		case multiwire.TypeDirect:
			// The hub sets from/to itself and ignores what the client wrote,
			// so a receiver can't impersonate someone or message another receiver.
			_ = hubRoom.sender.Send(encode(multiwire.Frame{Type: multiwire.TypeDirect, From: m.id, To: multiwire.SenderID, Payload: f.Payload}))
		case multiwire.TypePing:
			// keep-alive, nothing to do
		default: // DATA, KICK, a second JOIN, unknown types
			_ = hub.enqueue(m, errorFrame(multiwire.ErrBadFrame, "not allowed for receivers"))
		}
	}
}

// queueLen turns the byte budget into a channel capacity. DATA frames are
// ~32 KB, so 8 MB ≈ 256 frames.
func queueLen(cfg Config) int {
	n := max(cfg.QueueBytes/multiwire.ChunkSize, 1)
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
// For cases that requires forced removal of Conn: Slow/stalled (reason 1) nad Receiver disconnected on its own (reason 0)
// We notify the sender with a reason
//
// Deletes m from the room. Only the first caller for a given member gets past
// the check below; later callers return without doing anything.
func (hub *Hub) dropMember(r *room, id uint16, reason uint8, notifySender bool) {
	// remove member from the room on the hub
	m := hub.removeMember(r, id)
	if m == nil {
		return
	}

	if notifySender {
		log.Debugf("multi room %s: member %d left (%s)", shortRoom(r.name), id, leftReasonName(reason))
	} else {
		log.Debugf("multi room %s: member %d left (kicked)", shortRoom(r.name), id)
	}

	m.conn.Close() // makes a blocked m.conn.Send fail, so the writer goroutine exits and closes m.gone

	if !notifySender {
		return
	}
	payload := binary.LittleEndian.AppendUint16(nil, m.id)
	_ = r.sender.Send(encode(multiwire.Frame{Type: multiwire.TypeMemberLeft, To: multiwire.SenderID, Payload: append(payload, reason)}))
}

// helper func to remove the member from a room
func (hub *Hub) removeMember(r *room, id uint16) *member {
	hub.mu.Lock()
	m, ok := r.members[id]
	if !ok {
		// do nothing if member no longer exists
		hub.mu.Unlock()
		return nil
	}
	delete(r.members, id)
	hub.mu.Unlock()

	return m
}

// leftReasonName names a MEMBER_LEFT reason for the log.
func leftReasonName(reason uint8) string {
	switch reason {
	case multiwire.LeftQuit:
		return "quit"
	case multiwire.LeftSlow:
		return "slow: stall timeout"
	case multiwire.LeftNetwork:
		return "network"
	}
	return "unknown"
}

// shortRoom prints only the first 8 hex chars of "multi:<sha256>", so the
// relay log never holds the full room name.
func shortRoom(name string) string {
	name = strings.TrimPrefix(name, "multi:")
	if len(name) > 8 {
		name = name[:8]
	}
	return name
}

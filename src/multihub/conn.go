package multihub

import (
	"net"
	"time"

	"github.com/schollz/croc/v11/src/comm"
	"github.com/schollz/croc/v11/src/multiwire"
)

// Conn is the framed connection the hub serves. It is the same type the
// clients use (multiwire.Conn); the alias keeps the multihub.Conn name from
// docs/contracts.md §5.2.
type Conn = multiwire.Conn

// FromComm adapts a relay connection (already past the relay handshake) to
// Conn, so src/tcp can hand it to Hub.Serve.
func FromComm(c *comm.Comm) Conn { return multiwire.FromComm(c) }

// Pipe returns two Conns joined in memory: whatever one Sends, the other
// Receives. Typical use: give one end to hub.Serve, keep the other as the
// client. It is built from net.Pipe wrapped in comm.Comm, so frames get the
// same "croc" + length framing as on the real relay. net.Pipe has no buffer:
// Send blocks until the other end Receives.
func Pipe() (a, b Conn) {
	x, y := net.Pipe()
	return FromComm(comm.New(x)), FromComm(comm.New(y))
}

// idleConn closes c if nothing arrives for d. Each successful Receive pushes
// the deadline back.
type idleConn struct {
	Conn  // embedded: Send/Close/RemoteAddr pass straight through
	timer *time.Timer
	d     time.Duration
}

func (c idleConn) Receive() ([]byte, error) {
	b, err := c.Conn.Receive()
	if err == nil {
		c.timer.Reset(c.d)
	}
	return b, err
}

// touch pushes the idle deadline back without a Receive. The hub calls it when
// a DATA frame was delivered to this client: a receiver that keeps taking
// DATA is alive even if it never sends anything back.
func (c idleConn) touch() {
	c.timer.Reset(c.d)
}

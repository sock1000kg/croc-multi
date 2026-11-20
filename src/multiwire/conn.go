package multiwire

import "github.com/schollz/croc/v11/src/comm"

// Conn is one framed connection between a client and the hub. Each Send or
// Receive moves exactly one encoded hub frame. Both sides use it: the hub
// serves one Conn per member, and a client's multisession.Link wraps one.
//
// The real implementation is FromComm (a relay socket); tests use
// multihub.Pipe (two Conns joined in memory).
type Conn interface {
	Send(frame []byte) error
	Receive() ([]byte, error)
	Close() error
	RemoteAddr() string // shown to the sender in MEMBER_JOINED
}

// commConn adapts croc's comm.Comm (which already does the "croc" + length
// framing) to Conn.
type commConn struct{ c *comm.Comm }

func (x commConn) Send(b []byte) error      { return x.c.Send(b) }
func (x commConn) Receive() ([]byte, error) { return x.c.Receive() }
func (x commConn) Close() error             { x.c.Close(); return nil }
func (x commConn) RemoteAddr() string       { return x.c.Connection().RemoteAddr().String() }

// FromComm wraps a relay connection that has finished the relay handshake
// (password + room name). Used by the relay (src/tcp) and by multisession.Dial.
func FromComm(c *comm.Comm) Conn { return commConn{c} }

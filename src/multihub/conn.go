package multihub

import (
	"net"
	"time"

	"github.com/schollz/croc/v11/src/comm"
)

// A conenction for multihub logic
type Conn interface {
	Send(b []byte) error
	Receive() ([]byte, error)
	Close() error
	RemoteAddr() string
}

// Wrap over croc's comm.Comm to match the Conn interface used by multihub
// We want to reuse comm.Comm, but through interacting with the Conn interface
type commCon struct {
	c *comm.Comm
}

// Defines commCon's wrapper functions for comm.Comm
func (x commCon) Send(b []byte) error {
	return x.c.Send(b)
}

func (x commCon) Receive() ([]byte, error) {
	return x.c.Receive()
}

func (x commCon) Close() error {
	x.c.Close()
	return nil
}

func (x commCon) RemoteAddr() string {
	return x.c.Connection().RemoteAddr().String()
}

// Pipe tạo cặp Conn nối nhau trong bộ nhớ, dùng cho test và hub trong bộ nhớ.
// Dựng từ net.Pipe() bọc bằng comm.Comm, nên dùng đúng cách đóng khung "croc" của
// relay thật. net.Pipe không có bộ đệm: Send chặn tới khi đầu kia Receive.
//
// Pipe returns two Conns joined in memory: whatever one Sends, the other
// Receives. Typical use: give one end to hub.Serve, keep the other as the client.
func Pipe() (a, b Conn) {
	x, y := net.Pipe()
	return commCon{comm.New(x)}, commCon{comm.New(y)}
}

// idleConn closes c if nothing arrives for d. Each successful Receive pushes
// the deadline back.
type idleConn struct {
	// embedding: Send/Close/RemoteAddr pass straight through
	Conn
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

// FromComm adapts a relay connection (already past the relay handshake) to
// Conn, so src/tcp can hand it to Hub.Serve.
func FromComm(c *comm.Comm) Conn {
	return commCon{c}
}

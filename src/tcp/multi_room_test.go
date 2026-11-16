package tcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/comm"
)

// startMultiRelay runs a relay on a random local port and returns its address.
func startMultiRelay(t *testing.T) string {
	t.Helper()
	listener := newTrackedListener(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = RunWithListener(listener, "pass123", WithCtx(ctx), WithLogLevel("error")) }()
	address := listener.Addr().String()
	waitForRelayPort(t, address)
	return address
}

// joinMulti does the normal relay handshake into a "multi:" room, then sends
// a hub JOIN (type 1, payload version | role | maxReceivers) and returns the
// connection plus the hub's first reply.
func joinMulti(t *testing.T, address, room string, role byte) (*comm.Comm, []byte) {
	t.Helper()
	c, _, _, err := ConnectToTCPServer(address, "pass123", room)
	if err != nil {
		t.Fatalf("relay handshake: %v", err)
	}
	t.Cleanup(c.Close)
	join := []byte{1, 0, 0, 0, 0, 0, 1, role, 2} // header (6 bytes) + JOIN payload
	if err := c.Send(join); err != nil {
		t.Fatal(err)
	}
	reply, err := c.ReceiveWithDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("no reply to JOIN: %v", err)
	}
	return c, reply
}

func TestRelayHandsMultiRoomsToHub(t *testing.T) {
	address := startMultiRelay(t)
	room := MultiRoomPrefix + "test-room"

	sender, ack := joinMulti(t, address, room, 1)
	if ack[0] != 2 { // JOIN_ACK
		t.Fatalf("sender got frame type %d, want JOIN_ACK", ack[0])
	}
	receiver, ack := joinMulti(t, address, room, 2)
	if ack[0] != 2 {
		t.Fatalf("receiver got frame type %d, want JOIN_ACK", ack[0])
	}

	// DATA from the sender reaches the receiver through the real relay.
	data := []byte{6, 0, 0, 0, 0, 0, 'h', 'i'}
	if err := sender.Send(data); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := receiver.ReceiveWithDeadline(time.Now().Add(5 * time.Second))
		if err != nil {
			t.Fatalf("receiver got no DATA: %v", err)
		}
		if frame[0] == 7 { // PING
			continue
		}
		if frame[0] != 6 || binary.LittleEndian.Uint16(frame[2:4]) != 1 || !bytes.Equal(frame[6:], []byte("hi")) {
			t.Fatalf("receiver got %x, want DATA \"hi\" from member 1", frame)
		}
		return
	}
}

func TestRelayMultiRoomReportsMissingSender(t *testing.T) {
	address := startMultiRelay(t)
	_, reply := joinMulti(t, address, MultiRoomPrefix+"empty-room", 2)
	if reply[0] != 8 || reply[6] != 3 { // ERROR no-sender
		t.Fatalf("got %x, want ERROR no-sender", reply)
	}
}

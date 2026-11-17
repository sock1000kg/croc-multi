package tcp

import (
	"strings"
	"time"

	"github.com/schollz/croc/v11/src/comm"
	"github.com/schollz/croc/v11/src/multihub"
)

// Multi-send rooms (docs/contracts.md §1). A room whose name starts with
// MultiRoomPrefix is not paired 1-1 like a normal croc room: its connection is
// handed to the multi-send hub, which holds one sender and up to N receivers.
const MultiRoomPrefix = "multi:"

// DEFAULT_MULTI_JOIN_RATE is the joins per minute allowed per source IP (and
// per room) for multi-send rooms. It replaces the stricter 1-1 limits for
// these rooms, because several receivers may share one IP behind a NAT.
const DEFAULT_MULTI_JOIN_RATE = 120

func isMultiRoom(room string) bool {
	return strings.HasPrefix(room, MultiRoomPrefix)
}

// serveMultiRoom runs a connection that asked for a "multi:" room. It is
// called after the relay handshake (password and room name) succeeded, and
// returns when the hub is done with the connection.
func (s *server) serveMultiRoom(c *comm.Comm, room string, sendAdmission func(string) error) error {
	s.multiAdmissionOnce.Do(func() {
		s.multiAdmission = newAdmissionLimiter(s.multiJoinRate, s.multiJoinRate, time.Minute)
	})
	if !s.multiAdmission.allow(canonicalSource(c.Connection().RemoteAddr()), room) {
		if err := sendAdmission("rate limited"); err != nil {
			return err
		}
		return ErrAdmissionLimited
	}
	// Same confirmation a 1-1 room gets, so ConnectToTCPServerControlContext
	// returns successfully. From here on the client speaks hub frames (JOIN...).
	if err := sendAdmission("ok"); err != nil {
		return err
	}
	return s.multiHub.Serve(room, multihub.FromComm(c))
}

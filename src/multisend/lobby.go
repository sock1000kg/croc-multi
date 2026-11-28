package multisend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

// lobbyState is phase 1 of sendSession: it tracks who joined and who is
// ready, until opts.Receivers receivers are ready.
//
// Only sendSession's goroutine calls its methods, so ready and pending need
// no lock. The per-receiver goroutines started by startAuth only report back
// on results.
type lobbyState struct {
	ctx     context.Context // the session context (not the lobby timeout)
	link    *multisession.Link
	code    string
	session multisession.Session
	ap      *approver // nil when --approve is off

	events  <-chan multiwire.Frame // MEMBER_JOINED/LEFT, pumped from the link
	results chan peerResult        // per-receiver goroutines report here
	report  func(PeerProgress)     // progress events for the CLI (never nil)

	ready   map[uint16]bool // authenticated and said "ready"
	pending map[uint16]bool // joined, PAKE or approval in progress

	// authFailures counts wrong-code attempts (multisession.ErrAuthFailed).
	// Each is one online guess at the code's PAKE part, and unlike 1-1 croc a
	// failed receiver can simply rejoin, so the lobby gives up after
	// maxAuthFailures (see that constant).
	authFailures int
}

// maxAuthFailures is how many wrong-code attempts one session tolerates.
//
// Why a cap: a generated code is three EFF short words. The first picks the
// room (one of 1296, so a room is easy to find); the last two are the PAKE
// password, 1296² ≈ 1.7 million choices. PAKE allows no offline guessing, but
// every receiver that joins gets one online guess, and here it can rejoin as
// often as it likes while the lobby is open (10 minutes by default, forever
// with --timeout 0). Ten guesses give a success chance of about 6 in a
// million; then the sender stops and the user starts over with a new code.
// Real users rarely mistype more than once or twice.
const maxAuthFailures = 10

// run is the lobby loop. It returns nil when 'want' receivers are ready, the
// lobby timeout error when lobby ends first, or the error that stopped the
// link. The hub enforces the receiver cap (room-full), so at most 'want'
// receivers can be here at once.
func (lb *lobbyState) run(lobby context.Context, eventErr <-chan error, want int) error {
	for {
		if len(lb.ready) >= want {
			// Looks full: re-check the events already waiting in queue (see
			// drainEvents). If one was a ready receiver leaving, continue the
			// lobby instead of stopping the it.
			if err := lb.drainEvents(); err != nil {
				return err
			}
			if len(lb.ready) >= want {
				return nil
			}
		}
		select {
		case f := <-lb.events:
			if err := lb.handleEvent(f); err != nil {
				return err
			}
		case r := <-lb.results:
			// Count a wrong code even if that receiver already left: a
			// guesser that disconnects right after its attempt must not
			// escape the cap.
			if errors.Is(r.err, multisession.ErrAuthFailed) {
				lb.authFailures++
			}
			if lb.pending[r.id] {
				delete(lb.pending, r.id)
				if r.err == nil {
					lb.ready[r.id] = true
					lb.report(PeerProgress{ID: r.id, Name: r.name, State: "ready"})
				} else {
					// Wrong code, refused, or never ready: free its slot.
					kick(lb.link, r.id)
					lb.report(PeerProgress{ID: r.id, Name: r.name, State: "rejected", Reason: rejectReason(r.err)})
				}
			} // else it left mid-PAKE; MEMBER_LEFT already freed and reported it
			if lb.authFailures >= maxAuthFailures {
				return fmt.Errorf("stopped after %d wrong-code attempts; send again with a new code", lb.authFailures)
			}
		case e := <-eventErr:
			return e
		case <-lobby.Done():
			if lb.ctx.Err() != nil {
				return lb.ctx.Err()
			}
			return fmt.Errorf("Timed out waiting for receivers: %d/%d ready", len(lb.ready), want)
		}
	}
}

// handleEvent applies one hub event (MEMBER_JOINED/LEFT) to the lobby state.
// Both run and drainEvents use it.
func (lb *lobbyState) handleEvent(f multiwire.Frame) error {
	switch f.Type {
	case multiwire.TypeMemberJoined:
		id, host, err := parseMemberJoined(f.Payload)
		if err != nil {
			return err
		}
		lb.pending[id] = true
		lb.report(PeerProgress{ID: id, Addr: host, State: "joined"})
		lb.startAuth(id, host)
	case multiwire.TypeMemberLeft:
		if len(f.Payload) >= 2 {
			id := binary.LittleEndian.Uint16(f.Payload)
			if lb.ready[id] || lb.pending[id] {
				lb.report(PeerProgress{ID: id, State: "left"})
			}
			delete(lb.ready, id)
			delete(lb.pending, id)
		}
	}
	return nil
}

// drainEvents handles every event that is already waiting, without blocking.
//
// Why: run's select picks at random when a result and an event are both
// waiting. So receiver B's "ready" can be counted before the MEMBER_LEFT of
// receiver A that arrived earlier: ready then looks full although A is gone,
// and the transfer would start one receiver short (A failing as
// "disconnected"). Applying the queued events before trusting len(ready)
// closes that window. A leave that arrives after this point is an ordinary
// mid-transfer disconnect.
func (lb *lobbyState) drainEvents() error {
	for {
		select {
		case f := <-lb.events:
			if err := lb.handleEvent(f); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// kickPending KICKs every receiver still pending when the lobby closes (its
// PAKE or approval prompt was still running, e.g. it took the slot of a
// receiver that just left). It never gets the dataKey, so it cannot decrypt
// anything, but the hub would copy every DATA frame to it, and a receiver
// that is not reading slows the fan-out for everyone. Its late result is
// ignored in phase 3, since it is not in ready.
func (lb *lobbyState) kickPending() {
	for id := range lb.pending {
		kick(lb.link, id)
		lb.report(PeerProgress{ID: id, State: "rejected", Reason: "lobby closed"})
	}
}

// startAuth runs PAKE (and the approval prompt, if on) with receiver id in its
// own goroutine, then waits for its "ready". The outcome goes to results.
//
// The hook is always installed, even without --approve: AuthorizeReceiver
// calls it (in this goroutine) once PAKE has succeeded, which is the only
// moment the receiver's sanitized name is known. The name is passed on with
// the result, so the CLI can show who is ready.
func (lb *lobbyState) startAuth(id uint16, host string) {
	go func() {
		var name string
		hook := func(info multisession.ReceiverInfo) bool {
			// AuthorizeReceiver fills in ID and the sanitized Name.
			name = info.Name
			if lb.ap == nil {
				return true
			}
			info.Addr = host
			return lb.ap.approve(info)
		}
		err := authorizeReceiver(lb.ctx, lb.link, lb.code, id, lb.session, hook)
		lb.results <- peerResult{id: id, name: name, err: err}
	}()
}

// rejectReason is a short reason for a lobby rejection, shown by the CLI.
func rejectReason(err error) string {
	switch {
	case errors.Is(err, multisession.ErrAuthFailed):
		return "wrong code"
	case errors.Is(err, multisession.ErrRefused):
		return "refused"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	default:
		return err.Error()
	}
}

// authorizeReceiver is one receiver's lobby: PAKE within authTimeout, then
// its "ready" within another authTimeout.
func authorizeReceiver(ctx context.Context, link *multisession.Link, code string, id uint16, session multisession.Session, hook func(multisession.ReceiverInfo) bool) error {
	authCtx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	if err := multisession.AuthorizeReceiver(authCtx, link, code, id, session, hook); err != nil {
		return err
	}
	// The receiver answers "ready" once it has checked the manifest and can
	// write every file. Give it authTimeout for that. Not authCtx, because
	// approval may have used that up.
	readyCtx, cancelReady := context.WithTimeout(ctx, authTimeout)
	defer cancelReady()
	frame, err := link.RecvPeer(readyCtx, id)
	if err != nil {
		return err
	}
	var msg struct {
		Type string `json:"t"`
	}
	if err = json.Unmarshal(frame.Payload, &msg); err != nil {
		return err
	}
	if msg.Type != "ready" {
		return errors.New("receiver did not become ready")
	}
	return nil
}

// parseMemberJoined reads a MEMBER_JOINED payload (memberID u16 | addrLen u8 |
// addr) and returns the member ID and the host part of its address. The port
// is dropped: a receiver that reconnects gets a new one, and approval
// remembers refusals by host.
func parseMemberJoined(p []byte) (id uint16, host string, err error) {
	if len(p) < 3 || len(p) != 3+int(p[2]) {
		return 0, "", errors.New("invalid member joined")
	}
	addr := string(p[3:])
	host, _, err = net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port (e.g. an in-memory pipe)
	}
	return binary.LittleEndian.Uint16(p), host, nil
}

// kick tells the hub to close receiver id and free its slot.
// KICK payload: memberID u16 | reason u8
func kick(link *multisession.Link, id uint16) {
	payload := binary.LittleEndian.AppendUint16(nil, id)
	payload = append(payload, 0)
	_ = link.Send(multiwire.Frame{Type: multiwire.TypeKick, Payload: payload})
}

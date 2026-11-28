package multisend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/schollz/croc/v11/src/codephrase"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

// authTimeout bounds one receiver's PAKE exchange, so a receiver that joins
// and goes quiet cannot hold its slot in the lobby forever.
const authTimeout = 10 * time.Second

// peerResult is what a per-receiver goroutine reports back to sendSession:
// in the lobby, whether the receiver authenticated and became ready; during
// the transfer, whether it finished (err == nil) or failed.
type peerResult struct {
	id   uint16
	name string // sanitized receiver name, set by lobby results once PAKE passed
	err  error
}

// errDisconnected is a ready receiver's result when the hub says it left.
var errDisconnected = errors.New("disconnected")

// controlMessage is the JSON a receiver sends in DIRECT frames after it has
// the session: ready, progress, done or fail (see multirecv/stream.go).
type controlMessage struct {
	Type   string `json:"t"`
	OK     bool   `json:"ok"`
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
}

// Send is the whole sender flow behind `croc send --receivers N FILE...`:
// build the manifest and a fresh dataKey, join the relay's hub as sender,
// then hand over to sendSession. It returns nil only if all N receivers
// reported "done".
func Send(ctx context.Context, opts SendOptions) error {
	// 255: the receiver count travels as one byte in JOIN.
	// The file count limit is checked in buildManifest, after folders are
	// expanded.
	if opts.Receivers < 1 || opts.Receivers > 255 || len(opts.Files) == 0 {
		return errors.New("invalid files or receiver count")
	}

	if opts.ThrottleBps != 0 {
		return errors.New("multi throttling is not yet supported")
	}
	code := opts.Code
	var err error
	if code == "" {
		code, err = codephrase.Generate()
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "Multi-send code: %s\n", code)
	// One random key for the whole session: every DATA record is encrypted
	// with it exactly once. Receivers get it only after authenticating.
	session := multisession.Session{DataKey: make([]byte, 32), ChunkSize: multiwire.ChunkSize}
	if _, err = rand.Read(session.DataKey); err != nil {
		return err
	}
	// The manifest (manifest.go): files and folders expanded into relative
	// paths like "photos/2024/a.jpg", plus the empty folders to create.
	// Built before connecting, so a bad path fails without touching the relay.
	files, sources, emptyDirs, total, err := buildManifest(ctx, opts.Files, os.Stderr)
	if err != nil {
		return err
	}
	session.Files, session.EmptyDirs, session.TotalSize = files, emptyDirs, total
	// From here on opts.Files is the expanded list: sendSession hands it to
	// StreamFiles, whose fileIdx must be each file's Idx in the manifest.
	opts.Files = sources

	link, err := multisession.Dial(ctx, opts.Relay, opts.RelayPass, code, multiwire.RoleSender, opts.Receivers)
	if err != nil {
		return err
	}
	defer link.Close()

	// Cancelling ctx (Ctrl+C, --timeout) closes the link, which unblocks any
	// pending read or write for sendSession().
	stop := context.AfterFunc(ctx, func() { link.Close() })

	// If AfterFunc callback isn't needed (normal route without cancellation), unregisters it
	defer stop()

	return sendSession(ctx, link, code, session, opts)
}

// sendSession runs a joined link in three phases:
//
//  1. Lobby: for every MEMBER_JOINED start a goroutine that runs PAKE with that
//     receiver and waits for its "ready". A receiver that fails is KICKed.
//     The phase ends when opts.Receivers receivers are ready.
//  2. Transfer: start one goroutine per ready receiver that watches for its
//     done/fail/disconnect, then StreamFiles sends every record as DATA.
//  3. Wait: collect a result from every ready receiver.
//
// All goroutines report on the results channel, so only this function's
// goroutine touches the ready/pending maps.
func sendSession(ctx context.Context, link *multisession.Link, code string, session multisession.Session, opts SendOptions) error {
	// report serializes every Progress call: the lobby, the per-receiver
	// watchers and this goroutine all report.
	var progressMu sync.Mutex
	report := func(p PeerProgress) {
		if opts.Progress != nil {
			progressMu.Lock()
			defer progressMu.Unlock()
			opts.Progress(p)
		}
	}

	results := make(chan peerResult, 256) // ≥ max receivers, so reporters never block
	events := make(chan multiwire.Frame, 128)
	eventErr := make(chan error, 1)
	// Pump the link's event queue (MEMBER_JOINED/LEFT, ERROR) into a channel
	// so the loops below can select on it together with results and ctx.
	go func() {
		for {
			f, e := link.RecvContext(ctx)
			if e != nil {
				eventErr <- e
				return
			}
			select {
			case events <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	// Phase 1: lobby (lobby.go). It ends when opts.Receivers receivers are
	// ready, or when --timeout runs out (it covers only this phase).
	lobby := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		lobby, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	// Approval prompts end with the lobby, so none is left waiting on stdin.
	promptCtx, endPrompts := context.WithCancel(lobby)
	defer endPrompts()

	lb := &lobbyState{
		ctx: ctx, link: link, code: code, session: session,
		events: events, results: results, report: report,
		ready: map[uint16]bool{}, pending: map[uint16]bool{},
		guesses: newGuessLimiter(),
	}
	if opts.Approve {
		lb.ap = &approver{ctx: promptCtx, ask: askApproval, refused: map[string]bool{}}
	}
	err := lb.run(lobby, eventErr, opts.Receivers)
	endPrompts()
	if err != nil {
		return err
	}

	// kickPending KICKs any receiver still mid-PAKE when the lobby ends.
	// Normally there is none.
	// It only happens if a ready receiver left and a new one took its slot just
	// before the MEMBER_LEFT reaches the sender (the case drainEvents can't see).
	// That receiver never gets the dataKey, so without the KICK it would only take DATA it can't use until the hub
	// drops it as stalled.
	lb.kickPending()
	ready := lb.ready

	// "sending" starts the CLI's clock (phan-cong.md, "Định dạng chung"). It
	// goes out before the watchers start, so any done/failed/left the CLI
	// sees belongs to the transfer, not to the lobby.
	report(PeerProgress{State: "sending", Total: session.TotalSize})

	// Phase 2: one watcher per ready receiver. Each ends with exactly one
	// result: done, fail, or disconnected (MEMBER_LEFT, read error). It also
	// reports that end right away, so the CLI shows a receiver that dropped
	// out while the upload is still running, and times each receiver on its
	// own "done".
	finish := func(id uint16, e error) {
		switch {
		case e == nil:
			report(PeerProgress{ID: id, State: "done"})
		case errors.Is(e, errDisconnected):
			report(PeerProgress{ID: id, State: "left", Reason: e.Error()})
		default:
			report(PeerProgress{ID: id, State: "failed", Reason: e.Error()})
		}
		results <- peerResult{id: id, err: e}
	}
	for id := range ready {
		go func(id uint16) {
			for {
				f, e := link.RecvPeer(ctx, id)
				if e != nil {
					finish(id, e)
					return
				}
				if f.Type == multiwire.TypeMemberLeft {
					finish(id, errDisconnected)
					return
				}
				var m controlMessage
				if e = json.Unmarshal(f.Payload, &m); e != nil {
					finish(id, e)
					return
				}
				switch m.Type {
				case "progress":
					report(PeerProgress{ID: id, Bytes: m.Bytes, State: "receiving"})
				case "done":
					if !m.OK {
						e = errors.New("receiver did not finish")
					} else {
						report(PeerProgress{ID: id, Bytes: m.Bytes, State: "receiving"})
					}
					finish(id, e)
					return
				case "fail":
					finish(id, fmt.Errorf("receiver failed: %s", m.Reason))
					return
				}
			}
		}(id)
	}
	// The upload: each record is encrypted once and sent once as DATA; the
	// hub copies it to every receiver. sent is the "uploaded" byte count.
	sent, err := StreamFiles(ctx, opts.Files, session.DataKey, func(data []byte) error { return link.Send(multiwire.Frame{Type: multiwire.TypeData, Payload: data}) })
	report(PeerProgress{State: "uploaded", Bytes: sent, Total: session.TotalSize})
	if err != nil {
		return err
	}
	if opts.Progress == nil {
		fmt.Fprintf(os.Stderr, "Multi-send uploaded %d bytes\n", sent)
	}
	// Phase 3: wait for every ready receiver's result. Results from receivers
	// that were never ready (late lobby PAKEs) are ignored.
	var failures []error
	for len(ready) > 0 {
		select {
		case r := <-results:
			if !ready[r.id] {
				continue
			}
			delete(ready, r.id)
			if r.err != nil { // already reported by finish
				failures = append(failures, fmt.Errorf("receiver %d: %w", r.id, r.err))
			}
		case <-events: // MEMBER_LEFT also reaches the watcher; nothing to do here
		case e := <-eventErr:
			return e
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.Join(failures...) // nil if every receiver was done
}

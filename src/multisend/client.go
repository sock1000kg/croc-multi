package multisend

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	id  uint16
	err error
}

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
	if opts.Receivers < 1 || opts.Receivers > 255 || len(opts.Files) == 0 || len(opts.Files) > 65536 {
		return errors.New("invalid files or receiver count")
	}
	if opts.Approve {
		return errors.New("multi approval is not yet supported")
	}
	if opts.ThrottleBps != 0 {
		return errors.New("multi throttling is not yet supported")
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
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
	// The manifest: Idx is the fileIdx StreamFiles puts in each record, so it
	// must be the position in opts.Files. Only regular files for now (no
	// folders); the receiver sees base names only.
	for i, name := range opts.Files {
		info, e := os.Stat(name)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", name)
		}
		session.Files = append(session.Files, multisession.FileEntry{Idx: i, Path: filepath.Base(name), Size: info.Size()})
		session.TotalSize += info.Size()
	}
	link, err := multisession.Dial(ctx, opts.Relay, opts.RelayPass, code, multiwire.RoleSender, opts.Receivers)
	if err != nil {
		return err
	}
	defer link.Close()
	// Cancelling ctx (Ctrl+C, --timeout) closes the link, which unblocks any
	// pending read or write.
	stop := context.AfterFunc(ctx, func() { link.Close() })
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
	ready := map[uint16]bool{}   // authenticated and said "ready"
	pending := map[uint16]bool{} // joined, PAKE in progress
	// Phase 1: lobby. The hub enforces the receiver cap (room-full), so at
	// most opts.Receivers can be here at once.
	for len(ready) < opts.Receivers {
		select {
		case f := <-events:
			switch f.Type {
			case multiwire.TypeMemberJoined:
				if len(f.Payload) < 2 {
					return errors.New("invalid member joined")
				}
				// MEMBER_JOINED payload: memberID u16 | addrLen u8 | addr
				id := binary.LittleEndian.Uint16(f.Payload)
				pending[id] = true
				go func() {
					authCtx, cancel := context.WithTimeout(ctx, authTimeout)
					defer cancel()
					e := multisession.AuthorizeReceiver(authCtx, link, code, id, session, nil)
					if e == nil {
						// The receiver answers "ready" once it has checked the
						// manifest and can write every file.
						var frame multiwire.Frame
						frame, e = link.RecvPeer(ctx, id)
						if e == nil {
							var msg struct {
								Type string `json:"t"`
							}
							e = json.Unmarshal(frame.Payload, &msg)
							if e == nil && msg.Type != "ready" {
								e = errors.New("receiver did not become ready")
							}
						}
					}
					results <- peerResult{id, e}
				}()
			case multiwire.TypeMemberLeft:
				if len(f.Payload) >= 2 {
					id := binary.LittleEndian.Uint16(f.Payload)
					delete(ready, id)
					delete(pending, id)
				}
			}
		case result := <-results:
			delete(pending, result.id)
			if result.err == nil {
				ready[result.id] = true
				report(PeerProgress{ID: result.id, State: "ready"})
			} else {
				// Wrong code, refused, or never ready: free its slot.
				// KICK payload: memberID u16 | reason u8
				payload := binary.LittleEndian.AppendUint16(nil, result.id)
				payload = append(payload, 0)
				_ = link.Send(multiwire.Frame{Type: multiwire.TypeKick, Payload: payload})
			}
		case e := <-eventErr:
			return e
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Phase 2: one watcher per ready receiver. Each ends with exactly one
	// result: done, fail, or disconnected (MEMBER_LEFT, read error).
	for id := range ready {
		go func(id uint16) {
			for {
				f, e := link.RecvPeer(ctx, id)
				if e != nil {
					results <- peerResult{id, e}
					return
				}
				if f.Type == multiwire.TypeMemberLeft {
					results <- peerResult{id, errors.New("disconnected")}
					return
				}
				var m controlMessage
				if e = json.Unmarshal(f.Payload, &m); e != nil {
					results <- peerResult{id, e}
					return
				}
				switch m.Type {
				case "progress":
					report(PeerProgress{ID: id, Bytes: m.Bytes, State: "receiving"})
				case "done":
					if !m.OK {
						e = errors.New("receiver did not finish")
					}
					results <- peerResult{id, e}
					return
				case "fail":
					results <- peerResult{id, fmt.Errorf("receiver failed: %s", m.Reason)}
					return
				}
			}
		}(id)
	}
	// The upload: each record is encrypted once and sent once as DATA; the
	// hub copies it to every receiver. sent is the "uploaded" byte count.
	sent, err := StreamFiles(ctx, opts.Files, session.DataKey, func(data []byte) error { return link.Send(multiwire.Frame{Type: multiwire.TypeData, Payload: data}) })
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Multi-send uploaded %d bytes\n", sent)
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
			if r.err != nil {
				failures = append(failures, fmt.Errorf("receiver %d: %w", r.id, r.err))
				report(PeerProgress{ID: r.id, State: "failed", Reason: r.err.Error()})
			} else {
				report(PeerProgress{ID: r.id, State: "done"})
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

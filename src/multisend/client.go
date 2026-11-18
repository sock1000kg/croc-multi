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

type peerResult struct {
	id  uint16
	err error
}

func Send(ctx context.Context, opts SendOptions) error {
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
	session := multisession.Session{DataKey: make([]byte, 32), ChunkSize: multiwire.ChunkSize}
	if _, err = rand.Read(session.DataKey); err != nil {
		return err
	}
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
	stop := context.AfterFunc(ctx, func() { link.Close() })
	defer stop()
	return sendSession(ctx, link, code, session, opts)
}
func sendSession(ctx context.Context, link *multisession.Link, code string, session multisession.Session, opts SendOptions) error {
	var progressMu sync.Mutex
	report := func(p PeerProgress) {
		if opts.Progress != nil {
			progressMu.Lock()
			defer progressMu.Unlock()
			opts.Progress(p)
		}
	}
	results := make(chan peerResult, 256)
	events := make(chan multiwire.Frame, 128)
	eventErr := make(chan error, 1)
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
	ready := map[uint16]bool{}
	pending := map[uint16]bool{}
	for len(ready) < opts.Receivers {
		select {
		case f := <-events:
			switch f.Type {
			case multiwire.TypeMemberJoined:
				if len(f.Payload) < 2 {
					return errors.New("invalid member joined")
				}
				id := binary.LittleEndian.Uint16(f.Payload)
				pending[id] = true
				go func() {
					authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					e := multisession.AuthorizeReceiver(authCtx, link, code, id, session, nil)
					if e == nil {
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
				payload := []byte{byte(result.id), byte(result.id >> 8), 0}
				_ = link.Send(multiwire.Frame{Type: multiwire.TypeKick, Payload: payload})
			}
		case e := <-eventErr:
			return e
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
				var m struct {
					Type   string `json:"t"`
					OK     bool   `json:"ok"`
					Bytes  int64  `json:"bytes"`
					Reason string `json:"reason"`
				}
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
	sent, err := StreamFiles(ctx, opts.Files, session.DataKey, func(data []byte) error { return link.Send(multiwire.Frame{Type: multiwire.TypeData, Payload: data}) })
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Multi-send uploaded %d bytes\n", sent)
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
		case <-events:
		case e := <-eventErr:
			return e
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}

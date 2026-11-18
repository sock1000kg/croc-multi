package multirecv

import (
	"context"
	"fmt"
	"time"

	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

type ReceiveOptions struct {
	Code, OutDir, Relay, RelayPass string
	Progress                       func(bytes, total int64)
}
type sessionStream struct{ link *multisession.Link }

func (s sessionStream) ReceiveData(ctx context.Context) ([]byte, error) {
	f, e := s.link.RecvContext(ctx)
	if e != nil {
		return nil, e
	}
	if f.Type == multiwire.TypeError {
		return nil, fmt.Errorf("hub error: %v", f.Payload)
	}
	if f.Type != multiwire.TypeData {
		return nil, fmt.Errorf("unexpected hub frame %d", f.Type)
	}
	return f.Payload, nil
}
func (s sessionStream) SendControl(ctx context.Context, b []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return s.link.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: 1, Payload: b})
}
func Receive(ctx context.Context, opts ReceiveOptions) error {
	var link *multisession.Link
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		link, err = multisession.Dial(ctx, opts.Relay, opts.RelayPass, opts.Code, multiwire.RoleReceiver, 0)
		if err == nil {
			break
		}
		if !multisession.IsNoSender(err) {
			return err
		}
		if attempt < 5 {
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}
	if err != nil {
		return err
	}
	defer link.Close()
	stop := context.AfterFunc(ctx, func() { link.Close() })
	defer stop()
	session, err := multisession.JoinSession(ctx, link, opts.Code)
	if err != nil {
		return err
	}
	files := make([]FileEntry, len(session.Files))
	for i, f := range session.Files {
		files[i] = FileEntry{Idx: f.Idx, Path: f.Path, Size: f.Size}
	}
	return ReceiveStream(ctx, sessionStream{link}, opts.OutDir, Session{DataKey: session.DataKey, ChunkSize: session.ChunkSize, Files: files, EmptyDirs: session.EmptyDirs, TotalSize: session.TotalSize}, opts.Progress)
}

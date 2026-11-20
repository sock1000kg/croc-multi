package multirecv

import (
	"context"
	"fmt"
	"time"

	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

// No-sender retries: a receiver started a little before the sender keeps
// trying for about (noSenderAttempts-1) × noSenderRetry = 25 s.
const (
	noSenderAttempts = 6
	noSenderRetry    = 5 * time.Second
)

// ReceiveOptions is what `croc --multi CODE` passes to Receive.
type ReceiveOptions struct {
	Code, OutDir, Relay, RelayPass string
	Progress                       func(bytes, total int64) // optional; called after each written chunk
}

// sessionStream adapts a joined multisession.Link to the Stream interface
// ReceiveStream reads from.
type sessionStream struct{ link *multisession.Link }

// ReceiveData returns the next DATA payload. A hub ERROR (e.g. sender-gone)
// becomes a *multiwire.HubError. A receiver gets no other frame types here:
// PING is answered inside the Link, DIRECT goes to the per-peer queue.
func (s sessionStream) ReceiveData(ctx context.Context) ([]byte, error) {
	f, e := s.link.RecvContext(ctx)
	if e != nil {
		return nil, e
	}
	switch f.Type {
	case multiwire.TypeData:
		return f.Payload, nil
	case multiwire.TypeError:
		return nil, multiwire.ParseError(f.Payload)
	default:
		return nil, fmt.Errorf("unexpected hub frame type %d", f.Type)
	}
}

// SendControl sends a ready/progress/done/fail JSON message to the sender.
func (s sessionStream) SendControl(ctx context.Context, b []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return s.link.Send(multiwire.Frame{Type: multiwire.TypeDirect, To: multiwire.SenderID, Payload: b})
}

// Receive is the whole receiver flow: join the room (retrying while the
// sender is not there yet), authenticate and get the session, then receive
// every file into opts.OutDir. It returns nil only if all files arrived and
// matched their hashes.
func Receive(ctx context.Context, opts ReceiveOptions) error {
	var link *multisession.Link
	var err error
	for attempt := range noSenderAttempts {
		link, err = multisession.Dial(ctx, opts.Relay, opts.RelayPass, opts.Code, multiwire.RoleReceiver, 0)
		if err == nil || !multisession.IsNoSender(err) {
			break // joined, or an error that waiting will not fix
		}
		if attempt < noSenderAttempts-1 {
			timer := time.NewTimer(noSenderRetry)
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
	// Cancelling ctx (Ctrl+C) closes the link, which unblocks any pending read.
	stop := context.AfterFunc(ctx, func() { link.Close() })
	defer stop()

	session, err := multisession.JoinSession(ctx, link, opts.Code)
	if err != nil {
		return err
	}
	return ReceiveStream(ctx, sessionStream{link}, opts.OutDir, session, opts.Progress)
}

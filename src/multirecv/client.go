package multirecv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

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
	Name                           string                   // shown to the sender; "" = hostname
	Progress                       func(bytes, total int64) // optional; called after each written chunk
	Status                         func(msg string)         // optional; one status line each (incl. "bench:")
	Joined                         func(Session)            // optional; manifest known, before any DATA
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
	status := opts.Status
	if status == nil {
		status = func(string) {}
	}
	name := opts.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	var link *multisession.Link
	var err error
	for attempt := range noSenderAttempts {
		link, err = multisession.Dial(ctx, opts.Relay, opts.RelayPass, opts.Code, multiwire.RoleReceiver, 0)
		if err == nil || !multisession.IsNoSender(err) {
			break // joined, or an error that waiting will not fix
		}
		if attempt == 0 {
			status("Đang chờ người gửi...")
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

	session, err := multisession.JoinSession(ctx, link, opts.Code, name)
	if err != nil {
		return err
	}
	status("Đã vào phòng, chờ người gửi bắt đầu")
	if opts.Joined != nil {
		opts.Joined(session)
	}
	stats, err := receiveStream(ctx, sessionStream{link}, opts.OutDir, session, opts.Progress)
	if err != nil {
		status(benchLine(name, stats, "failed"))
		return err
	}
	status(fmt.Sprintf("Đã nhận %s trong %.2f s (%.1f Mbit/s)", formatBytes(stats.Bytes), stats.Elapsed.Seconds(), mbps(stats)))
	for _, f := range session.Files {
		if path, e := filepath.Abs(filepath.Join(opts.OutDir, filepath.FromSlash(f.Path))); e == nil {
			status("Đã lưu: " + path)
		}
	}
	status(benchLine(name, stats, "done"))
	return nil
}

// benchLine is the machine-readable result line (phan-cong.md, "Định dạng
// chung"), e.g.
//
//	bench: role=recv name=an-pc bytes=1485169722 seconds=61.90 mbps=191.9 result=done
//
// bytes is what was written: the whole TotalSize on success, less on failure.
func benchLine(name string, s Stats, result string) string {
	return fmt.Sprintf("bench: role=recv name=%s bytes=%d seconds=%.2f mbps=%.1f result=%s",
		benchName(name), s.Bytes, s.Elapsed.Seconds(), mbps(s), result)
}

// mbps is file bytes × 8 / seconds / 1e6, the unit iperf3 and
// scripts/bench/azure.sh use.
func mbps(s Stats) float64 {
	if s.Elapsed <= 0 {
		return 0
	}
	return float64(s.Bytes) * 8 / s.Elapsed.Seconds() / 1e6
}

// benchName keeps a name to one key=value token: spaces and control
// characters become "_", at most 32 runes, "?" if empty.
func benchName(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n == 32 {
			break
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '=' {
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// formatBytes prints a size for people, e.g. "1.38 GiB".
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, 0
	for v >= 1024 && unit < 3 {
		v /= 1024
		unit++
	}
	return fmt.Sprintf("%.2f %s", v, [...]string{"KiB", "MiB", "GiB", "TiB"}[unit])
}

package multirecv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Stream is an authenticated session's data and control transport. ReceiveData
// returns only DATA payloads; the transport handles PING and hub errors.
// The real one is sessionStream (client.go); tests feed recorded frames.
type Stream interface {
	ReceiveData(context.Context) ([]byte, error)
	SendControl(context.Context, []byte) error
}

// controlMessage is the JSON the receiver sends the sender in DIRECT frames
// (contracts.md §3): "ready" (manifest accepted, send DATA), "progress"
// (at most every 500 ms), "done" (all hashes matched) or "fail" (with Reason).
// Pointer fields so that omitempty drops unset ones but keeps a real 0/false.
type controlMessage struct {
	Type   string `json:"t"`
	Files  *int   `json:"files,omitempty"`
	Bytes  *int64 `json:"bytes,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Stats is the timing of one received session (phan-cong.md, "Định dạng
// chung"): the clock starts at the first DATA frame and stops once all-end
// has been accepted (every hash matched), just before "done" is sent. The
// lobby is not counted. On failure it stops at the error.
type Stats struct {
	Bytes   int64         // plaintext file bytes written
	Elapsed time.Duration // 0 if no DATA arrived
}

// ReceiveStream receives an already authenticated session. The stream adapter
// must direct control messages to the sender and unblock on context cancellation.
//
// Order: New (validate manifest) → send "ready" → Accept every DATA payload
// until all-end → send "done". Any error sends "fail" (best effort) and is
// returned; Receiver.Close then deletes unfinished temp files.
func ReceiveStream(ctx context.Context, stream Stream, outDir string, session Session, progress func(int64, int64)) error {
	_, err := receiveStream(ctx, stream, outDir, session, progress)
	return err
}

// receiveStream is ReceiveStream that also reports the timing.
func receiveStream(ctx context.Context, stream Stream, outDir string, session Session, progress func(int64, int64)) (stats Stats, result error) {
	var started time.Time
	stop := func() {
		if !started.IsZero() && stats.Elapsed == 0 {
			stats.Elapsed = time.Since(started)
		}
	}
	send := func(message controlMessage) error {
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		return stream.SendControl(ctx, payload)
	}
	receiver, err := New(outDir, session, progress)
	if err != nil {
		_ = send(controlMessage{Type: "fail", Reason: "write-error"})
		return stats, err
	}
	defer func() {
		stop()
		stats.Bytes = receiver.received
		result = errors.Join(result, receiver.Close())
	}()
	if err = send(controlMessage{Type: "ready"}); err != nil {
		return stats, fmt.Errorf("send ready: %w", err)
	}
	lastProgress := time.Time{}
	for {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		payload, err := stream.ReceiveData(ctx)
		if err != nil {
			return stats, fmt.Errorf("receive data: %w", err)
		}
		if started.IsZero() {
			started = time.Now()
		}
		before := receiver.received
		done, err := receiver.Accept(payload)
		if err != nil {
			reason := "write-error"
			if errors.Is(err, ErrHashMismatch) {
				reason = "hash-mismatch"
			}
			_ = send(controlMessage{Type: "fail", Reason: reason})
			return stats, err
		}
		if done {
			stop()
			ok := true
			if err = send(controlMessage{Type: "done", OK: &ok, Bytes: &receiver.received}); err != nil {
				return stats, fmt.Errorf("send done: %w", err)
			}
			return stats, nil
		}
		if receiver.received != before && time.Since(lastProgress) >= 500*time.Millisecond {
			files := 0
			for _, file := range receiver.files {
				if file.done {
					files++
				}
			}
			if err = send(controlMessage{Type: "progress", Files: &files, Bytes: &receiver.received}); err != nil {
				return stats, fmt.Errorf("send progress: %w", err)
			}
			lastProgress = time.Now()
		}
	}
}

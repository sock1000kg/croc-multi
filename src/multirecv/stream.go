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
type Stream interface {
	ReceiveData(context.Context) ([]byte, error)
	SendControl(context.Context, []byte) error
}

type controlMessage struct {
	Type   string `json:"t"`
	Files  *int   `json:"files,omitempty"`
	Bytes  *int64 `json:"bytes,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ReceiveStream receives an already authenticated session. The stream adapter
// must direct control messages to the sender and unblock on context cancellation.
func ReceiveStream(ctx context.Context, stream Stream, outDir string, session Session, progress func(int64, int64)) (result error) {
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
		return err
	}
	defer func() { result = errors.Join(result, receiver.Close()) }()
	if err = send(controlMessage{Type: "ready"}); err != nil {
		return fmt.Errorf("send ready: %w", err)
	}
	lastProgress := time.Time{}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		payload, err := stream.ReceiveData(ctx)
		if err != nil {
			return fmt.Errorf("receive data: %w", err)
		}
		before := receiver.received
		done, err := receiver.Accept(payload)
		if err != nil {
			reason := "write-error"
			if errors.Is(err, ErrHashMismatch) {
				reason = "hash-mismatch"
			}
			_ = send(controlMessage{Type: "fail", Reason: reason})
			return err
		}
		if done {
			ok := true
			if err = send(controlMessage{Type: "done", OK: &ok, Bytes: &receiver.received}); err != nil {
				return fmt.Errorf("send done: %w", err)
			}
			return nil
		}
		if receiver.received != before && time.Since(lastProgress) >= 500*time.Millisecond {
			files := 0
			for _, file := range receiver.files {
				if file.done {
					files++
				}
			}
			if err = send(controlMessage{Type: "progress", Files: &files, Bytes: &receiver.received}); err != nil {
				return fmt.Errorf("send progress: %w", err)
			}
			lastProgress = time.Now()
		}
	}
}

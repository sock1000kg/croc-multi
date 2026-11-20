package multiwire

import (
	"bytes"
	"testing"
)

func TestFrameRoundTripAndLimits(t *testing.T) {
	want := Frame{Type: TypeDirect, From: 42, To: 7, Payload: []byte("payload")}
	b, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil || got.Type != want.Type || got.From != want.From || got.To != want.To || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err = Decode([]byte{1, 2}); err == nil {
		t.Fatal("accepted short frame")
	}
	if _, err = Encode(Frame{Payload: make([]byte, MaxFrame-5)}); err == nil {
		t.Fatal("accepted oversized frame")
	}
}

func TestHubErrorText(t *testing.T) {
	if got := ParseError([]byte{ErrRoomFull}).Error(); got != "hub: room-full" {
		t.Fatalf("got %q", got)
	}
	if got := ParseError(append([]byte{ErrSenderGone}, "sender disconnected"...)).Error(); got != "hub: sender-gone: sender disconnected" {
		t.Fatalf("got %q", got)
	}
	if got := ParseError(nil).Code; got != ErrBadFrame {
		t.Fatalf("empty payload code %d", got)
	}
}

func TestDecodeRecordRejectsBadSizes(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{RecordKindChunk, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},       // header only, no data
		append(EncodeChunk(0, 0, make([]byte, ChunkSize)), 0), // one byte too long
		append(EncodeFileEnd(0, [32]byte{}), 0),               // trailing byte
		{RecordKindAllEnd, 0},                                 // trailing byte
		{9},                                                   // unknown kind
	} {
		if _, _, _, err := DecodeRecord(b); err == nil {
			t.Fatalf("accepted %x", b)
		}
	}
}

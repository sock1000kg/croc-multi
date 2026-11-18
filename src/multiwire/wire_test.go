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

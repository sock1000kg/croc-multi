package multisend

import (
	"encoding/binary"
	"testing"
)

func memberJoined(id uint16, addr string) []byte {
	p := binary.LittleEndian.AppendUint16(nil, id)
	p = append(p, byte(len(addr)))
	return append(p, addr...)
}

func TestParseMemberJoined(t *testing.T) {
	tests := []struct {
		name     string
		payload  []byte
		wantID   uint16
		wantHost string
	}{
		// The port is dropped, so a reconnect from a new port is the same host.
		{"ipv4 with port", memberJoined(2, "1.2.3.4:5678"), 2, "1.2.3.4"},
		{"same host new port", memberJoined(3, "1.2.3.4:9999"), 3, "1.2.3.4"},
		{"ipv6 with port", memberJoined(4, "[2001:db8::1]:443"), 4, "2001:db8::1"},
		{"no port", memberJoined(5, "pipe"), 5, "pipe"},
		{"empty addr", memberJoined(6, ""), 6, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, host, err := parseMemberJoined(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if id != tt.wantID || host != tt.wantHost {
				t.Fatalf("got (%d, %q), want (%d, %q)", id, host, tt.wantID, tt.wantHost)
			}
		})
	}
}

// A length byte that disagrees with the payload must be an error, not a panic.
func TestParseMemberJoinedMalformed(t *testing.T) {
	bad := map[string][]byte{
		"empty":            nil,
		"id only":          {2, 0},
		"len too long":     {2, 0, 10, 'a', 'b'},
		"len too short":    {2, 0, 1, 'a', 'b'},
		"len 255, no addr": {2, 0, 255},
	}
	for name, p := range bad {
		if _, _, err := parseMemberJoined(p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

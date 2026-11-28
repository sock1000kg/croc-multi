package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multisend"
)

// fakeSendUI is a sendUI writing plain lines (no tty) with a settable clock.
func fakeSendUI(want int) (*sendUI, *bytes.Buffer, *time.Time) {
	var out bytes.Buffer
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	u := &sendUI{w: &out, want: want, peers: map[uint16]*peerView{}, width: func() int { return 0 }}
	u.now = func() time.Time { return now }
	return u, &out, &now
}

func TestSendUIEventsAndBenchLines(t *testing.T) {
	u, out, now := fakeSendUI(2)
	const total = 1485169722
	at := func(d time.Duration) { *now = now.Add(d) }

	u.update(multisend.PeerProgress{ID: 2, Addr: "203.0.113.5", State: "joined"})
	u.update(multisend.PeerProgress{ID: 2, Name: "binh laptop", State: "ready"})
	u.update(multisend.PeerProgress{ID: 3, Addr: "198.51.100.7", State: "joined"})
	u.update(multisend.PeerProgress{ID: 4, Addr: "192.0.2.9", State: "joined"})
	u.update(multisend.PeerProgress{ID: 4, State: "rejected", Reason: "wrong code"})
	u.update(multisend.PeerProgress{ID: 3, Name: "an-pc", State: "ready"})
	u.update(multisend.PeerProgress{State: "sending", Total: total})
	at(30 * time.Second)
	u.update(multisend.PeerProgress{ID: 2, Bytes: total / 2, State: "receiving"})
	u.update(multisend.PeerProgress{ID: 3, Bytes: total / 2, State: "receiving"})
	at(31840 * time.Millisecond)
	u.update(multisend.PeerProgress{ID: 3, State: "done"})
	u.update(multisend.PeerProgress{State: "uploaded", Bytes: 1486955012})
	at(1280 * time.Millisecond)
	u.update(multisend.PeerProgress{ID: 2, State: "done"})
	u.finish(nil)
	// Late events (a watcher after Send returned) must not print.
	u.update(multisend.PeerProgress{ID: 2, State: "failed", Reason: "late"})

	got := out.String()
	for _, want := range []string{
		"[2] 203.0.113.5 đã vào, đang xác thực\n",
		"[2] binh laptop (203.0.113.5) sẵn sàng (1/2)\n",
		"[4] 192.0.2.9 bị loại: sai code\n",
		"[3] an-pc (198.51.100.7) sẵn sàng (2/2)\n",
		"Bắt đầu gửi 1.38 GiB cho 2 người nhận\n",
		"[2] binh laptop 50% 24.8 MB/s | [3] an-pc xong | tải lên 24.8 MB/s\n",
		"[3] an-pc (198.51.100.7) xong: 1.38 GiB trong 61.84 s (192.1 Mbit/s)\n",
		"[2] binh laptop (203.0.113.5) xong: 1.38 GiB trong 63.12 s (188.2 Mbit/s)\n",
		"Tóm tắt: 2/2 xong. Tải lên 1.38 GiB (1.001x) trong 63.12 s (188.2 Mbit/s); chậm nhất [2]\n",
		"bench: role=send-peer id=3 name=an-pc bytes=1485169722 seconds=61.84 mbps=192.1 result=done\n" +
			"bench: role=send-peer id=2 name=binh_laptop bytes=1485169722 seconds=63.12 mbps=188.2 result=done\n" +
			"bench: role=send n=2 done=2 bytes=1485169722 uploaded=1486955012 ratio=1.001 seconds=63.12 mbps=188.2\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q\n--- output ---\n%s", want, got)
		}
	}
	// A log file must stay readable: no carriage returns or escape codes.
	if strings.ContainsAny(got, "\r\x1b") || strings.Contains(got, "late") {
		t.Errorf("unexpected control characters or late event:\n%q", got)
	}
}

func TestSendUIReceiverLostMidTransfer(t *testing.T) {
	u, out, now := fakeSendUI(2)
	u.update(multisend.PeerProgress{ID: 2, Addr: "10.0.0.2", State: "joined"})
	u.update(multisend.PeerProgress{ID: 2, Name: "a", State: "ready"})
	u.update(multisend.PeerProgress{ID: 3, Addr: "10.0.0.3", State: "joined"})
	u.update(multisend.PeerProgress{ID: 3, Name: "b", State: "ready"})
	u.update(multisend.PeerProgress{State: "sending", Total: 1000})
	*now = now.Add(2 * time.Second)
	u.update(multisend.PeerProgress{ID: 3, Bytes: 400, State: "receiving"})
	u.update(multisend.PeerProgress{ID: 3, State: "left", Reason: "disconnected"})
	*now = now.Add(2 * time.Second)
	u.update(multisend.PeerProgress{ID: 2, State: "done"})
	u.update(multisend.PeerProgress{State: "uploaded", Bytes: 1100})
	u.finish(errors.New("receiver 3: disconnected"))

	got := out.String()
	for _, want := range []string{
		"[3] b (10.0.0.3) mất kết nối ở 40%\n",
		"Tóm tắt: 1/2 xong.",
		"  [3] b (10.0.0.3): left (disconnected)\n",
		"bench: role=send-peer id=3 name=b bytes=400 seconds=2.00 mbps=0.0 result=left\n",
		"bench: role=send-peer id=2 name=a bytes=1000 seconds=4.00 mbps=0.0 result=done\n",
		"bench: role=send n=2 done=1 bytes=1000 uploaded=1100 ratio=1.100 seconds=4.00 mbps=0.0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q\n--- output ---\n%s", want, got)
		}
	}
}

// A lobby that never started uploading prints no summary or bench lines,
// and a receiver leaving the lobby frees its place in the count.
func TestSendUILobbyOnly(t *testing.T) {
	u, out, _ := fakeSendUI(2)
	u.update(multisend.PeerProgress{ID: 2, Addr: "10.0.0.2", State: "joined"})
	u.update(multisend.PeerProgress{ID: 2, Name: "a", State: "ready"})
	u.update(multisend.PeerProgress{ID: 2, State: "left"})
	u.update(multisend.PeerProgress{ID: 3, Addr: "10.0.0.3", State: "joined"})
	u.update(multisend.PeerProgress{ID: 3, Name: "b", State: "ready"})
	u.finish(errors.New("Timed out waiting for receivers: 1/2 ready"))
	got := out.String()
	if !strings.Contains(got, "[2] a (10.0.0.2) rời phòng chờ\n") || !strings.Contains(got, "[3] b (10.0.0.3) sẵn sàng (1/2)\n") {
		t.Errorf("unexpected lobby output:\n%s", got)
	}
	if strings.Contains(got, "bench:") || strings.Contains(got, "Tóm tắt") {
		t.Errorf("summary printed for a session that never started:\n%s", got)
	}
}

func TestTruncateWidth(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"tải lên 23.8 MB/s", "tải lê…"}, // Vietnamese letters are one column
		{"abc", "abc"},
		{"中文名字abc", "中文名…"}, // wide runes take two columns
	} {
		got := truncateWidth(tt.in, 7)
		if got != tt.want || displayWidth(got) > 7 {
			t.Errorf("truncateWidth(%q, 7) = %q (width %d), want %q", tt.in, got, displayWidth(got), tt.want)
		}
	}
}

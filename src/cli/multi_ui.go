package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/schollz/croc/v11/src/multisend"
	"github.com/schollz/croc/v11/src/termui"
	"golang.org/x/term"
	"golang.org/x/text/width"
)

// sendUI draws the multi-send sender's output on stderr (phan-cong.md,
// "Giao diện terminal"): one event line per receiver event (joined, ready,
// left, done, ...), one live progress line for every receiver, and at the end
// a summary plus the machine-readable "bench:" lines ("Định dạng chung").
//
// multisend.Send calls update from several goroutines (serialized, but some
// may still arrive after Send returned), and the ticker redraws the progress
// line, so every field is guarded by mu.
type sendUI struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool             // progress line redrawn in place with \r
	width func() int       // terminal width; 0 = unknown
	now   func() time.Time // time.Now; tests replace it

	want  int // --receivers
	peers map[uint16]*peerView

	total    int64     // manifest TotalSize, known at "sending"
	start    time.Time // the upload began; zero while in the lobby
	uploaded int64     // ciphertext bytes sent to the hub, known at "uploaded"

	lineOpen  bool      // the progress line is on screen, not ended by a newline
	lastPlain time.Time // last plain progress line (no tty)
	stopped   bool      // finish ran; later events are ignored
	quit      chan struct{}
	ticker    sync.WaitGroup
}

// peerView is what the sender knows about one receiver.
type peerView struct {
	id     uint16
	name   string // sanitized by multisession; "" until ready
	addr   string // IP from MEMBER_JOINED
	state  string // joined, ready, receiving, done, failed, left
	reason string

	ready     bool      // got the dataKey and said ready
	bytes     int64     // plaintext bytes it reported written
	speed     float64   // smoothed bytes/s
	lastBytes int64     // for the speed estimate
	lastAt    time.Time //
	end       time.Time // when it finished (done/failed/left)
}

// Refresh periods for the progress line: in place on a terminal, a plain
// line now and then in a log (no \r, phan-cong.md step 6).
const (
	sendUITick       = 500 * time.Millisecond
	sendUIPlainEvery = 5 * time.Second
	speedSmoothing   = 0.3 // weight of the newest sample
)

func newSendUI(want int) *sendUI {
	w, _ := termui.Output(os.Stderr)
	fd := int(os.Stderr.Fd())
	return &sendUI{
		w: w, tty: isTerminal(os.Stderr), want: want, now: time.Now,
		width: func() int {
			if cols, _, err := term.GetSize(fd); err == nil {
				return cols
			}
			return 0
		},
		peers: map[uint16]*peerView{},
	}
}

// startTicker launches the redraw ticker. finish stops it.
func (u *sendUI) startTicker() {
	u.quit = make(chan struct{})
	u.ticker.Add(1)
	go func() {
		defer u.ticker.Done()
		t := time.NewTicker(sendUITick)
		defer t.Stop()
		for {
			select {
			case <-u.quit:
				return
			case <-t.C:
				u.mu.Lock()
				u.drawProgress()
				u.mu.Unlock()
			}
		}
	}()
}

// update is multisend.SendOptions.Progress.
func (u *sendUI) update(p multisend.PeerProgress) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped {
		return
	}
	now := u.now()
	switch p.State {
	case "sending":
		u.start, u.total = now, p.Total
		u.event(fmt.Sprintf("Bắt đầu gửi %s cho %d người nhận", formatSize(p.Total), u.readyCount()))
		return
	case "uploaded":
		u.uploaded = p.Bytes
		return
	}

	v := u.peers[p.ID]
	if v == nil {
		if p.State != "joined" {
			return // an event for a receiver we never saw join; nothing to show
		}
		v = &peerView{id: p.ID}
		u.peers[p.ID] = v
	}
	if p.Name != "" {
		v.name = p.Name
	}
	if p.Addr != "" {
		v.addr = p.Addr
	}

	switch p.State {
	case "joined":
		v.state = "joined"
		u.event(fmt.Sprintf("[%d] %s đã vào, đang xác thực", v.id, v.addr))
	case "rejected":
		delete(u.peers, p.ID)
		u.event(fmt.Sprintf("[%d] %s bị loại: %s", v.id, v.label(), rejectReasonVI(p.Reason)))
	case "ready":
		v.state, v.ready = "ready", true
		// Not "starting" even at want/want: the lobby may still apply a
		// queued leave and keep waiting. "sending" says when it starts.
		u.event(fmt.Sprintf("[%d] %s sẵn sàng (%d/%d)", v.id, v.label(), u.readyCount(), u.want))
	case "receiving":
		u.sample(v, p.Bytes, now)
		if v.state == "ready" {
			v.state = "receiving"
		}
	case "done":
		v.state, v.end = "done", now
		if u.total > 0 {
			v.bytes = u.total
		}
		s := u.seconds(v.end)
		u.event(fmt.Sprintf("[%d] %s xong: %s trong %.2f s (%.1f Mbit/s)", v.id, v.label(), formatSize(v.bytes), s, mbitPerSec(v.bytes, s)))
	case "failed", "left":
		if !v.ready || u.start.IsZero() {
			// Still in the lobby: it simply leaves, its slot is free again.
			delete(u.peers, p.ID)
			u.event(fmt.Sprintf("[%d] %s rời phòng chờ", v.id, v.label()))
			return
		}
		v.state, v.end, v.reason = p.State, now, p.Reason
		what := "lỗi: " + p.Reason
		if p.State == "left" {
			what = "mất kết nối"
		}
		u.event(fmt.Sprintf("[%d] %s %s ở %s", v.id, v.label(), what, u.percent(v.bytes)))
	}
}

// sample records a progress report and updates the smoothed speed.
func (u *sendUI) sample(v *peerView, bytes int64, now time.Time) {
	if !v.lastAt.IsZero() {
		if dt := now.Sub(v.lastAt).Seconds(); dt > 0 && bytes >= v.lastBytes {
			inst := float64(bytes-v.lastBytes) / dt
			if v.speed == 0 {
				v.speed = inst
			} else {
				v.speed = speedSmoothing*inst + (1-speedSmoothing)*v.speed
			}
		}
	} else if !u.start.IsZero() {
		if dt := now.Sub(u.start).Seconds(); dt > 0 {
			v.speed = float64(bytes) / dt
		}
	}
	v.bytes, v.lastBytes, v.lastAt = bytes, bytes, now
}

// event prints one line, keeping the progress line below it.
func (u *sendUI) event(line string) {
	u.clearLine()
	fmt.Fprintln(u.w, line)
	u.drawProgress()
}

// clearLine erases the progress line so an event line can take its place.
func (u *sendUI) clearLine() {
	if u.lineOpen {
		fmt.Fprint(u.w, "\r\x1b[K")
		u.lineOpen = false
	}
}

// drawProgress (re)draws the live line while the upload runs: in place on a
// terminal, every sendUIPlainEvery as its own line otherwise.
func (u *sendUI) drawProgress() {
	if u.start.IsZero() || u.stopped {
		return
	}
	line := u.progressLine()
	if line == "" {
		return
	}
	if !u.tty {
		if now := u.now(); now.Sub(u.lastPlain) >= sendUIPlainEvery {
			u.lastPlain = now
			fmt.Fprintln(u.w, line)
		}
		return
	}
	if cols := u.width(); cols > 1 {
		line = truncateWidth(line, cols-1)
	}
	fmt.Fprint(u.w, "\r\x1b[K"+line)
	u.lineOpen = true
}

// progressLine is e.g.
//
//	[2] binh-laptop 64% 23.6 MB/s | [3] an-pc 61% 23.1 MB/s | tải lên 23.8 MB/s
//
// The upload speed is the fastest active receiver's: the hub never lets the
// sender run more than one 8 MB queue ahead of a receiver.
func (u *sendUI) progressLine() string {
	var parts []string
	var upload float64
	active := false
	for _, v := range u.sortedPeers() {
		if !v.ready {
			continue
		}
		switch v.state {
		case "done":
			parts = append(parts, fmt.Sprintf("[%d] %s xong", v.id, v.name))
		case "failed":
			parts = append(parts, fmt.Sprintf("[%d] %s lỗi", v.id, v.name))
		case "left":
			parts = append(parts, fmt.Sprintf("[%d] %s mất kết nối", v.id, v.name))
		default:
			parts = append(parts, fmt.Sprintf("[%d] %s %s %.1f MB/s", v.id, v.name, u.percent(v.bytes), v.speed/1e6))
			upload = max(upload, v.speed)
			active = true
		}
	}
	if len(parts) == 0 {
		return ""
	}
	line := strings.Join(parts, " | ")
	if active {
		line += fmt.Sprintf(" | tải lên %.1f MB/s", upload/1e6)
	}
	return line
}

// finish stops the live line and prints the summary and the bench lines.
// sendErr is what multisend.Send returned. Nothing is printed for a session
// that never started uploading (e.g. the lobby timed out): there is nothing
// to measure, and Send's error says why.
func (u *sendUI) finish(sendErr error) {
	u.mu.Lock()
	if u.quit != nil {
		close(u.quit)
	}
	u.mu.Unlock()
	u.ticker.Wait()

	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearLine()
	u.stopped = true
	if u.start.IsZero() {
		return
	}
	end := u.now()
	var ended []*peerView
	for _, v := range u.sortedPeers() {
		if !v.ready {
			continue
		}
		if v.end.IsZero() { // Send gave up before this receiver finished
			v.state, v.end = "failed", end
			if sendErr != nil {
				v.reason = sendErr.Error()
			}
		}
		ended = append(ended, v)
	}
	// Bench lines in the order receivers finished; the slowest is last.
	sort.SliceStable(ended, func(i, j int) bool { return ended[i].end.Before(ended[j].end) })

	done := 0
	last := u.start
	var slowest *peerView
	for _, v := range ended {
		if v.end.After(last) {
			last = v.end
		}
		if v.state == "done" {
			done++
			slowest = v
		}
	}
	seconds := last.Sub(u.start).Seconds()
	ratio := 0.0
	if u.total > 0 {
		ratio = float64(u.uploaded) / float64(u.total)
	}
	summary := fmt.Sprintf("Tóm tắt: %d/%d xong. Tải lên %s (%.3fx) trong %.2f s (%.1f Mbit/s)",
		done, len(ended), formatSize(u.uploaded), ratio, seconds, mbitPerSec(u.total, seconds))
	if slowest != nil && len(ended) > 1 {
		summary += fmt.Sprintf("; chậm nhất [%d]", slowest.id)
	}
	fmt.Fprintln(u.w, summary)
	for _, v := range ended {
		if v.state != "done" {
			fmt.Fprintf(u.w, "  [%d] %s: %s\n", v.id, v.label(), v.state+reasonSuffix(v.reason))
		}
	}
	for _, v := range ended {
		s := u.seconds(v.end)
		result := v.state
		if result != "done" && result != "left" {
			result = "failed"
		}
		fmt.Fprintf(u.w, "bench: role=send-peer id=%d name=%s bytes=%d seconds=%.2f mbps=%.1f result=%s\n",
			v.id, benchToken(v.name), v.bytes, s, mbitPerSec(v.bytes, s), result)
	}
	fmt.Fprintf(u.w, "bench: role=send n=%d done=%d bytes=%d uploaded=%d ratio=%.3f seconds=%.2f mbps=%.1f\n",
		len(ended), done, u.total, u.uploaded, ratio, seconds, mbitPerSec(u.total, seconds))
}

func (u *sendUI) readyCount() int {
	n := 0
	for _, v := range u.peers {
		if v.ready {
			n++
		}
	}
	return n
}

func (u *sendUI) sortedPeers() []*peerView {
	out := make([]*peerView, 0, len(u.peers))
	for _, v := range u.peers {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// seconds since the upload started (the "Định dạng chung" clock).
func (u *sendUI) seconds(t time.Time) float64 {
	if u.start.IsZero() || t.Before(u.start) {
		return 0
	}
	return t.Sub(u.start).Seconds()
}

func (u *sendUI) percent(bytes int64) string {
	if u.total <= 0 {
		return "100%"
	}
	return fmt.Sprintf("%.0f%%", float64(bytes)*100/float64(u.total))
}

// label is "name (ip)" once the name is known, else just the IP.
func (v *peerView) label() string {
	switch {
	case v.name != "" && v.addr != "":
		return fmt.Sprintf("%s (%s)", v.name, v.addr)
	case v.name != "":
		return v.name
	case v.addr != "":
		return v.addr
	}
	return "?"
}

// rejectReasonVI translates multisend's short lobby reasons.
func rejectReasonVI(reason string) string {
	switch reason {
	case "wrong code":
		return "sai code"
	case "refused":
		return "người gửi từ chối"
	case "timed out":
		return "quá hạn xác thực"
	case "lobby closed":
		return "phòng chờ đã đóng"
	case "busy":
		return "sai code quá nhiều lần gần đây, tạm không nhận người mới"
	}
	return reason
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// mbitPerSec is bytes × 8 / seconds / 1e6 ("Định dạng chung"), 0 for no time.
func mbitPerSec(bytes int64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(bytes) * 8 / seconds / 1e6
}

// formatSize is a human size in binary units, e.g. "1.38 GiB".
func formatSize(n int64) string {
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

// benchToken keeps a name to one key=value token, like the receiver's bench
// line: spaces, control characters and "=" become "_", "?" if empty.
func benchToken(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '=' {
			r = '_'
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// truncateWidth cuts s to at most cols terminal columns, so the progress
// line never wraps (a wrapped line cannot be redrawn with \r). East Asian
// wide and fullwidth runes take two columns.
func truncateWidth(s string, cols int) string {
	if displayWidth(s) <= cols {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > cols-1 { // keep one column for "…"
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

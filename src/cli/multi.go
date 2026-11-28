package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/schollz/croc/v11/internal/cli"
	"github.com/schollz/croc/v11/src/multirecv"
	"github.com/schollz/croc/v11/src/multisend"
	"github.com/schollz/croc/v11/src/termui"
	"github.com/schollz/croc/v11/src/utils"
	"github.com/schollz/progressbar/v3"
)

// registerMulti là hàm cầu nối duy nhất được gọi từ newApp() trong src/cli/cli.go.
// Giúp đăng ký cả phía gửi (N) và phía nhận (L) mà không cần sửa nhiều chỗ trong cli.go.
func registerMulti(app *cli.App) {
	registerMultiSend(app)
	registerMultiRecv(app)
}

// ============================================================================
// PHẦN CỦA N (NGUYÊN) - SENDER CLI
// ============================================================================

// multiSendFlags định nghĩa các cờ cho chế độ multi-send
func multiSendFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:  "receivers",
			Usage: "number of receivers in multi-send mode",
		},
		&cli.BoolFlag{
			Name:  "approve",
			Usage: "prompt to approve each receiver before file transfer",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Value: 10 * time.Minute,
			Usage: "max time to wait for receivers to be ready (0 = no limit)",
		},
	}
}

// registerMultiSend gắn cờ vào lệnh `send` và bọc Action để chuyển hướng khi có --receivers
func registerMultiSend(app *cli.App) {
	for _, cmd := range app.Commands {
		if cmd.Name == "send" {
			// Thêm các cờ mới vào croc send
			cmd.Flags = append(cmd.Flags, multiSendFlags()...)

			// Bọc action gốc của lệnh send
			origAction := cmd.Action
			cmd.Action = func(c *cli.Context) error {
				// Nếu có --receivers > 0 thì kích hoạt luồng multi-send của N
				if c.Int("receivers") > 0 {
					return sendMulti(c)
				}
				// Nếu không thì vẫn chạy luồng croc send 1-1 cũ bình thường
				if origAction != nil {
					return origAction(c)
				}
				return nil
			}
			break
		}
	}
}

// sendMulti là hàm điều khiển chính phía gửi (sẽ kết nối với src/multisend ở M1c)
func sendMulti(c *cli.Context) error {
	// Set the debug level based on CLI
	setDebugLevel(c)

	receivers := c.Int("receivers")
	approve := c.Bool("approve")
	timeout := c.Duration("timeout")
	files := c.Args().Slice()

	if c.String("text") != "" {
		return errors.New("multi-send chưa hỗ trợ --text; hãy gửi file")
	}
	if len(files) == 0 {
		return errors.New("cần chỉ định ít nhất một file để gửi")
	}

	// Same rule as 1-1 send: on UNIX a --code is visible in the process list,
	// so it is refused unless classic mode is on or CROC_SECRET is set.
	envSecret := os.Getenv("CROC_SECRET")
	classic := utils.Exists(getClassicConfigFile(true))
	if shouldExitForUnixSendCode(runtime.GOOS, c.IsSet("code"), classic, envSecret) {
		fmt.Fprint(os.Stderr, `On UNIX systems, to multi-send with a custom code phrase,
set the environment variable CROC_SECRET instead of --code:

  CROC_SECRET=**** croc send --receivers N file.txt

Or have the code generated:  croc send --receivers N file.txt
Or enable classic mode:      croc --classic

`)
		return nil
	}
	code := resolveSendSharedSecret(c.String("code"), envSecret)

	ui := newSendUI(receivers)
	ui.startTicker()
	err := multisend.Send(c.Context, multisend.SendOptions{Files: files, Receivers: receivers, Approve: approve, Timeout: timeout, Relay: c.String("relay"), RelayPass: c.String("pass"), Code: code, Progress: ui.update})
	ui.finish(err)
	return err
}

// ============================================================================
// PHẦN CỦA L - RECEIVER CLI (Tạo sẵn khung cho L)
// ============================================================================

// registerMultiRecv gắn cờ --multi toàn cục và bọc Action mặc định của croc
func registerMultiRecv(app *cli.App) {
	// Thêm cờ toàn cục để hiện trong `croc --help`
	app.Flags = append(app.Flags, &cli.BoolFlag{
		Name:  "multi",
		Usage: "receive files in multi-client mode",
	}, &cli.StringFlag{
		Name:  "name",
		Usage: "name shown to the sender in multi-client mode (default: hostname)",
	})

	origAction := app.Action
	app.Action = func(c *cli.Context) error {
		if c.Bool("multi") {
			return receiveMulti(c)
		}
		if origAction != nil {
			return origAction(c)
		}
		return nil
	}
}

// receiveMulti là hàm điều khiển chính phía nhận (L sẽ hoàn thiện ở M1d)
func receiveMulti(c *cli.Context) error {
	// Set the debug level based on CLI
	setDebugLevel(c)

	if c.NArg() > 1 {
		return errors.New("chỉ được chỉ định một code để nhận multi-send")
	}
	code, refused := resolveMultiReceiveCode(runtime.GOOS, c.Args().First(), os.Getenv("CROC_SECRET"), utils.Exists(getClassicConfigFile(true)))
	if refused {
		fmt.Fprint(os.Stderr, `For security, croc does not accept receive codes on the UNIX
command line because they can appear in the process list.

Receive with the code in the environment:  CROC_SECRET='****' croc --multi
Or enter it interactively:                 croc --multi
Or enable classic mode:                    croc --classic

`)
		return nil
	}
	if code == "" {
		var err error
		if code, err = utils.GetInputContext(c.Context, "Enter receive code: "); err != nil {
			return fmt.Errorf("could not read receive code: %w", err)
		}
		if code == "" {
			return errors.New("cần chỉ định một code để nhận multi-send")
		}
	}
	ui := newRecvUI()
	err := multirecv.Receive(c.Context, multirecv.ReceiveOptions{
		Code: code, OutDir: c.String("out"), Relay: c.String("relay"), RelayPass: c.String("pass"),
		Name: c.String("name"), Progress: ui.progress, Status: ui.status, Joined: ui.joined,
	})
	ui.endLine()
	return err
}

// resolveMultiReceiveCode mirrors the 1-1 receive rule: CROC_SECRET wins; a
// code on the UNIX command line is refused (refused = true) unless classic
// mode is on. code == "" with refused == false means "ask interactively".
func resolveMultiReceiveCode(goos, arg, envSecret string, classic bool) (code string, refused bool) {
	if envSecret != "" {
		return envSecret, false
	}
	if arg != "" && goos != "windows" && !classic {
		return "", true
	}
	return arg, false
}

// recvUI draws the receiver's output on stderr: status lines, and a progress
// bar on a terminal or a plain line every 5 s otherwise (no "\r" in logs).
// multirecv.Receive calls it from one goroutine only.
type recvUI struct {
	w           io.Writer
	tty, color  bool
	description string
	bar         *progressbar.ProgressBar
	lineOpen    bool // the bar is on the current line, not yet ended by a newline
	lastPlain   time.Time
}

func newRecvUI() *recvUI {
	w, color := termui.Output(os.Stderr)
	return &recvUI{w: w, tty: isTerminal(os.Stderr), color: color}
}

// isTerminal reports whether f is a terminal (the same check as
// termui.Output, which only reports colour: NO_COLOR also turns that off).
func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func (u *recvUI) joined(s multirecv.Session) {
	if len(s.Files) == 1 {
		u.description = s.Files[0].Path
	} else {
		u.description = fmt.Sprintf("%d files", len(s.Files))
	}
}

func (u *recvUI) progress(bytes, total int64) {
	if !u.tty {
		if now := time.Now(); now.Sub(u.lastPlain) >= 5*time.Second || bytes == total {
			u.lastPlain = now
			pct := 100.0
			if total > 0 {
				pct = float64(bytes) * 100 / float64(total)
			}
			fmt.Fprintf(u.w, "%s: %.0f%% (%d / %d bytes)\n", u.description, pct, bytes, total)
		}
		return
	}
	if u.bar == nil {
		u.bar = termui.NewProgress(termui.ProgressConfig{
			Max:          total,
			Description:  termui.ProgressDescription("", u.description, u.color),
			Writer:       u.w,
			ColorEnabled: u.color,
			Throttle:     100 * time.Millisecond,
		})
	}
	u.lineOpen = true
	_ = u.bar.Set64(bytes)
	if bytes >= total {
		_ = u.bar.Finish()
	}
}

// status prints one line, ending the progress bar's line first.
func (u *recvUI) status(msg string) {
	u.endLine()
	fmt.Fprintln(u.w, msg)
}

func (u *recvUI) endLine() {
	if u.lineOpen {
		fmt.Fprintln(u.w)
		u.lineOpen = false
	}
}

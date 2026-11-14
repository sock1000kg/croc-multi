package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/schollz/croc/v11/internal/cli"
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
			Usage: "session timeout waiting for receivers",
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
	receivers := c.Int("receivers")
	approve := c.Bool("approve")
	timeout := c.Duration("timeout")
	files := c.Args().Slice()

	if len(files) == 0 && c.String("text") == "" {
		return errors.New("cần chỉ định ít nhất một file hoặc văn bản (--text) để gửi")
	}

	fmt.Printf("[multi-send] Sẵn sàng gửi cho %d người nhận (approve=%v, timeout=%v)\n", receivers, approve, timeout)

	// TODO (M1c): Gọi engine gửi trong src/multisend
	return nil
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
	// TODO (L - M1d): Gọi engine nhận trong src/multirecv
	return errors.New("chế độ multi-receive chưa sẵn sàng (chờ M1d)")
}
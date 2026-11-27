package multisend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/utils"
)

// promptGap is the least time between two approval prompts, so a refused
// receiver that keeps rejoining cannot flood the sender with questions.
const promptGap = 3 * time.Second

// askApproval reads the sender's answer. Tests replace it.
var askApproval = utils.GetInputContext

type approver struct {
	ctx      context.Context                                          // a context that ends with lobby
	ask      func(ctx context.Context, prompt string) (string, error) // = utils.GetInputContext; tests swap it with sth else, so its passed in
	promptMu sync.Mutex                                               // held for the WHOLE question: one prompt at a time
	lastEnd  time.Time                                                // when the last answer arrived (guarded by promptMu)
	refused  map[string]bool                                          // key: name + "|" + host (guarded by promptMu)
}

func (a *approver) approve(info multisession.ReceiverInfo) bool {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()

	key := info.Name + "|" + info.Addr
	if a.refused[key] {
		return false
	}

	wait := time.Until(a.lastEnd.Add(promptGap))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-a.ctx.Done():
			return false
		}
	}

	answer, err := a.ask(
		a.ctx,
		fmt.Sprintf("Cho %q (%s) nhận file? (y/N)", info.Name, info.Addr),
	)

	a.lastEnd = time.Now()

	if err != nil {
		return false
	}

	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer == "y" || answer == "yes" {
		return true
	}

	a.refused[key] = true
	return false
}

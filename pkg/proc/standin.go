package proc

import (
	"os/exec"
	"sync"
	"time"
)

var standIn sync.Mutex

// BlockReaping is for tests, including those in other packages. Until release
// is closed, commands whose arguments match cannot be reaped, standing in for
// a process blocked in the kernel that no signal can end, and Run stops
// waiting for any command ReapTimeout+WaitDelay+stopAfter after its context
// ends. The returned function restores normal reaping;
// call it only after release is closed and every matching Run has returned.
func BlockReaping(match func(args []string) bool, release <-chan struct{}, stopAfter time.Duration) (restore func()) {
	standIn.Lock()
	originalWait, originalSlack := wait, stopSlack
	wait = func(cmd *exec.Cmd) error {
		if match(cmd.Args[1:]) {
			<-release
		}
		return originalWait(cmd)
	}
	stopSlack = stopAfter
	return func() {
		wait, stopSlack = originalWait, originalSlack
		standIn.Unlock()
	}
}

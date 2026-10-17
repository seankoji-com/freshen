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

// BlockStarting is for tests, including those in other packages. Until
// release is closed, starting a command whose arguments match does not
// return, standing in for a start blocked on an unresponsive filesystem. The
// start itself runs first, so one that returns late has started the command
// (or failed to). Run stops waiting for any command
// ReapTimeout+WaitDelay+stopAfter after its context ends. The returned
// function restores normal starting; call it only after release is closed and
// every matching Run has returned and its StillRunningError's Done has closed.
func BlockStarting(match func(args []string) bool, release <-chan struct{}, stopAfter time.Duration) (restore func()) {
	standIn.Lock()
	originalStart, originalSlack := start, stopSlack
	start = func(cmd *exec.Cmd) error {
		err := originalStart(cmd)
		if match(cmd.Args[1:]) {
			<-release
		}
		return err
	}
	stopSlack = stopAfter
	return func() {
		start, stopSlack = originalStart, originalSlack
		standIn.Unlock()
	}
}

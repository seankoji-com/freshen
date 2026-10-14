//go:build unix

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
}

// stopGroup kills what is left of group pgid once its leader has exited.
var stopGroup = killGroup

// killGroup kills every process in group pgid and waits up to ReapTimeout for
// them to exit. The leader is reaped by the Wait that is running alongside;
// orphaned children are reaped by init.
func killGroup(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	deadline := time.Now().Add(ReapTimeout)
	for groupAlive(pgid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%w (process group %d)", ErrSurvived, pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

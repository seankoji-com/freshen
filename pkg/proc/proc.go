// Package proc starts the external commands freshen runs (git, gh) so that a
// deadline or cancellation stops the whole process tree, and so that a child
// holding the output pipes open cannot keep the caller waiting after the
// command has exited or been killed.
package proc

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// WaitDelay bounds how long Wait keeps reading a command's output after the
// command exits or is killed. A hook or helper that inherits stdout or stderr
// and outlives the command would otherwise hold Wait open until it exits.
// A variable so tests can shorten it.
var WaitDelay = 5 * time.Second

// ReapTimeout bounds how long cancelling a command waits for every process in
// its group to exit after they are killed. A variable so tests can shorten it.
var ReapTimeout = 5 * time.Second

// ErrSurvived reports a process group that still had members ReapTimeout
// after it was killed, typically a process blocked in the kernel on a hung
// filesystem.
var ErrSurvived = errors.New("processes the command started were still running after it was stopped")

// Command is exec.CommandContext with WaitDelay set and, on Unix, the command
// in a process group of its own. When ctx ends the whole group is killed,
// including children such as the git a gh clone starts, and Wait returns only
// once the group has exited or ReapTimeout has passed.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = WaitDelay
	ownProcessGroup(cmd)
	return cmd
}

// Run, Output and CombinedOutput are the exec.Cmd methods of the same names,
// except that the exit status decides success. exec reports ErrWaitDelay when
// the command exited 0 but a process it started (a hook, a credential helper)
// still held its output open after WaitDelay; everything the command itself
// wrote had been read by then, so that is not a failure of the command.
func Run(cmd *exec.Cmd) error { return settle(cmd.Run()) }

// Output is cmd.Output with the exit status deciding success; see Run.
func Output(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	return out, settle(err)
}

// CombinedOutput is cmd.CombinedOutput with the exit status deciding
// success; see Run.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	return out, settle(err)
}

func settle(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

// Exited returns a channel closed once no process in cmd's group is running.
// A command that never started, or one on a platform without process groups,
// counts as exited.
func Exited(cmd *exec.Cmd) <-chan struct{} {
	done := make(chan struct{})
	if cmd.Process == nil || !groupAlive(cmd.Process.Pid) {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		for groupAlive(cmd.Process.Pid) {
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return done
}

// Survived reports whether any process in cmd's group is still running.
func Survived(cmd *exec.Cmd) bool {
	return cmd.Process != nil && groupAlive(cmd.Process.Pid)
}

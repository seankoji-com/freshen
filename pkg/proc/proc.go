// Package proc starts the external commands freshen runs (git, gh) so that a
// deadline or cancellation stops the whole process tree, a child holding the
// output pipes open cannot keep the caller waiting after the command has
// exited or been killed, and a command that cannot be reaped at all, because
// it is blocked in the kernel, still returns control to the caller.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// WaitDelay bounds how long Wait keeps reading a command's output after the
// command exits or is killed. A hook or helper that inherits stdout or stderr
// and outlives the command would otherwise hold Wait open until it exits.
// A variable so tests can shorten it.
var WaitDelay = 5 * time.Second

// ReapTimeout bounds how long stopping a command's process group waits for
// every process in it to exit after they are killed. A variable so tests can
// shorten it.
var ReapTimeout = 5 * time.Second

// stopSlack is added to ReapTimeout and WaitDelay to give the time Run keeps
// waiting for a command after its context ends. A variable so tests can
// shorten it.
var stopSlack = time.Second

// ErrSurvived reports a command, or a process it started, that was still
// running after it was killed, typically a process blocked in the kernel on a
// hung filesystem.
var ErrSurvived = errors.New("processes the command started were still running after it was stopped")

// StillRunningError is the error Run, Output and CombinedOutput return when
// they stop waiting for a command that has not finished: the command did not
// exit within ReapTimeout+WaitDelay of its context ending, or it exited but a
// process it started survived being killed. Either may still change files.
// Done is closed once the command has been reaped and no process in its group
// remains; whoever owns the paths the command acts on must keep them until
// then. errors.Is(err, ErrSurvived) reports true.
type StillRunningError struct {
	// Err is the command's own result when it had exited (nil for exit 0),
	// or nil when it could not be reaped.
	Err  error
	Done <-chan struct{}
}

func (e *StillRunningError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v; %v", e.Err, ErrSurvived)
	}
	return ErrSurvived.Error()
}

// Is reports ErrSurvived as a match.
func (e *StillRunningError) Is(target error) bool { return target == ErrSurvived }

func (e *StillRunningError) Unwrap() error { return e.Err }

// Command is exec.CommandContext with WaitDelay set and, on Unix, the command
// in a process group of its own. When ctx ends the whole group is killed,
// including children such as the git a gh clone starts.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = WaitDelay
	ownProcessGroup(cmd)
	return cmd
}

// Run is cmd.Run for a command made by Command, with three differences.
//
//   - The exit status decides success. exec reports ErrWaitDelay when the
//     command exited 0 but a process it started (a hook, a credential helper)
//     still held its output open after WaitDelay; everything the command
//     itself wrote had been read by then, so that is not a failure.
//   - Once the command exits, any process it started that is still running is
//     killed, so no part of the command outlives Run. If one survives that,
//     Run returns a *StillRunningError.
//   - Once the command's context ends, Run waits at most
//     ReapTimeout+WaitDelay+stopSlack for it. A command that is still not
//     reaped by then, such as one blocked in the kernel, makes Run return a
//     *StillRunningError; output it writes afterwards is discarded, so the
//     caller may read its buffers at once.
func Run(cmd *exec.Cmd) error { return run(cmd) }

// Output is cmd.Output with Run's behaviour.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := run(cmd)
	return stdout.Bytes(), err
}

// CombinedOutput is cmd.CombinedOutput with Run's behaviour.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("exec: Stdout or Stderr already set")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := run(cmd)
	return out.Bytes(), err
}

// wait is the reaping Run does; tests swap it to stand in for a process the
// kernel never lets exit.
var wait = (*exec.Cmd).Wait

func run(cmd *exec.Cmd) error {
	outputs := guardOutputs(cmd)
	// exec calls Cancel when the context ends; that is when Run's own wait
	// starts counting down.
	stopped := make(chan struct{})
	if cancel := cmd.Cancel; cancel != nil {
		var once sync.Once
		cmd.Cancel = func() error {
			once.Do(func() { close(stopped) })
			return cancel()
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	reapCmd, stopAfter := wait, ReapTimeout+WaitDelay+stopSlack
	go func() { waited <- reapCmd(cmd) }()
	select {
	case err := <-waited:
		return reap(cmd, settle(err))
	case <-stopped:
	}
	timer := time.NewTimer(stopAfter)
	defer timer.Stop()
	select {
	case err := <-waited:
		return reap(cmd, settle(err))
	case <-timer.C:
	}
	outputs.drop()
	done := make(chan struct{})
	go func() {
		<-waited
		<-Exited(cmd)
		close(done)
	}()
	return &StillRunningError{Done: done}
}

// reap kills whatever is left of cmd's process group after the command
// exited, and reports a group that survives that.
func reap(cmd *exec.Cmd, err error) error {
	if !Survived(cmd) {
		return err
	}
	if stopGroup(cmd.Process.Pid) == nil || !Survived(cmd) {
		return err
	}
	return &StillRunningError{Err: err, Done: Exited(cmd)}
}

func settle(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

// guardedWriter passes writes through until it is dropped, then discards
// them, so a caller can read the underlying buffer while a command Run gave
// up on may still write.
type guardedWriter struct {
	mu      sync.Mutex
	w       io.Writer
	dropped bool
}

func (g *guardedWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.dropped {
		return len(p), nil
	}
	return g.w.Write(p)
}

type guardedOutputs []*guardedWriter

func (gs guardedOutputs) drop() {
	for _, g := range gs {
		g.mu.Lock()
		g.dropped = true
		g.mu.Unlock()
	}
}

// guardOutputs wraps cmd's in-memory stdout and stderr writers. Files are
// left alone: exec hands them to the process without a copying goroutine.
// One writer used for both stays one writer, as exec expects.
func guardOutputs(cmd *exec.Cmd) guardedOutputs {
	var gs guardedOutputs
	wrap := func(w io.Writer) io.Writer {
		if w == nil {
			return nil
		}
		if _, isFile := w.(*os.File); isFile {
			return w
		}
		g := &guardedWriter{w: w}
		gs = append(gs, g)
		return g
	}
	same := sameWriter(cmd.Stdout, cmd.Stderr)
	cmd.Stdout = wrap(cmd.Stdout)
	if same {
		cmd.Stderr = cmd.Stdout
	} else {
		cmd.Stderr = wrap(cmd.Stderr)
	}
	return gs
}

// sameWriter is a == b for writers whose dynamic type may not be comparable.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a != nil && a == b
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

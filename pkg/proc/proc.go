// Package proc starts the external commands freshen runs (git, gh) so that a
// deadline or cancellation stops the whole process tree, a child holding the
// output pipes open cannot keep the caller waiting after the command has
// exited or been killed, and a command whose lookup, start or reaping is
// blocked in the kernel still returns control to the caller.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"weak"
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
// they stop waiting for a command that has not finished: its start did not
// return, or it did not exit, within ReapTimeout+WaitDelay of its context
// ending, or it exited but a process it started survived being killed. Any
// of these may still change files. Done is closed once the start has
// returned, the command (if it started) has been reaped and no process in its
// group remains; whoever owns the paths the command acts on must keep them
// until then. errors.Is(err, ErrSurvived) reports true.
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
// including children such as the git a gh clone starts. If ctx ends while
// the executable is still being looked up in PATH, Command stops waiting and
// the command fails to start with ctx's error.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := command(ctx, name, args...)
	cmd.WaitDelay = WaitDelay
	ownProcessGroup(cmd)
	if done := ctx.Done(); done != nil {
		contexts.Store(weak.Make(cmd), done)
		runtime.AddCleanup(cmd, func(key weak.Pointer[exec.Cmd]) { contexts.Delete(key) }, weak.Make(cmd))
	}
	return cmd
}

// newCommand builds a command; tests swap it to stand in for an executable
// lookup that blocks.
var newCommand = exec.CommandContext

// command is exec.CommandContext, except that it stops waiting for the PATH
// lookup when ctx ends: Go cannot interrupt a lookup blocked on an
// unresponsive mount. The lookup runs on, but it starts and changes nothing.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	build := newCommand
	if ctx.Done() == nil || filepath.Base(name) != name {
		return build(ctx, name, args...)
	}
	built := make(chan *exec.Cmd, 1)
	go func() { built <- build(ctx, name, args...) }()
	select {
	case cmd := <-built:
		return cmd
	case <-ctx.Done():
		return &exec.Cmd{
			Path: name,
			Args: append([]string{name}, args...),
			Err:  fmt.Errorf("exec: looking up %q: %w", name, ctx.Err()),
		}
	}
}

// contexts holds the Done channel of the context of each command Command
// made, so Run can stop waiting for a start that blocks: exec watches the
// context only once the process exists. The keys are weak, so an entry goes
// when its command is collected, whether or not it ran.
var contexts sync.Map // weak.Pointer[exec.Cmd] -> <-chan struct{}

// contextDone is the Done channel of the context cmd was made with, or nil
// for a command Command did not make.
func contextDone(cmd *exec.Cmd) <-chan struct{} {
	stored, _ := contexts.Load(weak.Make(cmd))
	done, _ := stored.(<-chan struct{})
	return done
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
//     ReapTimeout+WaitDelay+stopSlack for it to start and be reaped. A
//     command whose start has not returned by then (one exec'ing a file on a
//     hung mount) or that is still not reaped (one blocked in the kernel)
//     makes Run return a *StillRunningError; output it writes afterwards is
//     discarded, so the caller may read its buffers at once. A start that
//     returns later is killed by its context and reaped before Done closes.
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

// start and wait are the starting and reaping Run does; tests swap them to
// stand in for a start blocked on the filesystem and a process the kernel
// never lets exit.
var (
	start = (*exec.Cmd).Start
	wait  = (*exec.Cmd).Wait
)

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
	startCmd, reapCmd, stopAfter := start, wait, ReapTimeout+WaitDelay+stopSlack
	// Go cannot interrupt a start blocked on the filesystem, and on Unix
	// Start waits for the child to exec, so the start runs aside too.
	started := make(chan error, 1)
	go func() { started <- startCmd(cmd) }()
	var giveUp <-chan time.Time
	select {
	case err := <-started:
		if err != nil {
			return err
		}
	case <-contextDone(cmd):
		timer := time.NewTimer(stopAfter)
		defer timer.Stop()
		giveUp = timer.C
		select {
		case err := <-started:
			if err != nil {
				return err
			}
		case <-giveUp:
			outputs.drop()
			return &StillRunningError{Done: abandonStart(cmd, started, reapCmd)}
		}
	}
	waited := make(chan error, 1)
	go func() { waited <- reapCmd(cmd) }()
	select {
	case err := <-waited:
		return reap(cmd, settle(err))
	case <-stopped:
	}
	if giveUp == nil {
		timer := time.NewTimer(stopAfter)
		defer timer.Stop()
		giveUp = timer.C
	}
	select {
	case err := <-waited:
		return reap(cmd, settle(err))
	case <-giveUp:
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

// abandonStart returns a channel closed once a start Run stopped waiting for
// has returned and, if it started the command, the command has been reaped
// and its group is empty. Its context has ended, so exec kills a command that
// does start.
func abandonStart(cmd *exec.Cmd, started <-chan error, reapCmd func(*exec.Cmd) error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if <-started == nil {
			_ = reapCmd(cmd)
			<-Exited(cmd)
		}
	}()
	return done
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

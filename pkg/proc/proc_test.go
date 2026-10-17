package proc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func needShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures are POSIX shell scripts")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

func shortWaitDelay(t *testing.T) {
	t.Helper()
	original := WaitDelay
	WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { WaitDelay = original })
}

// A command that exits while a child it started keeps stdout open returns
// after WaitDelay instead of waiting for the child, with the command's own
// output and its exit status deciding the result.
func TestCommandBoundsHeldOutput(t *testing.T) {
	needShell(t)
	shortWaitDelay(t)
	// The child outlives any plausible scheduling delay by far, so the bound
	// below separates waiting for it from a slow machine; Run stops it.
	for name, script := range map[string]string{"success": "sleep 60 & echo started", "failure": "sleep 60 & echo started; exit 3"} {
		t.Run(name, func(t *testing.T) {
			cmd := Command(context.Background(), "sh", "-c", script)
			start := time.Now()
			out, err := Output(cmd)
			if waited := time.Since(start); waited > 30*time.Second {
				t.Fatalf("Output waited %s for a child holding stdout", waited)
			}
			if Survived(cmd) {
				t.Fatal("the child holding stdout outlived Output")
			}
			if got := string(out); got != "started\n" {
				t.Fatalf("output %q", got)
			}
			if wantErr := name == "failure"; (err != nil) != wantErr {
				t.Fatalf("Output() error = %v", err)
			}
		})
	}
	// Without proc's settling, exec reports the held pipe as an error.
	cmd := Command(context.Background(), "sh", "-c", "sleep 3 & echo started")
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Run(); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("exec Run() = %v, want ErrWaitDelay", err)
	}
}

// Cancelling a command kills the children it started too: a writer the
// command left running cannot keep changing files after Run returns.
func TestCancelStopsTheWholeTree(t *testing.T) {
	needShell(t)
	shortWaitDelay(t)
	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := Command(ctx, "sh", "-c", `(while :; do echo x >> "$1"; sleep 0.02; done) & wait`, "sh", marker)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for size(marker) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the child never wrote")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	returned := make(chan error, 1)
	go func() { returned <- cmd.Wait() }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
	if Survived(cmd) {
		t.Fatal("a process in the group survived cancellation")
	}
	select {
	case <-Exited(cmd):
	default:
		t.Fatal("Exited is open for a group with no processes")
	}
	before := size(marker)
	time.Sleep(200 * time.Millisecond)
	if after := size(marker); after != before {
		t.Fatalf("the child kept writing after Wait returned: %d -> %d bytes", before, after)
	}
}

func size(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// A command the kernel never lets exit, which no kill can end, does not hold
// Run: it returns a StillRunningError soon after the context ends, the
// caller can read the output buffer at once, and Done closes only once the
// command is finally reaped.
func TestRunStopsWaitingForAnUnreapableCommand(t *testing.T) {
	needShell(t)
	shortWaitDelay(t)
	originalReap := ReapTimeout
	ReapTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ReapTimeout = originalReap })
	release := make(chan struct{})
	restore := BlockReaping(func([]string) bool { return true }, release, 100*time.Millisecond)
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		restore()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := Command(ctx, "sh", "-c", "echo started; while :; do echo more; sleep 0.01; done")
	var out bytes.Buffer
	cmd.Stdout = &out
	returned := make(chan error, 1)
	go func() { returned <- Run(cmd) }()
	var err error
	select {
	case err = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run waited for a command that could not be reaped")
	}
	var running *StillRunningError
	if !errors.As(err, &running) || !errors.Is(err, ErrSurvived) {
		t.Fatalf("Run() = %v, want a StillRunningError", err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("started\n")) {
		t.Fatalf("output %q", out.String())
	}
	select {
	case <-running.Done:
		t.Fatal("Done closed before the command was reaped")
	case <-time.After(100 * time.Millisecond):
	}
	released = true
	close(release)
	select {
	case <-running.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done stayed open after the command was reaped")
	}
}

// When a command exits, successfully or not, the processes it started go
// with it: a writer it left behind is stopped before Run returns.
func TestRunStopsWhatTheCommandLeftBehind(t *testing.T) {
	needShell(t)
	for name, exit := range map[string]string{"success": "0", "failure": "1"} {
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			script := `(while :; do echo x >> "$1"; sleep 0.02; done) </dev/null >/dev/null 2>&1 &
while [ ! -s "$1" ]; do sleep 0.01; done
exit ` + exit
			cmd := Command(context.Background(), "sh", "-c", script, "sh", marker)
			err := Run(cmd)
			if wantErr := exit != "0"; (err != nil) != wantErr || errors.Is(err, ErrSurvived) {
				t.Fatalf("Run() = %v", err)
			}
			if Survived(cmd) {
				t.Fatal("a process the command started is still running")
			}
			before := size(marker)
			time.Sleep(200 * time.Millisecond)
			if after := size(marker); before == 0 || after != before {
				t.Fatalf("the writer kept going after Run returned: %d -> %d bytes", before, after)
			}
		})
	}
}

// A process the command left behind that survives being killed makes Run
// report the command still running, with Done closing once it exits.
func TestRunReportsALeftoverThatSurvives(t *testing.T) {
	needShell(t)
	original := stopGroup
	stopGroup = func(int) error { return ErrSurvived } // the kill does not land
	t.Cleanup(func() { stopGroup = original })

	cmd := Command(context.Background(), "sh", "-c", "sleep 0.5 </dev/null >/dev/null 2>&1 & exit 0")
	err := Run(cmd)
	var running *StillRunningError
	if !errors.As(err, &running) || running.Err != nil {
		t.Fatalf("Run() = %v, want a StillRunningError for an exit-0 command", err)
	}
	select {
	case <-running.Done:
		t.Fatal("Done closed while the leftover still ran")
	default:
	}
	select {
	case <-running.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done stayed open after the leftover exited")
	}
}

// A start that does not return, such as one exec'ing a file on a hung mount,
// does not hold Run: it returns a StillRunningError soon after the context
// ends, and Done closes only once the start has returned and, if it started
// the command, the command has been killed and reaped.
func TestRunStopsWaitingForABlockedStart(t *testing.T) {
	needShell(t)
	for name, dir := range map[string]string{"starts late": "", "fails late": filepath.Join(t.TempDir(), "missing")} {
		t.Run(name, func(t *testing.T) {
			shortWaitDelay(t)
			originalReap := ReapTimeout
			ReapTimeout = 200 * time.Millisecond
			t.Cleanup(func() { ReapTimeout = originalReap })
			release := make(chan struct{})
			released := false
			releaseStart := func() {
				if !released {
					released = true
					close(release)
				}
			}
			restore := BlockStarting(func([]string) bool { return true }, release, 100*time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			cmd := Command(ctx, "sh", "-c", "sleep 60")
			cmd.Dir = dir
			returned, finished := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(finished)
				err := Run(cmd)
				returned <- err
				var running *StillRunningError
				if errors.As(err, &running) {
					<-running.Done
				}
			}()
			// exec's cancellation goroutine reads ReapTimeout when it kills
			// the command, so restore the tunables only once the start has
			// returned and the command has been reaped.
			t.Cleanup(func() {
				releaseStart()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("the start never finished")
				}
				restore()
			})

			var err error
			select {
			case err = <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("Run waited for a start that did not return")
			}
			var running *StillRunningError
			if !errors.As(err, &running) || running.Err != nil || !errors.Is(err, ErrSurvived) {
				t.Fatalf("Run() = %v, want a StillRunningError", err)
			}
			select {
			case <-running.Done:
				t.Fatal("Done closed before the start returned")
			case <-time.After(100 * time.Millisecond):
			}
			releaseStart()
			select {
			case <-running.Done:
			case <-time.After(5 * time.Second):
				t.Fatal("Done stayed open after the start returned")
			}
			if started := cmd.Process != nil; started != (dir == "") {
				t.Fatalf("command started = %v", started)
			}
			if Survived(cmd) {
				t.Fatal("the command that started late outlived Done")
			}
		})
	}
}

// A start that returns after its context ended installs exec's context
// watcher too late: when the command has already exited, the watcher can
// hand Wait its result without cancelling, so nothing kills the group. A
// process the late command left behind is still stopped before Done closes.
func TestRunStopsWhatALateStartLeftBehind(t *testing.T) {
	needShell(t)
	shortWaitDelay(t)
	originalReap, originalSlack, originalStart := ReapTimeout, stopSlack, start
	ReapTimeout, stopSlack = 200*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, finished := make(chan struct{}), make(chan struct{})
	start = func(cmd *exec.Cmd) error {
		cmd.Cancel = nil // the watcher hands over the result and never cancels
		err := originalStart(cmd)
		cancel()
		<-release
		return err
	}

	cmd := Command(ctx, "sh", "-c", "sleep 60 </dev/null >/dev/null 2>&1 & exit 0")
	returned := make(chan error, 1)
	go func() {
		defer close(finished)
		err := Run(cmd)
		returned <- err
		var running *StillRunningError
		if errors.As(err, &running) {
			<-running.Done
		}
	}()
	// Restore the tunables only once Done has closed; if it never does, stop
	// the leftover so it does.
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = stopGroup(cmd.Process.Pid)
			}
			<-finished
		}
		ReapTimeout, stopSlack, start = originalReap, originalSlack, originalStart
	})

	var err error
	select {
	case err = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run waited for a start that did not return")
	}
	var running *StillRunningError
	if !errors.As(err, &running) {
		t.Fatalf("Run() = %v, want a StillRunningError", err)
	}
	close(release)
	select {
	case <-running.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done stayed open while the late command's leftover ran")
	}
	if Survived(cmd) {
		t.Fatal("a process the late command started outlived Done")
	}
}

// A PATH lookup that does not return, such as one on a hung mount, does not
// hold Command past its context: the command fails to start with the
// context's error, and nothing is left running.
func TestCommandStopsWaitingForABlockedLookup(t *testing.T) {
	release, finished := make(chan struct{}), make(chan struct{})
	original := newCommand
	newCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		defer close(finished)
		<-release
		return original(ctx, name, args...)
	}
	t.Cleanup(func() {
		close(release)
		<-finished
		newCommand = original
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	made := make(chan *exec.Cmd, 1)
	go func() { made <- Command(ctx, "freshen-blocked-lookup") }()
	var cmd *exec.Cmd
	select {
	case cmd = <-made:
	case <-time.After(5 * time.Second):
		t.Fatal("Command waited for a lookup that did not return")
	}
	if err := Run(cmd); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSurvived) {
		t.Fatalf("Run() = %v, want the context's error", err)
	}
	if cmd.Process != nil {
		t.Fatal("a command whose lookup was abandoned started")
	}
}

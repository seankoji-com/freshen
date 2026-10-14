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
	for name, script := range map[string]string{"success": "sleep 3 & echo started", "failure": "sleep 3 & echo started; exit 3"} {
		t.Run(name, func(t *testing.T) {
			cmd := Command(context.Background(), "sh", "-c", script)
			start := time.Now()
			out, err := Output(cmd)
			if waited := time.Since(start); waited > 2*time.Second {
				t.Fatalf("Output waited %s for a child holding stdout", waited)
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

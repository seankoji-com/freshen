//go:build !unix

package proc

import "os/exec"

// Without process groups, cancelling kills the command itself (exec's
// default) and WaitDelay still bounds the wait for its output.
func ownProcessGroup(*exec.Cmd) {}

func groupAlive(int) bool { return false }

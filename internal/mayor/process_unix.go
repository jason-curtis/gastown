//go:build !windows

package mayor

import (
	"os"
	"syscall"
)

// isProcessAlive checks if a process with the given PID is still running.
// On Unix, we send signal 0 which checks process existence without side effects.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

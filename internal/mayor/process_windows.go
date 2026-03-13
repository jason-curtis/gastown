//go:build windows

package mayor

import (
	"math"

	"golang.org/x/sys/windows"
)

const processStillActive = 259

// isProcessAlive checks if a process with the given PID is still running.
// On Windows, we open the process handle and check its exit code.
func isProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == processStillActive
}

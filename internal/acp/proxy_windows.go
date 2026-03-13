//go:build windows

package acp

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const processStillActive = 259

// signalsToHandle returns the signals that Forward() should listen for.
// On Windows, only os.Interrupt is available (CTRL+C).
func signalsToHandle() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// setupProcessGroup is a no-op on Windows.
// Windows doesn't have process groups like Unix.
func (p *Proxy) setupProcessGroup() {
	// No-op on Windows - no process group support
}

// isProcessAlive checks if the agent process is still running.
// On Windows, we open the process handle and check its exit code.
func (p *Proxy) isProcessAlive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	pid := p.cmd.Process.Pid
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

// terminateProcess kills the agent process.
// On Windows, we use Process.Kill() as there's no graceful SIGTERM equivalent.
func (p *Proxy) terminateProcess() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

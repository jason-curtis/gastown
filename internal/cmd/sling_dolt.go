package cmd

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
)

// ensureDoltForSling checks if the Dolt server was idle-stopped and restarts it.
// Called at the start of sling before any bd commands that need Dolt.
// This makes Dolt demand-driven: stopped when idle, started on sling.
func ensureDoltForSling(townRoot string) {
	// Only act if Dolt was explicitly idle-stopped by the daemon.
	if !daemon.IsDoltIdleStopped(townRoot) {
		return
	}

	// Check if Dolt is actually not running (the daemon may have restarted it).
	running, _, err := doltserver.IsRunning(townRoot)
	if err == nil && running {
		return
	}

	fmt.Printf("%s Dolt idle-stopped, restarting for sling...\n", style.Dim.Render("●"))

	// Signal the daemon to exit idle state.
	_ = daemon.SignalWake(townRoot)

	// Start Dolt directly — don't wait for the next daemon heartbeat.
	if err := doltserver.Start(townRoot); err != nil {
		fmt.Printf("%s Could not auto-start Dolt: %v\n", style.Dim.Render("Warning:"), err)
		fmt.Printf("  Try: gt dolt start\n")
		return
	}

	// Wait for it to be reachable before proceeding.
	if err := doltserver.CheckServerReachable(townRoot); err != nil {
		fmt.Printf("%s Dolt started but not reachable yet: %v\n", style.Dim.Render("Warning:"), err)
	} else {
		fmt.Printf("%s Dolt restarted\n", style.Bold.Render("✓"))
	}
}

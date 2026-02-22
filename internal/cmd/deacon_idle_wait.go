package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var deaconIdleWaitCmd = &cobra.Command{
	Use:   "idle-wait",
	Short: "Sleep if the system is idle (for deacon patrol backoff)",
	Long: `Check if the system is idle and sleep for the recommended backoff duration.

When no polecats or convoys are active, the deacon should slow down its patrol
cycle to reduce CPU usage. This command reads the idle state written by the
daemon and sleeps for the recommended backoff interval.

If the system is active, returns immediately.
If woken early (wake signal from sling), returns immediately.

The backoff interval increases exponentially from 30s to 5min while idle,
and resets to 0 when work arrives.

Exit codes:
  0 - Returned normally (active system or sleep completed)

Examples:
  gt deacon idle-wait              # Sleep if idle, return if active
  gt deacon idle-wait --max=2m     # Cap sleep at 2 minutes`,
	RunE: runDeaconIdleWait,
}

var idleWaitMax time.Duration

func init() {
	deaconCmd.AddCommand(deaconIdleWaitCmd)
	deaconIdleWaitCmd.Flags().DurationVar(&idleWaitMax, "max", 5*time.Minute,
		"Maximum sleep duration (caps the backoff)")
}

func runDeaconIdleWait(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	state := daemon.ReadIdleState(townRoot)
	if state == nil || !state.Idle {
		// System is active — return immediately.
		fmt.Printf("%s System active, no wait needed\n", style.Dim.Render("○"))
		return nil
	}

	sleepDuration := state.BackoffInterval
	if sleepDuration <= 0 {
		sleepDuration = 30 * time.Second
	}
	if sleepDuration > idleWaitMax {
		sleepDuration = idleWaitMax
	}

	fmt.Printf("%s System idle, sleeping %s (backoff)\n",
		style.Dim.Render("○"), sleepDuration.Round(time.Second))

	// Sleep with periodic wake signal checks.
	// This allows early wake when sling fires.
	deadline := time.Now().Add(sleepDuration)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check for wake signal (written by sling).
			if _, err := os.Stat(daemon.IdleWakePath(townRoot)); err == nil {
				fmt.Printf("%s Wake signal detected, returning early\n", style.Bold.Render("▶"))
				return nil
			}
			// Check if we've slept long enough.
			if time.Now().After(deadline) {
				return nil
			}
		}
	}
}

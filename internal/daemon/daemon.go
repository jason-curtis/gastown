package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/keepalive"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Daemon is the town-level background service.
// It ensures patrol agents (Deacon, Witnesses) are running and detects failures.
// This is recovery-focused: normal wake is handled by feed subscription (bd activity --follow).
// The daemon is the safety net for dead sessions, GUPP violations, and orphaned work.
type Daemon struct {
	config *Config
	tmux   *tmux.Tmux
	logger *log.Logger
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new daemon instance.
func New(config *Config) (*Daemon, error) {
	// Ensure daemon directory exists
	daemonDir := filepath.Dir(config.LogFile)
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return nil, fmt.Errorf("creating daemon directory: %w", err)
	}

	// Open log file
	logFile, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	logger := log.New(logFile, "", log.LstdFlags)
	ctx, cancel := context.WithCancel(context.Background())

	return &Daemon{
		config: config,
		tmux:   tmux.NewTmux(),
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Run starts the daemon main loop.
func (d *Daemon) Run() error {
	d.logger.Printf("Daemon starting (PID %d)", os.Getpid())

	// Write PID file
	if err := os.WriteFile(d.config.PidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}
	defer func() { _ = os.Remove(d.config.PidFile) }() // best-effort cleanup

	// Update state
	state := &State{
		Running:   true,
		PID:       os.Getpid(),
		StartedAt: time.Now(),
	}
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save state: %v", err)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)

	// Dynamic heartbeat timer with exponential backoff based on activity
	// Start with base interval
	nextInterval := d.config.HeartbeatInterval
	timer := time.NewTimer(nextInterval)
	defer timer.Stop()

	d.logger.Printf("Daemon running, initial heartbeat interval %v", nextInterval)

	// Initial heartbeat
	d.heartbeat(state)

	for {
		select {
		case <-d.ctx.Done():
			d.logger.Println("Daemon context cancelled, shutting down")
			return d.shutdown(state)

		case sig := <-sigChan:
			if sig == syscall.SIGUSR1 {
				// SIGUSR1: immediate lifecycle processing (from gt handoff)
				d.logger.Println("Received SIGUSR1, processing lifecycle requests immediately")
				d.processLifecycleRequests()
			} else {
				d.logger.Printf("Received signal %v, shutting down", sig)
				return d.shutdown(state)
			}

		case <-timer.C:
			d.heartbeat(state)

			// Calculate next interval based on activity
			nextInterval = d.calculateHeartbeatInterval()
			timer.Reset(nextInterval)
			d.logger.Printf("Next heartbeat in %v", nextInterval)
		}
	}
}

// Backoff thresholds for exponential slowdown when idle
const (
	// Base interval when there's recent activity
	baseInterval = 5 * time.Minute

	// Tier thresholds for backoff
	tier1Threshold = 5 * time.Minute  // 0-5 min idle → 5 min interval
	tier2Threshold = 15 * time.Minute // 5-15 min idle → 10 min interval
	tier3Threshold = 45 * time.Minute // 15-45 min idle → 30 min interval
	// 45+ min idle → 60 min interval (max)

	// Corresponding intervals
	tier1Interval = 5 * time.Minute
	tier2Interval = 10 * time.Minute
	tier3Interval = 30 * time.Minute
	tier4Interval = 60 * time.Minute // max
)

// calculateHeartbeatInterval determines the next heartbeat interval based on activity.
// Reads ~/gt/daemon/activity.json to determine how long since the last gt/bd command.
// Returns exponentially increasing intervals as idle time grows.
//
// | Idle Duration | Next Heartbeat |
// |---------------|----------------|
// | 0-5 min       | 5 min (base)   |
// | 5-15 min      | 10 min         |
// | 15-45 min     | 30 min         |
// | 45+ min       | 60 min (max)   |
func (d *Daemon) calculateHeartbeatInterval() time.Duration {
	activity := keepalive.ReadTownActivity()
	if activity == nil {
		// No activity file - assume recent activity (might be first run)
		return baseInterval
	}

	idleDuration := activity.Age()

	switch {
	case idleDuration < tier1Threshold:
		return tier1Interval
	case idleDuration < tier2Threshold:
		return tier2Interval
	case idleDuration < tier3Threshold:
		return tier3Interval
	default:
		return tier4Interval
	}
}

// heartbeat performs one heartbeat cycle.
// The daemon is recovery-focused: it ensures agents are running and detects failures.
// Normal wake is handled by feed subscription (bd activity --follow).
// The daemon is the safety net for edge cases:
// - Dead sessions that need restart
// - Agents with work-on-hook not progressing (GUPP violation)
// - Orphaned work (assigned to dead agents)
func (d *Daemon) heartbeat(state *State) {
	d.logger.Println("Heartbeat starting (recovery-focused)")

	// 1. Ensure Deacon is running (restart if dead)
	d.ensureDeaconRunning()

	// 2. Ensure Witnesses are running for all rigs (restart if dead)
	d.ensureWitnessesRunning()

	// 3. Trigger pending polecat spawns (bootstrap mode - ZFC violation acceptable)
	// This ensures polecats get nudged even when Deacon isn't in a patrol cycle.
	// Uses regex-based WaitForClaudeReady, which is acceptable for daemon bootstrap.
	d.triggerPendingSpawns()

	// 4. Process lifecycle requests
	d.processLifecycleRequests()

	// 5. Check for stale agents (timeout fallback)
	// Agents that report "running" but haven't updated in too long are marked dead
	d.checkStaleAgents()

	// 6. Check for GUPP violations (agents with work-on-hook not progressing)
	d.checkGUPPViolations()

	// 7. Check for orphaned work (assigned to dead agents)
	d.checkOrphanedWork()

	// 8. Check polecat session health (proactive crash detection)
	// This validates tmux sessions are still alive for polecats with work-on-hook
	d.checkPolecatSessionHealth()

	// Update state
	state.LastHeartbeat = time.Now()
	state.HeartbeatCount++
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save state: %v", err)
	}

	d.logger.Printf("Heartbeat complete (#%d)", state.HeartbeatCount)
}

// DeaconSessionName is the tmux session name for the Deacon.
const DeaconSessionName = "gt-deacon"

// DeaconRole is the role name for the Deacon's handoff bead.
const DeaconRole = "deacon"


// ensureDeaconRunning ensures the Deacon is running.
// ZFC-compliant: trusts agent bead state, no tmux inference.
// The Deacon is the system's heartbeat - it must always be running.
func (d *Daemon) ensureDeaconRunning() {
	// Check agent bead state (ZFC: trust what agent reports)
	beadState, beadErr := d.getAgentBeadState("gt-deacon")
	if beadErr == nil {
		if beadState == "running" || beadState == "working" {
			// Agent reports it's running - trust it
			// Note: gt-2hzl4 will add timeout fallback for stale state
			return
		}
	}
	// Agent not running (or bead not found) - start it
	d.logger.Println("Deacon not running per agent bead, starting...")

	// Create session in deacon directory (ensures correct CLAUDE.md is loaded)
	deaconDir := filepath.Join(d.config.TownRoot, "deacon")
	if err := d.tmux.NewSession(DeaconSessionName, deaconDir); err != nil {
		d.logger.Printf("Error creating Deacon session: %v", err)
		return
	}

	// Set environment (non-fatal: session works without these)
	_ = d.tmux.SetEnvironment(DeaconSessionName, "GT_ROLE", "deacon")
	_ = d.tmux.SetEnvironment(DeaconSessionName, "BD_ACTOR", "deacon")

	// Launch Claude directly (no shell respawn loop)
	// The daemon will detect if Claude exits and restart it on next heartbeat
	// Export GT_ROLE and BD_ACTOR so Claude inherits them (tmux SetEnvironment doesn't export to processes)
	if err := d.tmux.SendKeys(DeaconSessionName, "export GT_ROLE=deacon BD_ACTOR=deacon && claude --dangerously-skip-permissions"); err != nil {
		d.logger.Printf("Error launching Claude in Deacon session: %v", err)
		return
	}

	d.logger.Println("Deacon session started successfully")
}


// ensureWitnessesRunning ensures witnesses are running for all rigs.
// Called on each heartbeat to maintain witness patrol loops.
func (d *Daemon) ensureWitnessesRunning() {
	rigs := d.getKnownRigs()
	for _, rigName := range rigs {
		d.ensureWitnessRunning(rigName)
	}
}

// ensureWitnessRunning ensures the witness for a specific rig is running.
func (d *Daemon) ensureWitnessRunning(rigName string) {
	agentID := beads.WitnessBeadID(rigName)
	sessionName := "gt-" + rigName + "-witness"

	// Check agent bead state (ZFC: trust what agent reports)
	beadState, beadErr := d.getAgentBeadState(agentID)
	if beadErr == nil {
		if beadState == "running" || beadState == "working" {
			// Agent reports it's running - trust it
			return
		}
	}

	// Agent not running (or bead not found) - start it
	d.logger.Printf("Witness for %s not running per agent bead, starting...", rigName)

	// Create session in witness directory
	witnessDir := filepath.Join(d.config.TownRoot, rigName, "witness")
	if err := d.tmux.NewSession(sessionName, witnessDir); err != nil {
		d.logger.Printf("Error creating witness session for %s: %v", rigName, err)
		return
	}

	// Set environment
	_ = d.tmux.SetEnvironment(sessionName, "GT_ROLE", "witness")
	_ = d.tmux.SetEnvironment(sessionName, "GT_RIG", rigName)
	_ = d.tmux.SetEnvironment(sessionName, "BD_ACTOR", rigName+"-witness")

	// Launch Claude
	envExport := fmt.Sprintf("export GT_ROLE=witness GT_RIG=%s BD_ACTOR=%s-witness && claude --dangerously-skip-permissions", rigName, rigName)
	if err := d.tmux.SendKeys(sessionName, envExport); err != nil {
		d.logger.Printf("Error launching Claude in witness session for %s: %v", rigName, err)
		return
	}

	d.logger.Printf("Witness session for %s started successfully", rigName)
}


// getKnownRigs returns list of registered rig names.
func (d *Daemon) getKnownRigs() []string {
	rigsPath := filepath.Join(d.config.TownRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return nil
	}

	var parsed struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}

	var rigs []string
	for name := range parsed.Rigs {
		rigs = append(rigs, name)
	}
	return rigs
}

// triggerPendingSpawns polls pending polecat spawns and triggers those that are ready.
// This is bootstrap mode - uses regex-based WaitForClaudeReady which is acceptable
// for daemon operations when no AI agent is guaranteed to be running.
// The timeout is short (2s) to avoid blocking the heartbeat.
func (d *Daemon) triggerPendingSpawns() {
	const triggerTimeout = 2 * time.Second

	// Check for pending spawns (from POLECAT_STARTED messages in Deacon inbox)
	pending, err := polecat.CheckInboxForSpawns(d.config.TownRoot)
	if err != nil {
		d.logger.Printf("Error checking pending spawns: %v", err)
		return
	}

	if len(pending) == 0 {
		return
	}

	d.logger.Printf("Found %d pending spawn(s), attempting to trigger...", len(pending))

	// Trigger pending spawns (uses WaitForClaudeReady with short timeout)
	results, err := polecat.TriggerPendingSpawns(d.config.TownRoot, triggerTimeout)
	if err != nil {
		d.logger.Printf("Error triggering spawns: %v", err)
		return
	}

	// Log results
	triggered := 0
	for _, r := range results {
		if r.Triggered {
			triggered++
			d.logger.Printf("Triggered polecat: %s/%s", r.Spawn.Rig, r.Spawn.Polecat)
		} else if r.Error != nil {
			d.logger.Printf("Error triggering %s: %v", r.Spawn.Session, r.Error)
		}
	}

	if triggered > 0 {
		d.logger.Printf("Triggered %d/%d pending spawn(s)", triggered, len(pending))
	}

	// Prune stale pending spawns (older than 5 minutes - likely dead sessions)
	pruned, _ := polecat.PruneStalePending(d.config.TownRoot, 5*time.Minute)
	if pruned > 0 {
		d.logger.Printf("Pruned %d stale pending spawn(s)", pruned)
	}
}

// processLifecycleRequests checks for and processes lifecycle requests.
func (d *Daemon) processLifecycleRequests() {
	d.ProcessLifecycleRequests()
}

// shutdown performs graceful shutdown.
func (d *Daemon) shutdown(state *State) error {
	d.logger.Println("Daemon shutting down")

	state.Running = false
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save final state: %v", err)
	}

	d.logger.Println("Daemon stopped")
	return nil
}

// Stop signals the daemon to stop.
func (d *Daemon) Stop() {
	d.cancel()
}

// IsRunning checks if a daemon is running for the given town.
func IsRunning(townRoot string) (bool, int, error) {
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return false, 0, nil
	}

	// Check if process is running
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0, nil
	}

	// On Unix, FindProcess always succeeds. Send signal 0 to check if alive.
	err = process.Signal(syscall.Signal(0))
	if err != nil {
		// Process not running, clean up stale PID file (best-effort cleanup)
		_ = os.Remove(pidFile)
		return false, 0, nil
	}

	return true, pid, nil
}

// StopDaemon stops the running daemon for the given town.
func StopDaemon(townRoot string) error {
	running, pid, err := IsRunning(townRoot)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("daemon is not running")
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process: %w", err)
	}

	// Send SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM: %w", err)
	}

	// Wait a bit for graceful shutdown
	time.Sleep(constants.ShutdownNotifyDelay)

	// Check if still running
	if err := process.Signal(syscall.Signal(0)); err == nil {
		// Still running, force kill (best-effort)
		_ = process.Signal(syscall.SIGKILL)
	}

	// Clean up PID file (best-effort cleanup)
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	_ = os.Remove(pidFile)

	return nil
}

// checkPolecatSessionHealth proactively validates polecat tmux sessions.
// This detects crashed polecats that:
// 1. Have work-on-hook (assigned work)
// 2. Report state=running/working in their agent bead
// 3. But the tmux session is actually dead
//
// When a crash is detected, the polecat is automatically restarted.
// This provides faster recovery than waiting for GUPP timeout or Witness detection.
func (d *Daemon) checkPolecatSessionHealth() {
	rigs := d.getKnownRigs()
	for _, rigName := range rigs {
		d.checkRigPolecatHealth(rigName)
	}
}

// checkRigPolecatHealth checks polecat session health for a specific rig.
func (d *Daemon) checkRigPolecatHealth(rigName string) {
	// Get polecat directories for this rig
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return // No polecats directory - rig might not have polecats
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		polecatName := entry.Name()
		d.checkPolecatHealth(rigName, polecatName)
	}
}

// checkPolecatHealth checks a single polecat's session health.
// If the polecat has work-on-hook but the tmux session is dead, it's restarted.
func (d *Daemon) checkPolecatHealth(rigName, polecatName string) {
	// Build the expected tmux session name
	sessionName := fmt.Sprintf("gt-%s-%s", rigName, polecatName)

	// Check if tmux session exists
	sessionAlive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		d.logger.Printf("Error checking session %s: %v", sessionName, err)
		return
	}

	if sessionAlive {
		// Session is alive - nothing to do
		return
	}

	// Session is dead. Check if the polecat has work-on-hook.
	agentBeadID := beads.PolecatBeadID(rigName, polecatName)
	info, err := d.getAgentBeadInfo(agentBeadID)
	if err != nil {
		// Agent bead doesn't exist or error - polecat might not be registered
		return
	}

	// Check if polecat has hooked work
	if info.HookBead == "" {
		// No hooked work - no need to restart (polecat was idle)
		return
	}

	// Polecat has work but session is dead - this is a crash!
	d.logger.Printf("CRASH DETECTED: polecat %s/%s has hook_bead=%s but session %s is dead",
		rigName, polecatName, info.HookBead, sessionName)

	// Auto-restart the polecat
	if err := d.restartPolecatSession(rigName, polecatName, sessionName); err != nil {
		d.logger.Printf("Error restarting polecat %s/%s: %v", rigName, polecatName, err)
		// Notify witness as fallback
		d.notifyWitnessOfCrashedPolecat(rigName, polecatName, info.HookBead, err)
	} else {
		d.logger.Printf("Successfully restarted crashed polecat %s/%s", rigName, polecatName)
	}
}

// restartPolecatSession restarts a crashed polecat session.
func (d *Daemon) restartPolecatSession(rigName, polecatName, sessionName string) error {
	// Determine working directory
	workDir := filepath.Join(d.config.TownRoot, rigName, "polecats", polecatName)

	// Verify the worktree exists
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		return fmt.Errorf("polecat worktree does not exist: %s", workDir)
	}

	// Pre-sync workspace (ensure beads are current)
	d.syncWorkspace(workDir)

	// Create new tmux session
	if err := d.tmux.NewSession(sessionName, workDir); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	// Set environment variables
	_ = d.tmux.SetEnvironment(sessionName, "GT_ROLE", "polecat")
	_ = d.tmux.SetEnvironment(sessionName, "GT_RIG", rigName)
	_ = d.tmux.SetEnvironment(sessionName, "GT_POLECAT", polecatName)

	bdActor := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
	_ = d.tmux.SetEnvironment(sessionName, "BD_ACTOR", bdActor)

	beadsDir := filepath.Join(d.config.TownRoot, rigName, ".beads")
	_ = d.tmux.SetEnvironment(sessionName, "BEADS_DIR", beadsDir)
	_ = d.tmux.SetEnvironment(sessionName, "BEADS_NO_DAEMON", "1")
	_ = d.tmux.SetEnvironment(sessionName, "BEADS_AGENT_NAME", fmt.Sprintf("%s/%s", rigName, polecatName))

	// Apply theme
	theme := tmux.AssignTheme(rigName)
	_ = d.tmux.ConfigureGasTownSession(sessionName, theme, rigName, polecatName, "polecat")

	// Set pane-died hook for future crash detection
	agentID := fmt.Sprintf("%s/%s", rigName, polecatName)
	_ = d.tmux.SetPaneDiedHook(sessionName, agentID)

	// Launch Claude with environment exported inline
	startCmd := fmt.Sprintf("export GT_ROLE=polecat GT_RIG=%s GT_POLECAT=%s BD_ACTOR=%s && claude --dangerously-skip-permissions",
		rigName, polecatName, bdActor)
	if err := d.tmux.SendKeys(sessionName, startCmd); err != nil {
		return fmt.Errorf("sending startup command: %w", err)
	}

	return nil
}

// notifyWitnessOfCrashedPolecat notifies the witness when a polecat restart fails.
func (d *Daemon) notifyWitnessOfCrashedPolecat(rigName, polecatName, hookBead string, restartErr error) {
	witnessAddr := rigName + "/witness"
	subject := fmt.Sprintf("CRASHED_POLECAT: %s/%s restart failed", rigName, polecatName)
	body := fmt.Sprintf(`Polecat %s crashed and automatic restart failed.

hook_bead: %s
restart_error: %v

Manual intervention may be required.`,
		polecatName, hookBead, restartErr)

	cmd := exec.Command("gt", "mail", "send", witnessAddr, "-s", subject, "-m", body)
	cmd.Dir = d.config.TownRoot
	if err := cmd.Run(); err != nil {
		d.logger.Printf("Warning: failed to notify witness of crashed polecat: %v", err)
	}
}

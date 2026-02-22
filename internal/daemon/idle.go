package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

const (
	// DefaultIdleGracePeriod is how long the system must be idle before
	// the Dolt server is stopped. Prevents thrashing from brief gaps
	// between back-to-back slings.
	DefaultIdleGracePeriod = 60 * time.Second

	// idleStateFile is the filename for idle state within the daemon directory.
	idleStateFile = "idle-state.json"

	// idleWakeFile is a signal file that sling writes to wake the system
	// from idle state. The daemon clears it on the next heartbeat.
	idleWakeFile = "idle-wake"
)

// IdleState represents the system's idle/active state.
type IdleState struct {
	// Idle is true when no polecats or convoys are active.
	Idle bool `json:"idle"`

	// Since is when the system became idle (zero if not idle).
	Since time.Time `json:"since,omitempty"`

	// PolecatCount is the number of active polecat tmux sessions.
	PolecatCount int `json:"polecat_count"`

	// ConvoyCount is the number of open convoys.
	ConvoyCount int `json:"convoy_count"`

	// DoltStopped is true when Dolt was intentionally stopped due to idle.
	DoltStopped bool `json:"dolt_stopped,omitempty"`

	// BackoffInterval is the recommended sleep duration for the deacon
	// between patrol cycles. Increases when idle, resets when active.
	BackoffInterval time.Duration `json:"backoff_interval,omitempty"`

	// UpdatedAt is when this state was last written.
	UpdatedAt time.Time `json:"updated_at"`
}

// IdleStatePath returns the path to the idle state file.
func IdleStatePath(townRoot string) string {
	return filepath.Join(townRoot, "daemon", idleStateFile)
}

// IdleWakePath returns the path to the idle wake signal file.
func IdleWakePath(townRoot string) string {
	return filepath.Join(townRoot, "daemon", idleWakeFile)
}

// WriteIdleState writes the idle state to disk.
func WriteIdleState(townRoot string, state *IdleState) error {
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(IdleStatePath(townRoot), data, 0644)
}

// ReadIdleState reads the idle state from disk.
// Returns nil if the file doesn't exist.
func ReadIdleState(townRoot string) *IdleState {
	data, err := os.ReadFile(IdleStatePath(townRoot))
	if err != nil {
		return nil
	}
	var state IdleState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

// IsSystemIdle returns true if the system has no active polecats or convoys.
// This is a quick check for use by other packages (e.g., sling).
func IsSystemIdle(townRoot string) bool {
	state := ReadIdleState(townRoot)
	return state != nil && state.Idle
}

// IsDoltIdleStopped returns true if Dolt was intentionally stopped due to idle.
func IsDoltIdleStopped(townRoot string) bool {
	state := ReadIdleState(townRoot)
	return state != nil && state.DoltStopped
}

// SignalWake writes the wake signal file to tell the daemon to exit idle state.
// Called by sling before starting work.
func SignalWake(townRoot string) error {
	wakePath := IdleWakePath(townRoot)
	if err := os.MkdirAll(filepath.Dir(wakePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(wakePath, []byte(time.Now().UTC().Format(time.RFC3339)), 0644)
}

// ConsumeWakeSignal checks for and removes the wake signal file.
// Returns true if a wake signal was present.
func ConsumeWakeSignal(townRoot string) bool {
	wakePath := IdleWakePath(townRoot)
	if _, err := os.Stat(wakePath); err != nil {
		return false
	}
	_ = os.Remove(wakePath)
	return true
}

// countActivePolecatSessions counts polecat tmux sessions across all rigs.
func countActivePolecatSessions() int {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		identity, err := session.ParseSessionName(line)
		if err != nil {
			continue
		}
		if identity.Role == session.RolePolecat {
			count++
		}
	}
	return count
}

// countOpenConvoys counts open convoys using gt convoy list.
func countOpenConvoys(townRoot string) int {
	cmd := exec.Command("gt", "convoy", "list", "--json")
	cmd.Dir = townRoot
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	// Parse JSON output to count open convoys
	var convoys []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return 0
	}

	count := 0
	for _, c := range convoys {
		if c.Status == "open" || c.Status == "" {
			count++
		}
	}
	return count
}

// nextBackoffInterval calculates the next backoff duration for deacon patrol.
// Doubles the current interval up to 5 minutes.
func nextBackoffInterval(current time.Duration) time.Duration {
	const (
		minBackoff = 30 * time.Second
		maxBackoff = 5 * time.Minute
	)
	if current < minBackoff {
		return minBackoff
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

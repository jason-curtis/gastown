// Package ratelimit provides rate limit state management for Claude Pro/Max subscriptions.
//
// When Claude Code sessions hit API rate limits, they stop processing. This package
// provides a mechanism to record when rate limits are hit, when they reset, and allows
// the daemon to automatically wake agents when the rate limit period ends.
//
// Rate limit state is stored in <townRoot>/.runtime/ratelimit/state.json and is
// checked by the daemon on each heartbeat cycle.
package ratelimit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// State represents the current rate limit state.
// When a rate limit is active, ResetAt indicates when it should clear.
type State struct {
	// Active is true if a rate limit is currently in effect.
	Active bool `json:"active"`

	// ResetAt is when the rate limit is expected to reset.
	// The daemon will attempt to wake agents after this time.
	ResetAt time.Time `json:"reset_at"`

	// RecordedAt is when this rate limit was recorded.
	RecordedAt time.Time `json:"recorded_at"`

	// RecordedBy identifies who/what recorded the rate limit.
	// Could be "daemon", "deacon", "polecat/name", etc.
	RecordedBy string `json:"recorded_by,omitempty"`

	// Reason provides additional context about the rate limit.
	// e.g., "API rate limit exceeded", "Claude Pro limit reached"
	Reason string `json:"reason,omitempty"`

	// RetryAfterSeconds is the original retry-after value from the API.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`

	// WakeAttempts tracks how many times we've tried to wake after reset.
	// Used to prevent infinite wake loops if rate limit persists.
	WakeAttempts int `json:"wake_attempts,omitempty"`

	// LastWakeAttempt is when we last tried to wake agents.
	LastWakeAttempt time.Time `json:"last_wake_attempt,omitempty"`
}

// GetStateFile returns the path to the rate limit state file.
func GetStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "ratelimit", "state.json")
}

// LoadState loads the rate limit state from disk.
// Returns nil if the state file doesn't exist.
func LoadState(townRoot string) (*State, error) {
	stateFile := GetStateFile(townRoot)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// SaveState saves the rate limit state to disk.
func SaveState(townRoot string, state *State) error {
	stateFile := GetStateFile(townRoot)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(stateFile, data, 0644)
}

// ClearState removes the rate limit state file.
func ClearState(townRoot string) error {
	stateFile := GetStateFile(townRoot)
	err := os.Remove(stateFile)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// WakeBuffer is the buffer time to wait after the reset time before waking agents.
// This accounts for potential clock skew and allows the API to fully reset.
const WakeBuffer = 2 * time.Minute

// MaxWakeAttempts is the maximum number of wake attempts before giving up.
// This prevents infinite wake loops if the rate limit persists.
const MaxWakeAttempts = 3

// WakeAttemptCooldown is the minimum time between wake attempts.
const WakeAttemptCooldown = 5 * time.Minute

// ShouldWake checks if it's time to wake agents after a rate limit reset.
// Returns true if:
// - A rate limit is active
// - The reset time has passed (plus buffer)
// - We haven't exceeded max wake attempts
// - Enough time has passed since the last wake attempt
func (s *State) ShouldWake() bool {
	if !s.Active {
		return false
	}

	// Check if reset time has passed (with buffer)
	wakeTime := s.ResetAt.Add(WakeBuffer)
	if time.Now().Before(wakeTime) {
		return false
	}

	// Check wake attempt limits
	if s.WakeAttempts >= MaxWakeAttempts {
		return false
	}

	// Check cooldown between attempts
	if !s.LastWakeAttempt.IsZero() {
		if time.Since(s.LastWakeAttempt) < WakeAttemptCooldown {
			return false
		}
	}

	return true
}

// RecordWakeAttempt records that a wake attempt was made.
func (s *State) RecordWakeAttempt() {
	s.WakeAttempts++
	s.LastWakeAttempt = time.Now()
}

// rateLimitPatterns are regex patterns to detect rate limit messages in Claude Code output.
// These patterns are designed to match various rate limit message formats that Claude Code
// might output when hitting API limits.
var rateLimitPatterns = []*regexp.Regexp{
	// "Usage limit reached" or "rate limit" patterns
	regexp.MustCompile(`(?i)(?:usage|rate)\s*limit\s*(?:reached|exceeded|hit)`),

	// "Try again" with time indicator
	regexp.MustCompile(`(?i)try\s*again\s*(?:in|after)\s*(\d+)\s*(minutes?|hours?|seconds?)`),

	// RFC3339 timestamp in error message
	regexp.MustCompile(`(?i)(?:reset[s]?|available)\s*(?:at|after)\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})`),

	// "X minutes" or "X hours" remaining/until reset
	regexp.MustCompile(`(?i)(\d+)\s*(minutes?|hours?)\s*(?:remaining|until\s*reset)`),

	// Anthropic-specific patterns
	regexp.MustCompile(`(?i)claude\s*(?:pro|max)?\s*limit`),
	regexp.MustCompile(`(?i)anthropic.*rate.*limit`),

	// Error code pattern (429 is HTTP rate limit)
	regexp.MustCompile(`(?i)error.*429`),
}

// ParseRateLimitOutput checks if the given output contains rate limit indicators.
// If a rate limit is detected, it returns a State with the parsed information.
// If no rate limit is detected, it returns nil.
func ParseRateLimitOutput(output string) *State {
	// First check if any rate limit pattern matches
	isRateLimited := false
	reason := ""
	for _, pattern := range rateLimitPatterns {
		if pattern.MatchString(output) {
			isRateLimited = true
			// Extract the matching portion for the reason
			if matches := pattern.FindStringSubmatch(output); len(matches) > 0 {
				reason = matches[0]
			}
			break
		}
	}

	if !isRateLimited {
		return nil
	}

	// Try to extract reset time
	resetAt := extractResetTime(output)
	if resetAt.IsZero() {
		// Default to 1 hour if we can't parse the reset time
		resetAt = time.Now().Add(1 * time.Hour)
	}

	return &State{
		Active:     true,
		ResetAt:    resetAt,
		RecordedAt: time.Now(),
		Reason:     reason,
	}
}

// extractResetTime attempts to extract a reset time from rate limit output.
// Returns zero time if no reset time can be determined.
func extractResetTime(output string) time.Time {
	// Try RFC3339 timestamp first
	tsPattern := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})?)`)
	if matches := tsPattern.FindStringSubmatch(output); len(matches) > 1 {
		if t, err := time.Parse(time.RFC3339, matches[1]); err == nil {
			return t
		}
		// Try without timezone
		if t, err := time.Parse("2006-01-02T15:04:05", matches[1]); err == nil {
			return t.Local()
		}
	}

	// Try "in X minutes/hours" pattern
	durPattern := regexp.MustCompile(`(?i)(?:in|after)\s*(\d+)\s*(minutes?|hours?|seconds?)`)
	if matches := durPattern.FindStringSubmatch(output); len(matches) > 2 {
		amount := 0
		if _, err := parseAmount(matches[1], &amount); err == nil {
			unit := strings.ToLower(matches[2])
			var duration time.Duration
			switch {
			case strings.HasPrefix(unit, "second"):
				duration = time.Duration(amount) * time.Second
			case strings.HasPrefix(unit, "minute"):
				duration = time.Duration(amount) * time.Minute
			case strings.HasPrefix(unit, "hour"):
				duration = time.Duration(amount) * time.Hour
			}
			if duration > 0 {
				return time.Now().Add(duration)
			}
		}
	}

	// Try "X minutes remaining" pattern
	remPattern := regexp.MustCompile(`(?i)(\d+)\s*(minutes?|hours?)\s*(?:remaining|left|until)`)
	if matches := remPattern.FindStringSubmatch(output); len(matches) > 2 {
		amount := 0
		if _, err := parseAmount(matches[1], &amount); err == nil {
			unit := strings.ToLower(matches[2])
			var duration time.Duration
			switch {
			case strings.HasPrefix(unit, "minute"):
				duration = time.Duration(amount) * time.Minute
			case strings.HasPrefix(unit, "hour"):
				duration = time.Duration(amount) * time.Hour
			}
			if duration > 0 {
				return time.Now().Add(duration)
			}
		}
	}

	// Try time of day pattern (e.g., "4:00 PM PST", "16:00")
	timePattern := regexp.MustCompile(`(?i)(?:at|after)\s*(\d{1,2}):(\d{2})\s*([AaPp][Mm])?(?:\s*([A-Z]{2,4}))?`)
	if matches := timePattern.FindStringSubmatch(output); len(matches) > 2 {
		hour := 0
		minute := 0
		if _, err := parseAmount(matches[1], &hour); err == nil {
			if _, err := parseAmount(matches[2], &minute); err == nil {
				// Handle AM/PM
				if len(matches) > 3 && matches[3] != "" {
					isPM := strings.ToUpper(matches[3]) == "PM"
					if isPM && hour < 12 {
						hour += 12
					} else if !isPM && hour == 12 {
						hour = 0
					}
				}

				// Create time for today, or tomorrow if the time has passed
				now := time.Now()
				resetTime := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
				if resetTime.Before(now) {
					resetTime = resetTime.Add(24 * time.Hour)
				}
				return resetTime
			}
		}
	}

	return time.Time{}
}

// parseAmount is a helper to parse integer amounts
func parseAmount(s string, result *int) (int, error) {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	*result = n
	return n, nil
}

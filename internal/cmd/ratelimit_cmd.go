package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/ratelimit"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	ratelimitSession string
	ratelimitVerbose bool
	ratelimitReason  string
	ratelimitMinutes int
)

func init() {
	rootCmd.AddCommand(ratelimitCmd)
	ratelimitCmd.AddCommand(ratelimitRecordCmd)
	ratelimitCmd.AddCommand(ratelimitStatusCmd)
	ratelimitCmd.AddCommand(ratelimitClearCmd)
	ratelimitCmd.AddCommand(ratelimitSetCmd)

	ratelimitRecordCmd.Flags().StringVar(&ratelimitSession, "session", "", "Session name (e.g., gt-gastown-toast)")
	ratelimitRecordCmd.Flags().BoolVarP(&ratelimitVerbose, "verbose", "v", false, "Show debug output")

	ratelimitSetCmd.Flags().IntVarP(&ratelimitMinutes, "minutes", "m", 60, "Minutes until rate limit resets")
	ratelimitSetCmd.Flags().StringVarP(&ratelimitReason, "reason", "r", "Manual rate limit", "Reason for rate limit")
}

var ratelimitCmd = &cobra.Command{
	Use:   "ratelimit",
	Short: "Manage rate limit state for Claude Pro/Max sessions",
	Long: `Manage rate limit state for Claude Pro/Max sessions.

When Claude Code sessions hit API rate limits, they stop processing. This command
provides a mechanism to record when rate limits are hit, when they reset, and
allows the daemon to automatically wake agents when the rate limit period ends.

Subcommands:
  gt ratelimit record    # Detect and record rate limit from session transcript (Stop hook)
  gt ratelimit status    # Show current rate limit state
  gt ratelimit clear     # Clear rate limit state (after manual verification)
  gt ratelimit set       # Manually set rate limit state

The record subcommand is designed to be called from a Claude Code Stop hook.
It parses the session transcript for rate limit messages and records the state.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var ratelimitRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Detect and record rate limit from session transcript (Stop hook)",
	Long: `Detect rate limit from session transcript and record state.

This command is intended to be called from a Claude Code Stop hook.
It reads the session transcript from ~/.claude/projects/... and searches
for rate limit error messages. If found, it records the rate limit state
so the daemon can wake agents after the limit resets.

Rate limit patterns detected:
- "rate limit" / "rate_limit" / "ratelimit"
- "usage limit" / "usage_limit"
- HTTP 429 errors
- "retry after" / "retry-after" with time values
- Claude-specific: "You've reached your limit"

Examples:
  gt ratelimit record --session gt-gastown-toast
  gt ratelimit record  # Auto-detect from GT_SESSION or tmux`,
	RunE: runRatelimitRecord,
}

var ratelimitStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current rate limit state",
	Long: `Show current rate limit state.

Displays whether a rate limit is currently active, when it's expected to reset,
and any wake attempt tracking information.`,
	RunE: runRatelimitStatus,
}

var ratelimitClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear rate limit state",
	Long: `Clear rate limit state.

Use this after manually verifying the rate limit has reset, or to force
the system to attempt waking agents again.`,
	RunE: runRatelimitClear,
}

var ratelimitSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Manually set rate limit state",
	Long: `Manually set rate limit state.

Use this when you know a rate limit is in effect but it wasn't auto-detected.

Examples:
  gt ratelimit set --minutes 60 --reason "Claude Pro limit"
  gt ratelimit set -m 30 -r "API rate limit"`,
	RunE: runRatelimitSet,
}

func runRatelimitRecord(cmd *cobra.Command, args []string) error {
	// Get session from flag or environment
	session := ratelimitSession
	if session == "" {
		session = os.Getenv("GT_SESSION")
	}
	if session == "" {
		session = deriveSessionName()
	}
	if session == "" {
		session = detectCurrentTmuxSession()
	}

	// Get working directory
	workDir := os.Getenv("GT_CWD")
	if workDir == "" {
		var err error
		workDir, err = getTmuxSessionWorkDir(session)
		if err != nil && ratelimitVerbose {
			fmt.Fprintf(os.Stderr, "[ratelimit] could not get workdir: %v\n", err)
		}
	}

	if workDir == "" {
		if ratelimitVerbose {
			fmt.Fprintf(os.Stderr, "[ratelimit] no workdir available, cannot check transcript\n")
		}
		return nil // Silent exit - nothing to do
	}

	// Find and read transcript
	transcript, err := readTranscript(workDir)
	if err != nil {
		if ratelimitVerbose {
			fmt.Fprintf(os.Stderr, "[ratelimit] could not read transcript: %v\n", err)
		}
		return nil // Silent exit
	}

	// Check for rate limit patterns
	isRateLimited, resetDuration, reason := detectRateLimit(transcript)
	if !isRateLimited {
		if ratelimitVerbose {
			fmt.Fprintf(os.Stderr, "[ratelimit] no rate limit detected in transcript\n")
		}
		return nil
	}

	// Get town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("getting town root: %w", err)
	}

	// Record the rate limit
	recordedBy := session
	if recordedBy == "" {
		recordedBy = "unknown"
	}

	if err := ratelimit.RecordRateLimit(townRoot, resetDuration, recordedBy, reason); err != nil {
		return fmt.Errorf("recording rate limit: %w", err)
	}

	fmt.Printf("%s Rate limit detected and recorded\n", style.Success.Render("⚠"))
	fmt.Printf("  Reason: %s\n", reason)
	fmt.Printf("  Resets in: %s\n", resetDuration.Round(time.Minute))
	fmt.Printf("  Recorded by: %s\n", recordedBy)

	return nil
}

func runRatelimitStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("getting town root: %w", err)
	}

	state, err := ratelimit.GetState(townRoot)
	if err != nil {
		return fmt.Errorf("reading rate limit state: %w", err)
	}

	if state == nil || !state.Active {
		fmt.Printf("%s No active rate limit\n", style.Success.Render("✓"))
		return nil
	}

	// Check if expired
	isLimited, _, _ := ratelimit.IsRateLimited(townRoot)

	headerStyle := lipgloss.NewStyle().Bold(true)

	if isLimited {
		fmt.Printf("%s Rate limit ACTIVE\n", style.Warning.Render("⚠"))
	} else {
		fmt.Printf("%s Rate limit EXPIRED (awaiting wake)\n", style.Info.Render("○"))
	}

	fmt.Printf("\n%s\n", headerStyle.Render("State:"))
	fmt.Printf("  Reset at:      %s\n", state.ResetAt.Local().Format(time.RFC1123))
	fmt.Printf("  Time remaining: %s\n", formatRatelimitDuration(time.Until(state.ResetAt)))
	fmt.Printf("  Recorded at:   %s\n", state.RecordedAt.Local().Format(time.RFC1123))
	fmt.Printf("  Recorded by:   %s\n", state.RecordedBy)
	if state.Reason != "" {
		fmt.Printf("  Reason:        %s\n", state.Reason)
	}
	if state.WakeAttempts > 0 {
		fmt.Printf("  Wake attempts: %d\n", state.WakeAttempts)
		fmt.Printf("  Last attempt:  %s\n", state.LastWakeAttempt.Local().Format(time.RFC1123))
	}

	return nil
}

func runRatelimitClear(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("getting town root: %w", err)
	}

	if err := ratelimit.Clear(townRoot); err != nil {
		return fmt.Errorf("clearing rate limit state: %w", err)
	}

	fmt.Printf("%s Rate limit state cleared\n", style.Success.Render("✓"))
	return nil
}

func runRatelimitSet(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("getting town root: %w", err)
	}

	resetDuration := time.Duration(ratelimitMinutes) * time.Minute
	recordedBy := os.Getenv("BD_ACTOR")
	if recordedBy == "" {
		recordedBy = "manual"
	}

	if err := ratelimit.RecordRateLimit(townRoot, resetDuration, recordedBy, ratelimitReason); err != nil {
		return fmt.Errorf("setting rate limit: %w", err)
	}

	fmt.Printf("%s Rate limit set\n", style.Success.Render("✓"))
	fmt.Printf("  Resets in: %d minutes\n", ratelimitMinutes)
	fmt.Printf("  Reason: %s\n", ratelimitReason)

	return nil
}

// detectRateLimit parses transcript content for rate limit indicators.
// Returns (isRateLimited, resetDuration, reason).
//
// Detection patterns are based on:
// - Anthropic API error format: {"type": "error", "error": {"type": "rate_limit_error", ...}}
// - HTTP 429 status code
// - retry-after header values
// - User-facing messages from Claude Code and Claude.ai
//
// Reference: https://platform.claude.com/docs/en/api/errors
// Reference: https://platform.claude.com/docs/en/api/rate-limits
func detectRateLimit(transcript string) (bool, time.Duration, string) {
	// Convert to lowercase for case-insensitive matching
	lower := strings.ToLower(transcript)

	// Check for rate limit patterns, ordered by specificity
	// Official API patterns first, then user-facing messages
	rateLimitPatterns := []struct {
		pattern string
		reason  string
	}{
		// Official Anthropic API error type (most specific)
		{"rate_limit_error", "Anthropic API rate_limit_error"},
		// HTTP status code
		{"status.*429", "HTTP 429 Too Many Requests"},
		{"error.*429", "HTTP 429 error"},
		{"429", "HTTP 429"},
		// API overload error (related but distinct)
		{"overloaded_error", "Anthropic API overloaded_error (529)"},
		// Rate limit phrases
		{"rate limit", "rate limit detected"},
		{"ratelimit", "ratelimit detected"},
		{"too many requests", "too many requests"},
		// Usage/subscription limits (Claude Pro/Max)
		{"usage limit", "usage limit reached"},
		{"you've reached your limit", "subscription limit reached"},
		{"you have reached your limit", "subscription limit reached"},
		{"exceeded your limit", "limit exceeded"},
		{"reached your usage limit", "usage limit reached"},
		{"usage cap", "usage cap reached"},
		// Token limits
		{"token limit", "token limit reached"},
		{"tokens per minute", "TPM limit"},
		{"requests per minute", "RPM limit"},
		// Generic
		{"api limit", "API limit"},
		{"request limit", "request limit"},
	}

	var found bool
	var reason string
	for _, p := range rateLimitPatterns {
		if strings.Contains(lower, p.pattern) {
			found = true
			reason = p.reason
			break
		}
	}

	if !found {
		return false, 0, ""
	}

	// Try to extract reset time
	resetDuration := extractResetDuration(transcript)
	if resetDuration == 0 {
		// Default to 1 hour if we can't parse the reset time
		// Claude Pro/Max limits typically reset hourly
		resetDuration = time.Hour
		reason += " (default 1h reset)"
	}

	return true, resetDuration, reason
}

// extractResetDuration tries to parse reset time from transcript.
// Handles multiple formats:
// - retry-after header: "retry-after: 60" (seconds)
// - Human readable: "retry after 5 minutes"
// - Anthropic API reset headers: "anthropic-ratelimit-tokens-reset: 2026-01-29T12:00:00Z"
// - Time-based: "reset at 3:00 PM"
func extractResetDuration(transcript string) time.Duration {
	lower := strings.ToLower(transcript)

	// Pattern: retry-after header with just seconds (API standard)
	// e.g., "retry-after: 60" or "retry-after\":60"
	retryAfterSecsRe := regexp.MustCompile(`retry-after["']?[:\s]+(\d+)`)
	if matches := retryAfterSecsRe.FindStringSubmatch(lower); len(matches) >= 2 {
		value, _ := strconv.Atoi(matches[1])
		if value > 0 && value < 86400 { // Sanity check: less than 24 hours
			return time.Duration(value) * time.Second
		}
	}

	// Pattern: "retry after X seconds/minutes/hours" (human readable)
	retryAfterRe := regexp.MustCompile(`retry[- ]?after[:\s]+(\d+)\s*(second|minute|hour|sec|min|hr|s|m|h)`)
	if matches := retryAfterRe.FindStringSubmatch(lower); len(matches) >= 3 {
		value, _ := strconv.Atoi(matches[1])
		unit := matches[2]
		switch {
		case strings.HasPrefix(unit, "s"):
			return time.Duration(value) * time.Second
		case strings.HasPrefix(unit, "m"):
			return time.Duration(value) * time.Minute
		case strings.HasPrefix(unit, "h"):
			return time.Duration(value) * time.Hour
		}
	}

	// Pattern: Anthropic reset timestamp header (RFC 3339)
	// e.g., "anthropic-ratelimit-tokens-reset: 2026-01-29T12:00:00Z"
	resetTimestampRe := regexp.MustCompile(`ratelimit-\w+-reset["']?:\s*["']?(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z?)`)
	if matches := resetTimestampRe.FindStringSubmatch(transcript); len(matches) >= 2 {
		if t, err := time.Parse(time.RFC3339, matches[1]); err == nil {
			if duration := time.Until(t); duration > 0 {
				return duration
			}
		}
	}

	// Pattern: "in X minutes/hours" or "try again in X"
	inTimeRe := regexp.MustCompile(`(?:reset|available|try again|wait)\s+(?:in\s+)?(\d+)\s*(second|minute|hour|sec|min|hr|s|m|h)`)
	if matches := inTimeRe.FindStringSubmatch(lower); len(matches) >= 3 {
		value, _ := strconv.Atoi(matches[1])
		unit := matches[2]
		switch {
		case strings.HasPrefix(unit, "s"):
			return time.Duration(value) * time.Second
		case strings.HasPrefix(unit, "m"):
			return time.Duration(value) * time.Minute
		case strings.HasPrefix(unit, "h"):
			return time.Duration(value) * time.Hour
		}
	}

	// Pattern: "at HH:MM" - calculate duration until that time
	atTimeRe := regexp.MustCompile(`(?:reset|available)\s+at\s+(\d{1,2}):(\d{2})`)
	if matches := atTimeRe.FindStringSubmatch(lower); len(matches) >= 3 {
		hour, _ := strconv.Atoi(matches[1])
		minute, _ := strconv.Atoi(matches[2])
		now := time.Now()
		resetTime := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if resetTime.Before(now) {
			resetTime = resetTime.Add(24 * time.Hour)
		}
		return time.Until(resetTime)
	}

	return 0
}

// readTranscript reads the Claude Code transcript from the working directory.
func readTranscript(workDir string) (string, error) {
	// Claude stores transcripts in ~/.claude/projects/<path-with-dashes>/
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Convert workDir path to Claude's format (slashes to dashes)
	projectPath := strings.ReplaceAll(workDir, "/", "-")
	if strings.HasPrefix(projectPath, "-") {
		projectPath = projectPath[1:]
	}

	transcriptDir := filepath.Join(home, ".claude", "projects", projectPath)

	// Find the most recent transcript file
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return "", fmt.Errorf("reading transcript dir: %w", err)
	}

	var latestFile string
	var latestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = filepath.Join(transcriptDir, entry.Name())
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("no transcript files found")
	}

	// Read and parse transcript - just extract message content
	data, err := os.ReadFile(latestFile)
	if err != nil {
		return "", err
	}

	// The transcript is JSON - extract text content
	var transcript struct {
		Messages []struct {
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &transcript); err != nil {
		// If not valid JSON, treat the whole file as text
		return string(data), nil
	}

	// Concatenate all message content
	var content strings.Builder
	for _, msg := range transcript.Messages {
		switch c := msg.Content.(type) {
		case string:
			content.WriteString(c)
			content.WriteString("\n")
		case []interface{}:
			for _, item := range c {
				if m, ok := item.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						content.WriteString(text)
						content.WriteString("\n")
					}
				}
			}
		}
	}

	return content.String(), nil
}

func formatRatelimitDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

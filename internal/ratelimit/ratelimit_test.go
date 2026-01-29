package ratelimit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateFilePath(t *testing.T) {
	got := GetStateFile("/home/user/gt")
	want := "/home/user/gt/.runtime/ratelimit/state.json"
	if got != want {
		t.Errorf("GetStateFile() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadState(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ratelimit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create and save state
	resetTime := time.Now().Add(1 * time.Hour)
	state := &State{
		Active:            true,
		ResetAt:           resetTime,
		RecordedAt:        time.Now(),
		RecordedBy:        "test",
		Reason:            "test rate limit",
		RetryAfterSeconds: 3600,
		WakeAttempts:      1,
		LastWakeAttempt:   time.Now().Add(-5 * time.Minute),
	}

	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Verify file exists
	stateFile := GetStateFile(tmpDir)
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("state file was not created")
	}

	// Load state
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if loaded == nil {
		t.Fatal("LoadState() returned nil")
	}

	if !loaded.Active {
		t.Error("loaded.Active = false, want true")
	}

	// Compare reset times (truncate to seconds for comparison)
	if !loaded.ResetAt.Truncate(time.Second).Equal(state.ResetAt.Truncate(time.Second)) {
		t.Errorf("loaded.ResetAt = %v, want %v", loaded.ResetAt, state.ResetAt)
	}

	if loaded.RecordedBy != "test" {
		t.Errorf("loaded.RecordedBy = %q, want %q", loaded.RecordedBy, "test")
	}

	if loaded.Reason != "test rate limit" {
		t.Errorf("loaded.Reason = %q, want %q", loaded.Reason, "test rate limit")
	}

	if loaded.WakeAttempts != 1 {
		t.Errorf("loaded.WakeAttempts = %d, want %d", loaded.WakeAttempts, 1)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ratelimit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	state, err := LoadState(tmpDir)
	if err != nil {
		t.Errorf("LoadState() error = %v, want nil", err)
	}
	if state != nil {
		t.Error("LoadState() returned non-nil for missing file")
	}
}

func TestClearState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ratelimit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create state
	state := &State{
		Active:     true,
		ResetAt:    time.Now().Add(1 * time.Hour),
		RecordedAt: time.Now(),
	}
	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Clear state
	if err := ClearState(tmpDir); err != nil {
		t.Fatalf("ClearState() error = %v", err)
	}

	// Verify file is gone
	stateFile := GetStateFile(tmpDir)
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Error("state file still exists after ClearState()")
	}

	// ClearState on non-existent file should not error
	if err := ClearState(tmpDir); err != nil {
		t.Errorf("ClearState() on missing file error = %v", err)
	}
}

func TestShouldWake(t *testing.T) {
	tests := []struct {
		name  string
		state *State
		want  bool
	}{
		{
			name: "not active",
			state: &State{
				Active:  false,
				ResetAt: time.Now().Add(-1 * time.Hour),
			},
			want: false,
		},
		{
			name: "reset time not passed",
			state: &State{
				Active:  true,
				ResetAt: time.Now().Add(1 * time.Hour),
			},
			want: false,
		},
		{
			name: "reset time just passed (within buffer)",
			state: &State{
				Active:  true,
				ResetAt: time.Now().Add(-1 * time.Minute), // Within 2-min buffer
			},
			want: false,
		},
		{
			name: "reset time passed with buffer",
			state: &State{
				Active:  true,
				ResetAt: time.Now().Add(-3 * time.Minute), // Past 2-min buffer
			},
			want: true,
		},
		{
			name: "max wake attempts reached",
			state: &State{
				Active:       true,
				ResetAt:      time.Now().Add(-1 * time.Hour),
				WakeAttempts: MaxWakeAttempts,
			},
			want: false,
		},
		{
			name: "wake cooldown not elapsed",
			state: &State{
				Active:          true,
				ResetAt:         time.Now().Add(-1 * time.Hour),
				WakeAttempts:    1,
				LastWakeAttempt: time.Now().Add(-1 * time.Minute), // Too recent
			},
			want: false,
		},
		{
			name: "wake cooldown elapsed",
			state: &State{
				Active:          true,
				ResetAt:         time.Now().Add(-1 * time.Hour),
				WakeAttempts:    1,
				LastWakeAttempt: time.Now().Add(-10 * time.Minute), // Long enough ago
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.ShouldWake()
			if got != tt.want {
				t.Errorf("ShouldWake() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecordWakeAttempt(t *testing.T) {
	state := &State{
		Active:       true,
		WakeAttempts: 0,
	}

	state.RecordWakeAttempt()

	if state.WakeAttempts != 1 {
		t.Errorf("WakeAttempts = %d, want 1", state.WakeAttempts)
	}

	if state.LastWakeAttempt.IsZero() {
		t.Error("LastWakeAttempt not set after RecordWakeAttempt()")
	}
}

func TestParseRateLimitOutput(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantActive  bool
		wantReason  string
		checkReset  bool
		resetWithin time.Duration // If checkReset is true, reset should be within this duration of now
	}{
		{
			name:       "no rate limit",
			output:     "Claude: Here's your code...",
			wantActive: false,
		},
		{
			name:       "usage limit reached",
			output:     "Error: Usage limit reached. Try again later.",
			wantActive: true,
			wantReason: "Usage limit reached",
		},
		{
			name:       "rate limit exceeded",
			output:     "API error: Rate limit exceeded",
			wantActive: true,
			wantReason: "Rate limit exceeded",
		},
		{
			name:        "try again in X minutes",
			output:      "You've hit your usage limit. Try again in 30 minutes.",
			wantActive:  true,
			checkReset:  true,
			resetWithin: 35 * time.Minute, // 30 min + some buffer
		},
		{
			name:        "try again in X hours",
			output:      "Rate limited. Try again in 2 hours.",
			wantActive:  true,
			checkReset:  true,
			resetWithin: 130 * time.Minute, // 2 hours + buffer
		},
		{
			name:       "Claude Pro limit",
			output:     "You've reached your Claude Pro limit for this period.",
			wantActive: true,
		},
		{
			name:       "HTTP 429 error",
			output:     "Error 429: Too many requests",
			wantActive: true,
		},
		{
			name:       "anthropic rate limit",
			output:     "Anthropic API rate limit hit",
			wantActive: true,
		},
		{
			name:        "minutes remaining",
			output:      "Usage limit reached. 45 minutes remaining until reset.",
			wantActive:  true,
			checkReset:  true,
			resetWithin: 50 * time.Minute,
		},
		{
			name:       "RFC3339 timestamp",
			output:     "Rate limited. Resets at 2026-01-29T18:00:00Z",
			wantActive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseRateLimitOutput(tt.output)

			if tt.wantActive {
				if result == nil {
					t.Fatal("ParseRateLimitOutput() returned nil, want non-nil")
				}
				if !result.Active {
					t.Error("result.Active = false, want true")
				}
				if tt.wantReason != "" && result.Reason != tt.wantReason {
					t.Errorf("result.Reason = %q, want %q", result.Reason, tt.wantReason)
				}
				if tt.checkReset {
					expected := time.Now().Add(tt.resetWithin)
					if result.ResetAt.After(expected) {
						t.Errorf("ResetAt = %v, want before %v", result.ResetAt, expected)
					}
					if result.ResetAt.Before(time.Now()) {
						t.Errorf("ResetAt = %v, want after now", result.ResetAt)
					}
				}
			} else {
				if result != nil {
					t.Errorf("ParseRateLimitOutput() = %+v, want nil", result)
				}
			}
		})
	}
}

func TestExtractResetTime(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		expectNonZero  bool
		expectedOffset time.Duration // Approximate offset from now
	}{
		{
			name:          "no time info",
			output:        "Rate limit reached",
			expectNonZero: false,
		},
		{
			name:           "in 30 minutes",
			output:         "Try again in 30 minutes",
			expectNonZero:  true,
			expectedOffset: 30 * time.Minute,
		},
		{
			name:           "in 2 hours",
			output:         "Available again in 2 hours",
			expectNonZero:  true,
			expectedOffset: 2 * time.Hour,
		},
		{
			name:           "45 minutes remaining",
			output:         "45 minutes remaining",
			expectNonZero:  true,
			expectedOffset: 45 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractResetTime(tt.output)

			if tt.expectNonZero {
				if result.IsZero() {
					t.Fatal("extractResetTime() returned zero time, want non-zero")
				}

				// Check that the result is approximately correct
				expected := time.Now().Add(tt.expectedOffset)
				diff := result.Sub(expected)
				if diff < 0 {
					diff = -diff
				}
				// Allow 2-minute tolerance for test execution time
				if diff > 2*time.Minute {
					t.Errorf("extractResetTime() = %v, want approximately %v (diff: %v)",
						result, expected, diff)
				}
			} else {
				if !result.IsZero() {
					t.Errorf("extractResetTime() = %v, want zero time", result)
				}
			}
		})
	}
}

func TestStateDirectoryCreation(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ratelimit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save state (should create directories)
	state := &State{
		Active:     true,
		ResetAt:    time.Now().Add(1 * time.Hour),
		RecordedAt: time.Now(),
	}

	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Verify directory structure was created
	runtimeDir := filepath.Join(tmpDir, ".runtime", "ratelimit")
	if _, err := os.Stat(runtimeDir); os.IsNotExist(err) {
		t.Error(".runtime/ratelimit directory was not created")
	}
}

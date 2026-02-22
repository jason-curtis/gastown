package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAndReadIdleState(t *testing.T) {
	dir := t.TempDir()
	daemonDir := filepath.Join(dir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	state := &IdleState{
		Idle:            true,
		Since:           time.Now().Add(-5 * time.Minute),
		PolecatCount:    0,
		ConvoyCount:     0,
		DoltStopped:     true,
		BackoffInterval: 2 * time.Minute,
	}

	if err := WriteIdleState(dir, state); err != nil {
		t.Fatalf("WriteIdleState: %v", err)
	}

	got := ReadIdleState(dir)
	if got == nil {
		t.Fatal("ReadIdleState returned nil")
	}
	if !got.Idle {
		t.Error("expected Idle=true")
	}
	if !got.DoltStopped {
		t.Error("expected DoltStopped=true")
	}
	if got.BackoffInterval != 2*time.Minute {
		t.Errorf("BackoffInterval = %v, want 2m", got.BackoffInterval)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestReadIdleState_Missing(t *testing.T) {
	dir := t.TempDir()
	got := ReadIdleState(dir)
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestIsSystemIdle(t *testing.T) {
	dir := t.TempDir()
	daemonDir := filepath.Join(dir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No state file → not idle.
	if IsSystemIdle(dir) {
		t.Error("expected not idle when no state file")
	}

	// Write idle state.
	if err := WriteIdleState(dir, &IdleState{Idle: true}); err != nil {
		t.Fatal(err)
	}
	if !IsSystemIdle(dir) {
		t.Error("expected idle after writing idle state")
	}

	// Write active state.
	if err := WriteIdleState(dir, &IdleState{Idle: false, PolecatCount: 1}); err != nil {
		t.Fatal(err)
	}
	if IsSystemIdle(dir) {
		t.Error("expected not idle after writing active state")
	}
}

func TestIsDoltIdleStopped(t *testing.T) {
	dir := t.TempDir()
	daemonDir := filepath.Join(dir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	if IsDoltIdleStopped(dir) {
		t.Error("expected false when no state file")
	}

	if err := WriteIdleState(dir, &IdleState{Idle: true, DoltStopped: true}); err != nil {
		t.Fatal(err)
	}
	if !IsDoltIdleStopped(dir) {
		t.Error("expected true after idle-stop")
	}
}

func TestSignalWakeAndConsume(t *testing.T) {
	dir := t.TempDir()
	daemonDir := filepath.Join(dir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No signal initially.
	if ConsumeWakeSignal(dir) {
		t.Error("expected no signal initially")
	}

	// Write signal.
	if err := SignalWake(dir); err != nil {
		t.Fatalf("SignalWake: %v", err)
	}

	// Consume it.
	if !ConsumeWakeSignal(dir) {
		t.Error("expected signal after SignalWake")
	}

	// Signal consumed — second consume should return false.
	if ConsumeWakeSignal(dir) {
		t.Error("expected signal consumed")
	}
}

func TestNextBackoffInterval(t *testing.T) {
	tests := []struct {
		current time.Duration
		want    time.Duration
	}{
		{0, 30 * time.Second},
		{10 * time.Second, 30 * time.Second},
		{30 * time.Second, 60 * time.Second},
		{60 * time.Second, 120 * time.Second},
		{2 * time.Minute, 4 * time.Minute},
		{4 * time.Minute, 5 * time.Minute}, // capped at 5min
		{5 * time.Minute, 5 * time.Minute}, // stays at cap
		{10 * time.Minute, 5 * time.Minute},
	}

	for _, tt := range tests {
		got := nextBackoffInterval(tt.current)
		if got != tt.want {
			t.Errorf("nextBackoffInterval(%v) = %v, want %v", tt.current, got, tt.want)
		}
	}
}

package testutil

import (
	"testing"

	"github.com/steveyegge/gastown/internal/workspace"
)

// RequireTownEnv skips the test if the process is not running inside a Gas
// Town workspace. It checks workspace.FindFromCwd and, when a workspace is
// found, verifies that mayor/rigs.json exists (a proxy for a fully
// initialized town).
//
// Use this guard for integration tests that shell out to gt/bd or otherwise
// depend on a live Gas Town directory tree being present. Tests that create
// their own temporary town structure (via t.TempDir) do NOT need this guard.
func RequireTownEnv(t *testing.T) {
	t.Helper()

	root, err := workspace.FindFromCwd()
	if err != nil {
		t.Skipf("skipping: not in a Gas Town workspace (%v)", err)
	}
	if root == "" {
		t.Skip("skipping: not in a Gas Town workspace")
	}
}

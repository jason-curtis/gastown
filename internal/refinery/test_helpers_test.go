package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// testRigDir creates a temp directory with a mayor/rig subdirectory initialized
// as a git repo with an "origin" remote. This satisfies NewEngineer's validation
// that the rig has a valid git directory with remotes (gt-zcj5r).
func testRigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mayorRig := filepath.Join(dir, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", mayorRig)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init in %s: %v", mayorRig, err)
	}
	cmd = exec.Command("git", "-C", mayorRig, "remote", "add", "origin", "https://example.com/test.git")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add in %s: %v", mayorRig, err)
	}
	return dir
}

// setupTestGitInRigDir creates mayor/rig as a git repo with an origin remote
// inside an existing rigDir. Used for tests where rigDir is a subdirectory of
// a "town root" (e.g., convoy tests).
func setupTestGitInRigDir(t *testing.T, rigDir string) {
	t.Helper()
	mayorRig := filepath.Join(rigDir, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", mayorRig)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init in %s: %v", mayorRig, err)
	}
	cmd = exec.Command("git", "-C", mayorRig, "remote", "add", "origin", "https://example.com/test.git")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add in %s: %v", mayorRig, err)
	}
}

// mustNewEngineer calls NewEngineer and fails the test if it returns an error.
func mustNewEngineer(t *testing.T, r *rig.Rig) *Engineer {
	t.Helper()
	e, err := NewEngineer(r)
	if err != nil {
		t.Fatalf("NewEngineer: %v", err)
	}
	return e
}

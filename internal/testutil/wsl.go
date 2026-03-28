package testutil

import (
	"os"
	"strings"
	"testing"
)

// SkipIfWSL skips the test if running under Windows Subsystem for Linux.
// WSL on NTFS does not enforce POSIX read-only directory permissions (chmod 0555),
// so tests that rely on permission-denied errors from read-only dirs will fail.
func SkipIfWSL(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return // not Linux or can't read — not WSL
	}
	lower := strings.ToLower(string(data))
	if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
		t.Skip("chmod-based read-only directories are not enforced on WSL/NTFS")
	}
}

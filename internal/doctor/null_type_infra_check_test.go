package doctor

import (
	"testing"
)

func TestInferExpectedType(t *testing.T) {
	tests := []struct {
		id       string
		title    string
		expected string
	}{
		// Agent beads
		{"gt-gastown-polecat-alpha", "Agent: gt-gastown-polecat-alpha", "agent"},
		{"gt-gastown-witness-main", "Agent: gt-gastown-witness-main", "agent"},
		{"gt-gastown-refinery-main", "Agent: gt-gastown-refinery-main", "agent"},
		{"gt-gastown-deacon-main", "Agent: gt-gastown-deacon-main", "agent"},
		{"hq-dog-reaper", "Agent: hq-dog-reaper", "agent"},
		{"gt-gastown-crew-devops", "Agent: gt-gastown-crew-devops", "agent"},
		{"hq-mayor-main", "Agent: hq-mayor-main", "agent"},

		// Rig beads
		{"gt-rig-gastown", "gastown", "rig"},
		{"bd-rig-beads", "beads", "rig"},

		// Molecule beads
		{"ma-wisp-q1a9", "mol-witness-patrol", "molecule"},
		{"gt-wisp-abc", "mol-deacon-patrol", "molecule"},
		{"gt-1234", "Witness Patrol", "molecule"},

		// Non-infra beads (should return empty)
		{"gt-abc123", "Fix login bug", ""},
		{"bd-task-1", "Update docs", ""},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := inferExpectedType(tt.id, tt.title)
			if got != tt.expected {
				t.Errorf("inferExpectedType(%q, %q) = %q, want %q", tt.id, tt.title, got, tt.expected)
			}
		})
	}
}

func TestLabelTable(t *testing.T) {
	if got := labelTable("wisps"); got != "wisp_labels" {
		t.Errorf("labelTable(wisps) = %q, want wisp_labels", got)
	}
	if got := labelTable("issues"); got != "labels" {
		t.Errorf("labelTable(issues) = %q, want labels", got)
	}
}

func TestNewNullTypeInfraCheck(t *testing.T) {
	check := NewNullTypeInfraCheck()
	if check.Name() != "null-type-infra-beads" {
		t.Errorf("Name() = %q, want null-type-infra-beads", check.Name())
	}
	if !check.CanFix() {
		t.Error("CanFix() should be true")
	}
}

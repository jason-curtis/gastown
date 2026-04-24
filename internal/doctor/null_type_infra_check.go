package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// NullTypeInfraCheck detects infrastructure beads (agent, rig, molecule) that
// have issue_type=NULL or an incorrect type. These beads bypass bd list's
// --include-infra filter and leak into user-facing views.
//
// Detection uses label-based identification (gt:agent, gt:rig) and ID patterns
// (*-rig-*) to find beads that should have a specific type but don't.
//
// Fix sets the correct issue_type via bd update --type.
type NullTypeInfraCheck struct {
	FixableCheck
	affected []nullTypeInfraRow
}

type nullTypeInfraRow struct {
	ID           string
	Title        string
	CurrentType  string // Current issue_type (may be NULL, empty, or "task")
	ExpectedType string // What type it should be
	RigDB        string // Dolt database name
	Table        string // "issues" or "wisps"
}

// NewNullTypeInfraCheck creates a new null-type infrastructure bead check.
func NewNullTypeInfraCheck() *NullTypeInfraCheck {
	return &NullTypeInfraCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "null-type-infra-beads",
				CheckDescription: "Detect infra beads with missing or incorrect issue_type",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run queries each rig database for infrastructure beads with wrong types.
func (c *NullTypeInfraCheck) Run(ctx *CheckContext) *CheckResult {
	c.affected = nil

	databases, err := doltserver.ListDatabases(ctx.TownRoot)
	if err != nil || len(databases) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig databases found (skipping)",
			Category: c.Category(),
		}
	}

	for _, db := range databases {
		prefix := db + "-"
		rigDir := beads.GetRigPathForPrefix(ctx.TownRoot, prefix)
		if rigDir == "" {
			rigDir = filepath.Join(ctx.TownRoot, db)
			if db == "hq" {
				rigDir = ctx.TownRoot
			}
		}
		rows := c.findMistypedInfraBeads(rigDir, db)
		c.affected = append(c.affected, rows...)
	}

	if len(c.affected) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No mistyped infra beads found",
			Category: c.Category(),
		}
	}

	details := make([]string, 0, len(c.affected))
	for _, row := range c.affected {
		curType := row.CurrentType
		if curType == "" {
			curType = "NULL"
		}
		details = append(details, fmt.Sprintf("[%s] %s — type=%s, want=%s (%s)",
			row.RigDB, row.ID, curType, row.ExpectedType, shortenTitle(row.Title, 40)))
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d infra bead(s) with missing or incorrect type — bypasses --include-infra filter", len(c.affected)),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix' to set correct issue_type",
		Category: c.Category(),
	}
}

// Fix sets the correct issue_type on affected beads via bd update --type.
func (c *NullTypeInfraCheck) Fix(ctx *CheckContext) error {
	if len(c.affected) == 0 {
		return nil
	}

	// Group by rig database for batch commits.
	rigBatches := make(map[string][]nullTypeInfraRow)
	for _, row := range c.affected {
		rigBatches[row.RigDB] = append(rigBatches[row.RigDB], row)
	}

	var errs []string
	for db, batch := range rigBatches {
		rigDir := filepath.Join(ctx.TownRoot, db)
		if db == "hq" {
			rigDir = ctx.TownRoot
		}

		for _, row := range batch {
			table := row.Table
			query := fmt.Sprintf("UPDATE `%s` SET issue_type = '%s' WHERE id = '%s'",
				table,
				strings.ReplaceAll(row.ExpectedType, "'", "''"),
				strings.ReplaceAll(row.ID, "'", "''"))
			if err := execBdSQLWrite(rigDir, query); err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s: %v", db, row.ID, err))
			}
		}

		// Commit to Dolt history.
		commitMsg := fmt.Sprintf("fix: set correct issue_type on %d infra bead(s) (gt doctor)", len(batch))
		if err := doltserver.CommitServerWorkingSet(ctx.TownRoot, db, commitMsg); err != nil {
			_ = err // Non-fatal
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial fix: %s", strings.Join(errs, "; "))
	}
	return nil
}

// findMistypedInfraBeads queries both issues and wisps tables for infra beads
// with incorrect types. Uses label and ID pattern matching.
func (c *NullTypeInfraCheck) findMistypedInfraBeads(rigDir, rigName string) []nullTypeInfraRow {
	var results []nullTypeInfraRow

	// Check both tables: issues and wisps
	for _, table := range []string{"issues", "wisps"} {
		rows := c.queryTable(rigDir, rigName, table)
		results = append(results, rows...)
	}

	return results
}

func (c *NullTypeInfraCheck) queryTable(rigDir, rigName, table string) []nullTypeInfraRow {
	// Query for beads with labels indicating infra type
	// Also check ID patterns for rig beads (*-rig-*) and molecule wisps
	query := fmt.Sprintf(
		"SELECT i.id, i.title, COALESCE(i.issue_type, '') as itype "+
			"FROM `%s` i "+
			"LEFT JOIN `%s` l ON i.id = l.issue_id "+
			"WHERE ((l.label IN ('gt:agent', 'gt:rig') "+
			"OR i.id LIKE '%%-rig-%%') "+
			"AND (i.issue_type IS NULL OR i.issue_type = '' OR i.issue_type = 'task')) "+
			"OR ((i.id LIKE '%%-wisp-%%' OR i.title LIKE 'mol-%%') "+
			"AND (i.issue_type IS NULL OR i.issue_type = '' OR i.issue_type = 'task' OR i.issue_type = 'epic')) "+
			"GROUP BY i.id, i.title, i.issue_type",
		table, labelTable(table))

	cmd := exec.Command("bd", "sql", "--csv", query) //nolint:gosec // G204: query built from constants
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil // Table may not exist
	}

	r := csv.NewReader(strings.NewReader(string(output)))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}

	var rows []nullTypeInfraRow
	for _, rec := range records[1:] {
		if len(rec) < 3 {
			continue
		}
		id := strings.TrimSpace(rec[0])
		title := strings.TrimSpace(rec[1])
		curType := strings.TrimSpace(rec[2])

		expectedType := inferExpectedType(id, title)
		if expectedType == "" || expectedType == curType {
			continue
		}

		rows = append(rows, nullTypeInfraRow{
			ID:           id,
			Title:        title,
			CurrentType:  curType,
			ExpectedType: expectedType,
			RigDB:        rigName,
			Table:        table,
		})
	}

	return rows
}

// labelTable returns the label table name for a given base table.
func labelTable(table string) string {
	if table == "wisps" {
		return "wisp_labels"
	}
	return "labels"
}

// inferExpectedType determines the correct type for an infra bead based on
// its ID pattern and title.
func inferExpectedType(id, title string) string {
	// Agent beads: ID contains agent pattern or title starts with "Agent:"
	if strings.Contains(id, "-polecat-") ||
		strings.Contains(id, "-witness-") ||
		strings.Contains(id, "-refinery-") ||
		strings.Contains(id, "-deacon-") ||
		strings.Contains(id, "-mayor-") ||
		strings.Contains(id, "-crew-") ||
		strings.Contains(id, "-dog-") ||
		strings.HasPrefix(title, "Agent:") {
		return "agent"
	}

	// Rig beads: ID contains -rig-
	if strings.Contains(id, "-rig-") {
		return "rig"
	}

	// Molecule/patrol beads: title or ID matches molecule/wisp patterns
	if strings.HasPrefix(title, "mol-") ||
		strings.Contains(title, "Patrol") ||
		strings.Contains(title, "patrol") ||
		strings.Contains(id, "-wisp-") {
		return "molecule"
	}

	return ""
}

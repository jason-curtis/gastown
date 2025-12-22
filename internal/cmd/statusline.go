package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/tmux"
)

var (
	statusLineSession string
)

var statusLineCmd = &cobra.Command{
	Use:    "status-line",
	Short:  "Output status line content for tmux (internal use)",
	Hidden: true, // Internal command called by tmux
	RunE:   runStatusLine,
}

func init() {
	rootCmd.AddCommand(statusLineCmd)
	statusLineCmd.Flags().StringVar(&statusLineSession, "session", "", "Tmux session name")
}

func runStatusLine(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	// Get session environment
	var rigName, polecat, crew, issue, role string

	if statusLineSession != "" {
		rigName, _ = t.GetEnvironment(statusLineSession, "GT_RIG")
		polecat, _ = t.GetEnvironment(statusLineSession, "GT_POLECAT")
		crew, _ = t.GetEnvironment(statusLineSession, "GT_CREW")
		issue, _ = t.GetEnvironment(statusLineSession, "GT_ISSUE")
		role, _ = t.GetEnvironment(statusLineSession, "GT_ROLE")
	} else {
		// Fallback to process environment
		rigName = os.Getenv("GT_RIG")
		polecat = os.Getenv("GT_POLECAT")
		crew = os.Getenv("GT_CREW")
		issue = os.Getenv("GT_ISSUE")
		role = os.Getenv("GT_ROLE")
	}

	// Determine identity and output based on role
	if role == "mayor" || statusLineSession == "gt-mayor" {
		return runMayorStatusLine(t)
	}

	// Deacon status line
	if role == "deacon" || statusLineSession == "gt-deacon" {
		return runDeaconStatusLine(t)
	}

	// Witness status line (session naming: gt-witness-<rig>)
	if role == "witness" || strings.HasPrefix(statusLineSession, "gt-witness-") {
		return runWitnessStatusLine(t, rigName)
	}

	// Refinery status line
	if role == "refinery" || strings.HasSuffix(statusLineSession, "-refinery") {
		return runRefineryStatusLine(rigName)
	}

	// Crew/Polecat status line
	return runWorkerStatusLine(rigName, polecat, crew, issue)
}

// runWorkerStatusLine outputs status for crew or polecat sessions.
func runWorkerStatusLine(rigName, polecat, crew, issue string) error {
	// Determine agent type and identity
	var icon, identity string
	if polecat != "" {
		icon = AgentTypeIcons[AgentPolecat]
		identity = fmt.Sprintf("%s/%s", rigName, polecat)
	} else if crew != "" {
		icon = AgentTypeIcons[AgentCrew]
		identity = fmt.Sprintf("%s/crew/%s", rigName, crew)
	}

	// Build status parts
	var parts []string

	// Add icon prefix
	if icon != "" {
		if issue != "" {
			parts = append(parts, fmt.Sprintf("%s %s", icon, issue))
		} else {
			parts = append(parts, icon)
		}
	} else if issue != "" {
		parts = append(parts, issue)
	}

	// Mail count
	if identity != "" {
		unread := getUnreadMailCount(identity)
		if unread > 0 {
			parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
		}
	}

	// Output
	if len(parts) > 0 {
		fmt.Print(strings.Join(parts, " | ") + " |")
	}

	return nil
}

func runMayorStatusLine(t *tmux.Tmux) error {
	// Count active sessions by listing tmux sessions
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	// Count polecats and rigs
	// Polecats: only actual polecats (not witnesses, refineries, deacon, crew)
	// Rigs: any rig with active sessions (witness, refinery, crew, or polecat)
	polecatCount := 0
	rigs := make(map[string]bool)
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}
		// Count rigs from any rig-level agent (has non-empty Rig field)
		if agent.Rig != "" {
			rigs[agent.Rig] = true
		}
		// Count only polecats for polecat count
		if agent.Type == AgentPolecat {
			polecatCount++
		}
	}
	rigCount := len(rigs)

	// Get mayor mail
	unread := getUnreadMailCount("mayor/")

	// Build status
	var parts []string
	parts = append(parts, fmt.Sprintf("%s %d polecats", AgentTypeIcons[AgentMayor], polecatCount))
	parts = append(parts, fmt.Sprintf("%d rigs", rigCount))
	if unread > 0 {
		parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runDeaconStatusLine outputs status for the deacon session.
// Shows: active rigs, patrol status, mail count
func runDeaconStatusLine(t *tmux.Tmux) error {
	// Count active rigs by checking for witnesses
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	rigs := make(map[string]bool)
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}
		if agent.Rig != "" {
			rigs[agent.Rig] = true
		}
	}
	rigCount := len(rigs)

	// Get deacon mail
	unread := getUnreadMailCount("deacon/")

	// Build status
	var parts []string
	parts = append(parts, fmt.Sprintf("%s %d rigs", AgentTypeIcons[AgentDeacon], rigCount))
	if unread > 0 {
		parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runWitnessStatusLine outputs status for a witness session.
// Shows: polecat count under management, mail count
func runWitnessStatusLine(t *tmux.Tmux, rigName string) error {
	if rigName == "" {
		// Try to extract from session name: gt-witness-<rig>
		if strings.HasPrefix(statusLineSession, "gt-witness-") {
			rigName = strings.TrimPrefix(statusLineSession, "gt-witness-")
		}
	}

	// Count polecats in this rig
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	polecatCount := 0
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}
		// Count polecats in this specific rig
		if agent.Type == AgentPolecat && agent.Rig == rigName {
			polecatCount++
		}
	}

	// Get witness mail
	identity := fmt.Sprintf("%s/witness", rigName)
	unread := getUnreadMailCount(identity)

	// Build status
	var parts []string
	parts = append(parts, fmt.Sprintf("%s %d polecats", AgentTypeIcons[AgentWitness], polecatCount))
	if unread > 0 {
		parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runRefineryStatusLine outputs status for a refinery session.
// Shows: MQ length, current processing status, mail count
func runRefineryStatusLine(rigName string) error {
	if rigName == "" {
		// Try to extract from session name: gt-<rig>-refinery
		if strings.HasPrefix(statusLineSession, "gt-") && strings.HasSuffix(statusLineSession, "-refinery") {
			rigName = strings.TrimPrefix(statusLineSession, "gt-")
			rigName = strings.TrimSuffix(rigName, "-refinery")
		}
	}

	if rigName == "" {
		fmt.Printf("%s ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Get refinery manager using shared helper
	mgr, _, err := getRefineryManager(rigName)
	if err != nil {
		// Fallback to simple status if we can't access refinery
		fmt.Printf("%s MQ: ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Get queue
	queue, err := mgr.Queue()
	if err != nil {
		// Fallback to simple status if we can't read queue
		fmt.Printf("%s MQ: ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Count pending items (position > 0 means pending, 0 means currently processing)
	pending := 0
	processing := false
	for _, item := range queue {
		if item.Position == 0 {
			processing = true
		} else {
			pending++
		}
	}

	// Get refinery mail
	identity := fmt.Sprintf("%s/refinery", rigName)
	unread := getUnreadMailCount(identity)

	// Build status
	var parts []string
	icon := AgentTypeIcons[AgentRefinery]
	if processing {
		parts = append(parts, fmt.Sprintf("%s MQ: %d (+1)", icon, pending))
	} else if pending > 0 {
		parts = append(parts, fmt.Sprintf("%s MQ: %d", icon, pending))
	} else {
		parts = append(parts, fmt.Sprintf("%s idle", icon))
	}

	if unread > 0 {
		parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// getUnreadMailCount returns unread mail count for an identity.
// Fast path - returns 0 on any error.
func getUnreadMailCount(identity string) int {
	// Find workspace
	workDir, err := findMailWorkDir()
	if err != nil {
		return 0
	}

	// Create mailbox using beads
	mailbox := mail.NewMailboxBeads(identity, workDir)

	// Get count
	_, unread, err := mailbox.Count()
	if err != nil {
		return 0
	}

	return unread
}

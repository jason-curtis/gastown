# Wisp Persistence Bug Analysis (gt-cg0sz4)

## Root Cause

The Wisp system has a critical visibility/accessibility problem:

### The Problem
1. **Creation**: Wisps are created with `bd create --ephemeral` or `bd mol wisp` which stores them in the **wisps table** (dolt_ignored ephemeral storage)
2. **Visibility**: Many gastown operations read from the **issues table** only, not the wisps table
3. **Result**: Wisps are persisted successfully in Dolt BUT become inaccessible to subsequent operations
4. **Cascading Failure**: This breaks:
   - `bd mol current` - can't find molecule steps (stored as wisps)
   - Patrol operations - can't create/access patrol wisps
   - Formula instantiation - can't bond formulas to beads
   - All ephemeral workflows

### Related Evidence
- Commit `dfed4c90`: "The --ephemeral flag causes bd create to insert into the wisps table instead of the issues table. Since bd show reads from issues, agent beads created with --ephemeral were invisible to lookups"
- Commit `0409ca8a`: Fixed doctor checks to query BOTH tables using ListWispIDs
- Issue description: "UNIQUE constraint failed: issues.id errors" - suggests operations expect data in issues table

## Affected Code Paths

1. **Formula Wisp Creation** (`internal/cmd/sling_helpers.go:687-691`)
   - Creates wisp with `bd mol wisp` → stores in wisps table
   - Subsequent operations fail to find it

2. **Molecule Steps** (GT-wisp-* prefixed beads)
   - Molecule roots and steps are stored as wisps (ephemeral)
   - But querying them for `bd mol current` fails if using issues-table-only queries

3. **Patrol Formulas** (`mol-witness-patrol`, `mol-refinery-patrol`, `mol-deacon-patrol`)
   - Explicitly must use wisps (per hook configuration)
   - But underlying infrastructure doesn't support multi-table queries

## Solution

Ensure all wisp-reading operations check BOTH tables:
- `bd show` should return wisps from wisps table if not found in issues table
- `bd list` should include wisps from wisps table
- Query utilities should use bi-directional lookup
- Molecule querying should explicitly check wisps table

This maintains the ephemeral semantics while ensuring accessibility.

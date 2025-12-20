# Federation Architecture: Ultrathink

## The Problem

Gas Town needs to scale beyond a single machine:
- More workers than one machine can handle (RAM, CPU, context windows)
- Geographic distribution (workers close to data/services)
- Cost efficiency (pay-per-use vs always-on VMs)
- Platform flexibility (support various deployment targets)

## Two Deployment Models

### Model A: "Town Clone" (VMs)

Clone the entire `~/gt` workspace to a remote VM. It runs like a regular Gas Town:

```
┌─────────────────────────────────────────┐
│  GCE VM (or any Linux box)              │
│                                         │
│  ~/gt/                # Full town clone │
│  ├── config/          # Town config     │
│  ├── mayor/           # Mayor (or none) │
│  ├── gastown/         # Rig with agents │
│  │   ├── polecats/    # Workers here    │
│  │   ├── refinery/                      │
│  │   └── witness/                       │
│  └── beads/           # Another rig     │
│                                         │
│  Runs autonomously, syncs via git       │
└─────────────────────────────────────────┘
```

**Characteristics:**
- Full autonomy if disconnected
- Familiar model - it's just another Gas Town
- VM overhead (cost, management, always-on)
- Coarse-grained scaling (spin up whole VMs)
- Good for: always-on capacity, long-running work, full independence

**Federation via:**
- Git sync for beads (already works)
- Extended mail routing (`vm1:gastown/polecat`)
- SSH for remote commands

### Model B: "Cloud Run Workers" (Containers)

Workers are stateless containers that wake on demand:

```
┌─────────────────────────────────────────┐
│  Cloud Run Service: gastown-worker      │
│                                         │
│  ┌────────────────────────────────┐     │
│  │ Container Instance             │     │
│  │  - Claude Code + git           │     │
│  │  - HTTP endpoint for work      │     │
│  │  - Persistent volume mount     │     │
│  │  - Scales 0→N automatically    │     │
│  └────────────────────────────────┘     │
│                                         │
│  Zero cost when idle                    │
│  Persistent connections keep warm       │
└─────────────────────────────────────────┘
```

**Characteristics:**
- Pay-per-use (nearly free when idle)
- Scales elastically (0 to many workers)
- No VM management
- Stateless(ish) - needs fast bootstrap or persistent storage
- Good for: burst capacity, background work, elastic scaling

**Key insight from your friend:**
Persistent connections solve the "zero to one" problem. Keep the connection open, container stays warm, subsequent requests are fast. This transforms Cloud Run from "cold functions" to "elastic workers."

## Unified Abstraction: Outposts

To support "however people want to do it," we need an abstraction that covers both models (and future ones like K8s, bare metal, etc.).

### The Outpost Concept

An **Outpost** is a remote compute environment that can run workers.

```go
type Outpost interface {
    // Identity
    Name() string
    Type() OutpostType  // local, ssh, cloudrun, k8s

    // Capacity
    MaxWorkers() int
    ActiveWorkers() int

    // Worker lifecycle
    Spawn(issue string, config WorkerConfig) (Worker, error)
    Workers() []Worker

    // Health
    Ping() error

    // Optional: Direct communication (VM outposts)
    SendMail(worker string, msg Message) error
}

type OutpostType string
const (
    OutpostLocal    OutpostType = "local"
    OutpostSSH      OutpostType = "ssh"      // Full VM clone
    OutpostCloudRun OutpostType = "cloudrun" // Container workers
    OutpostK8s      OutpostType = "k8s"      // Future
)
```

### Worker Interface

```go
type Worker interface {
    ID() string
    Outpost() string
    Status() WorkerStatus  // idle, working, done, failed
    Issue() string         // Current issue being worked

    // For interactive outposts (local, SSH)
    Attach() error         // Connect to worker session

    // For all outposts
    Logs() (io.Reader, error)
    Stop() error
}
```

### Outpost Implementations

#### LocalOutpost
- Current model: tmux panes on localhost
- Uses existing Connection interface (LocalConnection)
- Workers are tmux sessions

#### SSHOutpost
- Full Gas Town clone on remote VM
- Uses SSHConnection for remote ops
- Workers are remote tmux sessions
- Town config replicated to VM

#### CloudRunOutpost
- Workers are container instances
- HTTP/gRPC for work dispatch
- No tmux (stateless containers)
- Persistent connections for warmth

## Cloud Run Deep Dive

### Container Design

```dockerfile
FROM golang:1.21 AS builder
# Build gt binary...

FROM ubuntu:22.04
# Install Claude Code
RUN npm install -g @anthropic-ai/claude-code

# Install git, common tools
RUN apt-get update && apt-get install -y git

# Copy gt binary
COPY --from=builder /app/gt /usr/local/bin/gt

# Entrypoint accepts work via HTTP
COPY worker-entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

### HTTP Work Protocol

```
POST /work
{
  "issue_id": "gt-abc123",
  "rig_url": "https://github.com/steveyegge/gastown",
  "beads_url": "https://github.com/steveyegge/gastown",
  "context": { /* optional hints */ }
}

Response (streaming):
{
  "status": "working|done|failed",
  "branch": "polecat/gt-abc123",
  "logs": "...",
  "pr_url": "..."  // if created
}
```

### Persistent Connections

The "zero to one" solution:
1. Mayor opens HTTP/2 connection to Cloud Run
2. Connection stays open (Cloud Run keeps container warm)
3. Send work requests over same connection
4. Container processes work, streams results back
5. On idle timeout, connection closes, container scales down
6. Next request: small cold start, but acceptable

```
┌──────────┐                     ┌────────────────┐
│  Mayor   │────HTTP/2 stream───▶│  Cloud Run     │
│          │◀───results stream───│  Container     │
└──────────┘                     └────────────────┘
     │                                   │
     │   Connection persists             │ Container stays
     │   for hours if needed             │ warm while
     │                                   │ connection open
     ▼                                   ▼
  [New work requests go over same connection]
```

### Git in Cloud Run

Options for code access:

1. **Clone on startup** (slow, ~30s+ for large repos)
   - Simple but adds latency
   - Acceptable if persistent connection keeps container warm

2. **Cloud Storage FUSE mount** (read-only)
   - Mount bucket with repo snapshot
   - Fast startup
   - Read-only limits usefulness

3. **Persistent volume** (Cloud Run now supports!)
   - Attach Cloud Storage or Filestore volume
   - Git clone persists across container restarts
   - Best of both worlds

4. **Shallow clone with depth**
   - `git clone --depth 1` for speed
   - Sufficient for most worker tasks
   - Can fetch more history if needed

**Recommendation:** Persistent volume with shallow clone. Container starts, checks if clone exists, pulls if yes, shallow clones if no.

### Beads Sync in Cloud Run

Workers need beads access. Options:

1. **Clone beads repo at startup**
   - Same as code: persistent volume helps
   - `bd sync` before and after work

2. **Beads as API** (future)
   - Central beads server
   - Workers query/update via HTTP
   - More complex but cleaner for distributed

3. **Beads in git (current)**
   - Works today
   - Worker clones .beads, does work, pushes
   - Git handles conflicts

**Recommendation:** Start with git-based beads. It works today and Cloud Run workers can push to the beads repo just like local workers.

### Mail in Cloud Run

For VM outposts, mail is filesystem-based. For Cloud Run:

**Option A: No mail needed**
- Cloud Run workers are "fire and forget"
- Mayor pushes work via HTTP, gets results via HTTP
- Simpler model for stateless workers

**Option B: Mail via git**
- Worker checks `mail/inbox.jsonl` in repo
- Rare for workers to need incoming mail
- Mostly they just do work and report results

**Recommendation:** Start with Option A. Cloud Run workers receive work via HTTP, report via HTTP. Mail is for long-running stateful agents (Witness, Refinery), not burst workers.

### Cost Model

Cloud Run pricing (as of late 2024):
- CPU: ~$0.00002400/vCPU-second
- Memory: ~$0.00000250/GiB-second
- Requests: ~$0.40/million

For a worker running 5 minutes (300s) with 2 vCPU, 4GB RAM:
- CPU: 300 × 2 × $0.000024 = $0.0144
- Memory: 300 × 4 × $0.0000025 = $0.003
- **Total: ~$0.017 per worker session**

50 workers × 5 minutes each = ~$0.85

**Key insight:** When idle (connection closed, scaled to zero): **$0**

Compare to a VM running 24/7: ~$50-200/month

Cloud Run makes burst capacity essentially free when not in use.

### Claude API in Cloud Run

Workers need Claude API access:

1. **API key in Secret Manager**
   - Cloud Run mounts secret as env var
   - Standard pattern

2. **Workload Identity** (if using Vertex AI)
   - Service account with Claude access
   - No keys to manage

3. **Rate limiting concerns**
   - Many concurrent workers = many API calls
   - May need to coordinate or queue
   - Could use Mayor as API proxy (future)

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                         MAYOR                                │
│  ┌──────────────────────────────────────────────────────┐   │
│  │               Outpost Manager                         │   │
│  │  - Tracks all registered outposts                     │   │
│  │  - Routes work to appropriate outpost                 │   │
│  │  - Monitors worker status across outposts             │   │
│  └──────────────────────────────────────────────────────┘   │
│         │              │                │                    │
│         ▼              ▼                ▼                    │
│  ┌──────────┐   ┌──────────┐     ┌──────────────┐           │
│  │  Local   │   │   SSH    │     │   CloudRun   │           │
│  │ Outpost  │   │ Outpost  │     │   Outpost    │           │
│  └────┬─────┘   └────┬─────┘     └──────┬───────┘           │
└───────┼──────────────┼──────────────────┼───────────────────┘
        │              │                  │
        ▼              ▼                  ▼
   ┌─────────┐   ┌─────────┐        ┌─────────────┐
   │  tmux   │   │  SSH    │        │  HTTP/2     │
   │ panes   │   │sessions │        │ connections │
   └─────────┘   └─────────┘        └─────────────┘
        │              │                  │
        ▼              ▼                  ▼
   ┌─────────┐   ┌─────────┐        ┌─────────────┐
   │ Workers │   │ Workers │        │  Workers    │
   │ (local) │   │  (VM)   │        │ (containers)│
   └─────────┘   └─────────┘        └─────────────┘
        │              │                  │
        └──────────────┼──────────────────┘
                       ▼
              ┌─────────────────┐
              │   Git Repos     │
              │  (beads sync)   │
              │  (code repos)   │
              └─────────────────┘
```

## Configuration

```yaml
# ~/gt/config/outposts.yaml
outposts:
  # Always present - the local machine
  - name: local
    type: local
    max_workers: 4

  # VM with full Gas Town clone
  - name: gce-worker-1
    type: ssh
    host: 10.0.0.5
    user: steve
    ssh_key: ~/.ssh/gce_worker
    town_path: /home/steve/ai
    max_workers: 8

  # Cloud Run for burst capacity
  - name: cloudrun-burst
    type: cloudrun
    project: my-gcp-project
    region: us-central1
    service: gastown-worker
    max_workers: 20  # Or unlimited with cost cap
    cost_cap_hourly: 5.00  # Optional spending limit

# Work assignment policy
policy:
  # Try local first, then VM, then Cloud Run
  default_preference: [local, gce-worker-1, cloudrun-burst]

  # Override for specific scenarios
  overrides:
    - condition: "priority >= P3"  # Background work
      prefer: cloudrun-burst
    - condition: "estimated_duration > 30m"  # Long tasks
      prefer: gce-worker-1
```

**When policy becomes a molecule**: Static policy handles simple preference-based routing. But when assignment requires cognition - analyzing work characteristics, checking outpost health in real-time, making cost/speed tradeoffs, or handling degraded outposts - escalate to `mol-outpost-assign`. The policy file declares preferences; the molecule executes intelligent assignment. See [Config vs Molecule](architecture.md#config-vs-molecule-when-to-use-which) for the general principle.

## Implementation Phases

### Phase 1: Outpost Abstraction (Local)
- Define Outpost/Worker interfaces
- Implement LocalOutpost (refactor current polecat spawning)
- Configuration file for outposts
- `gt outpost list`, `gt outpost status`

### Phase 2: SSH Outpost (VMs)
- Implement SSHConnection (extends existing Connection interface)
- Implement SSHOutpost
- VM provisioning docs (Terraform examples)
- `gt outpost add ssh ...`
- Test with actual GCE VM

### Phase 3: Cloud Run Outpost
- Define worker container image
- Implement CloudRunOutpost
- HTTP/2 work dispatch protocol
- Persistent connection management
- Cost tracking/limits
- `gt outpost add cloudrun ...`

### Phase 4: Policy & Intelligence
- Smart assignment based on workload characteristics
- Cost optimization (prefer free capacity)
- Auto-scaling policies
- Dashboard for cross-outpost visibility

## Key Design Decisions

### 1. Outpost as First-Class Concept
Rather than baking in specific platforms (SSH, Cloud Run), model the abstraction. This gives flexibility for future platforms (K8s, bare metal, other clouds).

### 2. Workers Are Ephemeral
Whether local tmux, VM process, or Cloud Run container - workers are spawned for work and can be terminated. Don't assume persistence.

### 3. Git as Source of Truth
Code and beads always sync via git. This works regardless of where workers run. Even Cloud Run workers clone/pull from git.

### 4. HTTP for Cloud Run Control Plane
For Cloud Run specifically, use HTTP for work dispatch. Don't try to make filesystem mail work across containers. Keep it simple.

### 5. Local-First Default
Always try local workers first. Remote outposts are for overflow/burst, not primary capacity. This keeps latency low and costs down.

### 6. Graceful Degradation
If Cloud Run is unavailable, fall back to VM. If VM is down, use local only. System works with any subset of outposts.

## Open Questions

1. **Long-running sessions**: Cloud Run has request timeout limits (configurable up to 60 min, maybe longer now?). How does this interact with long Claude sessions?

2. **Context handoff**: If a Cloud Run worker's container restarts mid-task, how do we resume? Mail-to-self? Checkpoint to storage?

3. **Refinery in Cloud Run**: Could the Refinery itself run as a Cloud Run service? Long-running connection for merge queue processing?

4. **Witness in Cloud Run**: Worker monitoring from Cloud Run? Or does Witness need to be local/VM?

5. **Multi-region**: Cloud Run in multiple regions for geographic distribution? How to coordinate?

## Summary

The Outpost abstraction lets Gas Town scale flexibly:

| Outpost Type | Best For | Cost Model | Scaling |
|--------------|----------|------------|---------|
| Local | Development, primary work | Free (your machine) | Fixed |
| SSH/VM | Long-running, full autonomy | Always-on VM cost | Manual |
| Cloud Run | Burst, background, elastic | Pay-per-use | Auto |

Cloud Run's persistent connections solve the cold start problem, making it viable for interactive-ish work. Combined with VMs for heavier work and local for development, this gives a flexible spectrum of compute options.

The key insight: **don't pick one model, support both.** Let users configure their outposts based on their needs, budget, and scale requirements.

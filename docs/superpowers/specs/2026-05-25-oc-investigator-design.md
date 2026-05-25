# OC Investigator — design

**Date:** 2026-05-25
**Status:** Draft for review
**Tracking task:** flow task `oc-investigator` (project: `flow-itself`)

## 1. Goal

When an on-call (OC) Jira ticket lands assigned to the user, automatically:

1. Create a flow task tracking it (slug `oc-NNNN`, project `oncall`).
2. Spawn a Claude session that investigates the issue across Jira history,
   prior OC tickets, the relevant repo, and Kubernetes — fanning out
   independent investigation phases as parallel subagents.
3. Post a concise RCA + proposed resolution comment back to Jira.
4. Save a detailed investigation note as a flow update.

**Non-goals.** No auto-remediation. No ticket state transitions. No paging.
The agent investigates and proposes; the human decides and applies.

## 2. Why

- Cut on-call triage latency: by the time the human looks at the ticket, a
  first-pass RCA and proposed resolution are already drafted.
- Capture institutional knowledge: prior OC tickets get surfaced as evidence
  for repeat patterns; the flow update accumulates investigation history
  across the `oncall` project.
- Keep the human in control: proposals only, never actions.

## 3. Architecture

```
┌─── macOS LaunchAgent (every 15 min) ─────────────────────────┐
│ com.swapnil.flow.oc-investigator.plist  (StartInterval=900)  │
└──────────────────────────┬───────────────────────────────────┘
                           ▼
┌─── runner.sh ────────────────────────────────────────────────┐
│  1. Load ~/.flow/oc-investigator/config.yaml                 │
│  2. JQL via Jira skill:                                      │
│       project=OC AND assignee=<email> AND created >= -<N>h   │
│     (assignee_email = "*" disables the assignee filter)      │
│  3. For each ticket OC-NNNN:                                 │
│     a. flow show task oc-NNNN → exists? skip                 │
│     b. Ensure 'oncall' project exists; create if not         │
│     c. flow add task --slug oc-NNNN --project oncall \       │
│         --work-dir <agent-workspace>                         │
│     d. Render brief.md from brief-template.md (Python        │
│        string.Template substitution), overwrite              │
│        ~/.flow/tasks/oc-NNNN/brief.md                        │
│     e. flow do oc-NNNN  (always spawn — no headless mode)    │
└──────────────────────────┬───────────────────────────────────┘
                           ▼
┌─── Spawned Claude session ───────────────────────────────────┐
│  SessionStart hook loads flow skill                          │
│  Session reads brief.md → investigation playbook             │
│                                                              │
│  PHASE 1 (sequential, main session):                         │
│    Fetch Jira ticket + comment history via Jira skill.       │
│    Extract: service name, env, error signatures, timestamps. │
│                                                              │
│  PHASES 2–4 (parallel — dispatched as subagents in one msg): │
│    • prior-tickets subagent — search past OC tickets (30d)   │
│      for matching error signatures or component              │
│    • repo-context subagent — locate service repo, read       │
│      recent commits/PRs, relevant code paths                 │
│    • k8s-context subagent — kubectl logs/events for          │
│      affected pods (only if env is named in ticket)          │
│    Subagents return full findings with evidence pointers.   │
│                                                              │
│  PHASE 5 (synthesis, main session):                          │
│    Root-cause hypothesis with evidence; flag weak evidence.  │
│    Draft proposed resolution in whatever shape fits:         │
│      - Code change (PR-shaped — files + direction)           │
│      - Manual ops steps (commands, restarts, config flips)   │
│      - Infra/config tweak (pool size, resource bump, unlock) │
│      - Escalation note (which team + why)                    │
│    Note risks / blast radius for any ops steps.              │
│                                                              │
│  PHASE 6 (parallel outputs):                                 │
│    • If output.jira_comment=true: post concise Jira comment  │
│      (<300 words) — symptom recap → root cause → proposed    │
│      resolution. ASCII diagram if it clarifies.              │
│    • If output.flow_update=true: save flow update at         │
│      tasks/oc-NNNN/updates/YYYY-MM-DD-investigation.md       │
│      with full detail, alternatives considered, code/log     │
│      excerpts.                                               │
│                                                              │
│  STOP. No auto-remediation, no ticket transitions, no pages. │
└──────────────────────────────────────────────────────────────┘
```

## 4. Components

### 4.1 LaunchAgent
- `~/Library/LaunchAgents/com.swapnil.flow.oc-investigator.plist`
- `StartInterval = 900` (15 min)
- `KeepAlive = false` (one-shot per interval)
- stdout/stderr → `~/.flow/oc-investigator/logs/{out,err}.log`
- ProgramArguments invokes `runner.sh` with absolute path

### 4.2 Runner (`scripts/oc-investigator/runner.sh`)

Responsibilities:
- Load config from `~/.flow/oc-investigator/config.yaml` (parse with
  `yq` or a tiny inline Python one-liner — pick whichever is present)
- Compose JQL using config values:
  - `assignee_email = "*"` → `project = OC AND created >= -<N>h`
  - any other value → `project = OC AND assignee = "<email>" AND created >= -<N>h`
- Fetch tickets by invoking the Jira skill's JQL search script.
  The skill's script directory is configurable via
  `jira.skill_scripts_dir` (see §4.3). The runner does NOT need to know
  individual script names beyond what's necessary to perform a JQL
  search and is free to use whatever the skill exposes for that
  purpose. Output is JSON; parse with `jq` or inline Python.
- For each ticket OC-NNNN:
  - Dedup: `flow show task oc-NNNN >/dev/null 2>&1` exit 0 → skip
  - Ensure `oncall` project exists once at top of sweep (not per ticket):
    `flow show project oncall >/dev/null 2>&1 || flow add project oncall --work-dir <task_work_dir>`
  - `flow add task "<truncated-summary>" --slug oc-NNNN --project oncall --work-dir <task_work_dir>`
  - Render brief via Python helper (`render_brief.py`) using
    `string.Template.safe_substitute()` — robust against quotes,
    backticks, newlines, and other special chars in ticket fields.
    Truncate the ticket `description` value to 2000 chars max before
    substitution; if cut, append "... (truncated — see <url>)" so the
    truncated brief still points at the full ticket.
  - Write rendered content to `~/.flow/tasks/oc-NNNN/brief.md`
  - `flow do oc-NNNN`
- Log every action with timestamps to `~/.flow/oc-investigator/logs/out.log`

PATH safety: prepend `/usr/local/bin` (or equivalent) so the newer `flow`
binary with `FLOW_TERM` support is picked up, matching the
teams-morning-digest runner fix.

No headless / test-only path. Every run creates a flow task and spawns a
tab. To "test" without Jira side-effects, set `output.jira_comment: false`
in config — the investigation still runs and writes a flow update, but
the Jira ticket is untouched.

### 4.3 Config (`~/.flow/oc-investigator/config.yaml`)

Jira `base_url` and auth are NOT in this config — they live with the
Jira skill itself. The runner just needs to know where the skill's
script dir lives on this machine (so it can run JQL from shell).

```yaml
jira:
  project_key: OC
  # Set to "*" to fire on every new OC ticket regardless of assignee.
  assignee_email: swapnil.dahiphale@getinsured.com
  # JQL look-back window. Wider = more robust against missed polls.
  window_hours: 2
  # Path to the Jira skill's CLI scripts dir. The runner uses these
  # for JQL search from shell. Inside the spawned Claude session, the
  # skill is loaded automatically — script paths are not needed there.
  skill_scripts_dir: /Users/swapnil/workspace/vimo/engineering-knowledge-base/skills/jira/scripts

flow:
  project_slug: oncall
  task_work_dir: /Users/swapnil/workspace/vimo/agent-workspace

investigation:
  enable_prior_tickets: true
  enable_repo_context: true
  enable_k8s_context: true
  prior_tickets_limit: 5

output:
  jira_comment: true     # set false during testing — no Jira side-effects
  flow_update: true
```

(No `spawn_tab` knob — every run spawns. To dry-run, use
`runner.sh --dry-run`; to suppress Jira side-effects during real runs,
flip `jira_comment: false`.)

### 4.4 Brief template (`scripts/oc-investigator/brief-template.md`)

Source of truth for the investigation playbook the spawned session follows.

Substitution uses Python `string.Template.safe_substitute()` syntax —
`$placeholder` or `${placeholder}` form. Placeholders:

| Placeholder | Source |
|---|---|
| `${TICKET_KEY}` | e.g. `OC-1905` |
| `${TICKET_KEY_LC}` | lowercased, e.g. `oc-1905` (matches flow slug) |
| `${TICKET_URL}` | composed from Jira skill's base_url + key |
| `${TICKET_SUMMARY}` | from Jira `summary` field |
| `${TICKET_DESCRIPTION}` | from Jira `description` field, truncated to 2000 chars |
| `${REPORTER}` | from Jira `reporter.displayName` |
| `${PRIORITY}` | from Jira `priority.name` |
| `${COMPONENTS}` | from Jira `components[].name`, comma-joined |
| `${CREATED}` | from Jira `created` (RFC3339) |
| `${PRIOR_TICKETS_LIMIT}` | from config `investigation.prior_tickets_limit` |

(Using `$VAR` instead of `{{VAR}}` for Python `string.Template` compatibility.)

The brief is agent-facing: it states *what* to do, not *which scripts*
to call. The Jira skill is loaded into the spawned Claude session
automatically — the agent discovers available capabilities (search,
fetch issue, post comment, etc.) from the skill itself.

```markdown
# [$TICKET_KEY] $TICKET_SUMMARY

## What
Investigate root cause for $TICKET_KEY and propose a resolution.
DO NOT auto-remediate.

## Ticket
- URL: $TICKET_URL
- Reporter: $REPORTER
- Priority: $PRIORITY
- Components: $COMPONENTS
- Created: $CREATED
- Description (verbatim, truncated to 2000 chars):
  > $TICKET_DESCRIPTION

## Investigation playbook

Read `~/.flow/oc-investigator/config.yaml` first; honor `enable_*` and
`output.*` toggles.

Use the Jira skill (loaded into this session) for all Jira operations:
fetching the full ticket and comments, searching past tickets, posting
the final comment. Use available domain skills (kubectl, repo tooling)
as needed for the other phases.

PHASE 1 (sequential, main session):
  Fetch full ticket and comment history for $TICKET_KEY. Extract:
  affected service name, affected env, error signatures, timestamps.

PHASES 2–4 (parallel — dispatch as subagents in a SINGLE message):
  • prior-tickets subagent:
      Search past OC tickets (last 30d) for matching error signatures
      or affected component. Return at most $PRIOR_TICKETS_LIMIT most
      relevant. Skip the subagent entirely if enable_prior_tickets=false.
  • repo-context subagent:
      Locate the affected service's repo, scan recent commits/PRs and
      relevant code paths. Skip if enable_repo_context=false.
  • k8s-context subagent:
      Pull logs/events for affected pods on the named env. Skip if
      enable_k8s_context=false or env is not named in the ticket.
  Each subagent reports full findings — no length cap. Include evidence
  pointers (file paths, line numbers, log timestamps, commit hashes,
  prior ticket keys) so the main session can cite specifics in the
  synthesis.

PHASE 5 (synthesis, main session):
  Build root-cause hypothesis with evidence; flag weak/missing evidence.
  Draft proposed resolution in whatever shape fits:
    • Code change (PR-shaped — files + direction)
    • Manual ops steps (commands, restarts, config flips)
    • Infra/config tweak (pool size, resource bump, liquibase unlock)
    • Escalation note (which team + why)
  Note risks / blast radius for any ops steps.

PHASE 6 (parallel outputs):
  • If output.jira_comment=true: post a concise comment on $TICKET_KEY
    (<300 words) — symptom → root cause → resolution. ASCII diagram if
    it clarifies.
  • If output.flow_update=true: save a flow update at
    `~/.flow/tasks/$TICKET_KEY_LC/updates/YYYY-MM-DD-investigation.md`
    with full detail, alternatives considered, code/log excerpts.

STOP. No auto-remediation, no ticket transitions, no pages.

## Out of scope
- Applying any fix
- Closing or transitioning the ticket
- Paging anyone

---
*Load the flow skill and the Jira skill before starting. Both are
discoverable via the standard skill-loading mechanism.*
```

### 4.5 Install script (`scripts/oc-investigator/install.sh`)

- `mkdir -p ~/.flow/oc-investigator/logs`
- Copy `config.example.yaml` → `~/.flow/oc-investigator/config.yaml` if not
  present (idempotent)
- Copy plist → `~/Library/LaunchAgents/`
- `launchctl bootstrap gui/$UID ~/Library/LaunchAgents/<plist>`
- Print next-step instructions

## 5. Data flow

```
Jira (OC project)
    │
    │ poll every 15m  (JQL: assignee + created < N hours)
    ▼
runner.sh ─────► flow show task oc-NNNN  (dedup via slug uniqueness)
    │                    │
    │                    └─► exists? skip.
    │
    ▼ (new ticket)
ensure project 'oncall' exists ─► flow add task --slug oc-NNNN ...
    │
    ▼
render brief.md from template with ticket fields + investigation playbook
    │
    ▼
flow do oc-NNNN  ──►  new terminal tab with Claude session
                              │
                              ▼
                       Claude follows playbook
                       Phase 1 → Phases 2-4 (parallel subagents) → Phase 5
                              │
                       ┌──────┴──────┐
                       ▼             ▼
                Jira comment      flow update
                (concise RCA)     (full detail)
```

## 6. Dedup strategy

Slug uniqueness in the flow DB is the source of truth.

- Convention: `oc-NNNN` (lowercased ticket key).
- Runner checks `flow show task oc-NNNN` exit code:
  - 0 → task exists → skip this ticket
  - non-zero → not found → proceed to create
- No watermark file. No separate seen-list. The DB is already authoritative
  and persists across restarts naturally.
- JQL window is generous (2h default, configurable) so missed polls recover
  on the next run; duplicates are filtered by the slug check.

## 7. Error handling

| Failure | Behavior |
|---|---|
| Jira API down / auth fail | Log to err.log, exit 0. Next poll retries. |
| `flow add task` fails | Log + skip ticket. Batch continues. |
| `flow do` fails (e.g. Accessibility) | Task row exists; user opens manually. |
| Subagent times out | Main session continues with partial evidence; flags gap. |
| `flow show project oncall` fails | Create project on the fly; if creation fails, abort run. |
| Config file missing/malformed | Log error + exit 0. User fixes config; next poll retries. |

## 8. Testing path

1. **Dry-run** — `runner.sh --dry-run` prints intended actions (which
   tickets would be processed, the JQL, the brief content) with no
   mutations: no `flow add task`, no `flow do`, no Jira comment.
2. **Tab, no Jira post** — set `output.jira_comment: false`. Trigger
   runner manually. A tab opens, the investigation runs end-to-end, the
   flow update is written at `~/.flow/tasks/oc-NNNN/updates/...`, and the
   Jira ticket is untouched. Inspect the flow update.
3. **End-to-end** — flip `output.jira_comment: true`. File a test OC
   ticket assigned to yourself (or use a real recent one not yet in the
   flow DB). Wait for the next poll. Verify the Jira comment appears and
   the flow update is saved.

## 9. Open considerations / future work

- **Tab noise.** Every new OC ticket pops a tab. User explicitly
  accepts this. If it ever becomes intolerable, the simplest mitigation
  is editing the runner to skip `flow do` for some tickets and letting
  the user open them manually via `flow do oc-NNNN`. No config knob in
  v1.
- **Concurrent tickets.** If 3 OC tickets land in one poll, 3 tabs spawn.
  Flow's spawner serializes terminal ops at the iTerm/Warp level — this
  is fine.
- **Sleep/wake.** LaunchAgents fire on wake if missed during sleep. Good
  for laptop use. The 2h JQL window catches anything missed in a normal
  weekend sleep window; longer absences need a wider `window_hours`.
- **Ticket updates after creation.** If someone adds comments to OC-1905
  after we've already created the flow task, we don't re-investigate.
  V1 design = one-shot per ticket. Re-investigation would need an
  explicit re-run command.
- **Escalation surface.** No automatic pager / Slack ping. Investigation
  outputs land in Jira + flow only. Adding a digest later is possible.

## 10. Out of scope (explicit)

- Modifying flow's Go source. This is pure userland automation built
  around the `flow` CLI, mirroring `scripts/teams-morning-digest/`.
- Webhook-driven triggers (polling only).
- Non-OC Jira projects.
- Auto-remediation of any kind.
- Ticket state transitions (the user's Jira hygiene rule keeps these
  manual).

## 11. Implementation order

1. `config.example.yaml` + config parser in `runner.sh`
2. `brief-template.md` — investigation playbook content (placeholders
   only; substitution comes in step 5)
3. Jira JQL search via the configured `jira.skill_scripts_dir` (JQL
   composition + JSON parse). Handle the `assignee_email = "*"` branch.
4. Dedup via `flow show task` exit code
5. `render_brief.py` — Python `string.Template.safe_substitute()`
   helper. Includes 2000-char description truncation.
6. Project ensure (`oncall`) + `flow add task` + write rendered brief
7. `flow do` spawn
8. `--dry-run` mode in runner
9. LaunchAgent plist + `install.sh`
10. README operator notes
11. Manual end-to-end test (testing path §8 steps 1 → 2 → 3)
12. Enable LaunchAgent

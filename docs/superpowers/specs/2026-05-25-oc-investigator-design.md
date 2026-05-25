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
│     d. Overwrite brief.md from brief-template.md, filling in │
│        ticket fields and the investigation playbook          │
│     e. If output.spawn_tab=true: flow do oc-NNNN             │
│        Else: claude -p --session-id <flow-allocated-uuid>    │
│        with the brief content piped in (headless)            │
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
│    Each reports <200 words: findings + evidence pointers.    │
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
- Load config from `~/.flow/oc-investigator/config.yaml`
- Compose JQL using config values (project_key, assignee_email, window_hours)
- Invoke Jira skill scripts to execute JQL and return ticket list (JSON)
- For each ticket:
  - Dedup: `flow show task oc-NNNN` exit code 0 → already seen, skip
  - Ensure `oncall` project exists: `flow show project oncall || flow add project ...`
  - Create task with `--slug oc-NNNN --project oncall --work-dir <task_work_dir>`
  - Render brief from `brief-template.md` with ticket fields substituted
  - Overwrite `~/.flow/tasks/oc-NNNN/brief.md` (Read once, then Write)
  - Spawn session:
    - `output.spawn_tab=true` → `flow do oc-NNNN`
    - `output.spawn_tab=false` → headless via `claude -p` against the
      flow-allocated session-id (testing path)
- Log every action with timestamps

PATH safety: prepend `/usr/local/bin` (or equivalent) so the newer `flow`
binary with `FLOW_TERM` support is picked up, matching the
teams-morning-digest runner fix.

### 4.3 Config (`~/.flow/oc-investigator/config.yaml`)

Jira `base_url` is NOT in this config — the runner fetches it from the
Jira skill's own config.

```yaml
jira:
  project_key: OC
  # Set to "*" to fire on every new OC ticket regardless of assignee.
  assignee_email: swapnil.dahiphale@getinsured.com
  # JQL look-back window. Wider = more robust against missed polls.
  window_hours: 2

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
  spawn_tab: true        # set false for headless testing
```

### 4.4 Brief template (`scripts/oc-investigator/brief-template.md`)

Source of truth for the investigation playbook the spawned session follows.
Substitution placeholders use `{{TICKET_KEY}}`, `{{TICKET_URL}}`,
`{{TICKET_SUMMARY}}`, `{{TICKET_DESCRIPTION}}`, `{{REPORTER}}`,
`{{PRIORITY}}`, `{{COMPONENTS}}`, `{{CREATED}}`.

```markdown
# [{{TICKET_KEY}}] {{TICKET_SUMMARY}}

## What
Investigate root cause for {{TICKET_KEY}} and propose a resolution.
DO NOT auto-remediate.

## Ticket
- URL: {{TICKET_URL}}
- Reporter: {{REPORTER}}
- Priority: {{PRIORITY}}
- Components: {{COMPONENTS}}
- Created: {{CREATED}}
- Description (verbatim):
  > {{TICKET_DESCRIPTION}}

## Investigation playbook

Read ~/.flow/oc-investigator/config.yaml first; honor enable_* and output
toggles.

PHASE 1 (sequential, main session):
  Fetch full ticket + comment history via Jira skill. Extract: affected
  service name, affected env, error signatures, timestamps.

PHASES 2–4 (parallel — dispatch as subagents in a SINGLE message):
  • prior-tickets subagent:
      search past OC tickets (last 30d) for matching error signatures or
      affected component. Skip if enable_prior_tickets=false.
  • repo-context subagent:
      locate service repo, read recent commits/PRs, scan relevant code
      paths. Skip if enable_repo_context=false.
  • k8s-context subagent:
      kubectl logs/events for affected pods on the named env. Skip if
      enable_k8s_context=false or env not in ticket.
  Each subagent reports <200 words: findings + evidence pointers.

PHASE 5 (synthesis, main session):
  Build root-cause hypothesis with evidence; flag weak/missing evidence.
  Draft proposed resolution in whatever shape fits:
    • Code change (PR-shaped — files + direction)
    • Manual ops steps (commands, restarts, config flips)
    • Infra/config tweak (pool size, resource bump, liquibase unlock)
    • Escalation note (which team + why)
  Note risks / blast radius for any ops steps.

PHASE 6 (parallel outputs):
  • If output.jira_comment=true: post concise Jira comment (<300 words)
    via Jira skill add_comment.py — symptom → root cause → resolution.
    ASCII diagram if it clarifies dependencies or flow.
  • If output.flow_update=true: save flow update at
    tasks/{{TICKET_KEY_LC}}/updates/YYYY-MM-DD-investigation.md with
    full detail, alternatives considered, code/log excerpts.

STOP. No auto-remediation, no ticket transitions, no pages.

## Out of scope
- Applying any fix
- Closing or transitioning the ticket
- Paging anyone

---
*Read the flow skill and the Jira skill at ~/.claude/skills/jira/ before
starting.*
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

1. **Dry-run** — `runner.sh --dry-run` prints intended actions, no mutations.
2. **Headless** — `output.jira_comment=false`, `output.spawn_tab=false`.
   Trigger runner manually against a known recent OC ticket. Inspect the
   flow update at `~/.flow/tasks/oc-NNNN/updates/`.
3. **Tab, no Jira post** — `spawn_tab=true`, `jira_comment=false`. Verify
   tab opens; verify flow update content; verify Jira ticket is untouched.
4. **End-to-end** — flip `jira_comment=true`, create a test OC ticket (or
   wait for a real one). Verify the Jira comment appears.

## 9. Open considerations / future work

- **Tab noise.** Every new OC ticket pops a tab. If this becomes noisy,
  flip `spawn_tab=false` and rely on flow updates; user opens specific
  tabs via `flow do oc-NNNN`. Current default = spawn (per user choice).
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

1. `config.example.yaml` + parser logic in `runner.sh`
2. JQL invocation via Jira skill + ticket-list parsing
3. Dedup via `flow show task` exit code
4. Project ensure + `flow add task` + brief template render
5. `flow do` spawn (and headless fallback)
6. Investigation playbook content (the brief template)
7. LaunchAgent plist + install script
8. README operator notes
9. Dry-run mode + manual end-to-end test
10. Enable LaunchAgent

# Teams morning digest

Manually-triggered morning playbook that fetches overnight Microsoft Teams
channel activity, classifies discussions by topic, and produces a structured
digest for standup catch-up. Complements the teams-triage routine (which
handles action items) by providing **situational awareness** — what topics
were discussed, what was decided, where conversations are heading.

## Where state lives

All persistent state for this playbook lives under
`~/.flow/playbooks/teams-morning-digest/`:

```
~/.flow/playbooks/teams-morning-digest/
│
├── brief.md                ← runtime instructions (frozen snapshot per run)
├── watermark.md            ← last successful run timestamp (absent = first run)
├── channels-excluded.md    ← user-maintained blocklist
│
├── topics.md               ← INDEX — scanned every run
│                              format: "- [slug](topics/slug.md) — 1-line | last-seen: YYYY-MM-DD"
│
├── topics/                 ← LAZY-LOADED — only files matching today's classification
│   ├── jenkins-security.md     frontmatter:
│   ├── hix-deploy-eks.md         slug, first-seen, last-seen
│   └── ...                       summary:        ← STABLE (what this topic IS)
│                                 current-focus:  ← MUTABLE (overwritten each run)
│                                 channels, key-people
│                              body:
│                                 ## Context
│                                 ## Key history   ← APPEND-ONLY chronology
│                                 ## Open threads
│                                 ## Last activity
│
└── digests/
    └── YYYY-MM-DD.md       ← one immutable digest per run
```

The discipline that keeps this scalable: **`topics.md` is the only thing
always read; per-topic files are read on demand**; the index stays tiny
(one line per topic) so the classifier can scan it cheaply even as the
store accumulates dozens of topics.

### Three time horizons in each topic file

| Field           | Lifetime    | Who writes it                          |
|-----------------|-------------|----------------------------------------|
| `summary:`      | **stable**  | Set at creation; rewritten only on explicit user rename/split |
| `current-focus:`| **mutable** | Overwritten every active run by step 12 |
| `Key history`   | **append-only** | New dated line per active run; chronology is permanent |

The classifier reads `summary:` + `current-focus:` from frontmatter (cheap,
~15 lines via `Read limit: 15`) to make routing decisions; the body is
loaded only when a topic is confirmed active. As discussions drift over
time (e.g. a "jenkins-security" thread evolving toward k8s migration), the
step 15 prompt offers **rename / split / keep-as-is** so the user owns the
taxonomy decision — `summary:` is never silently rewritten.

## Install

```bash
zsh scripts/teams-morning-digest/install.sh
```

Idempotent — safe to re-run. `brief.md` is always overwritten (it's the
source of truth); state seed files are only written on first install.

## First run

After install, `watermark.md` is absent — this triggers a one-time 14-day
lookback to seed the topic store. First run may take several minutes.

After the first run, review the digest and populate the exclusion list:
```bash
$EDITOR ~/.flow/playbooks/teams-morning-digest/channels-excluded.md
```

## Run

```bash
flow run playbook teams-morning-digest
```

## How a run works

```
flow run playbook teams-morning-digest
            │
            ▼
[1-2] Read watermark + exclusion list → window_start / window_end
            │
            ▼
[3-4] MCP fetch (paginated ≤500) + createdDateTime gate
            │
            ▼
[5-6] Group by channel + drop excluded channels + noise pre-filter
            │
            ▼
[7]   Per-channel topic classification — TWO-PASS:
        Pass 1: scan topics.md (one-line index)
        Pass 2: Read frontmatter (limit:15) of ambiguous candidates for summary:
        Emit drifted_from_summary flag for existing topics that have moved off
            │
   ┌────────┴─────────┐
   ▼                  ▼
[8] existing:    [9] new:
 read full         create topics/<slug>.md
 topic file        with frontmatter (incl. summary:
 for context       + current-focus:) + Context;
                   append line to topics.md
   │                  │
   └────────┬─────────┘
            ▼
[10]  Detect candidate action items (check flow tasks for dedup) — surface only
            │
            ▼
[11]  Write digests/YYYY-MM-DD.md (terminal echo)
            │
            ▼
[12]  For each existing active topic:
        - Append "Key history" line, overwrite "Last activity"
        - Overwrite current-focus: with today's focus
        - Update channels / key-people / last-seen
        - DO NOT touch summary:
        - Queue any drifted topics for step 15
            │
            ▼
[13]  Update topics.md last-seen + mark 30d+ stale
            │
            ▼
[14]  Advance watermark.md  (only on successful digest write)
            │
            ▼
[15]  AskUserQuestion:
        - drifted topics: rename / split / keep-as-is
        - KB candidates: approve / review / skip
        - untracked action items: create tasks / skip
      Exit WITHOUT `flow done`.
```

Key invariants:
- **Read-only on Teams** — no replies, reactions, or sends.
- **Append-only on topic history** — `## Key history` entries are never
  rewritten; the chronology is permanent.
- **No auto task creation** — action items are surfaced; user confirms in step 15.
- **Watermark advances only on success** — partial / failed runs are retryable.
- **Exits without `flow done`** — daily runs don't trigger the KB sweep.

## Inspect state

```bash
# Topic index
cat ~/.flow/playbooks/teams-morning-digest/topics.md

# Today's digest
cat ~/.flow/playbooks/teams-morning-digest/digests/$(date +%Y-%m-%d).md

# Excluded channels
cat ~/.flow/playbooks/teams-morning-digest/channels-excluded.md
```

## Manage topics

Topics are discovered and created automatically on each run. To maintain:

```bash
# View all known topics
cat ~/.flow/playbooks/teams-morning-digest/topics.md

# Inspect a specific topic's history
cat ~/.flow/playbooks/teams-morning-digest/topics/<slug>.md

# Mark a stale topic as archived (manual)
# Edit topics.md to remove the line, then move the file to a backup
```

## Source files (this directory)

- `brief.md` — source of truth for the playbook. Copied to
  `~/.flow/playbooks/teams-morning-digest/brief.md` on install.
  Edit here and re-run `install.sh` to deploy changes.
- `install.sh` — sets up the playbook entity and seeds state directories.

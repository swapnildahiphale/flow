# Teams morning digest

Manually-triggered morning playbook that fetches overnight Microsoft Teams
channel activity, classifies discussions by topic, and produces a structured
digest for standup catch-up. Complements the teams-triage routine (which
handles action items) by providing **situational awareness** — what topics
were discussed, what was decided, where conversations are heading.

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

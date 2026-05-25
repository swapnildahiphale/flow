# OC Investigator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build userland automation that polls Jira every 15 min for new OC (on-call) tickets, creates a flow task per ticket under the `oncall` project, spawns a Claude session via `flow do`, and lets that session investigate (with parallel subagents) and post a concise RCA to Jira + a detailed flow update. No auto-remediation.

**Architecture:** Pure userland scripts under `scripts/oc-investigator/` of the flow repo, mirroring the `scripts/teams-morning-digest/` pattern: a `runner.sh` invoked by a macOS LaunchAgent, a Python helper (`render_brief.py`) to render the brief template, a config at `~/.flow/oc-investigator/config.yaml`, and an `install.sh` for bootstrap. The runner uses the Jira skill's CLI scripts (path configurable) for JQL search; everything else (ticket fetch, comment post, subagents) happens inside the spawned Claude session via the loaded Jira skill. **No changes to flow's Go source.**

**Tech Stack:** Bash 5+, Python 3 (stdlib only — `string.Template`, `unittest`, `json`, `argparse`), macOS LaunchAgent (plist), the flow CLI, the Jira skill's Python scripts at the path given by `jira.skill_scripts_dir` in config.

**Spec reference:** [`docs/superpowers/specs/2026-05-25-oc-investigator-design.md`](../specs/2026-05-25-oc-investigator-design.md)

---

## File Structure

All paths absolute under `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/`:

| File | Responsibility |
|---|---|
| `runner.sh` | Top-level orchestration — load config, JQL search, dedup, project-ensure, task-create, brief-render, `flow do` spawn. |
| `render_brief.py` | Pure render helper — takes the template path + a vars dict, returns rendered text. Uses `string.Template.safe_substitute()`. Includes the 2000-char description truncation rule. |
| `test_render_brief.py` | Unit tests for `render_brief.py` using stdlib `unittest`. |
| `brief-template.md` | The investigation playbook the spawned Claude session reads. Pure markdown with `$VAR` placeholders. |
| `config.example.yaml` | Default config; copied to `~/.flow/oc-investigator/config.yaml` by `install.sh` if not present. |
| `com.swapnil.flow.oc-investigator.plist` | LaunchAgent. `StartInterval = 900` (15 min). Logs to `~/.flow/oc-investigator/logs/{out,err}.log`. |
| `install.sh` | Idempotent bootstrap: creates dirs, copies config + plist, `launchctl bootstrap` the agent. |
| `README.md` | Operator notes — what this does, how to install, how to test, how to disable. |

Runtime state directory (created by `install.sh`):

```
~/.flow/oc-investigator/
├── config.yaml      # user-editable config
└── logs/
    ├── out.log      # stdout of every runner invocation
    └── err.log      # stderr
```

---

## Task 0: Pre-flight

**Files:** none

- [ ] **Step 1: Verify git author is personal**

```bash
cd /Users/swapnil/workspace/swapnil/flow && git config user.email
```

Expected: `swapnil2233@yahoo.com`. If anything else, STOP and fix before committing anything in this repo.

- [ ] **Step 2: Verify Python 3 is available**

```bash
python3 --version
```

Expected: `Python 3.x` (any 3.x).

- [ ] **Step 3: Verify the Jira skill scripts dir exists**

```bash
ls /Users/swapnil/workspace/vimo/engineering-knowledge-base/skills/jira/scripts/search_issues.py
```

Expected: the file path prints (proves the skill is present). If missing, the user will need to clone `engineering-knowledge-base` first; flag and STOP.

- [ ] **Step 4: Verify the flow binary is on PATH and has FLOW_TERM support**

```bash
which flow && flow --version
```

Expected: a path and a version string. If `flow` is not found, STOP — the user must install/upgrade flow per `~/.claude/skills/flow/` §4.15.

---

## Task 1: Scaffold directory + config example

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/config.example.yaml`

- [ ] **Step 1: Create the scripts dir**

```bash
mkdir -p /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator
```

- [ ] **Step 2: Write `config.example.yaml`**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/config.example.yaml`

```yaml
# OC Investigator — config
# Copy to ~/.flow/oc-investigator/config.yaml (install.sh does this).

jira:
  project_key: OC
  # Filter: investigate tickets assigned to this email.
  # Set to "*" to fire for every new OC ticket regardless of assignee.
  assignee_email: swapnil.dahiphale@getinsured.com
  # JQL look-back window in hours. Wider = more robust against missed polls.
  window_hours: 2
  # Path to the Jira skill's CLI scripts dir. Runner uses these for JQL
  # search from shell. Inside the spawned Claude session the skill is
  # loaded automatically — script paths are not needed there.
  skill_scripts_dir: /Users/swapnil/workspace/vimo/engineering-knowledge-base/skills/jira/scripts

flow:
  # Project the OC tasks land under. Auto-created on first run if missing.
  project_slug: oncall
  # work_dir for each created task. Must be an existing dir on disk.
  task_work_dir: /Users/swapnil/workspace/vimo/agent-workspace

investigation:
  enable_prior_tickets: true
  enable_repo_context: true
  enable_k8s_context: true
  prior_tickets_limit: 5

output:
  # Set false to suppress the Jira comment (useful while testing).
  jira_comment: true
  # Set false to skip writing the detailed flow update.
  flow_update: true
```

- [ ] **Step 3: Verify file exists and is valid YAML**

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/config.example.yaml')); print('ok')"
```

Expected: `ok`. (If `yaml` isn't available, fall back to `python3 -c "import json; print('ok')"` — we'll switch the runner's parser accordingly in Task 4.)

- [ ] **Step 4: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/config.example.yaml
git commit -m "feat(oc-investigator): scaffold scripts dir + config example

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 2: `render_brief.py` — TDD

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/render_brief.py`
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/test_render_brief.py`

We TDD this one because it's pure-Python with deterministic behavior. The other scripts are mostly shell wiring around the CLI; we smoke-test those.

- [ ] **Step 1: Write the failing test for basic substitution**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/test_render_brief.py`

```python
"""Unit tests for render_brief — uses stdlib unittest, no extra deps."""
import os
import tempfile
import unittest

# render_brief.py is in the same directory; import directly.
import sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from render_brief import render, MAX_DESCRIPTION_CHARS


def _write_template(body: str) -> str:
    fd, path = tempfile.mkstemp(suffix=".md")
    with os.fdopen(fd, "w") as f:
        f.write(body)
    return path


class TestBasicSubstitution(unittest.TestCase):
    def test_substitutes_known_placeholders(self):
        tmpl = _write_template("Hello $NAME — ticket $TICKET_KEY")
        out = render(tmpl, {"NAME": "Swapnil", "TICKET_KEY": "OC-1905"})
        self.assertEqual(out, "Hello Swapnil — ticket OC-1905")

    def test_leaves_unknown_placeholders_intact(self):
        # safe_substitute does NOT raise on missing keys; the literal $X stays.
        tmpl = _write_template("Hello $NAME, $UNKNOWN")
        out = render(tmpl, {"NAME": "Swapnil"})
        self.assertEqual(out, "Hello Swapnil, $UNKNOWN")


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the test — verify it fails**

```bash
cd /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator
python3 -m unittest test_render_brief.py -v
```

Expected: ImportError on `from render_brief import ...` because the module doesn't exist yet.

- [ ] **Step 3: Write the minimal `render_brief.py`**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/render_brief.py`

```python
#!/usr/bin/env python3
"""Render the OC investigator brief from a template + vars dict.

Uses string.Template.safe_substitute() so special characters in ticket
fields (quotes, backticks, newlines) don't break rendering.
"""
import argparse
import json
import string
import sys

MAX_DESCRIPTION_CHARS = 2000


def render(template_path: str, vars: dict) -> str:
    with open(template_path, "r") as f:
        tmpl = string.Template(f.read())
    return tmpl.safe_substitute(vars)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Render OC investigator brief from template + JSON vars."
    )
    parser.add_argument("--template", required=True, help="Path to brief template")
    parser.add_argument(
        "--vars",
        required=True,
        help="JSON string of vars dict, e.g. '{\"TICKET_KEY\":\"OC-1\"}'",
    )
    args = parser.parse_args()
    vars_dict = json.loads(args.vars)
    sys.stdout.write(render(args.template, vars_dict))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Run the test — verify it passes**

```bash
cd /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator
python3 -m unittest test_render_brief.py -v
```

Expected: 2 tests pass.

- [ ] **Step 5: Write the failing test for special-character safety**

Append to `test_render_brief.py`:

```python
class TestSpecialCharSafety(unittest.TestCase):
    def test_handles_quotes_and_backticks(self):
        tmpl = _write_template("Summary: $SUMMARY")
        nasty = 'Pod "foo" failed `echo $PATH` — ${var} && rm -rf /'
        out = render(tmpl, {"SUMMARY": nasty})
        self.assertEqual(out, f"Summary: {nasty}")

    def test_handles_newlines_in_value(self):
        tmpl = _write_template("Desc:\n$DESC")
        multi = "line one\nline two\nline three"
        out = render(tmpl, {"DESC": multi})
        self.assertEqual(out, f"Desc:\n{multi}")
```

- [ ] **Step 6: Run tests — verify they pass (no code change needed; safe_substitute already handles this)**

```bash
python3 -m unittest test_render_brief.py -v
```

Expected: 4 tests pass.

- [ ] **Step 7: Write the failing test for description truncation**

Append to `test_render_brief.py`:

```python
class TestDescriptionTruncation(unittest.TestCase):
    def test_truncates_long_description_with_pointer(self):
        from render_brief import truncate_description

        long = "x" * 3000
        url = "https://jira.getinsured.com/browse/OC-1"
        out = truncate_description(long, url)
        self.assertLess(len(out), 3000)
        self.assertIn("(truncated", out)
        self.assertIn(url, out)
        # First 2000 chars are preserved verbatim
        self.assertTrue(out.startswith("x" * MAX_DESCRIPTION_CHARS))

    def test_short_description_unchanged(self):
        from render_brief import truncate_description

        short = "short description"
        out = truncate_description(short, "https://example.com/OC-1")
        self.assertEqual(out, short)

    def test_handles_none_description(self):
        from render_brief import truncate_description

        out = truncate_description(None, "https://example.com/OC-1")
        self.assertEqual(out, "")
```

- [ ] **Step 8: Run tests — verify they fail (no `truncate_description` yet)**

```bash
python3 -m unittest test_render_brief.py -v
```

Expected: 3 new failures with `ImportError: cannot import name 'truncate_description'`.

- [ ] **Step 9: Add `truncate_description` to `render_brief.py`**

In `render_brief.py`, add this function before `def render`:

```python
def truncate_description(text, ticket_url: str) -> str:
    """Truncate Jira description to MAX_DESCRIPTION_CHARS, appending a pointer."""
    if not text:
        return ""
    if len(text) <= MAX_DESCRIPTION_CHARS:
        return text
    head = text[:MAX_DESCRIPTION_CHARS]
    return f"{head}\n\n... (truncated — see {ticket_url})"
```

- [ ] **Step 10: Run all tests — verify they pass**

```bash
python3 -m unittest test_render_brief.py -v
```

Expected: 7 tests pass.

- [ ] **Step 11: Mark `render_brief.py` executable**

```bash
chmod +x /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/render_brief.py
```

- [ ] **Step 12: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/render_brief.py scripts/oc-investigator/test_render_brief.py
git commit -m "feat(oc-investigator): brief renderer with safe substitution + truncation

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 3: Brief template

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/brief-template.md`

- [ ] **Step 1: Write the brief template**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/brief-template.md`

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

- [ ] **Step 2: Smoke-render the template with sample vars**

```bash
cd /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator
python3 render_brief.py --template brief-template.md --vars '{
  "TICKET_KEY":"OC-1905",
  "TICKET_KEY_LC":"oc-1905",
  "TICKET_URL":"https://jira.getinsured.com/browse/OC-1905",
  "TICKET_SUMMARY":"Sample ticket",
  "TICKET_DESCRIPTION":"sample desc",
  "REPORTER":"Alice",
  "PRIORITY":"P2",
  "COMPONENTS":"ms-cp-tenant",
  "CREATED":"2026-05-25T10:00:00+05:30",
  "PRIOR_TICKETS_LIMIT":"5"
}' | head -20
```

Expected: First 20 lines of a rendered brief with `OC-1905`, the URL, `Alice`, etc. substituted in. No raw `$VAR` placeholders remaining for the keys you passed.

- [ ] **Step 3: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/brief-template.md
git commit -m "feat(oc-investigator): investigation playbook brief template

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 4: `runner.sh` skeleton + config loader + `--dry-run`

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

- [ ] **Step 1: Write the runner skeleton with config loader and `--dry-run`**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

```bash
#!/bin/zsh
# OC Investigator — runner.
# Invoked by the LaunchAgent every 15 min (or manually for testing).
# Reads ~/.flow/oc-investigator/config.yaml, finds new OC tickets, creates
# a flow task per ticket, renders a brief, and spawns a Claude session.
# Exits 0 even on Jira failures so the LaunchAgent keeps retrying.

set -u
emulate -L zsh

# --- constants -----------------------------------------------------------
SCRIPT_DIR="${0:A:h}"
CONFIG_FILE="${HOME}/.flow/oc-investigator/config.yaml"
LOG_DIR="${HOME}/.flow/oc-investigator/logs"
LOG_FILE="${LOG_DIR}/out.log"
ERR_FILE="${LOG_DIR}/err.log"

# Make sure flow + python3 from /usr/local/bin (or arch-specific brew dir)
# come before the homebrew-shipped older flow. Mirrors the
# teams-morning-digest PATH fix.
export PATH="/usr/local/bin:/opt/homebrew/bin:${PATH}"

# --- flags ---------------------------------------------------------------
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --help|-h)
      echo "Usage: runner.sh [--dry-run]"
      exit 0
      ;;
  esac
done

# --- logging -------------------------------------------------------------
mkdir -p "$LOG_DIR"
log()  { print -r -- "$(date '+%Y-%m-%dT%H:%M:%S%z') $*" >> "$LOG_FILE"; }
logerr(){ print -r -- "$(date '+%Y-%m-%dT%H:%M:%S%z') ERR $*" >> "$ERR_FILE"; }

log "=== runner start  dry_run=${DRY_RUN} ==="

# --- config --------------------------------------------------------------
if [[ ! -f "$CONFIG_FILE" ]]; then
  logerr "config file not found: $CONFIG_FILE — run install.sh first"
  exit 0
fi

# Parse the YAML config via Python (stdlib only — yaml may not be
# installed). Emit shell-eval'able assignments. Each line: KEY=value.
config_vars=$(python3 - "$CONFIG_FILE" <<'PY'
import sys, re
path = sys.argv[1]
# Minimal YAML reader for our flat-ish config. Handles top-level keys
# with nested 2-space-indented scalar children. No lists, no anchors.
with open(path) as f:
    lines = f.readlines()

current = None
out = {}
for raw in lines:
    line = raw.rstrip("\n")
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    m_top = re.match(r"^([a-zA-Z_][a-zA-Z0-9_]*):\s*$", line)
    if m_top:
        current = m_top.group(1)
        continue
    m_kv = re.match(r"^\s{2,}([a-zA-Z_][a-zA-Z0-9_]*):\s*(.+?)\s*(#.*)?$", line)
    if m_kv and current is not None:
        key = f"{current}_{m_kv.group(1)}".upper()
        val = m_kv.group(2)
        # Strip surrounding quotes
        if (val.startswith('"') and val.endswith('"')) or \
           (val.startswith("'") and val.endswith("'")):
            val = val[1:-1]
        # Shell-quote for safe eval
        val_q = val.replace("'", "'\\''")
        print(f"{key}='{val_q}'")
PY
) || { logerr "config parse failed"; exit 0; }

eval "$config_vars"

# Validate required keys
for key in JIRA_PROJECT_KEY JIRA_ASSIGNEE_EMAIL JIRA_WINDOW_HOURS \
           JIRA_SKILL_SCRIPTS_DIR FLOW_PROJECT_SLUG FLOW_TASK_WORK_DIR; do
  if [[ -z "${(P)key:-}" ]]; then
    logerr "missing required config key: $key"
    exit 0
  fi
done

log "config loaded: project=${JIRA_PROJECT_KEY} assignee=${JIRA_ASSIGNEE_EMAIL} window=${JIRA_WINDOW_HOURS}h"

# --- TODO: subsequent tasks fill in --------------------------------------
# Task 5: JQL composition + Jira invocation
# Task 6: dedup
# Task 7: ensure project
# Task 8: create task + render brief + flow do
# Task 9: logging hooks

log "=== runner end ==="
```

- [ ] **Step 2: Mark the runner executable**

```bash
chmod +x /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh
```

- [ ] **Step 3: Smoke test config parsing — first put a config in place**

```bash
mkdir -p ~/.flow/oc-investigator/logs
cp /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/config.example.yaml \
   ~/.flow/oc-investigator/config.yaml
```

- [ ] **Step 4: Run the runner with `--dry-run`, then inspect the log**

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh --dry-run
tail -5 ~/.flow/oc-investigator/logs/out.log
```

Expected: Last lines show `runner start dry_run=1`, `config loaded: project=OC assignee=swapnil.dahiphale@getinsured.com window=2h`, and `runner end`. No errors in `~/.flow/oc-investigator/logs/err.log`.

- [ ] **Step 5: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/runner.sh
git commit -m "feat(oc-investigator): runner skeleton with config loader + dry-run

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 5: JQL composition + Jira skill invocation

**Files:**
- Modify: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

- [ ] **Step 1: Replace the `# Task 5: JQL composition + Jira invocation` placeholder block with the JQL build + invocation**

In `runner.sh`, replace the `# Task 5:` line with:

```bash
# --- JQL composition -----------------------------------------------------
if [[ "$JIRA_ASSIGNEE_EMAIL" == "*" ]]; then
  JQL="project = ${JIRA_PROJECT_KEY} AND created >= -${JIRA_WINDOW_HOURS}h ORDER BY created ASC"
else
  JQL="project = ${JIRA_PROJECT_KEY} AND assignee = \"${JIRA_ASSIGNEE_EMAIL}\" AND created >= -${JIRA_WINDOW_HOURS}h ORDER BY created ASC"
fi
log "JQL: ${JQL}"

# --- Jira search ---------------------------------------------------------
SEARCH_SCRIPT="${JIRA_SKILL_SCRIPTS_DIR}/search_issues.py"
if [[ ! -x "$SEARCH_SCRIPT" && ! -f "$SEARCH_SCRIPT" ]]; then
  logerr "Jira skill search script not found at: $SEARCH_SCRIPT"
  exit 0
fi

if (( DRY_RUN )); then
  log "[dry-run] would run: python3 $SEARCH_SCRIPT --jql '<...>' --fields summary,description,assignee,reporter,priority,components,created"
fi

# Always do the read — JQL search is side-effect-free, and we need ticket
# data even in dry-run mode to log what would be processed.
SEARCH_OUT=$(python3 "$SEARCH_SCRIPT" \
  --jql "$JQL" \
  --fields "summary,description,assignee,reporter,priority,components,created" \
  2>>"$ERR_FILE") || {
  logerr "Jira search failed (rc=$?) — see err.log; will retry next poll"
  exit 0
}

# Extract ticket keys + key fields via Python (jq is not guaranteed
# installed). Pipes the Jira JSON in on stdin, emits one ticket per
# line as TAB-separated:
#   KEY \t SUMMARY \t REPORTER \t PRIORITY \t COMPONENTS \t CREATED \t DESCRIPTION
TICKETS_TSV=$(printf "%s" "$SEARCH_OUT" | python3 - <<'PY'
import json, sys
data = json.load(sys.stdin)
issues = data.get("issues", []) if isinstance(data, dict) else data
for issue in issues:
    key = issue.get("key", "")
    f = issue.get("fields", {}) or {}
    summary = (f.get("summary") or "").replace("\t", " ").replace("\n", " ")
    reporter = ((f.get("reporter") or {}).get("displayName") or "").replace("\t", " ")
    priority = ((f.get("priority") or {}).get("name") or "").replace("\t", " ")
    comps = ",".join(c.get("name","") for c in (f.get("components") or []))
    created = (f.get("created") or "")
    desc = (f.get("description") or "").replace("\t", " ").replace("\n", "\\n")
    print("\t".join([key, summary, reporter, priority, comps, created, desc]))
PY
)

TICKET_COUNT=$(printf "%s" "$TICKETS_TSV" | grep -c . || true)
log "Jira returned ${TICKET_COUNT} ticket(s)"
```

- [ ] **Step 2: Smoke test — dry-run, inspect the log**

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh --dry-run
tail -10 ~/.flow/oc-investigator/logs/out.log
```

Expected: Log shows the composed JQL, an N>=0 ticket count. If Jira returns 0 (no recent OC tickets assigned to you in last 2h), that's a valid pass — proves the round-trip works.

- [ ] **Step 3: Smoke test the `assignee = "*"` branch**

Temporarily edit `~/.flow/oc-investigator/config.yaml` and set `assignee_email: "*"`. Re-run:

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh --dry-run
tail -3 ~/.flow/oc-investigator/logs/out.log
```

Expected: JQL log line should NOT contain `assignee = "..."` — only the project + created clauses.

Revert the config edit after testing.

- [ ] **Step 4: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/runner.sh
git commit -m "feat(oc-investigator): JQL composition + Jira skill search invocation

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 6: Dedup via `flow show task`

**Files:**
- Modify: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

- [ ] **Step 1: Add the ticket iteration + dedup block**

In `runner.sh`, immediately after the `log "Jira returned ${TICKET_COUNT} ticket(s)"` line, append:

```bash
# --- per-ticket iteration ------------------------------------------------
# Loop over the TSV; produce a list of NEW tickets to process.
NEW_KEYS=()
NEW_TSV=""

while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  KEY="${line%%$'\t'*}"
  SLUG="${KEY:l}"   # lowercase

  if flow show task "$SLUG" >/dev/null 2>&1; then
    log "skip ${KEY} — flow task ${SLUG} already exists"
    continue
  fi

  log "new ticket: ${KEY} (slug ${SLUG})"
  NEW_KEYS+=("$KEY")
  NEW_TSV+="${line}"$'\n'
done <<< "$TICKETS_TSV"

log "${#NEW_KEYS[@]} new ticket(s) to process"

if (( ${#NEW_KEYS[@]} == 0 )); then
  log "=== runner end (nothing to do) ==="
  exit 0
fi
```

- [ ] **Step 2: Smoke test the dedup with an existing slug**

```bash
# Pick any existing flow task slug; create a fake TSV via tmp config
# and verify it's skipped. Simplest: temporarily lower the JQL window
# so a known-existing OC- ticket gets returned.
```

Easier alternative: just run normally — any already-processed ticket should log `skip ${KEY}`. If you have no existing oc-* tasks yet, this branch can't be exercised until Task 8 runs; defer the validation to after Task 8.

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh --dry-run
tail -10 ~/.flow/oc-investigator/logs/out.log
```

Expected: `N new ticket(s) to process` matches `Jira returned N ticket(s)` (assuming none have ever been created). After Task 8 runs end-to-end, a re-invocation should log `skip OC-XXXX`.

- [ ] **Step 3: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/runner.sh
git commit -m "feat(oc-investigator): dedup new tickets against flow task slugs

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 7: Ensure `oncall` project exists

**Files:**
- Modify: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

- [ ] **Step 1: Add the project-ensure block above the per-ticket iteration**

In `runner.sh`, immediately AFTER the line:

```bash
log "${#NEW_KEYS[@]} new ticket(s) to process"
```

…and BEFORE the `if (( ${#NEW_KEYS[@]} == 0 ))` early-exit, insert:

```bash
# --- ensure 'oncall' project exists once per sweep -----------------------
if ! flow show project "$FLOW_PROJECT_SLUG" >/dev/null 2>&1; then
  if (( DRY_RUN )); then
    log "[dry-run] would create flow project '${FLOW_PROJECT_SLUG}'"
  else
    if ! flow add project "On-call" --slug "$FLOW_PROJECT_SLUG" --work-dir "$FLOW_TASK_WORK_DIR" >/dev/null 2>>"$ERR_FILE"; then
      logerr "failed to create flow project ${FLOW_PROJECT_SLUG}"
      exit 0
    fi
    log "created flow project ${FLOW_PROJECT_SLUG}"
  fi
fi
```

- [ ] **Step 2: Smoke test — project ensure is idempotent**

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh --dry-run
flow list projects | grep -q oncall && echo "project exists" || echo "project missing"
```

Expected after a real (non-dry) sweep that creates the project: `project exists`. In dry-run, the project may or may not exist yet — log says `[dry-run] would create flow project 'oncall'`.

- [ ] **Step 3: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/runner.sh
git commit -m "feat(oc-investigator): ensure oncall project exists once per sweep

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 8: Create task + render brief + `flow do`

**Files:**
- Modify: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh`

- [ ] **Step 1: Add the per-new-ticket processing block after the early-exit**

In `runner.sh`, immediately AFTER the `if (( ${#NEW_KEYS[@]} == 0 ))` block (which exits early), append:

```bash
# --- process each new ticket --------------------------------------------
JIRA_BASE_URL="https://jira.getinsured.com"  # used only to compose ticket URL
TEMPLATE="${SCRIPT_DIR}/brief-template.md"
RENDERER="${SCRIPT_DIR}/render_brief.py"

while IFS= read -r line; do
  [[ -z "$line" ]] && continue

  # Split TSV: KEY  SUMMARY  REPORTER  PRIORITY  COMPONENTS  CREATED  DESCRIPTION
  KEY=$(printf "%s" "$line" | cut -f1)
  SUMMARY=$(printf "%s" "$line" | cut -f2)
  REPORTER=$(printf "%s" "$line" | cut -f3)
  PRIORITY=$(printf "%s" "$line" | cut -f4)
  COMPONENTS=$(printf "%s" "$line" | cut -f5)
  CREATED=$(printf "%s" "$line" | cut -f6)
  DESC_ESC=$(printf "%s" "$line" | cut -f7)
  DESCRIPTION=$(printf "%b" "${DESC_ESC//\\n/$'\n'}")  # un-escape \n
  SLUG="${KEY:l}"
  URL="${JIRA_BASE_URL}/browse/${KEY}"

  if (( DRY_RUN )); then
    log "[dry-run] would create task ${SLUG}, render brief, run flow do ${SLUG}"
    continue
  fi

  # Create flow task. Truncate summary at 100 chars for the task name.
  SHORT_SUMMARY="${SUMMARY:0:100}"
  if ! flow add task "[${KEY}] ${SHORT_SUMMARY}" \
        --slug "$SLUG" \
        --project "$FLOW_PROJECT_SLUG" \
        --work-dir "$FLOW_TASK_WORK_DIR" \
        >/dev/null 2>>"$ERR_FILE"; then
    logerr "flow add task failed for ${KEY}; skipping"
    continue
  fi
  log "created flow task ${SLUG}"

  # Tag the task #oncall so it surfaces in tag listings.
  flow update task "$SLUG" --tag oncall >/dev/null 2>&1 || true

  # Render the brief.
  BRIEF_PATH="${HOME}/.flow/tasks/${SLUG}/brief.md"
  VARS_JSON=$(python3 - "$KEY" "$SLUG" "$URL" "$SUMMARY" "$DESCRIPTION" \
                          "$REPORTER" "$PRIORITY" "$COMPONENTS" "$CREATED" \
                          "$INVESTIGATION_PRIOR_TICKETS_LIMIT" <<'PY'
import json, sys
from render_brief import truncate_description  # noqa: E402

(key, slug, url, summary, desc, reporter, priority, comps, created, limit) = sys.argv[1:11]
print(json.dumps({
    "TICKET_KEY": key,
    "TICKET_KEY_LC": slug,
    "TICKET_URL": url,
    "TICKET_SUMMARY": summary,
    "TICKET_DESCRIPTION": truncate_description(desc, url),
    "REPORTER": reporter,
    "PRIORITY": priority,
    "COMPONENTS": comps,
    "CREATED": created,
    "PRIOR_TICKETS_LIMIT": limit,
}))
PY
)

  # render_brief is in SCRIPT_DIR; cd there so the import works.
  ( cd "$SCRIPT_DIR" && python3 render_brief.py \
       --template "$TEMPLATE" \
       --vars "$VARS_JSON" \
  ) > "$BRIEF_PATH" 2>>"$ERR_FILE" || {
    logerr "brief render failed for ${KEY}; flow task created but brief is stub"
    continue
  }
  log "rendered brief at ${BRIEF_PATH}"

  # Spawn the Claude session in a new tab.
  if ! flow do "$SLUG" >/dev/null 2>>"$ERR_FILE"; then
    logerr "flow do ${SLUG} failed; user can open the task manually later"
    continue
  fi
  log "spawned session for ${SLUG}"
done <<< "$NEW_TSV"

log "=== runner end ==="
```

- [ ] **Step 2: Set the previous `=== runner end ===` log line to ONLY appear once**

Search `runner.sh` for `log "=== runner end ==="` — there should be exactly TWO occurrences:
1. Inside the early-exit `if (( ${#NEW_KEYS[@]} == 0 ))` block.
2. At the very end of the script (the one just added in Step 1).

If there's a third one left over from Task 4, remove the trailing standalone `log "=== runner end ==="` that previously sat at the bottom of the file — it's now redundant.

- [ ] **Step 3: End-to-end smoke test with `jira_comment: false`**

```bash
# Temporarily set output.jira_comment: false so the spawned session
# does NOT post to Jira even on a real ticket.
sed -i.bak 's/jira_comment: true/jira_comment: false/' ~/.flow/oc-investigator/config.yaml

# Confirm the spawn actually opens a tab — this WILL fire flow do.
# If no new OC tickets exist in last 2h, force one by widening the window:
sed -i.bak 's/window_hours: 2/window_hours: 168/' ~/.flow/oc-investigator/config.yaml

/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh
tail -20 ~/.flow/oc-investigator/logs/out.log
```

Expected: a flow task is created with slug `oc-XXXX`, a brief is written to `~/.flow/tasks/oc-XXXX/brief.md`, and a new terminal tab opens running the investigation. Verify with:

```bash
flow list tasks --project oncall
ls ~/.flow/tasks/oc-*/brief.md 2>/dev/null | head
```

Revert window:

```bash
sed -i.bak 's/window_hours: 168/window_hours: 2/' ~/.flow/oc-investigator/config.yaml
```

If a tab opens, close it manually (don't let the Claude session post to Jira if you didn't mean to — `jira_comment: false` should already gate that, but be careful).

- [ ] **Step 4: Restore `jira_comment: true` for normal operation**

```bash
sed -i.bak 's/jira_comment: false/jira_comment: true/' ~/.flow/oc-investigator/config.yaml
rm -f ~/.flow/oc-investigator/config.yaml.bak
```

- [ ] **Step 5: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/runner.sh
git commit -m "feat(oc-investigator): create flow task, render brief, spawn session

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 9: LaunchAgent plist

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/com.swapnil.flow.oc-investigator.plist`

- [ ] **Step 1: Write the plist**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/com.swapnil.flow.oc-investigator.plist`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.swapnil.flow.oc-investigator</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/runner.sh</string>
    </array>
    <key>StartInterval</key>
    <integer>900</integer>
    <key>RunAtLoad</key>
    <false/>
    <key>KeepAlive</key>
    <false/>
    <key>StandardOutPath</key>
    <string>/Users/swapnil/.flow/oc-investigator/logs/out.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/swapnil/.flow/oc-investigator/logs/err.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
</dict>
</plist>
```

- [ ] **Step 2: Validate the plist syntax**

```bash
plutil -lint /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/com.swapnil.flow.oc-investigator.plist
```

Expected: `... OK`. If it errors, fix the XML.

- [ ] **Step 3: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/com.swapnil.flow.oc-investigator.plist
git commit -m "feat(oc-investigator): LaunchAgent plist (15-min interval)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 10: `install.sh`

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/install.sh`

- [ ] **Step 1: Write the install script**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/install.sh`

```bash
#!/bin/zsh
# Idempotent installer for the OC investigator LaunchAgent.

set -eu

SCRIPT_DIR="${0:A:h}"
PLIST_NAME="com.swapnil.flow.oc-investigator.plist"
SRC_PLIST="${SCRIPT_DIR}/${PLIST_NAME}"
DEST_PLIST="${HOME}/Library/LaunchAgents/${PLIST_NAME}"
SRC_CONFIG="${SCRIPT_DIR}/config.example.yaml"
DEST_CONFIG_DIR="${HOME}/.flow/oc-investigator"
DEST_CONFIG="${DEST_CONFIG_DIR}/config.yaml"

print "Installing OC investigator…"

# 1. Runtime dirs
mkdir -p "${DEST_CONFIG_DIR}/logs"

# 2. Config — only copy if not present (preserve user edits).
if [[ -f "$DEST_CONFIG" ]]; then
  print "config exists, not overwriting: $DEST_CONFIG"
else
  cp "$SRC_CONFIG" "$DEST_CONFIG"
  print "config copied: $DEST_CONFIG  ← edit this to set assignee_email"
fi

# 3. Plist — replace and reload.
mkdir -p "${HOME}/Library/LaunchAgents"
cp "$SRC_PLIST" "$DEST_PLIST"
print "plist installed: $DEST_PLIST"

# 4. Unload existing, then bootstrap.
UID_NUM=$(id -u)
if launchctl print "gui/${UID_NUM}/com.swapnil.flow.oc-investigator" >/dev/null 2>&1; then
  launchctl bootout "gui/${UID_NUM}" "$DEST_PLIST" 2>/dev/null || true
fi
launchctl bootstrap "gui/${UID_NUM}" "$DEST_PLIST"
print "LaunchAgent loaded — first poll within 15 min"

print ""
print "Next steps:"
print "  1. Edit $DEST_CONFIG and confirm assignee_email"
print "  2. Trigger an immediate run with:"
print "       launchctl kickstart gui/${UID_NUM}/com.swapnil.flow.oc-investigator"
print "  3. Tail logs at $DEST_CONFIG_DIR/logs/"
```

- [ ] **Step 2: Mark executable**

```bash
chmod +x /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/install.sh
```

- [ ] **Step 3: Smoke-test the install**

```bash
/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/install.sh
launchctl list | grep oc-investigator
```

Expected: install prints all 4 steps; `launchctl list` shows the agent. Don't `kickstart` it yet unless you want it to fire immediately.

- [ ] **Step 4: Verify the LaunchAgent fires (optional — needs a tab to open)**

```bash
launchctl kickstart "gui/$(id -u)/com.swapnil.flow.oc-investigator"
tail -f ~/.flow/oc-investigator/logs/out.log &
TAIL=$!
sleep 30
kill $TAIL 2>/dev/null
```

Expected: log shows a fresh `runner start … runner end` sequence. If there are new OC tickets, a tab opens.

- [ ] **Step 5: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/install.sh
git commit -m "feat(oc-investigator): idempotent install.sh — dirs, config, launchctl bootstrap

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 11: README

**Files:**
- Create: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/README.md`

- [ ] **Step 1: Write operator notes**

Path: `/Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/README.md`

```markdown
# oc-investigator

Userland automation that polls Jira every 15 min for new on-call (OC)
tickets assigned to you, creates a flow task per ticket under the
`oncall` project, and spawns a Claude session that investigates the
issue (with parallel subagents) and posts a concise RCA to the Jira
ticket + a detailed update to the flow task.

**No auto-remediation.** Proposals only.

See the design spec at `docs/superpowers/specs/2026-05-25-oc-investigator-design.md`.

## Install

```bash
./install.sh
# Then edit ~/.flow/oc-investigator/config.yaml as needed.
```

## Test

1. **Dry-run** (no mutations):

   ```bash
   ./runner.sh --dry-run
   tail ~/.flow/oc-investigator/logs/out.log
   ```

2. **Run, but skip Jira comments** — set `output.jira_comment: false`
   in the config, then trigger:

   ```bash
   launchctl kickstart "gui/$(id -u)/com.swapnil.flow.oc-investigator"
   ```

   A new tab opens per new OC ticket; the investigation writes a flow
   update at `~/.flow/tasks/oc-NNNN/updates/` but does NOT touch Jira.

3. **Full end-to-end** — flip `jira_comment: true` and wait for a real
   OC ticket (or `kickstart` after creating a test ticket).

## Disable

```bash
launchctl bootout "gui/$(id -u)" ~/Library/LaunchAgents/com.swapnil.flow.oc-investigator.plist
```

## Re-enable

```bash
./install.sh
```

## Logs

- `~/.flow/oc-investigator/logs/out.log` — every runner invocation.
- `~/.flow/oc-investigator/logs/err.log` — anything that went wrong.

## Config

`~/.flow/oc-investigator/config.yaml` — see `config.example.yaml` in
this dir for the full schema with comments.

Key knobs:
- `jira.assignee_email` — defaults to your email; set to `"*"` for any assignee.
- `jira.window_hours` — JQL look-back window. Wider survives missed polls.
- `output.jira_comment` — set false during testing to avoid Jira side-effects.
- `investigation.enable_*` — turn off specific subagent phases.
```

- [ ] **Step 2: Commit**

```bash
cd /Users/swapnil/workspace/swapnil/flow
git add scripts/oc-investigator/README.md
git commit -m "docs(oc-investigator): operator README — install, test, disable

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
```

---

## Task 12: Final end-to-end verification

**Files:** none (verification only)

- [ ] **Step 1: Confirm files in place**

```bash
ls /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator/
```

Expected exactly:
```
README.md
brief-template.md
com.swapnil.flow.oc-investigator.plist
config.example.yaml
install.sh
render_brief.py
runner.sh
test_render_brief.py
```

- [ ] **Step 2: Confirm unit tests still pass**

```bash
cd /Users/swapnil/workspace/swapnil/flow/scripts/oc-investigator
python3 -m unittest test_render_brief.py -v
```

Expected: 7 tests pass.

- [ ] **Step 3: Confirm the LaunchAgent is loaded**

```bash
launchctl print "gui/$(id -u)/com.swapnil.flow.oc-investigator" | head -20
```

Expected: a `program = …/runner.sh` line and `state = …` (likely `waiting` until next interval).

- [ ] **Step 4: Trigger an immediate sweep and watch the log**

```bash
launchctl kickstart "gui/$(id -u)/com.swapnil.flow.oc-investigator"
sleep 5
tail -20 ~/.flow/oc-investigator/logs/out.log
```

Expected: a `=== runner start ===` block, the JQL log, a ticket count, and either new task creation lines (if there were new OC tickets) or `=== runner end (nothing to do) ===`.

- [ ] **Step 5: Confirm the on-call project exists in flow**

```bash
flow list projects | grep oncall
```

Expected: `oncall` row appears (status `active`).

- [ ] **Step 6: Save a progress note on the `oc-investigator` task**

This implementation is being tracked as flow task `oc-investigator`
(project `flow-itself`). Save a closing-style progress note:

```bash
mkdir -p ~/.flow/tasks/oc-investigator/updates
cat > ~/.flow/tasks/oc-investigator/updates/$(date +%Y-%m-%d)-implementation-complete.md <<'EOF'
# Implementation complete

All scripts/oc-investigator/ files in place, LaunchAgent loaded,
unit tests pass, end-to-end smoke test verified. Awaiting real OC
tickets to validate the full investigation flow + Jira commenting.

Next: monitor first few real-world runs for issues — especially the
subagent fan-out behavior and Jira comment quality. Adjust the
brief-template.md investigation playbook based on real-world findings.
EOF
```

- [ ] **Step 7: Do NOT mark the flow task done yet**

The task stays in-progress until at least one real OC ticket has been
processed end-to-end successfully (Jira comment landed, flow update
written, no errors in `err.log`). Closing it would trigger the flow
close-out sweep before there's anything substantive to distill.

---

## Self-Review

| Spec section / requirement | Plan coverage |
|---|---|
| §1 Goal (auto flow task, spawn session, RCA to Jira, detailed flow update) | Tasks 6–8 (task create, brief render, spawn); investigation outputs happen in the spawned session per `brief-template.md` written in Task 3 |
| §3 Architecture (LaunchAgent → runner → spawned session w/ subagents) | Tasks 4–9 build runner; Task 9 plist; Task 3 brief encodes the 6-phase playbook |
| §4.1 LaunchAgent details | Task 9 |
| §4.2 Runner responsibilities | Tasks 4–8 step-by-step |
| §4.3 Config schema (incl. `jira.skill_scripts_dir`, no `spawn_tab`) | Task 1 config.example.yaml matches §4.3 verbatim |
| §4.4 Brief template w/ `$VAR` placeholders + 2000-char truncation | Task 3 (template) + Task 2 (renderer w/ `truncate_description`) |
| §4.5 Install script | Task 10 |
| §6 Dedup via `flow show task` exit code | Task 6 |
| §7 Error handling table (rows: Jira fail, flow add fail, etc.) | Task 4 (config-fail → exit 0), Task 5 (Jira fail → exit 0), Task 7 (project-fail → exit 0), Task 8 (per-ticket failures → log + continue) |
| §8 Testing path | Tasks 4 step 4, 8 step 3, 12 |
| §11 Implementation order | Plan tasks 1→12 follow it |

**Placeholder scan:** none found.

**Type/signature consistency:**
- `render(template_path, vars)` and `truncate_description(text, ticket_url)` — signatures used in Task 2 tests match the implementations and the call site in Task 8.
- Config keys `JIRA_PROJECT_KEY`, `JIRA_ASSIGNEE_EMAIL`, `JIRA_WINDOW_HOURS`, `JIRA_SKILL_SCRIPTS_DIR`, `FLOW_PROJECT_SLUG`, `FLOW_TASK_WORK_DIR`, `INVESTIGATION_PRIOR_TICKETS_LIMIT` — produced by the Task 4 parser (top-level `<group>` + child key → `GROUP_CHILD`), consumed in Tasks 5/7/8. Verified consistent.
- TSV column order in Task 5 (`KEY \t SUMMARY \t REPORTER \t PRIORITY \t COMPONENTS \t CREATED \t DESCRIPTION`) matches the `cut -f1..f7` extraction in Task 8.

**Known follow-ups (not in scope of this plan):**
- The Phase-2-4 subagent prompts inside `brief-template.md` are written narratively; the real session interprets them. If the first few real runs show the agent dispatching less-than-parallel subagents or skipping fan-out, tighten the wording in a follow-up task.
- No automated test of the LaunchAgent lifecycle — relies on macOS `launchctl` which is hard to mock. Manual verification only (Task 10 step 4, Task 12 step 3).

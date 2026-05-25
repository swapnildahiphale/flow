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

# Task 6: dedup
# Task 7: ensure project
# Task 8: create task + render brief + flow do
# Task 9: logging hooks

log "=== runner end ==="

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

if (( ${#NEW_KEYS[@]} == 0 )); then
  log "=== runner end (nothing to do) ==="
  exit 0
fi

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

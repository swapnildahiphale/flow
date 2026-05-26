#!/bin/zsh
# Idempotent runner for the teams-morning-digest LaunchAgent.
#
# Fires at 08:00 IST Mon–Fri via StartCalendarInterval, plus on every agent
# load (RunAtLoad) so we catch up when the laptop opens after a closed window.
# This script is the gate that decides whether a run is actually warranted.
#
# Skip if:
#   - it's a weekend (Saturday or Sunday)
#   - it's before 08:00 local time today
#   - today's digest already exists (a run already produced output)
# Otherwise: spawn the playbook run via `flow run playbook teams-morning-digest`.

set -eu

PLAYBOOK_DIR="$HOME/.flow/playbooks/teams-morning-digest"
LOG_FILE="$PLAYBOOK_DIR/runner.log"
TODAY=$(date "+%Y-%m-%d")
DIGEST_FILE="$PLAYBOOK_DIR/digests/${TODAY}.md"
DOW=$(date "+%u")     # 1=Monday … 7=Sunday
HOUR=$(date "+%H")    # 00..23
NOW=$(date "+%Y-%m-%dT%H:%M:%S%z")

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$NOW] $*" >> "$LOG_FILE"
}

# Weekend skip
if [ "$DOW" -gt 5 ]; then
  log "skip: weekend (DOW=$DOW)"
  exit 0
fi

# Before 8 AM local — the scheduled fire hasn't happened yet
if [ "$HOUR" -lt 8 ]; then
  log "skip: before 08:00 (HOUR=$HOUR)"
  exit 0
fi

# Already ran today
if [ -f "$DIGEST_FILE" ]; then
  log "skip: digest already exists ($DIGEST_FILE)"
  exit 0
fi

# Ensure flow CLI is reachable when spawned from launchd (limited PATH).
# $HOME/.local/bin first — that's where `make install` writes the up-to-date
# binary. /opt/homebrew/bin may hold an older copy from a prior install, and
# we don't want launchd to find that one (it predates FLOW_TERM support).
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/go/bin:$PATH"

if ! command -v flow >/dev/null 2>&1; then
  log "ERROR: flow not on PATH after augmentation (PATH=$PATH)"
  exit 1
fi

log "starting: flow run playbook teams-morning-digest"
flow run playbook teams-morning-digest >> "$LOG_FILE" 2>&1
log "done (exit $?)"

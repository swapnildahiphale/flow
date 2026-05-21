#!/bin/zsh
# Install the Teams morning digest playbook into user-local paths.
#
# Idempotent: safe to re-run. Existing brief.md is overwritten; state
# seed files (channels-excluded.md, topics.md) are only written on first
# install — re-running does not clobber user edits.
#
# Usage:
#   zsh scripts/teams-morning-digest/install.sh

set -eu

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
PLAYBOOK_DIR="$HOME/.flow/playbooks/teams-morning-digest"

# 0. Preflight: flow CLI present.
if ! command -v flow >/dev/null 2>&1; then
  echo "ERROR: flow CLI not on PATH. Install flow first." >&2
  exit 1
fi

# 1. Create the playbook entity (idempotent — only if not already there).
if ! flow show playbook teams-morning-digest >/dev/null 2>&1; then
  flow add playbook "Teams morning digest" \
    --slug teams-morning-digest \
    --project flow-itself \
    --work-dir /Users/Swapnil/workspace/swapnil/flow
fi

# 2. Install the playbook brief (always overwrite — this is the source of truth).
cp "$SRC_DIR/brief.md" "$PLAYBOOK_DIR/brief.md"

# 3. Create state subdirectories.
mkdir -p "$PLAYBOOK_DIR/topics"
mkdir -p "$PLAYBOOK_DIR/digests"

# 4. Seed state files (first install only — do not overwrite user edits).

if [ ! -f "$PLAYBOOK_DIR/channels-excluded.md" ]; then
  cat > "$PLAYBOOK_DIR/channels-excluded.md" <<'EXCLUDEEOF'
# Channels excluded from the morning digest
# Add channel display names below, one per line, prefixed with `- `.
# Matching is case-insensitive substring — partial names work.
# Example:
#   - Jenkins Builds
#   - Random Social
#   - General Notifications
EXCLUDEEOF
fi

if [ ! -f "$PLAYBOOK_DIR/topics.md" ]; then
  cat > "$PLAYBOOK_DIR/topics.md" <<'TOPICSEOF'
# Known topics
<!-- One line per topic: - [slug](topics/slug.md) — description | last-seen: YYYY-MM-DD -->
TOPICSEOF
fi

# Do NOT seed watermark.md — its absence signals "first run → 14-day lookback".

# 5. Install runner script (idempotent — always overwrite, source of truth in repo).
RUNNER_DIR="$HOME/.flow/scripts"
RUNNER_SCRIPT="$RUNNER_DIR/teams-morning-digest-runner.sh"
mkdir -p "$RUNNER_DIR"
cp "$SRC_DIR/runner.sh" "$RUNNER_SCRIPT"
chmod +x "$RUNNER_SCRIPT"

# 6. Install LaunchAgent plist. Expand $HOME so the plist has absolute paths
#    (launchd does NOT expand env vars inside ProgramArguments strings).
LAUNCHAGENT_DIR="$HOME/Library/LaunchAgents"
LAUNCHAGENT_LABEL="com.swapnil.flow.teams-morning-digest"
LAUNCHAGENT_PLIST="$LAUNCHAGENT_DIR/${LAUNCHAGENT_LABEL}.plist"
mkdir -p "$LAUNCHAGENT_DIR"
sed "s#\$HOME#$HOME#g" "$SRC_DIR/${LAUNCHAGENT_LABEL}.plist" > "$LAUNCHAGENT_PLIST"

# 7. Reload the LaunchAgent (unload-if-loaded, then load).
if launchctl list | grep -q "$LAUNCHAGENT_LABEL"; then
  launchctl unload "$LAUNCHAGENT_PLIST" 2>/dev/null || true
fi
launchctl load "$LAUNCHAGENT_PLIST"

# 8. Done.
cat <<DONEEOF

Install complete.

Playbook files:
  $PLAYBOOK_DIR/brief.md          (source of truth for each run)
  $PLAYBOOK_DIR/topics/           (per-topic state files)
  $PLAYBOOK_DIR/digests/          (daily digest output)

Seeded (first install only, not overwritten on re-run):
  $PLAYBOOK_DIR/channels-excluded.md
  $PLAYBOOK_DIR/topics.md

Scheduler:
  $RUNNER_SCRIPT
  $LAUNCHAGENT_PLIST  (loaded)

Fires at 08:00 local Mon–Fri. If the laptop is closed at 08:00, the run
catches up on next wake (launchd coalesces missed StartCalendarInterval
events). The runner script is idempotent: weekend/before-8/digest-already-
exists all short-circuit it.

watermark.md: absent → first run will do a 14-day lookback and seed the topic store.

Logs:
  tail -f $PLAYBOOK_DIR/runner.log

Trigger a manual run now:
  flow run playbook teams-morning-digest

Disable the schedule:
  launchctl unload $LAUNCHAGENT_PLIST
DONEEOF

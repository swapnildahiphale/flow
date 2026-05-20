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

# 5. Done.
cat <<DONEEOF

Install complete.

Files installed / updated:
  $PLAYBOOK_DIR/brief.md  (source of truth for each run)
  $PLAYBOOK_DIR/topics/   (per-topic state files; empty on first install)
  $PLAYBOOK_DIR/digests/  (daily digest output; empty on first install)

Seeded (first install only, not overwritten on re-run):
  $PLAYBOOK_DIR/channels-excluded.md
  $PLAYBOOK_DIR/topics.md

watermark.md: absent → first run will do a 14-day lookback and seed the topic store.

To run the playbook:
  flow run playbook teams-morning-digest

First run note:
  The 14-day lookback may fetch 500+ messages and take several minutes.
  After the first run, review $PLAYBOOK_DIR/channels-excluded.md
  and add noisy channels (CI builds, social, etc.) to keep future runs focused.
DONEEOF

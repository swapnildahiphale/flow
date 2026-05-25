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

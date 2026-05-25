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

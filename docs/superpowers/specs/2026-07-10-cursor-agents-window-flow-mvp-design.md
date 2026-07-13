# Cursor Agents Window — minimal flow MVP (skill-first)

**Date:** 2026-07-10 (written 2026-07-13)
**Status:** Design approved in conversation; awaiting user review of this file
**Task:** `cursor-flow-mvp` (project `flow-itself`)
**Related:** `docs/codex-adapter-estimation.md` (full harness parity estimate — out of scope here)

## Summary

Make flow usable from Cursor’s **Agents Window** without spawn UI, hooks, or auto-rebind. The user opens a chat manually, asks to work on a flow task, and a Cursor-flavored skill binds the current conversation via `flow do --here`. Scoop stays in-session. Close-out distillation runs in the same chat; the Cursor harness’s `SkipPermissionsRun` is a no-op so `flow done` does not spawn a second agent.

Chosen approach: **thin Cursor harness + Cursor-flavored skill** (not skill-only, not full CLI parity).

## Goals

- Bind Agents Window chats to flow tasks via `CURSOR_CONVERSATION_ID`
- Keep scoop (§4.10) and close-out judgment in the interactive session
- Install a Cursor-native flow skill; stop Cursor from loading the Claude-oriented skill copy
- Keep binary changes small and testable without opening the Agents Window UI

## Non-goals

- `flow do` spawning or focusing an Agents Window chat
- Cursor hooks (`sessionStart` / `beforeSubmitPrompt`) for auto-rebind or drift anchors
- `[live]` markers for IDE chats
- `flow do --auto`, owners, or playbook headless runs in Agents Window
- Full Claude harness parity / Codex / Gemini adapters
- Using `cursor-agent` CLI as the primary UX

## Architecture

Two pieces:

1. **`internal/harness/cursor/`** — thin adapter registered in `allHarnesses()`. Enables ambient detection, `--here` bind, transcript render, and a no-op close-out sweep. Does not spawn UI.

2. **Cursor-flavored flow skill** — same recipes as the Claude skill, rewritten for bind-here-only session lifecycle and in-session close-out before `flow done`.

```
You open Agents Window chat
  → "work on <task>"
  → skill: flow do --here <slug>   (session_id + harness=cursor)
  → work + scoop in this chat
  → on done: skill distills KB/project update, then flow done <slug>
  → binary flips status; SkipPermissionsRun no-ops
```

## Decisions

| Topic | Decision |
|---|---|
| Primary surface | Agents Window (not CLI) |
| Approach | Thin harness + Cursor skill |
| Close-out ownership | **Option B:** skill distills; harness `SkipPermissionsRun` returns success no-op; plain `flow done` (no `--no-sweep` flag required) |
| Spawn / hooks / `[live]` | Deferred |
| Skill conflict | On install, remove/replace Cursor imports of the Claude-oriented flow skill (notably `~/.agents/skills/flow/`) so only the Cursor-flavored skill is active |

## Skill deltas

| Topic | Claude skill | Cursor skill |
|---|---|---|
| Start work | `flow do` / `--here` / `--auto` | **Only** bind-here after user opens a chat |
| Unbound chat | SessionStart may rebind | No hooks — ask which task and bind, or intake |
| Scoop (§4.10) | In-session append | Same |
| Close-out (§4.7) | Confirm → `flow done` → headless sweep | Confirm → skill distills → `flow done` |
| `flow done` | Status + sweep | Status flip; sweep no-op under cursor harness |
| Playbooks `--auto` / owners | Supported | Explicitly unsupported in this MVP |

**Install path:** `~/.cursor/skills/flow/SKILL.md` + VERSION sidecar (mirrors Claude layout). `flow skill install` / `update` targets the ambient harness (cursor when `CURSOR_CONVERSATION_ID` is set).

**Hygiene:** `InstallSkill` writes the Cursor skill and removes/replaces the stale Agents-path flow skill so Cursor does not keep loading Claude-oriented instructions.

## Thin harness contract

| Method | Behavior |
|---|---|
| `Name` / `Binary` | `"cursor"` / `"cursor-agent"` (identity only) |
| `SessionIDEnvVar` | `CURSOR_CONVERSATION_ID` |
| `NewSessionID` | Unused / error — MVP is `--here` only |
| `ValidateSessionID` | UUID format check |
| `ValidateSession` | Always `nil` (sid-keyed transcripts) |
| `LaunchCmd` / `ResumeCmd` | Refuse with clear guidance: open Agents Window and `flow do --here` |
| `SkipPermissionsRun` | **No-op success** (option B) |
| `AutoRunArgv` | Unsupported error |
| `LiveSessionIDs` | Empty map |
| `RenderTranscript` | `~/.cursor/projects/<ws>/agent-transcripts/<id>/<id>.jsonl` — simple `role`/`message` (+ `turn_ended`) decoder |
| Skill paths | `~/.cursor/skills/flow/SKILL.md` + VERSION |
| Hook install/uninstall | No-op `(false, nil)` |
| Wire-up | `cursor.New()` in `allHarnesses()` |

## Bind, scoop, close-out

**Bind**

- Ambient harness sees `CURSOR_CONVERSATION_ID` → cursor adapter.
- `flow do --here <slug>` pins `session_id` + `harness=cursor`, flips in-progress.
- Plain `flow do <slug>` (spawn) errors: Agents Window users must open a chat and use `--here`.

**Scoop**

- Unchanged skill behavior: append durable facts to `~/.flow/kb/*`. No binary change.

**Close-out**

1. Skill detects wrap-up → confirm with user.
2. Skill distills KB bullets and optional project update (same bars as today’s sweep prompt).
3. Skill may cross-check with `flow transcript <slug>`; primary context is the live chat.
4. Skill runs `flow done <slug>`.
5. Binary flips status; `SkipPermissionsRun` no-ops cleanly (no warning spam).

**Errors**

- Unbound `flow show task`: same as today — prompt to bind.
- Missing transcript on done: status still flips; skill already distilled.
- Unknown `tasks.harness` name: refuse (existing behavior).

## Testing

- Harness unit: env var, session-id validation, `SkipPermissionsRun` no-op, launch/resume refuse, hooks no-op.
- Transcript: fixture matching Agents Window jsonl.
- App wiring: with `CURSOR_CONVERSATION_ID` set, `--here` pins `harness=cursor` (temp `FLOW_ROOT` + `$HOME`).
- Skill install: writes `~/.cursor/skills/flow/`, removes/replaces `~/.agents/skills/flow` Claude-oriented copy.
- No e2e that drives Agents Window UI.

## Implementation deliverables (next phase)

1. `internal/harness/cursor/` + register in `allHarnesses()`
2. Cursor-flavored skill content + install/update via ambient harness
3. Install hygiene for stale `.agents` / Cursor imports of Claude skill
4. Focused tests as above

Packaging detail (separate embed vs shared source with Cursor overlays) is deferred to the implementation plan; default preference is a clear Cursor skill artifact installed to `~/.cursor/skills/flow/`.

## Effort (rough)

~1–2 focused days after the implementation plan — far below full Codex-style parity (~25–35h) because spawn, hooks, and live detection are out of scope.

## Open items for the plan (not blockers for this design)

- Exact workspace-dir → `~/.cursor/projects/<encoded>/` mapping for `RenderTranscript`
- Whether refuse messages for `LaunchCmd` are returned as strings vs hard errors at the `flow do` call site
- How much of the Claude skill is copied vs rewritten for Cursor (prefer rewrite of session sections only)

## Success criteria

1. This design doc reviewed and accepted
2. Later: `--here` bind works from Agents Window; scoop/close-out work in-session; `flow done` does not spawn Claude
3. Cursor loads only the Cursor-flavored flow skill

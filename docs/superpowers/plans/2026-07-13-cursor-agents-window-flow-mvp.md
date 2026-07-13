# Cursor Agents Window flow MVP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make flow usable from Cursor Agents Window via a thin `cursor` harness (`flow do --here` bind + transcript + no-op sweep) and a Cursor-flavored skill (bind-here-only, in-session scoop/close-out).

**Architecture:** Add `internal/harness/cursor` implementing `harness.Harness` with Agents Window semantics (env `CURSOR_CONVERSATION_ID`, refuse spawn, no-op `SkipPermissionsRun`). Register it in `allHarnesses()`. Gate plain `flow do` (spawn) when the selected harness is cursor. Ship a second embedded skill at `internal/app/skill/cursor/SKILL.md` installed to `~/.cursor/skills/flow/`, and remove the stale Claude-oriented copy at `~/.agents/skills/flow` on install.

**Tech Stack:** Go (existing module), `modernc.org/sqlite`, stdlib only. Patterns from `internal/harness/claude/`.

**Spec:** `docs/superpowers/specs/2026-07-10-cursor-agents-window-flow-mvp-design.md`

## Global Constraints

- **No CGO.** Pure Go only.
- **No new third-party deps.**
- **Flag parsing:** `flagSet(name)` from `internal/app/helpers.go`.
- **Exit codes:** 0 success, 1 runtime, 2 usage.
- **Timestamps:** RFC3339 strings.
- **Tests:** real SQLite in temp dir; override `$HOME` / `$FLOW_ROOT`. No DB mocks. Run `go test ./...` / `make test`.
- **YAGNI:** no hooks, no `[live]`, no Agents Window spawn, no `--auto`/owners for cursor.
- **TDD:** failing test → implement → pass → commit per task.
- **Commits:** only when the user asked for commits in the session executing this plan; otherwise skip commit steps and leave a clean working tree checkpoint note.

## File map

| File | Role |
|---|---|
| `internal/harness/harness.go` | Add `NameCursor` |
| `internal/harness/cursor/cursor.go` | Identity, validation, refuse launch/resume, no-op sweep/hooks, skill paths + install hygiene |
| `internal/harness/cursor/transcript.go` | Path resolve + jsonl render |
| `internal/harness/cursor/cursor_test.go` | Harness unit tests |
| `internal/harness/cursor/transcript_test.go` | Decoder + path tests |
| `internal/app/harness.go` | `cursor.New()` in `allHarnesses()` |
| `internal/app/do.go` | Spawn gate for cursor; soften `--here` help text |
| `internal/app/do_cursor_test.go` | `--here` pins `harness=cursor`; spawn refuses |
| `internal/app/skill/cursor/SKILL.md` | Cursor-flavored skill |
| `internal/app/skill.go` | Dual embed; select content by harness |
| `internal/app/init.go` | Use `skillContentFor(h)` |
| `internal/app/skill_test.go` | Cursor install + content pins |

---

### Task 1: `NameCursor` + cursor identity / validation / no-ops

**Files:**
- Modify: `internal/harness/harness.go`
- Create: `internal/harness/cursor/cursor.go`
- Create: `internal/harness/cursor/cursor_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/harness/cursor/cursor_test.go` with tests for:

- `Name() == harness.NameCursor`, `Binary() == "cursor-agent"`, `SessionIDEnvVar() == "CURSOR_CONVERSATION_ID"`
- `ValidateSessionID` accepts lowercase UUID hex; rejects `"not-a-uuid"`
- `SkipPermissionsRun` returns nil
- All four hook install/uninstall methods return `(false, nil)`
- `LiveSessionIDs` returns empty map, nil error
- `NewSessionID` returns an error mentioning `--here`
- `ValidateSession` always returns nil
- `LaunchCmd` / `ResumeCmd` strings contain `"Agents Window"` and `"--here"`
- `AutoRunArgv` returns a nil/empty slice

Use `strings.Contains` for substring checks.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harness/cursor/ -count=1`

Expected: FAIL (package / `NameCursor` missing)

- [ ] **Step 3: Write minimal implementation**

In `internal/harness/harness.go` add:

```go
const (
	NameClaude Name = "claude"
	NameCursor Name = "cursor"
)
```

Create `internal/harness/cursor/cursor.go`:

```go
package cursor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"flow/internal/harness"
)

func New() harness.Harness { return &cursorHarness{} }

type cursorHarness struct{}

func (c *cursorHarness) Name() harness.Name      { return harness.NameCursor }
func (c *cursorHarness) Binary() string          { return "cursor-agent" }
func (c *cursorHarness) SessionIDEnvVar() string { return "CURSOR_CONVERSATION_ID" }

var sessionIDRe = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

func (c *cursorHarness) NewSessionID() (string, error) {
	return "", fmt.Errorf("cursor harness does not mint sessions — open Agents Window and use flow do --here")
}

func (c *cursorHarness) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid cursor conversation UUID: %q", s)
	}
	return nil
}

func (c *cursorHarness) ValidateSession(workDir, sessionID string) error { return nil }

func (c *cursorHarness) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	return "error: cursor Agents Window cannot be spawned by flow — open a chat and run: flow do --here <slug>"
}

func (c *cursorHarness) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	return "error: cursor Agents Window cannot be resumed by flow — open the chat and run: flow do --here <slug>"
}

func (c *cursorHarness) SkipPermissionsRun(prompt string) error { return nil }

func (c *cursorHarness) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	return nil
}

func (c *cursorHarness) LiveSessionIDs() (map[string]int, error) {
	return map[string]int{}, nil
}

// Temporary stub until Task 2.
func (c *cursorHarness) RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error {
	return fmt.Errorf("cursor RenderTranscript not implemented")
}

func (c *cursorHarness) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "skills", "flow", "SKILL.md"), nil
}

func (c *cursorHarness) SkillVersionPath() (string, error) {
	p, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "VERSION"), nil
}

func (c *cursorHarness) InstallSkill(content []byte) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0o644)
}

func (c *cursorHarness) UninstallSkill() error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

func (c *cursorHarness) InstallSessionStartHook(command string) (bool, error)     { return false, nil }
func (c *cursorHarness) UninstallSessionStartHook(command string) (bool, error)   { return false, nil }
func (c *cursorHarness) InstallUserPromptSubmitHook(command string) (bool, error) { return false, nil }
func (c *cursorHarness) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return false, nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/harness/cursor/ -count=1`

- [ ] **Step 5: Commit** (only if user requested commits this session)

```bash
git add internal/harness/harness.go internal/harness/cursor/
git commit -m "$(cat <<'EOF'
feat(harness): add thin cursor adapter identity and no-ops

EOF
)"
```

---

### Task 2: Cursor transcript path + decoder

**Files:**
- Create: `internal/harness/cursor/transcript.go`
- Modify: `internal/harness/cursor/cursor.go` (remove RenderTranscript stub)
- Create: `internal/harness/cursor/transcript_test.go`

**Path contract (verified on disk):**

- Encode cwd: strip leading `/`, replace `/` with `-` only (do **not** replace `.` or `_`).
  - `/Users/Swapnil/workspace/swapnil/flow` → `Users-Swapnil-workspace-swapnil-flow`
- Deterministic: `~/.cursor/projects/<encoded>/agent-transcripts/<sid>/<sid>.jsonl`
- Fallback: glob `~/.cursor/projects/*/agent-transcripts/<sid>/<sid>.jsonl`, prefer newest mtime

**JSONL sample:**

```json
{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"pong"}]}}
{"type":"turn_ended","status":"success"}
```

- [ ] **Step 1: Write failing tests**

```go
package cursor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeProjectDir(t *testing.T) {
	got := EncodeProjectDir("/Users/Swapnil/workspace/swapnil/flow")
	want := "Users-Swapnil-workspace-swapnil-flow"
	if got != want {
		t.Fatalf("EncodeProjectDir = %q, want %q", got, want)
	}
}

func TestRenderJSONL(t *testing.T) {
	in := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"pong"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
		`garbage`,
	}, "\n")
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(in), false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "user:") || !strings.Contains(out, "hi") {
		t.Fatalf("missing user turn: %s", out)
	}
	if !strings.Contains(out, "assistant:") || !strings.Contains(out, "pong") {
		t.Fatalf("missing assistant turn: %s", out)
	}
}

func TestRenderTranscriptResolvesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/Users/me/proj"
	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".cursor", "projects", EncodeProjectDir(cwd), "agent-transcripts", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","message":{"content":[{"type":"text","text":"bound"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := New().RenderTranscript(cwd, sid, false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "bound") {
		t.Fatalf("got %q", buf.String())
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/harness/cursor/ -run 'Encode|Render' -count=1`

- [ ] **Step 3: Implement `transcript.go`**

```go
package cursor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EncodeProjectDir maps an absolute cwd to Cursor's projects folder name.
func EncodeProjectDir(cwd string) string {
	cwd = filepath.Clean(cwd)
	if strings.HasPrefix(cwd, string(filepath.Separator)) {
		cwd = cwd[1:]
	}
	return strings.ReplaceAll(cwd, string(filepath.Separator), "-")
}

func (c *cursorHarness) RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home dir: %w", err)
	}
	p, err := resolveTranscriptPath(home, cwd, sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open cursor transcript %s: %w", p, err)
	}
	defer f.Close()
	return RenderJSONL(f, compact, w)
}

func resolveTranscriptPath(home, cwd, sessionID string) (string, error) {
	projects := filepath.Join(home, ".cursor", "projects")
	det := filepath.Join(projects, EncodeProjectDir(cwd), "agent-transcripts", sessionID, sessionID+".jsonl")
	if _, err := os.Stat(det); err == nil {
		return det, nil
	}
	pattern := filepath.Join(projects, "*", "agent-transcripts", sessionID, sessionID+".jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return "", fmt.Errorf("cursor transcript not found for session %s under %s", sessionID, projects)
	}
	newest, newestMod := matches[0], int64(-1)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().UnixNano() > newestMod {
			newest, newestMod = m, fi.ModTime().UnixNano()
		}
	}
	return newest, nil
}

// RenderJSONL writes a readable transcript. compact is ignored in MVP
// (no separate thinking/tool-result blocks in the sample schema).
func RenderJSONL(r io.Reader, compact bool, w io.Writer) error {
	_ = compact
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		if _, ok := raw["type"]; ok {
			if _, hasRole := raw["role"]; !hasRole {
				continue // turn_ended etc.
			}
		}
		var role string
		_ = json.Unmarshal(raw["role"], &role)
		if role == "" {
			continue
		}
		text := extractText(raw["message"])
		if text == "" {
			continue
		}
		fmt.Fprintf(w, "%s:\n%s\n\n", role, text)
	}
	return sc.Err()
}

func extractText(msg json.RawMessage) string {
	if len(msg) == 0 {
		return ""
	}
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/harness/cursor/ -count=1`

- [ ] **Step 5: Commit** (if requested)

```bash
git add internal/harness/cursor/
git commit -m "$(cat <<'EOF'
feat(harness/cursor): render Agents Window transcripts

EOF
)"
```

---

### Task 3: Register harness + spawn gate in `flow do`

**Files:**
- Modify: `internal/app/harness.go`
- Modify: `internal/app/do.go`
- Create: `internal/app/do_cursor_test.go`

- [ ] **Step 1: Write failing tests**

Follow existing `do_test.go` temp `FLOW_ROOT` / `$HOME` / task-insert helpers.

```go
func TestDoSpawnRefusesCursorHarness(t *testing.T) {
	// setup temp flow root + task "cursor-spawn-task" with work_dir
	t.Setenv("CURSOR_CONVERSATION_ID", "627189e8-5e30-424b-bf68-44301c4e201f")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	rc := cmdDo([]string{"cursor-spawn-task"})
	if rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
}

func TestDoHereBindsCursorHarness(t *testing.T) {
	t.Setenv("CURSOR_CONVERSATION_ID", "627189e8-5e30-424b-bf68-44301c4e201f")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	rc := cmdDo([]string{"--here", "cursor-here-task"})
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	task := mustLoadTask(t, "cursor-here-task")
	if !task.Harness.Valid || task.Harness.String != "cursor" {
		t.Fatalf("harness=%v, want cursor", task.Harness)
	}
	if task.SessionID.String != "627189e8-5e30-424b-bf68-44301c4e201f" {
		t.Fatalf("session_id=%v", task.SessionID)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/app/ -run 'Cursor' -count=1`

- [ ] **Step 3: Implement**

`internal/app/harness.go`:

```go
import "flow/internal/harness/cursor"

func allHarnesses() []harness.Harness {
	return []harness.Harness{
		claude.New(),
		cursor.New(),
	}
}
```

In `cmdDo` spawn path, immediately after `harnessForSpawn` succeeds:

```go
if h.Name() == harness.NameCursor {
	fmt.Fprintln(os.Stderr,
		"error: cursor Agents Window sessions cannot be spawned by flow — open a chat in Agents Window, then run: flow do --here <slug>")
	return 1
}
```

Also refuse `--auto` and `$FLOW_TERM=bg` when harness is cursor (same or clearer “unsupported” message) before those branches proceed.

Update the `--here` flag help string to harness-agnostic wording (drop “Claude Code session” exclusivity).

- [ ] **Step 4: Run**

```bash
go test ./internal/app/ -run 'Cursor|Here' -count=1
go test ./... -count=1
```

- [ ] **Step 5: Commit** (if requested)

```bash
git add internal/app/harness.go internal/app/do.go internal/app/do_cursor_test.go
git commit -m "$(cat <<'EOF'
feat(app): wire cursor harness and refuse Agents Window spawn

EOF
)"
```

---

### Task 4: Skill install hygiene

**Files:**
- Modify: `internal/harness/cursor/cursor.go`
- Modify: `internal/harness/cursor/cursor_test.go`

- [ ] **Step 1: Failing test**

```go
func TestInstallSkillWritesCursorPathAndRemovesAgentsCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, ".agents", "skills", "flow")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "SKILL.md"), []byte("old claude-oriented"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New()
	if err := h.InstallSkill([]byte("# cursor skill\n")); err != nil {
		t.Fatal(err)
	}
	p, _ := h.SkillInstallPath()
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "cursor skill") {
		t.Fatalf("cursor skill missing: %v %s", err, b)
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Fatalf("stale ~/.agents/skills/flow still present: %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement**

At end of `InstallSkill`, after writing the Cursor skill file:

```go
func removeStaleAgentsFlowSkill() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".agents", "skills", "flow")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}
```

`UninstallSkill` only removes `~/.cursor/skills/flow`.

- [ ] **Step 4: PASS** — `go test ./internal/harness/cursor/ -count=1`

- [ ] **Step 5: Commit** (if requested)

---

### Task 5: Cursor-flavored embedded skill + selection

**Files:**
- Create: `internal/app/skill/cursor/SKILL.md`
- Modify: `internal/app/skill.go`
- Modify: `internal/app/init.go`
- Modify: `internal/app/skill_test.go`

**Content requirements for `skill/cursor/SKILL.md`:**

Copy `internal/app/skill/SKILL.md`, then surgically change:

1. Frontmatter: Cursor Agents Window + flow CLI.
2. Session model: default start = open Agents Window → `flow do --here`. No spawn-as-default.
3. §4.7: distill KB/project update **before** `flow done`; binary sweep is a no-op under cursor.
4. Explicitly unsupported: spawn `flow do`, `--auto`, owners, SessionStart/UserPromptSubmit hooks.
5. Keep scoop §4.10 and intake recipes.
6. Binding mechanics reference `$CURSOR_CONVERSATION_ID` where relevant.

- [ ] **Step 1: Failing tests**

In `skill.go`:

```go
//go:embed skill/SKILL.md
var embeddedSkill []byte

//go:embed skill/cursor/SKILL.md
var embeddedCursorSkill []byte

func skillContentFor(h harness.Harness) []byte {
	if h != nil && h.Name() == harness.NameCursor {
		return embeddedCursorSkill
	}
	return embeddedSkill
}
```

Tests:

```go
func TestCursorSkillMentionsBindHereAndAgentsWindow(t *testing.T) {
	got := string(embeddedCursorSkill)
	for _, want := range []string{"Agents Window", "flow do --here", "CURSOR_CONVERSATION_ID"} {
		if !strings.Contains(got, want) {
			t.Errorf("cursor skill missing %q", want)
		}
	}
}

func TestSkillInstallUsesCursorContentWhenAmbient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_CONVERSATION_ID", "627189e8-5e30-424b-bf68-44301c4e201f")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := cmdSkill([]string{"install", "--skip-hook"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	b, err := os.ReadFile(filepath.Join(home, ".cursor", "skills", "flow", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Agents Window") {
		t.Fatalf("installed skill is not cursor-flavored")
	}
}
```

- [ ] **Step 2: FAIL** until skill file + wiring exist

- [ ] **Step 3: Implement skill file; replace `InstallSkill(embeddedSkill)` with `InstallSkill(skillContentFor(h))` in `skill.go` and `init.go`**

- [ ] **Step 4:**

```bash
go test ./internal/app/ -count=1
go test ./... -count=1
```

- [ ] **Step 5: Commit** (if requested)

```bash
git add internal/app/skill.go internal/app/init.go internal/app/skill/cursor/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): install Cursor-flavored flow skill for Agents Window

EOF
)"
```

---

### Task 6: Smoke + flow update note

- [ ] **Step 1: Build and smoke**

```bash
make build
make test
# In Agents Window (or with env set):
# flow do --here cursor-flow-mvp
# flow show task
# flow skill update --skip-hook
# confirm ~/.cursor/skills/flow/SKILL.md exists and ~/.agents/skills/flow is gone
```

- [ ] **Step 2: Write progress note** to `~/.flow/tasks/cursor-flow-mvp/updates/YYYY-MM-DD.md`

- [ ] **Step 3: Commit** any remaining repo changes (if requested). Do not commit `~/.flow`.

---

## Spec coverage checklist

| Spec requirement | Task |
|---|---|
| Thin cursor harness registered | 1, 3 |
| `CURSOR_CONVERSATION_ID` / `--here` bind | 1, 3 |
| Refuse spawn | 3 |
| `SkipPermissionsRun` no-op (option B) | 1 |
| Hooks no-op | 1 |
| Render Agents Window transcripts | 2 |
| Cursor skill install path | 1, 4, 5 |
| Remove `~/.agents/skills/flow` | 4 |
| In-session close-out in skill | 5 |
| Scoop unchanged | 5 |
| No Agents Window UI e2e | 1–5 |

## Plan self-review

- No `--no-sweep` flag — option B only.
- `EncodeProjectDir` deliberately differs from Claude `EncodeCwd`.
- Spawn / `--auto` / bg gated for cursor in Task 3.
- Dual embed keeps Claude installs intact when ambient is Claude.

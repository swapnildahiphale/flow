package app

import (
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

type fakeHarness struct {
	name   harness.Name
	envVar string
	binary string
}

func (f fakeHarness) Name() harness.Name { return f.name }
func (f fakeHarness) Binary() string {
	if f.binary != "" {
		return f.binary
	}
	return string(f.name)
}
func (f fakeHarness) SessionIDEnvVar() string              { return f.envVar }
func (f fakeHarness) ValidateSessionID(string) error       { return nil }
func (f fakeHarness) ValidateSession(string, string) error { return nil }
func (f fakeHarness) PrepareFreshSession(harness.SessionContext, string, harness.LaunchOpts) (harness.PreparedSession, error) {
	return harness.PreparedSession{SessionID: "fake-session", LaunchCommand: "fake resume"}, nil
}
func (f fakeHarness) BootstrapFreshSession(harness.SessionContext, string, string, harness.LaunchOpts) error {
	return nil
}
func (f fakeHarness) ResumeCmd(string, harness.LaunchOpts) string                       { return "fake resume" }
func (f fakeHarness) SkipPermissionsRun(harness.SessionContext, string) error           { return nil }
func (f fakeHarness) LiveSessionIDs() (map[string]int, error)                           { return map[string]int{}, nil }
func (f fakeHarness) RenderTranscript(string, string, bool, time.Time, io.Writer) error { return nil }
func (f fakeHarness) SkillInstallPath() (string, error)                                 { return "", nil }
func (f fakeHarness) SkillVersionPath() (string, error)                                 { return "", nil }
func (f fakeHarness) InstallSkill([]byte) error                                         { return nil }
func (f fakeHarness) UninstallSkill() error                                             { return nil }
func (f fakeHarness) InstallSessionStartHook(string) (bool, error)                      { return false, nil }
func (f fakeHarness) UninstallSessionStartHook(string) (bool, error)                    { return false, nil }
func (f fakeHarness) UninstallUserPromptSubmitHook(string) (bool, error)                { return false, nil }

func withHarnessRegistry(t *testing.T, hs ...harness.Harness) {
	t.Helper()
	old := harnessRegistry
	harnessRegistry = func() []harness.Harness { return hs }
	t.Cleanup(func() { harnessRegistry = old })
}

// TestAmbientHarness covers the env-var probe: returns the matching
// harness when its session-id env var is set, nil otherwise.
func TestAmbientHarness(t *testing.T) {
	// Unset every known harness env var so we control the starting state.
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	if got := ambientHarness(); got != nil {
		t.Errorf("ambientHarness with no env set = %v, want nil", got)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	got := ambientHarness()
	if got == nil {
		t.Fatal("ambientHarness with $CLAUDE_CODE_SESSION_ID set = nil, want claude")
	}
	if got.Name() != harness.NameClaude {
		t.Errorf("ambientHarness = %v, want claude", got.Name())
	}
}

func TestParseHarnessName(t *testing.T) {
	cases := []struct {
		raw  string
		want harness.Name
	}{
		{"", ""},
		{"auto", ""},
		{"claude", harness.NameClaude},
		{"codex", harness.NameCodex},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseHarnessName(tc.raw)
			if err != nil {
				t.Fatalf("parseHarnessName(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseHarnessName(%q)=%q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseHarnessNameRejectsUnknown(t *testing.T) {
	if _, err := parseHarnessName("wat"); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("parseHarnessName(wat) err=%v, want unknown harness", err)
	}
}

// TestHarnessForTask covers the column → adapter lookup. NULL and
// empty resolve to claude+nil error (back-compat). Unknown
// non-empty names resolve to nil+error so callers refuse rather
// than silently coerce.
func TestHarnessForTask(t *testing.T) {
	cases := []struct {
		name    string
		harness sql.NullString
		want    harness.Name
		wantErr bool
	}{
		{"null column → claude", sql.NullString{}, harness.NameClaude, false},
		{"empty string → claude", sql.NullString{Valid: true, String: ""}, harness.NameClaude, false},
		{"claude pin", sql.NullString{Valid: true, String: "claude"}, harness.NameClaude, false},
		{"unknown name → error", sql.NullString{Valid: true, String: "future"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &flowdb.Task{Harness: tc.harness}
			h, err := harnessForTask(task)
			if tc.wantErr {
				if err == nil {
					t.Errorf("got nil error, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := h.Name(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCmdDoPersistsHarnessOnBootstrap pins the contract: the first
// `flow do` on a previously-unbound task writes the chosen harness
// AND the session_cwd to the tasks row atomically with session_id.
// Future `flow do` invocations read both back to look up the right
// adapter and find the transcript on disk.
func TestCmdDoPersistsHarnessOnBootstrap(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "harness-bootstrap")
	_, _ = stubITerm(t)

	// Clean env so ambient detection falls back to claude default.
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	if rc := cmdDo([]string{"harness-bootstrap"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "harness-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("task.harness after bootstrap = %+v, want claude", task.Harness)
	}
}

func TestCmdDoExplicitClaudePersistsHarnessOnBootstrap(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "harness-explicit-claude")
	_, _ = stubITerm(t)

	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	if rc := cmdDo([]string{"harness-explicit-claude", "--harness", "claude"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "harness-explicit-claude")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("task.harness after bootstrap = %+v, want claude", task.Harness)
	}
}

func TestCmdDoRejectsUnknownHarnessFlag(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "harness-wat")
	count, _ := stubITerm(t)

	stderr := captureStderr(t)
	rc := cmdDo([]string{"harness-wat", "--harness", "wat"})
	if rc != 2 {
		t.Fatalf("cmdDo rc=%d, want 2", rc)
	}
	got := stderr()
	if !strings.Contains(got, "unknown harness") {
		t.Fatalf("stderr=%q", got)
	}
	if *count != 0 {
		t.Fatalf("spawn count=%d, want 0", *count)
	}
}

func TestCmdDoRejectsExtraTrailingArgument(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "extra-arg-task")
	count, _ := stubITerm(t)

	stderr := captureStderr(t)
	rc := cmdDo([]string{"extra-arg-task", "--harness", "claude", "surprise"})
	if rc != 2 {
		t.Fatalf("cmdDo rc=%d, want 2", rc)
	}
	got := stderr()
	if !strings.Contains(got, "unexpected argument") {
		t.Fatalf("stderr=%q", got)
	}
	if *count != 0 {
		t.Fatalf("spawn count=%d, want 0", *count)
	}
}

func TestCmdDoExplicitCodexFailsBeforeSessionAllocation(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "harness-codex")
	count, _ := stubITerm(t)

	oldNewUUID := claude.NewUUID
	claude.NewUUID = func() (string, error) {
		t.Fatal("claude.NewUUID should not be called for unsupported explicit codex")
		return "", nil
	}
	t.Cleanup(func() { claude.NewUUID = oldNewUUID })

	stderr := captureStderr(t)
	rc := cmdDo([]string{"harness-codex", "--harness", "codex"})
	if rc == 0 {
		t.Fatalf("cmdDo rc=%d, want non-zero", rc)
	}
	got := stderr()
	if !strings.Contains(got, "codex") || !strings.Contains(got, "isn't supported") {
		t.Fatalf("stderr=%q", got)
	}
	if *count != 0 {
		t.Fatalf("spawn count=%d, want 0", *count)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "harness-codex")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.Valid {
		t.Fatalf("session_id=%+v, want NULL", task.SessionID)
	}
	if task.Harness.Valid && task.Harness.String != "" {
		t.Fatalf("harness=%+v, want NULL/empty", task.Harness)
	}
}

// TestCmdDoRefusesUnsupportedHarnessPin pins the two
// related contracts:
//
//  1. A task pinned to a harness this binary doesn't register
//     must NOT spawn as the silent claude fallback. cmdDo returns
//     an error mentioning the unsupported name and the registered
//     alternatives — better that the user sees the issue
//     explicitly than that we silently run the wrong adapter.
//
//  2. The pin survives the refusal. cmdDo doesn't UPDATE the
//     harness column when it can't resolve the pin, so a future
//     binary that DOES register the pinned harness can still pick
//     up the task untouched.
//
// Together: today's binary refuses, tomorrow's binary works,
// downgrade is safe.
func TestCmdDoRefusesUnsupportedHarnessPin(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "future-pin")
	_, _ = stubITerm(t)

	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	// Simulate a future build having pinned the task.
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='future-pin'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	stderr := captureStderr(t)
	rc := cmdDo([]string{"future-pin"})
	if rc == 0 {
		t.Fatalf("cmdDo rc=%d, want non-zero (unsupported pin should refuse)", rc)
	}
	got := stderr()
	if !strings.Contains(got, "codex") || !strings.Contains(got, "isn't supported") {
		t.Errorf("stderr should name the unsupported harness; got:\n%s", got)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "future-pin")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "codex" {
		t.Errorf("refusal should preserve the pin; got %q, want codex",
			task.Harness.String)
	}
	if task.SessionID.Valid {
		t.Errorf("refusal should not allocate a session_id; got %+v", task.SessionID)
	}
}

// TestCmdDoHerePersistsHarnessColumn pins that --here writes the
// harness column on bind, alongside session_id. (Pre-invariant
// versions also captured session_cwd here; that column is gone now
// — the invariant `session_id != NULL ⟹ work_dir == cwd-of-session`
// makes recording the cwd separately redundant.)
func TestCmdDoHerePersistsHarnessColumn(t *testing.T) {
	setupFlowRoot(t)
	// Seed at the test process's cwd + stub the on-disk
	// validation so this test isolates the harness/session_id
	// writes from the invariant check.
	seedTaskAtCwd(t, "here-harness")
	stubClaudeStatOK(t)

	const sid = "11111111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	if rc := cmdDoHere("here-harness", false, ""); rc != 0 {
		t.Fatalf("cmdDoHere rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "here-harness")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("task.harness after --here = %+v, want claude", task.Harness)
	}
	if task.SessionID.String != sid {
		t.Errorf("task.session_id after --here = %q, want %s", task.SessionID.String, sid)
	}
}

// TestCmdDoHereRejectsCrossHarness pins the safety rail: a task
// pinned to harness X can't be --here-bound from a session of
// harness Y without --force. The check is purely string-based on
// the harness column, so we can exercise it without a second
// adapter implementation — just set the column directly.
func TestCmdDoHereRejectsCrossHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "pinned-elsewhere")

	// Pin the task to a fictional "codex" harness by writing
	// directly to the column.
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='pinned-elsewhere'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Ambient is claude.
	const sid = "11111111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	stderr := captureStderr(t)
	rc := cmdDoHere("pinned-elsewhere", false, "")
	if rc != 1 {
		t.Errorf("cmdDoHere across harnesses rc=%d, want 1", rc)
	}
	got := stderr()
	if !strings.Contains(got, "pinned to harness") {
		t.Errorf("stderr should explain harness mismatch; got:\n%s", got)
	}

	// Task should be unchanged.
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "pinned-elsewhere")
	if task.SessionID.Valid {
		t.Errorf("rejected --here should not touch session_id; got %v", task.SessionID)
	}
	if task.Harness.String != "codex" {
		t.Errorf("rejected --here should not touch harness column; got %q", task.Harness.String)
	}
}

// TestCmdDoHereForceSwitchesHarness pins the --force escape: when
// the user explicitly asks, we switch the task's harness pinning
// alongside the session_id rebind. The old harness's transcript
// stays on disk but flow no longer tracks it.
func TestCmdDoHereForceSwitchesHarness(t *testing.T) {
	setupFlowRoot(t)
	// Cross-harness --force still has to satisfy the
	// cwd==work_dir invariant; seed at the test process's cwd
	// + stub the on-disk validation so the harness-switch is
	// the only thing --force is overriding here.
	seedTaskAtCwd(t, "force-switch")
	stubClaudeStatOK(t)

	// Pre-pin to "codex" with an existing session_id.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET harness='codex', session_id='old-codex-sid', session_started=? WHERE slug='force-switch'`,
		flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Ambient: claude.
	const sid = "22222222-3333-4444-8555-666666666666"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	if rc := cmdDoHere("force-switch", true, ""); rc != 0 {
		t.Fatalf("cmdDoHere --force rc=%d, want 0", rc)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "force-switch")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "claude" {
		t.Errorf("task.harness after --force switch = %q, want claude", task.Harness.String)
	}
	if task.SessionID.String != sid {
		t.Errorf("task.session_id after --force switch = %q, want %s", task.SessionID.String, sid)
	}
}

// TestHarnessForSpawn_AllPathsLandOnClaudeToday smoke-tests every
// branch of harnessForSpawn under the single-harness registry
// (claude is the only adapter today). Each of the three branches
// — null pin + no ambient, null pin + claude ambient, claude pin —
// lands on claude, which is the only assertion the test can make
// without a second registered adapter.
//
// The actual *precedence* properties (pinned > ambient > default)
// can't be exercised here: when ambient is claude and pin is empty,
// "matches ambient" and "fell through to default" are
// indistinguishable. Add real coverage once codex/gemini registers
// and a non-claude ambient is possible.
func TestHarnessForSpawn_AllPathsLandOnClaudeToday(t *testing.T) {
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	mustResolve := func(t *testing.T, task *flowdb.Task) harness.Harness {
		t.Helper()
		h, err := harnessForSpawn(task, "", false)
		if err != nil {
			t.Fatalf("harnessForSpawn: unexpected error: %v", err)
		}
		return h
	}

	// Branch 1: ambient nil + null pin → claude (fallback).
	task := &flowdb.Task{}
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("no ambient + null pin = %v, want claude", got)
	}

	// Branch 2: claude ambient + null pin → claude. Doesn't prove
	// ambient is *preferred over* fallback (same answer either way);
	// only proves the branch doesn't error.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("ambient claude + null pin = %v, want claude", got)
	}

	// Branch 3: claude pin → claude. Doesn't prove pinned is
	// *preferred over* ambient (same as above).
	task.Harness = sql.NullString{Valid: true, String: "claude"}
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("pinned claude = %v, want claude", got)
	}

	// Sanity: returned value satisfies harness.Harness.
	if _, ok := mustResolve(t, task).(harness.Harness); !ok {
		t.Error("harnessForSpawn return doesn't satisfy harness.Harness")
	}
	_ = claude.New() // import-keep
}

func TestHarnessForSpawnExplicitClaudeOnUnpinnedTask(t *testing.T) {
	task := &flowdb.Task{Slug: "t"}
	h, err := harnessForSpawn(task, harness.NameClaude, false)
	if err != nil {
		t.Fatalf("harnessForSpawn: %v", err)
	}
	if h.Name() != harness.NameClaude {
		t.Fatalf("got %s, want claude", h.Name())
	}
}

func TestHarnessForSpawnPinnedClaudeExplicitCodexWithoutFreshErrors(t *testing.T) {
	task := &flowdb.Task{
		Slug:    "t",
		Harness: sql.NullString{Valid: true, String: string(harness.NameClaude)},
	}
	_, err := harnessForSpawn(task, harness.NameCodex, false)
	if err == nil || !strings.Contains(err.Error(), "pass --fresh") {
		t.Fatalf("err=%v, want --fresh mismatch error", err)
	}
}

func TestHarnessForSpawnPinnedClaudeExplicitCodexWithFreshResolvesExplicit(t *testing.T) {
	fakeCodex := fakeHarness{name: harness.NameCodex, envVar: "CODEX_THREAD_ID"}
	withHarnessRegistry(t, claude.New(), fakeCodex)
	task := &flowdb.Task{
		Slug:    "t",
		Harness: sql.NullString{Valid: true, String: string(harness.NameClaude)},
	}
	h, err := harnessForSpawn(task, harness.NameCodex, true)
	if err != nil {
		t.Fatalf("harnessForSpawn: %v", err)
	}
	if h.Name() != harness.NameCodex {
		t.Fatalf("got %s, want codex", h.Name())
	}
}

func TestHarnessForSpawnExplicitCodexUnregisteredErrors(t *testing.T) {
	_, err := harnessForSpawn(&flowdb.Task{Slug: "t"}, harness.NameCodex, false)
	if err == nil || !strings.Contains(err.Error(), "codex") || !strings.Contains(err.Error(), "isn't supported") {
		t.Fatalf("err=%v, want unsupported codex", err)
	}
}

func TestHarnessForSpawnAmbiguousAmbientRequiresExplicit(t *testing.T) {
	fakeCodex := fakeHarness{name: harness.NameCodex, envVar: "CODEX_THREAD_ID"}
	withHarnessRegistry(t, claude.New(), fakeCodex)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	t.Setenv("CODEX_THREAD_ID", "018f3f8e-97f7-7cc2-a871-bfbfd8f4fd40")
	_, err := harnessForSpawn(&flowdb.Task{Slug: "t"}, "", false)
	if err == nil || !strings.Contains(err.Error(), "multiple harness session env vars") {
		t.Fatalf("err=%v", err)
	}
}

func TestCmdDoHereExplicitHarnessRequiresMatchingEnv(t *testing.T) {
	setupFlowRoot(t)
	seedTaskAtCwd(t, "here-explicit")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	stderr := captureStderr(t)
	rc := cmdDoHere("here-explicit", false, harness.NameClaude)
	if rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
	got := stderr()
	if !strings.Contains(got, "--here with --harness claude requires $CLAUDE_CODE_SESSION_ID") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestCmdDoHereAmbiguousAmbientRequiresExplicit(t *testing.T) {
	setupFlowRoot(t)
	seedTaskAtCwd(t, "here-ambiguous")
	fakeCodex := fakeHarness{name: harness.NameCodex, envVar: "CODEX_THREAD_ID"}
	withHarnessRegistry(t, claude.New(), fakeCodex)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "11111111-2222-4333-8444-555555555555")
	t.Setenv("CODEX_THREAD_ID", "thread-1")

	stderr := captureStderr(t)
	rc := cmdDoHere("here-ambiguous", false, "")
	if rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
	got := stderr()
	if !strings.Contains(got, "multiple harness session env vars") || !strings.Contains(got, "pass --harness") {
		t.Fatalf("stderr=%q", got)
	}
}

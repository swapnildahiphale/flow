package codex

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flow/internal/harness"
)

const testThreadID = "018f3f8e-97f7-7cc2-a871-bfbfd8f4fd40"

func stubCommandRunner(t *testing.T, fn Runner) {
	t.Helper()
	old := CommandRunner
	CommandRunner = fn
	t.Cleanup(func() { CommandRunner = old })
}

func TestIdentity(t *testing.T) {
	h := New()
	if h.Name() != harness.NameCodex {
		t.Fatalf("Name=%q, want %q", h.Name(), harness.NameCodex)
	}
	if h.Binary() != "codex" {
		t.Fatalf("Binary=%q, want codex", h.Binary())
	}
	if h.SessionIDEnvVar() != "CODEX_THREAD_ID" {
		t.Fatalf("SessionIDEnvVar=%q, want CODEX_THREAD_ID", h.SessionIDEnvVar())
	}
}

func TestValidateSessionIDAcceptsUUIDShapeWithoutV4Restriction(t *testing.T) {
	h := New()
	for _, id := range []string{
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d",
		testThreadID,
		"018F3F8E-97F7-7CC2-A871-BFBFD8F4FD40",
	} {
		if err := h.ValidateSessionID(id); err != nil {
			t.Fatalf("ValidateSessionID(%q): %v", id, err)
		}
	}

	for _, id := range []string{"", "not-a-uuid", "018f3f8e-97f7-7cc2-a871-bfbfd8f4fd4"} {
		if err := h.ValidateSessionID(id); err == nil {
			t.Fatalf("ValidateSessionID(%q)=nil, want error", id)
		}
	}
}

func TestValidateSessionAlwaysSucceeds(t *testing.T) {
	if err := New().ValidateSession("/tmp/elsewhere", testThreadID); err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
}

func TestPrepareFreshSessionParsesThreadStarted(t *testing.T) {
	var gotCtx harness.SessionContext
	var gotArgs []string
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		gotCtx = ctx
		gotArgs = append([]string(nil), args...)
		return []byte(`{"type":"noise"}` + "\n" + `{"type":"thread.started","thread":{"thread_id":"` + testThreadID + `"}}` + "\n"), nil
	})

	ctx := harness.SessionContext{WorkDir: "/tmp/work", Env: []string{"FLOW_ROOT=/tmp/flow-root"}}
	got, err := New().PrepareFreshSession(ctx, "bootstrap prompt should be ignored for allocation", harness.LaunchOpts{})
	if err != nil {
		t.Fatalf("PrepareFreshSession: %v", err)
	}

	wantArgs := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		allocationPrompt,
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("args=%q, want %q", gotArgs, wantArgs)
	}
	if gotCtx.WorkDir != ctx.WorkDir || !slices.Equal(gotCtx.Env, ctx.Env) {
		t.Fatalf("ctx=%+v, want %+v", gotCtx, ctx)
	}
	if got.SessionID != testThreadID {
		t.Fatalf("SessionID=%q, want %q", got.SessionID, testThreadID)
	}
	if got.LaunchCommand != "codex resume "+testThreadID {
		t.Fatalf("LaunchCommand=%q", got.LaunchCommand)
	}
}

func TestPrepareFreshSessionDangerousFlagAndRunnerErrorWins(t *testing.T) {
	var gotArgs []string
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{"type":"thread.started","thread":{"thread_id":"` + testThreadID + `"}}` + "\n"), errors.New("codex failed")
	})

	_, err := New().PrepareFreshSession(harness.SessionContext{}, "prompt", harness.LaunchOpts{SkipPermissions: true})
	if err == nil || !strings.Contains(err.Error(), "codex allocate thread") || !strings.Contains(err.Error(), "codex failed") {
		t.Fatalf("err=%v, want contextual runner error", err)
	}
	wantArgs := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		allocationPrompt,
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("args=%q, want %q", gotArgs, wantArgs)
	}
}

func TestPrepareFreshSessionMissingThreadStarted(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return []byte(`{"type":"message","text":"hello"}` + "\n"), nil
	})
	_, err := New().PrepareFreshSession(harness.SessionContext{}, "prompt", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "thread.started") {
		t.Fatalf("err=%v, want missing thread.started error", err)
	}
}

func TestPrepareFreshSessionMalformedJSON(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return []byte(`{"type":"thread.started"`), nil
	})
	_, err := New().PrepareFreshSession(harness.SessionContext{}, "prompt", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "parse codex json") {
		t.Fatalf("err=%v, want malformed JSON error", err)
	}
}

func TestPrepareFreshSessionMalformedJSONAfterThreadStarted(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return []byte(`{"type":"thread.started","thread":{"thread_id":"` + testThreadID + `"}}` + "\n" +
			`{"type":"broken"`), nil
	})
	_, err := New().PrepareFreshSession(harness.SessionContext{}, "prompt", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "parse codex json") {
		t.Fatalf("err=%v, want malformed JSON error", err)
	}
}

func TestPrepareFreshSessionRejectsInvalidThreadID(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return []byte(`{"type":"thread.started","thread":{"thread_id":"not-a-uuid"}}` + "\n"), nil
	})
	_, err := New().PrepareFreshSession(harness.SessionContext{}, "prompt", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "not a valid codex thread UUID") {
		t.Fatalf("err=%v, want invalid thread id error", err)
	}
}

func TestBootstrapFreshSessionArgsAndInjection(t *testing.T) {
	var gotArgs []string
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return nil, nil
	})

	err := New().BootstrapFreshSession(
		harness.SessionContext{WorkDir: "/tmp/work"},
		testThreadID,
		"bootstrap",
		harness.LaunchOpts{SkipPermissions: true, Inject: "extra"},
	)
	if err != nil {
		t.Fatalf("BootstrapFreshSession: %v", err)
	}
	wantPrompt := "bootstrap\n\n" + harness.InjectionMarker + "\nextra"
	wantArgs := []string{
		"exec",
		"resume",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		testThreadID,
		wantPrompt,
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("args=%q, want %q", gotArgs, wantArgs)
	}
}

func TestBootstrapFreshSessionWrapsRunnerError(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return nil, errors.New("runner failed")
	})
	err := New().BootstrapFreshSession(harness.SessionContext{}, testThreadID, "bootstrap", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "codex bootstrap thread "+testThreadID) || !strings.Contains(err.Error(), "runner failed") {
		t.Fatalf("err=%v, want contextual runner error", err)
	}
}

func TestResumeCmdWithInjectionExecsThenResumes(t *testing.T) {
	got := New().ResumeCmd(testThreadID, harness.LaunchOpts{SkipPermissions: true, Inject: "follow up"})
	want := "codex exec resume --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox " +
		testThreadID + " '" + harness.InjectionMarker + "\nfollow up' && codex resume " + testThreadID
	if got != want {
		t.Fatalf("ResumeCmd=\n%q\nwant\n%q", got, want)
	}
}

func TestResumeCmdWithoutInjection(t *testing.T) {
	got := New().ResumeCmd(testThreadID, harness.LaunchOpts{})
	if got != "codex resume "+testThreadID {
		t.Fatalf("ResumeCmd=%q", got)
	}
}

func TestSkipPermissionsRunUsesCodexExecDangerously(t *testing.T) {
	var gotCtx harness.SessionContext
	var gotArgs []string
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		gotCtx = ctx
		gotArgs = append([]string(nil), args...)
		return nil, nil
	})

	ctx := harness.SessionContext{WorkDir: "/tmp/work", Env: []string{"FLOW_ROOT=/tmp/flow-root"}}
	if err := New().SkipPermissionsRun(ctx, "sweep"); err != nil {
		t.Fatalf("SkipPermissionsRun: %v", err)
	}
	wantArgs := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "sweep"}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("args=%q, want %q", gotArgs, wantArgs)
	}
	if gotCtx.WorkDir != ctx.WorkDir || !slices.Equal(gotCtx.Env, ctx.Env) {
		t.Fatalf("ctx=%+v, want %+v", gotCtx, ctx)
	}
}

func TestSkipPermissionsRunWrapsRunnerError(t *testing.T) {
	stubCommandRunner(t, func(ctx harness.SessionContext, args []string) ([]byte, error) {
		return nil, errors.New("runner failed")
	})
	err := New().SkipPermissionsRun(harness.SessionContext{}, "sweep")
	if err == nil || !strings.Contains(err.Error(), "codex close-out sweep") || !strings.Contains(err.Error(), "runner failed") {
		t.Fatalf("err=%v, want contextual runner error", err)
	}
}

func TestRunCodexAppliesContextAndCapturesOutput(t *testing.T) {
	binDir := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "record.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := `#!/bin/sh
printf '%s\n' "$PWD" > "$CODEX_RECORD"
printf '%s\n' "$FLOW_ROOT" >> "$CODEX_RECORD"
printf '%s\n' "$*" >> "$CODEX_RECORD"
if [ "$1" = "fail" ]; then
  echo "partial stdout"
  echo "bad stderr" >&2
  exit 7
fi
echo "ok stdout"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	ctx := harness.SessionContext{
		WorkDir: workDir,
		Env:     append(os.Environ(), "FLOW_ROOT=/tmp/flow-root", "CODEX_RECORD="+recordPath),
	}
	out, err := runCodex(ctx, []string{"one", "two"})
	if err != nil {
		t.Fatalf("runCodex success: %v", err)
	}
	if string(out) != "ok stdout\n" {
		t.Fatalf("stdout=%q", out)
	}
	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(record)), "\n")
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines, []string{resolvedWorkDir, "/tmp/flow-root", "one two"}) {
		t.Fatalf("record lines=%q", lines)
	}

	out, err = runCodex(ctx, []string{"fail"})
	if err == nil || !strings.Contains(err.Error(), "bad stderr") {
		t.Fatalf("err=%v, want stderr in error", err)
	}
	if string(out) != "partial stdout\n" {
		t.Fatalf("failure stdout=%q", out)
	}
}

func TestLiveSessionIDsParsesCodexResumeRows(t *testing.T) {
	old := PSRunner
	t.Cleanup(func() { PSRunner = old })
	PSRunner = func() ([]byte, error) {
		return []byte(`  PID COMMAND
1001 codex resume 018F3F8E-97F7-7CC2-A871-BFBFD8F4FD40
1002 codex exec resume --skip-git-repo-check 018f3f8e-97f7-7cc2-a871-bfbfd8f4fd40 'hello'
1003 /opt/homebrew/bin/codex exec resume --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox 11111111-2222-7333-8444-555555555555 'x' && codex resume 11111111-2222-7333-8444-555555555555
1004 codex exec --skip-git-repo-check 'not a resume'
1005 grep codex resume 99999999-9999-7999-8999-999999999999
`), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatalf("LiveSessionIDs: %v", err)
	}
	want := map[string]int{
		testThreadID:                           2,
		"11111111-2222-7333-8444-555555555555": 1,
	}
	if len(live) != len(want) {
		t.Fatalf("live=%#v, want %#v", live, want)
	}
	for id, n := range want {
		if live[id] != n {
			t.Fatalf("live[%q]=%d, want %d (all=%#v)", id, live[id], n, live)
		}
	}
}

func TestSkillInstallUninstallPaths(t *testing.T) {
	home := t.TempDir()
	oldHome := UserHomeDir
	UserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { UserHomeDir = oldHome })

	h := New()
	skillPath, err := h.SkillInstallPath()
	if err != nil {
		t.Fatalf("SkillInstallPath: %v", err)
	}
	wantSkill := filepath.Join(home, ".agents", "skills", "flow", "SKILL.md")
	if skillPath != wantSkill {
		t.Fatalf("SkillInstallPath=%q, want %q", skillPath, wantSkill)
	}
	versionPath, err := h.SkillVersionPath()
	if err != nil {
		t.Fatalf("SkillVersionPath: %v", err)
	}
	if versionPath != filepath.Join(home, ".agents", "skills", "flow", "VERSION") {
		t.Fatalf("SkillVersionPath=%q", versionPath)
	}
	if err := h.InstallSkill([]byte("skill body")); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	if got, err := os.ReadFile(skillPath); err != nil || string(got) != "skill body" {
		t.Fatalf("installed skill=(%q,%v)", got, err)
	}
	if err := os.WriteFile(versionPath, []byte("version"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if err := h.UninstallSkill(); err != nil {
		t.Fatalf("UninstallSkill: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(skillPath)); !os.IsNotExist(err) {
		t.Fatalf("skill dir still exists or unexpected err: %v", err)
	}
}

func TestHooksAndTranscriptUnsupportedForNow(t *testing.T) {
	h := New()
	if added, err := h.InstallSessionStartHook("flow hook session-start"); err == nil || added || !strings.Contains(err.Error(), "not wired yet") {
		t.Fatalf("InstallSessionStartHook=(%v,%v), want unsupported", added, err)
	}
	if removed, err := h.UninstallSessionStartHook("flow hook session-start"); err == nil || removed || !strings.Contains(err.Error(), "not wired yet") {
		t.Fatalf("UninstallSessionStartHook=(%v,%v), want unsupported", removed, err)
	}
	if removed, err := h.UninstallUserPromptSubmitHook("flow hook user-prompt-submit"); err == nil || removed || !strings.Contains(err.Error(), "not wired yet") {
		t.Fatalf("UninstallUserPromptSubmitHook=(%v,%v), want unsupported", removed, err)
	}
	var out strings.Builder
	if err := h.RenderTranscript("/tmp/work", testThreadID, false, time.Time{}, &out); err == nil || !strings.Contains(err.Error(), "not wired yet") {
		t.Fatalf("RenderTranscript err=%v, want unsupported", err)
	}
}

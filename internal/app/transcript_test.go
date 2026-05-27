package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/iterm"
)

// Parser-level tests live in internal/harness/claude/transcript_test.go
// (the claude jsonl format and the cutoff-filter behavior are owned
// by the harness impl). The tests here exercise the cmdTranscript
// wiring: ref resolution, the "no session" gate, the happy-path
// dispatch into the harness.

func TestTranscriptCmdNoSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)
	cmdAdd([]string{"task", "No Session Task", "--slug", "no-session"})

	rc := cmdTranscript([]string{"no-session"})
	if rc != 1 {
		t.Errorf("transcript with no session: rc=%d, want 1", rc)
	}
}

func TestTranscriptCmdNoRefNoHarnessEnvMessage(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	stderr := captureStderr(t)
	rc := cmdTranscript(nil)
	if rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	got := stderr()
	if !strings.Contains(got, "not running inside a known harness session") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestTranscriptCmdNoRefAmbiguousHarnessEnvMessage(t *testing.T) {
	setupFlowRoot(t)
	fakeCodex := fakeHarness{name: harness.NameCodex, envVar: "CODEX_THREAD_ID"}
	withHarnessRegistry(t, claude.New(), fakeCodex)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	t.Setenv("CODEX_THREAD_ID", "thread-1")

	stderr := captureStderr(t)
	rc := cmdTranscript(nil)
	if rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	got := stderr()
	if !strings.Contains(got, "multiple harness session env vars") || !strings.Contains(got, "pass a task ref explicitly") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestTranscriptCmdWithSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)

	repo := filepath.Join(tmp, "code", "myrepo")
	os.MkdirAll(repo, 0o755)
	cmdAdd([]string{"task", "Transcript Test", "--slug", "tx-test", "--work-dir", repo})

	// Simulate session bootstrap.
	dbPath, _ := flowDBPath()
	db, _ := flowdb.OpenDB(dbPath)
	defer db.Close()

	sid := "deadbeef-1234-5678-9abc-def012345678"
	now := flowdb.NowISO()
	db.Exec(`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
		sid, now, now, "tx-test")

	// Write a minimal jsonl at the claude convention path.
	encoded := claude.EncodeCwd(repo)
	sessionDir := filepath.Join(tmp, ".claude", "projects", encoded)
	os.MkdirAll(sessionDir, 0o755)
	os.WriteFile(
		filepath.Join(sessionDir, sid+".jsonl"),
		[]byte(`{"type":"user","message":{"role":"user","content":"test message"},"uuid":"u1","timestamp":"2026-04-12T10:00:00Z","sessionId":"`+sid+`"}`+"\n"),
		0o644,
	)

	rc := cmdTranscript([]string{"tx-test"})
	if rc != 0 {
		t.Errorf("transcript with session: rc=%d, want 0", rc)
	}
}

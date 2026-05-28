package app

import (
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/iterm"
	"flow/internal/spawner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFullRoundtrip exercises the full command surface in the order a
// user would hit it for a realistic session: init, add project, add task
// under the project, do (bootstrap + spawn), show both, list both, waiting
// set/clear, priority change, update file drop, done, archive, unarchive,
// workdir registry.
//
// Mocks claudeRunner and iterm.Runner so nothing actually spawns
// claude or osascript. Uses a temp FLOW_ROOT so the user's real ~/.flow is
// untouched.
func TestE2EFullRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	flowRoot := filepath.Join(tmp, "flow")
	t.Setenv("FLOW_ROOT", flowRoot)
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")

	// Fake repo that serves as the project's work_dir.
	repo := filepath.Join(tmp, "code", "budgeting-app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pin the spawner backend so a kitty/zellij/Terminal.app host does
	// not route SpawnTab to a real terminal CLI. Without this, running
	// the test inside kitty (KITTY_WINDOW_ID set) opens a real tab and
	// types the fixture command into the user's shell.
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendITerm
	t.Cleanup(func() { spawner.Override = oldOverride })

	// Stub osascript for the whole test.
	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	// Stub the headless claude runner so cmdDone doesn't try to invoke
	// the real claude CLI for its post-flip KB sweep.
	oldClaude := claude.SkipPermissionsRunner
	claude.SkipPermissionsRunner = func(ctx harness.SessionContext, prompt string) error { return nil }
	t.Cleanup(func() { claude.SkipPermissionsRunner = oldClaude })

	// Pin the UUID `flow do` allocates so downstream assertions can
	// reference a known session_id. In production this minted a random
	// v4 UUID written to tasks.session_id before the iTerm tab spawns
	// and passed to `claude --session-id`. Pinning lets us check that
	// known id survived end to end.
	const fixedSID = "e2e-session-uuid"
	oldNewUUID := claude.NewUUID
	claude.NewUUID = func() (string, error) { return fixedSID, nil }
	t.Cleanup(func() { claude.NewUUID = oldNewUUID })

	step := func(name string, rc int) {
		t.Helper()
		if rc != 0 {
			t.Fatalf("%s: rc=%d", name, rc)
		}
	}

	// 1. init — creates tree, db, installs skill
	step("init", cmdInit(nil))
	if _, err := os.Stat(filepath.Join(flowRoot, "flow.db")); err != nil {
		t.Fatalf("flow.db not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flowRoot, "projects")); err != nil {
		t.Fatalf("projects dir not created: %v", err)
	}

	// 2. add project
	step("add project", cmdAdd([]string{"project", "Budgeting App Revamp", "--work-dir", repo}))
	if _, err := os.Stat(filepath.Join(flowRoot, "projects", "budgeting-app-revamp", "brief.md")); err != nil {
		t.Fatalf("project brief.md not created: %v", err)
	}

	// 3. add task under the project
	step("add task", cmdAdd([]string{"task", "Fix Auth Token Expiry",
		"--project", "budgeting-app-revamp"}))
	taskDir := filepath.Join(flowRoot, "tasks", "fix-auth-token-expiry")
	if _, err := os.Stat(filepath.Join(taskDir, "brief.md")); err != nil {
		t.Fatalf("task brief.md not created: %v", err)
	}

	// 4. add a floating task (auto workspace)
	step("add floating task", cmdAdd([]string{"task", "Scratch Investigation"}))
	scratchDir := filepath.Join(flowRoot, "tasks", "scratch-investigation", "workspace")
	if _, err := os.Stat(scratchDir); err != nil {
		t.Fatalf("floating task workspace not created: %v", err)
	}

	// 5. do — pre-allocates the session UUID and spawns the tab. The
	// session_id lands in the DB synchronously; no self-registration
	// step is needed.
	step("do", cmdDo([]string{"fix-auth-token-expiry"}))
	db, err := flowdb.OpenDB(filepath.Join(flowRoot, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task, err := flowdb.GetTask(db, "fix-auth-token-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != fixedSID {
		t.Errorf("session_id after fresh spawn: got %+v, want %s", task.SessionID, fixedSID)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}

	// 5b. Write real jsonl content at the path claude would have used
	// given our pre-allocated session_id, so transcript can parse it.
	{
		encoded := claude.EncodeCwd(task.WorkDir)
		sessionDir := filepath.Join(tmp, ".claude", "projects", encoded)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sessionFile := filepath.Join(sessionDir, fixedSID+".jsonl")
		content := `{"type":"user","message":{"role":"user","content":"Hello"},"uuid":"u1","timestamp":"2026-04-12T10:00:00Z","sessionId":"` + fixedSID + `"}` + "\n" +
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]},"uuid":"a1","timestamp":"2026-04-12T10:00:01Z","sessionId":"` + fixedSID + `"}` + "\n"
		if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
			t.Fatalf("write session jsonl: %v", err)
		}
	}

	// 5c. transcript — should succeed now that session exists.
	step("transcript", cmdTranscript([]string{"fix-auth-token-expiry"}))

	// 6. do again — now session_id is populated, should spawn --resume.
	step("do resume", cmdDo([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.SessionID.String != fixedSID {
		t.Errorf("session_id should be preserved across resume: got %q", task.SessionID.String)
	}
	if !task.SessionLastResumed.Valid {
		t.Error("session_last_resumed should be set after resume")
	}

	// 7. show task
	step("show task", cmdShow([]string{"task", "fix-auth-token-expiry"}))

	// 8. show project
	step("show project", cmdShow([]string{"project", "budgeting-app-revamp"}))

	// 9. list tasks — should include both
	step("list tasks", cmdList([]string{"tasks"}))

	// 10. list tasks filtered by project
	step("list tasks --project", cmdList([]string{"tasks", "--project", "budgeting-app-revamp"}))

	// 11. list projects
	step("list projects", cmdList([]string{"projects"}))

	// 12. waiting (via flow update task)
	step("waiting set", cmdUpdate([]string{"task", "fix-auth-token-expiry", "--waiting", "Alice review"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if !task.WaitingOn.Valid || task.WaitingOn.String != "Alice review" {
		t.Errorf("waiting_on = %v, want Alice review", task.WaitingOn)
	}

	step("waiting clear", cmdUpdate([]string{"task", "fix-auth-token-expiry", "--clear-waiting"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.WaitingOn.Valid {
		t.Errorf("waiting_on should be cleared, got %v", task.WaitingOn)
	}

	// 13. priority (via flow update task)
	step("priority", cmdUpdate([]string{"task", "fix-auth-token-expiry", "--priority", "high"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.Priority != "high" {
		t.Errorf("priority = %q, want high", task.Priority)
	}

	// 14. drop an update file (skill-written, we simulate with os.WriteFile)
	updatePath := filepath.Join(taskDir, "updates", "2026-04-11-first-milestone.md")
	if err := os.WriteFile(updatePath, []byte("# First milestone\n\nFinished the token refresh endpoint.\n"), 0o644); err != nil {
		t.Fatalf("write update: %v", err)
	}

	// 15. show task again — should list the update file
	// (we can't easily capture stdout here, but we can verify the command returns 0
	// and the file is on disk)
	step("show task with update", cmdShow([]string{"task", "fix-auth-token-expiry"}))

	// 16. done
	step("done", cmdDone([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.Status != "done" {
		t.Errorf("status after done = %q, want done", task.Status)
	}
	// session_id should still be present (flow done is DB-only)
	if task.SessionID.String != "e2e-session-uuid" {
		t.Errorf("session_id cleared by done: %v", task.SessionID)
	}

	// 17. archive
	step("archive", cmdArchive([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if !task.ArchivedAt.Valid {
		t.Errorf("archived_at not set after archive")
	}

	// 18. list tasks (archived should be hidden)
	step("list tasks post-archive", cmdList([]string{"tasks"}))
	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Slug == "fix-auth-token-expiry" && !task.ArchivedAt.Valid {
			t.Errorf("archived task leaked into default list")
		}
	}

	// 19. unarchive
	step("unarchive", cmdUnarchive([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.ArchivedAt.Valid {
		t.Errorf("archived_at not cleared after unarchive: %v", task.ArchivedAt)
	}

	// 20. workdir list — the project's work_dir should have been auto-registered
	step("workdir list", cmdWorkdir([]string{"list"}))
	wd, err := flowdb.GetWorkdir(db, repo)
	if err != nil {
		t.Fatalf("repo not auto-registered as workdir: %v", err)
	}
	if wd == nil {
		t.Fatal("GetWorkdir returned nil for auto-registered path")
	}
}

func TestE2ECodexHarnessRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	flowRoot := filepath.Join(tmp, "flow")
	codexHome := filepath.Join(tmp, "codex-home")
	t.Setenv("FLOW_ROOT", flowRoot)
	t.Setenv("HOME", tmp)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")

	repo := filepath.Join(tmp, "code", "codex-smoke")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	_, getScript := stubITerm(t)

	const sid = "018f3f8e-97f7-7cc2-a871-bfbfd8f4fd40"
	calls := stubCodexCommandRunner(t, func(call int, ctx harness.SessionContext, args []string) ([]byte, error) {
		switch call {
		case 1:
			return []byte(`{"type":"thread.started","thread_id":"` + sid + `"}` + "\n"), nil
		case 2:
			return nil, nil
		case 3:
			return nil, nil
		default:
			t.Fatalf("unexpected codex call %d: %q", call, args)
			return nil, nil
		}
	})

	step := func(name string, rc int) {
		t.Helper()
		if rc != 0 {
			t.Fatalf("%s: rc=%d", name, rc)
		}
	}

	step("init", cmdInit(nil))
	step("add task", cmdAdd([]string{"task", "Codex Smoke", "--work-dir", repo}))
	step("do codex", cmdDo([]string{"--harness", "codex", "codex-smoke"}))

	db, err := flowdb.OpenDB(filepath.Join(flowRoot, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task, err := flowdb.GetTask(db, "codex-smoke")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "codex" {
		t.Fatalf("harness after codex do: got %+v, want codex", task.Harness)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Fatalf("session_id after codex do: got %+v, want %s", task.SessionID, sid)
	}
	if task.Status != "in-progress" {
		t.Fatalf("status after codex do: got %q, want in-progress", task.Status)
	}
	if script := getScript(); !strings.Contains(script, "codex resume "+sid) {
		t.Fatalf("spawn script missing codex resume: %s", script)
	} else {
		for key, value := range map[string]string{"FLOW_ROOT": flowRoot, "HOME": tmp, "CODEX_HOME": codexHome} {
			needle := key + "='" + value + "'"
			if !strings.Contains(script, needle) {
				t.Fatalf("spawn script missing %s propagation: %s", needle, script)
			}
		}
	}

	writeAppCodexRollout(t, codexHome, sid, `{"type":"session_meta","payload":{"id":"`+sid+`"},"timestamp":"2026-05-28T10:00:00Z"}
{"type":"event_msg","payload":{"type":"user_message","message":"codex smoke user"},"timestamp":"2026-05-28T10:00:01Z"}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex smoke assistant"}]},"timestamp":"2026-05-28T10:00:02Z"}
`)

	var transcriptRC int
	out := captureStdout(t, func() {
		transcriptRC = cmdTranscript([]string{"codex-smoke", "--compact"})
	})
	step("transcript codex", transcriptRC)
	for _, want := range []string{"codex smoke user", "codex smoke assistant"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript output missing %q:\n%s", want, out)
		}
	}

	step("done codex", cmdDone([]string{"codex-smoke"}))
	task, err = flowdb.GetTask(db, "codex-smoke")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Fatalf("status after done: got %q, want done", task.Status)
	}
	if len(*calls) != 3 {
		t.Fatalf("codex calls=%d, want 3 (%#v)", len(*calls), *calls)
	}
	if got := (*calls)[2].args; len(got) < 4 || got[0] != "exec" || !strings.Contains(got[3], "flow transcript codex-smoke") {
		t.Fatalf("codex close-out args=%q", got)
	}
}

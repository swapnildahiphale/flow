package app

import (
	"flow/internal/flowdb"
	"testing"
)

const cursorTestSID = "627189e8-5e30-424b-bf68-44301c4e201f"

// mustLoadTask loads a task by slug or fails the test.
func mustLoadTask(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		t.Fatalf("GetTask(%q): %v", slug, err)
	}
	return task
}

// TestDoSpawnRefusesCursorHarness pins that plain `flow do` refuses when
// the ambient harness is cursor — Agents Window sessions are bind-here-only.
func TestDoSpawnRefusesCursorHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "cursor-spawn-task")

	t.Setenv("CURSOR_CONVERSATION_ID", cursorTestSID)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	spawns, _ := stubITerm(t)
	if rc := cmdDo([]string{"cursor-spawn-task"}); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
	if *spawns != 0 {
		t.Errorf("cursor spawn gate should not open a tab; got %d spawns", *spawns)
	}
}

// TestDoHereBindsCursorHarness pins that `flow do --here` binds the
// ambient cursor conversation id and pins harness=cursor on the task.
func TestDoHereBindsCursorHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "cursor-here-task")

	t.Setenv("CURSOR_CONVERSATION_ID", cursorTestSID)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	if rc := cmdDo([]string{"--here", "cursor-here-task"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}

	task := mustLoadTask(t, "cursor-here-task")
	if !task.Harness.Valid || task.Harness.String != "cursor" {
		t.Fatalf("harness=%v, want cursor", task.Harness)
	}
	if !task.SessionID.Valid || task.SessionID.String != cursorTestSID {
		t.Fatalf("session_id=%v, want %s", task.SessionID, cursorTestSID)
	}
}

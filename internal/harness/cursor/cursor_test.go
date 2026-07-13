package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

func TestIdentity(t *testing.T) {
	h := New()
	if h.Name() != harness.NameCursor {
		t.Errorf("Name() = %q, want %q", h.Name(), harness.NameCursor)
	}
	if h.Binary() != "cursor-agent" {
		t.Errorf("Binary() = %q, want cursor-agent", h.Binary())
	}
	if h.SessionIDEnvVar() != "CURSOR_CONVERSATION_ID" {
		t.Errorf("SessionIDEnvVar() = %q, want CURSOR_CONVERSATION_ID", h.SessionIDEnvVar())
	}
}

func TestValidateSessionID(t *testing.T) {
	h := New()
	good := "627189e8-5e30-424b-bf68-44301c4e201f"
	if err := h.ValidateSessionID(good); err != nil {
		t.Errorf("ValidateSessionID(%q) = %v, want nil", good, err)
	}
	if err := h.ValidateSessionID("not-a-uuid"); err == nil {
		t.Error("ValidateSessionID(not-a-uuid) = nil, want error")
	}
}

func TestSkipPermissionsRunNoOp(t *testing.T) {
	if err := New().SkipPermissionsRun("anything"); err != nil {
		t.Errorf("SkipPermissionsRun = %v, want nil", err)
	}
}

func TestHooksNoOp(t *testing.T) {
	h := New()
	cmd := "flow hook session-start"
	for _, fn := range []struct {
		name string
		run  func() (bool, error)
	}{
		{"InstallSessionStartHook", func() (bool, error) { return h.InstallSessionStartHook(cmd) }},
		{"UninstallSessionStartHook", func() (bool, error) { return h.UninstallSessionStartHook(cmd) }},
		{"InstallUserPromptSubmitHook", func() (bool, error) { return h.InstallUserPromptSubmitHook(cmd) }},
		{"UninstallUserPromptSubmitHook", func() (bool, error) { return h.UninstallUserPromptSubmitHook(cmd) }},
	} {
		changed, err := fn.run()
		if err != nil {
			t.Errorf("%s: err = %v, want nil", fn.name, err)
		}
		if changed {
			t.Errorf("%s: changed = true, want false", fn.name)
		}
	}
}

func TestLiveSessionIDsEmpty(t *testing.T) {
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatalf("LiveSessionIDs: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("LiveSessionIDs = %v, want empty map", live)
	}
}

func TestNewSessionIDRefuses(t *testing.T) {
	_, err := New().NewSessionID()
	if err == nil {
		t.Fatal("NewSessionID = nil, want error")
	}
	if !strings.Contains(err.Error(), "--here") {
		t.Errorf("NewSessionID error %q, want mention of --here", err)
	}
}

func TestValidateSessionAlwaysNil(t *testing.T) {
	if err := New().ValidateSession("/any/cwd", "627189e8-5e30-424b-bf68-44301c4e201f"); err != nil {
		t.Errorf("ValidateSession = %v, want nil", err)
	}
}

func TestLaunchResumeRefuse(t *testing.T) {
	h := New()
	opts := harness.LaunchOpts{}
	for _, cmd := range []string{
		h.LaunchCmd("627189e8-5e30-424b-bf68-44301c4e201f", "prompt", opts),
		h.ResumeCmd("627189e8-5e30-424b-bf68-44301c4e201f", opts),
	} {
		if !strings.Contains(cmd, "Agents Window") {
			t.Errorf("cmd %q missing Agents Window", cmd)
		}
		if !strings.Contains(cmd, "--here") {
			t.Errorf("cmd %q missing --here", cmd)
		}
	}
}

func TestAutoRunArgvNil(t *testing.T) {
	got := New().AutoRunArgv("627189e8-5e30-424b-bf68-44301c4e201f", "prompt", harness.LaunchOpts{})
	if len(got) != 0 {
		t.Errorf("AutoRunArgv = %v, want nil/empty", got)
	}
}

func TestInstallSkillWritesCursorPathRemovesStaleCopiesKeepsClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, ".agents", "skills", "flow")
	claude := filepath.Join(home, ".claude", "skills", "flow")
	oldCursor := filepath.Join(home, ".cursor", "skills", "flow")
	for _, dir := range []string{agents, claude, oldCursor} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("old copy"), 0o644); err != nil {
			t.Fatal(err)
		}
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
	if _, err := os.Stat(oldCursor); !os.IsNotExist(err) {
		t.Fatalf("stale ~/.cursor/skills/flow still present: %v", err)
	}
	b, err = os.ReadFile(filepath.Join(claude, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "old copy") {
		t.Fatalf("claude skill should be untouched: %v %s", err, b)
	}
}

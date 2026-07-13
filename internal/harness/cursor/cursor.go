// Package cursor implements harness.Harness for Cursor's Agents Window.
// MVP is bind-here-only: no spawn, no hooks, no-op close-out sweep.
package cursor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"flow/internal/harness"
)

// New returns a fresh cursor harness. The struct is stateless.
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

// ValidateSession is a no-op: cursor transcripts are keyed by session id only.
func (c *cursorHarness) ValidateSession(workDir, sessionID string) error { return nil }

func (c *cursorHarness) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	return "error: cursor Agents Window cannot be spawned by flow — open a chat and run: flow do --here <slug>"
}

func (c *cursorHarness) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	return "error: cursor Agents Window cannot be resumed by flow — open the chat and run: flow do --here <slug>"
}

// SkipPermissionsRun is a no-op under cursor: close-out distillation runs in-session.
func (c *cursorHarness) SkipPermissionsRun(prompt string) error { return nil }

func (c *cursorHarness) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	return nil
}

func (c *cursorHarness) LiveSessionIDs() (map[string]int, error) {
	return map[string]int{}, nil
}

func (c *cursorHarness) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "skills", "flow-cursor", "SKILL.md"), nil
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
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return err
	}
	if err := removeStaleCursorFlowSkillDir(); err != nil {
		return err
	}
	return removeStaleAgentsFlowSkill()
}

// removeStaleCursorFlowSkillDir deletes ~/.cursor/skills/flow if present.
// Early cursor MVP installs used the same skill name/path as Claude; we now
// install to ~/.cursor/skills/flow-cursor/ so ~/.claude/skills/flow can stay.
func removeStaleCursorFlowSkillDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".cursor", "skills", "flow")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// removeStaleAgentsFlowSkill deletes ~/.agents/skills/flow if present.
// Cursor was loading that Claude-oriented copy before we installed flow-cursor.
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

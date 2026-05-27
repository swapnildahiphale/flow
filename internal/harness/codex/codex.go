// Package codex implements harness.Harness for OpenAI Codex CLI.
package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flow/internal/harness"
	"flow/internal/spawner"
)

type Runner func(ctx harness.SessionContext, args []string) ([]byte, error)

var (
	CommandRunner Runner = runCodex
	PSRunner             = runPS
	UserHomeDir          = os.UserHomeDir
)

const allocationPrompt = "Initialize a new flow-managed Codex thread. Do not inspect files, run commands, or modify anything. Reply exactly: flow session allocated."

func New() harness.Harness {
	return &codex{}
}

type codex struct{}

func (c *codex) Name() harness.Name      { return harness.NameCodex }
func (c *codex) Binary() string          { return "codex" }
func (c *codex) SessionIDEnvVar() string { return "CODEX_THREAD_ID" }

var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (c *codex) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid codex thread UUID: %q", s)
	}
	return nil
}

func (c *codex) ValidateSession(workDir, sessionID string) error {
	return nil
}

func (c *codex) PrepareFreshSession(ctx harness.SessionContext, prompt string, opts harness.LaunchOpts) (harness.PreparedSession, error) {
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, allocationPrompt)

	out, err := CommandRunner(ctx, args)
	if err != nil {
		return harness.PreparedSession{}, err
	}
	threadID, err := parseThreadStarted(out)
	if err != nil {
		return harness.PreparedSession{}, err
	}
	return harness.PreparedSession{
		SessionID:     threadID,
		LaunchCommand: c.ResumeCmd(threadID, harness.LaunchOpts{}),
	}, nil
}

func (c *codex) BootstrapFreshSession(ctx harness.SessionContext, sessionID, prompt string, opts harness.LaunchOpts) error {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	args := []string{"exec", "resume", "--skip-git-repo-check"}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, sessionID, prompt)
	_, err := CommandRunner(ctx, args)
	return err
}

func (c *codex) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	if opts.Inject == "" {
		return "codex resume " + sessionID
	}
	args := "codex exec resume --skip-git-repo-check"
	if opts.SkipPermissions {
		args += " --dangerously-bypass-approvals-and-sandbox"
	}
	args += " " + sessionID + " " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	return args + " && codex resume " + sessionID
}

func (c *codex) SkipPermissionsRun(ctx harness.SessionContext, prompt string) error {
	_, err := CommandRunner(ctx, []string{
		"exec",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	})
	return err
}

type codexEvent struct {
	Type   string `json:"type"`
	Thread struct {
		ThreadID string `json:"thread_id"`
	} `json:"thread"`
}

func parseThreadStarted(out []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var threadID string
	for {
		var ev codexEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("parse codex json: %w", err)
		}
		if ev.Type != "thread.started" {
			continue
		}
		if ev.Thread.ThreadID == "" {
			return "", fmt.Errorf("codex thread.started missing thread.thread_id")
		}
		if threadID == "" {
			threadID = ev.Thread.ThreadID
		}
	}
	if threadID == "" {
		return "", fmt.Errorf("codex output did not include thread.started event")
	}
	return threadID, nil
}

func runCodex(ctx harness.SessionContext, args []string) ([]byte, error) {
	cmd := exec.Command("codex", args...)
	cmd.Dir = ctx.WorkDir
	cmd.Env = ctx.EnvOrDefault()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return stdout.Bytes(), fmt.Errorf("codex %s: %w", strings.Join(args, " "), err)
		}
		return stdout.Bytes(), fmt.Errorf("codex %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

var runningRe = regexp.MustCompile(`codex\s+(?:exec\s+)?resume\s+(` + sessionIDRe.String()[1:len(sessionIDRe.String())-1] + `)`)

func (c *codex) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "codex") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range runningRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.ToLower(m[1])
			if seen[id] {
				continue
			}
			seen[id] = true
			live[id]++
		}
	}
	return live, nil
}

func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

func (c *codex) RenderTranscript(cwd, sessionID string, compact bool, cutoff time.Time, w io.Writer) error {
	return fmt.Errorf("codex transcript rendering is not wired yet")
}

func (c *codex) SkillInstallPath() (string, error) {
	home, err := UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".agents", "skills", "flow", "SKILL.md"), nil
}

func (c *codex) SkillVersionPath() (string, error) {
	skill, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(skill), "VERSION"), nil
}

func (c *codex) InstallSkill(content []byte) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

func (c *codex) UninstallSkill() error {
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

func (c *codex) InstallSessionStartHook(command string) (bool, error) {
	return false, fmt.Errorf("codex hook install is not wired yet")
}

func (c *codex) UninstallSessionStartHook(command string) (bool, error) {
	return false, fmt.Errorf("codex hook uninstall is not wired yet")
}

func (c *codex) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return false, fmt.Errorf("codex hook uninstall is not wired yet")
}

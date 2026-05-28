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
	"strconv"
	"strings"

	"flow/internal/harness"
	"flow/internal/spawner"
)

type Runner func(ctx harness.SessionContext, args []string) ([]byte, error)

var (
	CommandRunner Runner = runCodex
	PSRunner             = runPS
	UserHomeDir          = os.UserHomeDir
	ReadFile             = os.ReadFile
	WriteFile            = os.WriteFile
	MkdirAll             = os.MkdirAll
	RemoveAll            = os.RemoveAll
)

const allocationPrompt = "Initialize a new flow-managed Codex thread. Do not inspect files, run commands, or modify anything. Reply exactly: flow session allocated."
const hookMatcher = "startup|resume|clear|compact"
const hookDisabledWarning = "Codex hooks may be disabled; review ~/.codex/config.toml and Codex /hooks"

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
		return harness.PreparedSession{}, fmt.Errorf("codex allocate thread: %w", err)
	}
	threadID, err := parseThreadStarted(out)
	if err != nil {
		return harness.PreparedSession{}, err
	}
	if err := c.ValidateSessionID(threadID); err != nil {
		return harness.PreparedSession{}, err
	}
	threadID = strings.ToLower(threadID)
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
	if err != nil {
		return fmt.Errorf("codex bootstrap thread %s: %w", sessionID, err)
	}
	return nil
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
	if err != nil {
		return fmt.Errorf("codex close-out sweep: %w", err)
	}
	return nil
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Thread   struct {
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
		id := ev.ThreadID
		if id == "" {
			id = ev.Thread.ThreadID
		}
		if id == "" {
			return "", fmt.Errorf("codex thread.started missing thread_id")
		}
		if threadID == "" {
			threadID = id
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

func (c *codex) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		for _, id := range liveSessionIDsFromPSLine(line) {
			live[id]++
		}
	}
	return live, nil
}

func liveSessionIDsFromPSLine(line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] == "PID" {
		return nil
	}
	if _, err := strconv.Atoi(fields[0]); err == nil {
		fields = fields[1:]
	}
	if len(fields) == 0 || filepath.Base(fields[0]) != "codex" {
		return nil
	}

	i := 1
	if i < len(fields) && fields[i] == "exec" {
		i++
	}
	if i >= len(fields) || fields[i] != "resume" {
		return nil
	}
	for _, tok := range fields[i+1:] {
		tok = strings.Trim(tok, `'"`)
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if sessionIDRe.MatchString(tok) {
			return []string{strings.ToLower(tok)}
		}
		return nil
	}
	return nil
}

func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
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
	if err := MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	if err := WriteFile(p, content, 0o644); err != nil {
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
	return RemoveAll(dir)
}

func (c *codex) InstallSessionStartHook(command string) (bool, error) {
	return mutateHook(command, "SessionStart", true)
}

func (c *codex) UninstallSessionStartHook(command string) (bool, error) {
	return mutateHook(command, "SessionStart", false)
}

func (c *codex) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return false, nil
}

func (c *codex) SessionStartHookWarning() string {
	home, err := codexHome()
	if err != nil {
		return ""
	}
	raw, err := ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		for _, key := range []string{"hooks", "codex_hooks"} {
			if strings.HasPrefix(line, key) {
				rest := strings.TrimSpace(strings.TrimPrefix(line, key))
				if strings.HasPrefix(rest, "=") && strings.TrimSpace(strings.TrimPrefix(rest, "=")) == "false" {
					return hookDisabledWarning
				}
			}
		}
	}
	return ""
}

func codexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func hooksPath() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.json"), nil
}

func mutateHook(command, event string, install bool) (bool, error) {
	path, err := hooksPath()
	if err != nil {
		return false, err
	}
	raw, err := ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		raw = []byte("{}")
		if install {
			if err := MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
			}
		} else {
			return false, nil
		}
	}

	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if file == nil {
		file = map[string]any{}
	}
	hooks := map[string]any{}
	if rawHooks, exists := file["hooks"]; exists {
		var ok bool
		hooks, ok = rawHooks.(map[string]any)
		if !ok {
			return false, fmt.Errorf("parse %s: hooks must be an object", path)
		}
	} else if install {
		hooks = map[string]any{}
	}
	var entries []any
	if rawEntries, exists := hooks[event]; exists {
		var ok bool
		entries, ok = rawEntries.([]any)
		if !ok {
			return false, fmt.Errorf("parse %s: hooks.%s must be an array", path, event)
		}
	}

	changed := false
	if install && hasCanonicalHookCommand(entries, command) && countHookCommands(entries, command) == 1 {
		return false, nil
	}
	entries, removed := removeHookCommand(entries, command)
	if removed {
		changed = true
	}
	if install {
		entries = append(entries, map[string]any{
			"matcher": hookMatcher,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": command,
					"timeout": float64(10),
				},
			},
		})
		changed = true
	}

	if !changed {
		return false, nil
	}
	if len(entries) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = entries
	}
	if len(hooks) == 0 {
		delete(file, "hooks")
	} else {
		file["hooks"] = hooks
	}

	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	out = append(out, '\n')
	if err := WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func removeHookCommand(entries []any, command string) ([]any, bool) {
	changed := false
	kept := make([]any, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		inner, ok := m["hooks"].([]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		filtered := make([]any, 0, len(inner))
		entryChanged := false
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				filtered = append(filtered, h)
				continue
			}
			if cmd, _ := hm["command"].(string); strings.TrimSpace(cmd) == command {
				changed = true
				entryChanged = true
				continue
			}
			filtered = append(filtered, h)
		}
		if entryChanged && len(filtered) == 0 {
			continue
		}
		if entryChanged {
			m["hooks"] = filtered
		}
		kept = append(kept, m)
	}
	return kept, changed
}

func hasCanonicalHookCommand(entries []any, command string) bool {
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if matcher, _ := m["matcher"].(string); matcher != hookMatcher {
			continue
		}
		inner, _ := m["hooks"].([]any)
		if len(inner) != 1 {
			continue
		}
		hm, ok := inner[0].(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := hm["type"].(string); typ != "command" {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.TrimSpace(cmd) != command {
			continue
		}
		if timeout, ok := hm["timeout"].(float64); ok && timeout == 10 {
			return true
		}
	}
	return false
}

func countHookCommands(entries []any, command string) int {
	count := 0
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); strings.TrimSpace(cmd) == command {
				count++
			}
		}
	}
	return count
}

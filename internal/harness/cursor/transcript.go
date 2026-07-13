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
// Leading slash is stripped; path separators become hyphens. Dots and
// underscores are left unchanged (unlike Claude's EncodeCwd).
func EncodeProjectDir(cwd string) string {
	cwd = filepath.Clean(cwd)
	if strings.HasPrefix(cwd, string(filepath.Separator)) {
		cwd = cwd[1:]
	}
	return strings.ReplaceAll(cwd, string(filepath.Separator), "-")
}

// RenderTranscript opens the Agents Window jsonl for sessionID under
// ~/.cursor/projects/<encoded-cwd>/agent-transcripts/<sid>/<sid>.jsonl
// and writes a human-readable rendering to w.
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

// resolveTranscriptPath locates a session jsonl under ~/.cursor/projects.
// Tries the deterministic cwd-encoded path first, then falls back to a
// glob on the session id (preferring the newest mtime).
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

// RenderJSONL writes a readable transcript from a Cursor Agents Window
// jsonl stream. compact is ignored in MVP (no separate thinking/tool blocks).
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
			continue // skip malformed lines
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

package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RenderTranscript finds Codex's rollout jsonl for sessionID, decodes
// the Codex-specific event schema, and writes normalized human-readable
// output to w. cwd is unused because Codex stores rollout logs under
// CODEX_HOME rather than a cwd-encoded project directory.
func (c *codex) RenderTranscript(cwd, sessionID string, compact bool, cutoff time.Time, w io.Writer) error {
	p, err := findRollout(sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open codex rollout %s: %w", p, err)
	}
	defer f.Close()
	return RenderJSONL(f, compact, cutoff, w)
}

func findRollout(sessionID string) (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "sessions")
	sessionIDLower := strings.ToLower(sessionID)

	var newest string
	var newestMod time.Time
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil || d.IsDir() {
			if walkErr != nil && rolloutNameMatchesSession(filepath.Base(path), sessionIDLower) {
				return fmt.Errorf("access codex rollout candidate %s: %w", path, walkErr)
			}
			return nil
		}
		name := d.Name()
		if !rolloutNameMatchesSession(name, sessionIDLower) {
			return nil
		}
		matches, err := rolloutHasSessionMeta(path, sessionID)
		if err != nil {
			return err
		}
		if !matches {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat codex rollout %s: %w", path, err)
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = path
			newestMod = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if newest == "" {
		return "", fmt.Errorf("codex rollout not found under %s for session %s", root, sessionID)
	}
	return newest, nil
}

func rolloutNameMatchesSession(name, sessionIDLower string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "rollout-") && strings.Contains(name, sessionIDLower) && strings.HasSuffix(name, ".jsonl")
}

func rolloutHasSessionMeta(path, sessionID string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open codex rollout candidate %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Type == "session_meta" && strings.EqualFold(rec.Payload.ID, sessionID) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read codex rollout candidate %s: %w", path, err)
	}
	return false, nil
}

// RenderJSONL renders a Codex rollout jsonl byte-stream to w.
func RenderJSONL(r io.Reader, compact bool, cutoff time.Time, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var rec codexRolloutRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !cutoff.IsZero() && rec.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil && ts.Before(cutoff) {
				continue
			}
		}

		if renderCodexRecord(w, rec, compact, &first) {
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read codex rollout: %w", err)
	}
	return nil
}

type codexRolloutRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Item      json.RawMessage `json:"item"`
}

type codexEventPayload struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Message json.RawMessage `json:"message"`
	Content json.RawMessage `json:"content"`
	Item    json.RawMessage `json:"item"`
}

type codexResponseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	Content   json.RawMessage `json:"content"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func renderCodexRecord(w io.Writer, rec codexRolloutRecord, compact bool, first *bool) bool {
	switch rec.Type {
	case "event_msg":
		var payload codexEventPayload
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			return false
		}
		if payload.Type == "user_message" {
			text := extractCodexText(payload.Message)
			if text == "" {
				return false
			}
			printGap(w, first)
			fmt.Fprintln(w, "─── User ───")
			fmt.Fprintln(w, text)
			return true
		}
		if payload.Type == "agent_message" {
			return false
		}
		if payload.Role != "user" && payload.Role != "assistant" {
			return false
		}
		text := extractCodexText(payload.Content)
		if text == "" {
			return false
		}
		printGap(w, first)
		if payload.Role == "user" {
			fmt.Fprintln(w, "─── User ───")
		} else {
			fmt.Fprintln(w, "─── Assistant ───")
		}
		fmt.Fprintln(w, text)
		return true
	case "response_item":
		item := responseItem(rec)
		switch item.Type {
		case "message":
			text := extractCodexText(item.Content)
			if text == "" {
				return false
			}
			printGap(w, first)
			if item.Role == "user" {
				fmt.Fprintln(w, "─── User ───")
			} else {
				fmt.Fprintln(w, "─── Assistant ───")
			}
			fmt.Fprintln(w, text)
			return true
		case "function_call":
			printGap(w, first)
			name := item.Name
			if name == "" {
				name = "function_call"
			}
			fmt.Fprintf(w, "─── Tool: %s ───\n", name)
			if summary := formatFunctionArguments(item.Arguments); summary != "" {
				fmt.Fprintln(w, summary)
			}
			return true
		case "function_call_output":
			if compact {
				return false
			}
			output := extractCodexText(item.Output)
			if output == "" {
				return false
			}
			printGap(w, first)
			fmt.Fprintln(w, "─── Result ───")
			fmt.Fprintln(w, output)
			return true
		}
	}
	return false
}

func responseItem(rec codexRolloutRecord) codexResponseItem {
	var item codexResponseItem
	if len(rec.Item) > 0 && json.Unmarshal(rec.Item, &item) == nil && item.Type != "" {
		return item
	}
	if len(rec.Payload) > 0 && json.Unmarshal(rec.Payload, &item) == nil && item.Type != "" {
		return item
	}
	var payload codexEventPayload
	if len(rec.Payload) > 0 && json.Unmarshal(rec.Payload, &payload) == nil && len(payload.Item) > 0 {
		_ = json.Unmarshal(payload.Item, &item)
	}
	return item
}

func printGap(w io.Writer, first *bool) {
	if !*first {
		fmt.Fprintln(w)
	}
	*first = false
}

func extractCodexText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var block codexContentBlock
	if err := json.Unmarshal(raw, &block); err == nil && block.Text != "" {
		return block.Text
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, rawBlock := range blocks {
		if text := extractCodexText(rawBlock); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func formatFunctionArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		var nested map[string]any
		if err := json.Unmarshal([]byte(s), &nested); err == nil {
			if summary := formatFunctionArgumentMap(nested); summary != "" {
				return summary
			}
		}
		return s
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if summary := formatFunctionArgumentMap(m); summary != "" {
			return summary
		}
	}
	return strings.TrimSpace(string(raw))
}

func formatFunctionArgumentMap(m map[string]any) string {
	for _, key := range []string{"command", "cmd"} {
		if cmd, ok := m[key].(string); ok && cmd != "" {
			return "$ " + cmd
		}
	}
	return ""
}

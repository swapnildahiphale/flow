package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRolloutJSONL = `{"type":"session_meta","payload":{"id":"018f3f8e-97f7-7cc2-a871-bfbfd8f4fd40"},"timestamp":"2026-05-28T10:00:00Z"}
{"type":"event_msg","payload":{"role":"user","content":"Hello Codex"},"timestamp":"2026-05-28T10:00:01Z"}
{"type":"event_msg","payload":{"role":"assistant","content":[{"type":"text","text":"Hello human"}]},"timestamp":"2026-05-28T10:00:02Z"}
{"type":"response_item","item":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"echo hi\"}"},"timestamp":"2026-05-28T10:00:03Z"}
{"type":"response_item","item":{"type":"function_call_output","output":"tool output text\n"},"timestamp":"2026-05-28T10:00:04Z"}
`

func TestRenderTranscriptCompactOmitsToolOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeCodexRollout(t, home, "sessions/sub/rollout-"+testThreadID+"-new.jsonl", testRolloutJSONL, time.Now())

	h := New()
	var full strings.Builder
	if err := h.RenderTranscript("/unused", testThreadID, false, time.Time{}, &full); err != nil {
		t.Fatalf("RenderTranscript full: %v", err)
	}
	for _, want := range []string{"Hello Codex", "Hello human", "─── Tool: shell ───", "$ echo hi", "─── Result ───", "tool output text"} {
		if !strings.Contains(full.String(), want) {
			t.Fatalf("full output missing %q:\n%s", want, full.String())
		}
	}

	var compact strings.Builder
	if err := h.RenderTranscript("/unused", testThreadID, true, time.Time{}, &compact); err != nil {
		t.Fatalf("RenderTranscript compact: %v", err)
	}
	if !strings.Contains(compact.String(), "Hello Codex") || !strings.Contains(compact.String(), "─── Tool: shell ───") {
		t.Fatalf("compact output missing expected message/tool:\n%s", compact.String())
	}
	if strings.Contains(compact.String(), "─── Result") || strings.Contains(compact.String(), "tool output text") {
		t.Fatalf("compact output included tool result:\n%s", compact.String())
	}
}

func TestRenderTranscriptRealCodexPayloadShapes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	raw := `{"type":"session_meta","payload":{"id":"` + testThreadID + `"},"timestamp":"2026-05-28T10:00:00Z"}
{"type":"event_msg","payload":{"type":"user_message","message":"real user request"},"timestamp":"2026-05-28T10:00:01Z"}
{"type":"event_msg","payload":{"type":"agent_message","message":"duplicate assistant event"},"timestamp":"2026-05-28T10:00:02Z"}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"real assistant reply"}]},"timestamp":"2026-05-28T10:00:03Z"}
{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"flow show task\"}"},"timestamp":"2026-05-28T10:00:04Z"}
{"type":"response_item","payload":{"type":"function_call_output","output":"real tool output"},"timestamp":"2026-05-28T10:00:05Z"}
`
	writeCodexRollout(t, home, "sessions/rollout-"+testThreadID+".jsonl", raw, time.Now())

	var out strings.Builder
	if err := New().RenderTranscript("/unused", testThreadID, false, time.Time{}, &out); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	for _, want := range []string{"real user request", "real assistant reply", "$ flow show task", "real tool output"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "duplicate assistant event") {
		t.Fatalf("agent_message duplicate should be skipped:\n%s", out.String())
	}
}

func TestFindRolloutChoosesNewestVerifiedRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	older := writeCodexRollout(
		t,
		home,
		"sessions/2026/05/28/rollout-"+testThreadID+"-older.jsonl",
		`{"type":"session_meta","payload":{"id":"`+testThreadID+`"}}`+"\n",
		time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
	)
	newer := writeCodexRollout(
		t,
		home,
		"sessions/2026/05/28/rollout-"+testThreadID+"-newer.jsonl",
		`malformed`+"\n"+`{"type":"session_meta","payload":{"id":"`+testThreadID+`"}}`+"\n",
		time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
	)
	writeCodexRollout(
		t,
		home,
		"sessions/2026/05/28/rollout-"+testThreadID+"-false-positive.jsonl",
		`{"type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000000"}}`+"\n",
		time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	)
	writeCodexRollout(
		t,
		home,
		"sessions/rollout-"+testThreadID+"-broken.jsonl",
		`{`,
		time.Date(2026, 5, 28, 13, 0, 0, 0, time.UTC),
	)

	got, err := findRollout(testThreadID)
	if err != nil {
		t.Fatalf("findRollout: %v", err)
	}
	if got != newer {
		t.Fatalf("findRollout=%q, want newest verified %q (older %q)", got, newer, older)
	}
}

func TestFindRolloutMatchesSessionIDCaseInsensitively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeCodexRollout(
		t,
		home,
		"sessions/rollout-"+strings.ToLower(testThreadID)+".jsonl",
		`{"type":"session_meta","payload":{"id":"`+strings.ToLower(testThreadID)+`"}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"user_message","message":"case matched"}}`+"\n",
		time.Now(),
	)

	var out strings.Builder
	if err := New().RenderTranscript("/unused", strings.ToUpper(testThreadID), false, time.Time{}, &out); err != nil {
		t.Fatalf("RenderTranscript uppercase session id: %v", err)
	}
	if !strings.Contains(out.String(), "case matched") {
		t.Fatalf("output missing case-insensitive rollout content:\n%s", out.String())
	}
}

func TestRenderTranscriptFiltersByCutoff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	raw := `{"type":"session_meta","payload":{"id":"` + testThreadID + `"},"timestamp":"2026-05-28T10:00:00Z"}
{"type":"event_msg","payload":{"role":"user","content":"old request"},"timestamp":"2026-05-28T10:00:01Z"}
{"type":"event_msg","payload":{"role":"assistant","content":"new reply"},"timestamp":"2026-05-28T10:30:00Z"}
{"type":"event_msg","payload":{"role":"assistant","content":"missing timestamp stays"}}
`
	writeCodexRollout(t, home, "sessions/rollout-"+testThreadID+".jsonl", raw, time.Now())
	cutoff, err := time.Parse(time.RFC3339Nano, "2026-05-28T10:15:00Z")
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := New().RenderTranscript("/unused", testThreadID, false, cutoff, &out); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if strings.Contains(out.String(), "old request") {
		t.Fatalf("old message leaked through cutoff:\n%s", out.String())
	}
	for _, want := range []string{"new reply", "missing timestamp stays"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func writeCodexRollout(t *testing.T, home, rel, raw string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

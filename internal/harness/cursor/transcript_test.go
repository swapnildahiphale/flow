package cursor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeProjectDir(t *testing.T) {
	got := EncodeProjectDir("/Users/Swapnil/workspace/swapnil/flow")
	want := "Users-Swapnil-workspace-swapnil-flow"
	if got != want {
		t.Fatalf("EncodeProjectDir = %q, want %q", got, want)
	}
}

func TestRenderJSONL(t *testing.T) {
	in := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"pong"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
		`garbage`,
	}, "\n")
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(in), false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "user:") || !strings.Contains(out, "hi") {
		t.Fatalf("missing user turn: %s", out)
	}
	if !strings.Contains(out, "assistant:") || !strings.Contains(out, "pong") {
		t.Fatalf("missing assistant turn: %s", out)
	}
}

func TestRenderTranscriptResolvesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/Users/me/proj"
	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".cursor", "projects", EncodeProjectDir(cwd), "agent-transcripts", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","message":{"content":[{"type":"text","text":"bound"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := New().RenderTranscript(cwd, sid, false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "bound") {
		t.Fatalf("got %q", buf.String())
	}
}

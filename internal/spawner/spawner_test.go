package spawner

import (
	"flow/internal/iterm"
	"flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/zellij"
	"strings"
	"testing"
)

// TestDetectFromEnv verifies the TERM_PROGRAM → backend mapping. The
// Override knob has higher precedence and is checked separately below.
func TestDetectFromEnv(t *testing.T) {
	cases := []struct {
		termProgram string
		want        Backend
	}{
		{"iTerm.app", BackendITerm},
		{"Apple_Terminal", BackendTerminal},
		{"WarpTerminal", BackendWarp},
		{"", BackendITerm},
		{"WezTerm", BackendITerm},
		{"vscode", BackendITerm},
	}
	for _, tc := range cases {
		t.Run(tc.termProgram, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("FLOW_TERM", "")
			t.Setenv("TERM_PROGRAM", tc.termProgram)
			Override = ""
			if got := Detect(); got != tc.want {
				t.Errorf("Detect() with TERM_PROGRAM=%q: got %q, want %q",
					tc.termProgram, got, tc.want)
			}
		})
	}
}

// TestOverrideBeatsEnv confirms the test escape hatch: setting Override
// pins the backend regardless of TERM_PROGRAM or FLOW_TERM.
func TestOverrideBeatsEnv(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	t.Setenv("FLOW_TERM", "iterm")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Cleanup(func() { Override = "" })

	Override = BackendTerminal
	if got := Detect(); got != BackendTerminal {
		t.Errorf("Override=Terminal: got %q, want %q", got, BackendTerminal)
	}
	Override = BackendWarp
	if got := Detect(); got != BackendWarp {
		t.Errorf("Override=Warp: got %q, want %q", got, BackendWarp)
	}
	Override = BackendITerm
	if got := Detect(); got != BackendITerm {
		t.Errorf("Override=ITerm: got %q, want %q", got, BackendITerm)
	}
}

// TestDetectZellij verifies the ZELLIJ env var beats TERM_PROGRAM.
// zellij sets ZELLIJ in every shell it spawns, so its presence means
// the user is inside a zellij session regardless of which terminal
// hosts it.
func TestDetectZellij(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	t.Setenv("FLOW_TERM", "")
	t.Setenv("TERM_PROGRAM", "iTerm.app") // proves ZELLIJ wins
	Override = ""
	if got := Detect(); got != BackendZellij {
		t.Errorf("Detect() with ZELLIJ=0: got %q, want %q", got, BackendZellij)
	}
}

// TestDetectFlowTermOverride — FLOW_TERM with a valid backend value
// wins over TERM_PROGRAM but loses to ZELLIJ. Iterates over every
// valid backend value so we catch regressions where a new backend is
// added to the switch but missed in Detect()'s FLOW_TERM filter.
func TestDetectFlowTermOverride(t *testing.T) {
	cases := []struct {
		flowTerm string
		want     Backend
	}{
		{"iterm", BackendITerm},
		{"terminal", BackendTerminal},
		{"zellij", BackendZellij},
		{"warp", BackendWarp},
	}
	for _, tc := range cases {
		t.Run(tc.flowTerm, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("FLOW_TERM", tc.flowTerm)
			t.Setenv("TERM_PROGRAM", "Apple_Terminal") // proves FLOW_TERM wins
			Override = ""
			if got := Detect(); got != tc.want {
				t.Errorf("Detect() with FLOW_TERM=%q: got %q, want %q",
					tc.flowTerm, got, tc.want)
			}
		})
	}
}

// TestDetectZellijBeatsFlowTerm — when both $ZELLIJ and $FLOW_TERM
// are set, ZELLIJ wins. Rationale: if the user is inside a zellij
// session, that's where their workflow lives.
func TestDetectZellijBeatsFlowTerm(t *testing.T) {
	t.Setenv("ZELLIJ", "1")
	t.Setenv("FLOW_TERM", "iterm")
	t.Setenv("TERM_PROGRAM", "")
	Override = ""
	if got := Detect(); got != BackendZellij {
		t.Errorf("Detect() with ZELLIJ=1 + FLOW_TERM=iterm: got %q, want %q", got, BackendZellij)
	}
}

// TestDetectFlowTermInvalidFallsThrough — an unrecognized FLOW_TERM
// value is silently ignored and TERM_PROGRAM detection takes over.
func TestDetectFlowTermInvalidFallsThrough(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	t.Setenv("FLOW_TERM", "garbage-not-a-backend")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	Override = ""
	if got := Detect(); got != BackendITerm {
		t.Errorf("Detect() with garbage FLOW_TERM: got %q, want %q", got, BackendITerm)
	}
}

// TestSpawnTabRoutesToITerm asserts the iterm Runner is the one called
// when Detect() resolves to BackendITerm.
func TestSpawnTabRoutesToITerm(t *testing.T) {
	Override = BackendITerm
	t.Cleanup(func() { Override = "" })

	calls := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*calls.iterm {
		t.Error("expected iterm.Runner to be called")
	}
	if *calls.terminal {
		t.Error("did not expect terminal.Runner to be called")
	}
	if *calls.zellij {
		t.Error("did not expect zellij.Runner to be called")
	}
	if *calls.warp {
		t.Error("did not expect warp.Runner to be called")
	}
}

// TestSpawnTabRoutesToTerminal asserts the terminal Runner is the one
// called when Detect() resolves to BackendTerminal.
func TestSpawnTabRoutesToTerminal(t *testing.T) {
	Override = BackendTerminal
	t.Cleanup(func() { Override = "" })

	calls := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *calls.iterm {
		t.Error("did not expect iterm.Runner to be called")
	}
	if !*calls.terminal {
		t.Error("expected terminal.Runner to be called")
	}
	if *calls.zellij {
		t.Error("did not expect zellij.Runner to be called")
	}
	if *calls.warp {
		t.Error("did not expect warp.Runner to be called")
	}
}

// TestSpawnTabRoutesToZellij asserts the zellij Runner is the one
// called when Detect() resolves to BackendZellij.
func TestSpawnTabRoutesToZellij(t *testing.T) {
	Override = BackendZellij
	t.Cleanup(func() { Override = "" })

	calls := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *calls.iterm {
		t.Error("did not expect iterm.Runner to be called")
	}
	if *calls.terminal {
		t.Error("did not expect terminal.Runner to be called")
	}
	if !*calls.zellij {
		t.Error("expected zellij.Runner to be called")
	}
	if *calls.warp {
		t.Error("did not expect warp.Runner to be called")
	}
}

// TestSpawnTabRoutesToWarp asserts the warp Runner is the one called
// when Detect() resolves to BackendWarp.
func TestSpawnTabRoutesToWarp(t *testing.T) {
	Override = BackendWarp
	t.Cleanup(func() { Override = "" })

	calls := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *calls.iterm {
		t.Error("did not expect iterm.Runner to be called")
	}
	if *calls.terminal {
		t.Error("did not expect terminal.Runner to be called")
	}
	if *calls.zellij {
		t.Error("did not expect zellij.Runner to be called")
	}
	if !*calls.warp {
		t.Error("expected warp.Runner to be called")
	}
}

// TestShellQuoteParity makes sure the re-exported helper matches
// every backend's implementation. All four quote identically.
func TestShellQuoteParity(t *testing.T) {
	cases := []string{"plain", "with space", "with'quote", `back\slash`, ""}
	for _, in := range cases {
		exp := iterm.ShellQuote(in)
		if got := ShellQuote(in); got != exp {
			t.Errorf("spawner.ShellQuote(%q) = %q; want %q", in, got, exp)
		}
		if got := terminal.ShellQuote(in); got != exp {
			t.Errorf("terminal.ShellQuote(%q) = %q; want %q", in, got, exp)
		}
		if got := zellij.ShellQuote(in); got != exp {
			t.Errorf("zellij.ShellQuote(%q) = %q; want %q", in, got, exp)
		}
		if got := warp.ShellQuote(in); got != exp {
			t.Errorf("warp.ShellQuote(%q) = %q; want %q", in, got, exp)
		}
	}
}

// runnerFlags bundles per-backend "was called" flags so routing tests
// can assert on which backend SpawnTab dispatched to without an
// awkward four-return-value tuple.
type runnerFlags struct {
	iterm, terminal, zellij, warp *bool
}

// stubAllRunners replaces every backend's Runner (plus warp's
// OpenURL and WriteScript) with no-op stubs that flip a per-backend
// boolean when called. Restores originals on test cleanup.
func stubAllRunners(t *testing.T) runnerFlags {
	t.Helper()
	var itermCalled, terminalCalled, zellijCalled, warpCalled bool

	oldITerm := iterm.Runner
	iterm.Runner = func(args []string) error {
		itermCalled = true
		if len(args) >= 2 && !strings.Contains(args[1], "iTerm2") {
			t.Errorf("iterm script does not target iTerm2: %s", args[1])
		}
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldITerm })

	oldTerm := terminal.Runner
	terminal.Runner = func(args []string) error {
		terminalCalled = true
		if len(args) >= 2 && !strings.Contains(args[1], `"Terminal"`) {
			t.Errorf("terminal script does not target Terminal: %s", args[1])
		}
		return nil
	}
	t.Cleanup(func() { terminal.Runner = oldTerm })

	oldZellij := zellij.Runner
	zellij.Runner = func(args []string) error {
		zellijCalled = true
		if len(args) >= 1 && args[0] != "action" {
			t.Errorf("zellij argv does not start with 'action': %v", args)
		}
		return nil
	}
	t.Cleanup(func() { zellij.Runner = oldZellij })

	oldWarp := warp.Runner
	warp.Runner = func(args []string) error {
		warpCalled = true
		if len(args) >= 2 && !strings.Contains(args[1], `"dev.warp.Warp-Stable"`) {
			t.Errorf("warp script does not target dev.warp.Warp-Stable: %s", args[1])
		}
		return nil
	}
	t.Cleanup(func() { warp.Runner = oldWarp })

	// warp also has OpenURL and WriteScript — stub them so the routing
	// test doesn't actually fire `open` or touch the filesystem.
	oldOpenURL := warp.OpenURL
	warp.OpenURL = func(string) error { return nil }
	t.Cleanup(func() { warp.OpenURL = oldOpenURL })

	oldWriteScript := warp.WriteScript
	warp.WriteScript = func(string) (string, error) { return "/tmp/flow-warp-stub.sh", nil }
	t.Cleanup(func() { warp.WriteScript = oldWriteScript })

	return runnerFlags{
		iterm:    &itermCalled,
		terminal: &terminalCalled,
		zellij:   &zellijCalled,
		warp:     &warpCalled,
	}
}

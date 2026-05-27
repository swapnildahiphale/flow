package app

import (
	"database/sql"
	"errors"
	"flag"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/spawner"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// loadInjectionText resolves --with / --with-file into the text that
// will be injected as the session's first user message. For
// --with-file we don't embed the file's contents — we synthesize a
// "read instructions at <abs-path>" prompt and let the session use its
// Read tool. That keeps the shell-quoted blob short regardless of file
// size and lets the receiving model reason about the file directly.
func loadInjectionText(fs *flag.FlagSet, withInstr, withFile string) (string, int) {
	passedWith, passedWithFile := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "with":
			passedWith = true
		case "with-file":
			passedWithFile = true
		}
	})
	if !passedWith && !passedWithFile {
		return "", 0
	}
	if passedWith && passedWithFile {
		fmt.Fprintln(os.Stderr, "error: --with and --with-file are mutually exclusive")
		return "", 2
	}
	if passedWith {
		text := strings.TrimSpace(withInstr)
		if text == "" {
			fmt.Fprintln(os.Stderr, "error: --with instruction is empty")
			return "", 2
		}
		return text, 0
	}
	if _, err := os.Stat(withFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: --with-file: %v\n", err)
		return "", 1
	}
	abs, err := filepath.Abs(withFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: --with-file: %v\n", err)
		return "", 1
	}
	return "read instructions at " + abs, 0
}

// openConcurrentDB opens flow.db with a generous busy_timeout so that two
// concurrent `flow do` processes (or two goroutines in the tests) will
// serialize at the SQLite file level rather than failing fast with
// SQLITE_BUSY. The pragma is applied at connection-open time via the DSN
// so every conn in the pool inherits it. Schema creation still runs via
// OpenDB to keep DDL in one place.
func openConcurrentDB(path string) (*sql.DB, error) {
	// Ensure schema exists via the shared OpenDB path.
	pre, err := flowdb.OpenDB(path)
	if err != nil {
		return nil, err
	}
	pre.Close()

	q := url.Values{}
	// 30s is enough to cover realistic bootstraps; tests finish in ms.
	q.Set("_pragma", "busy_timeout(30000)")
	// BEGIN IMMEDIATE acquires a RESERVED lock up-front, so two concurrent
	// `flow do` transactions serialize at tx.Begin() (waiting on the busy
	// timeout) instead of racing to the first write and failing.
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}
	return db, nil
}

// cmdDo flips a task to in-progress, bootstraps a harness session if
// needed (race-free via atomic UPDATE ... WHERE session_id IS ?), and
// spawns a terminal tab to resume it. See spec §6 for the full protocol.
func cmdDo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: do requires a task ref")
		return 2
	}
	fs := flagSet("do")
	fresh := fs.Bool("fresh", false, "discard existing session and re-bootstrap")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "skip per-tool approval prompts in the spawned harness")
	force := fs.Bool("force", false, "open even if the task's harness session is already running elsewhere")
	here := fs.Bool("here", false, "bind THIS harness session to the task (no new tab); requires running inside a known harness session")
	harnessFlag := fs.String("harness", "auto", "agent harness to use: auto, claude, or codex")
	withInstr := fs.String("with", "", "inject `<instruction>` as the first user message after the bootstrap/resume")
	withFile := fs.String("with-file", "", "inject 'read instructions at <path>' (mutually exclusive with --with)")
	// Two-pass parse so the slug positional may appear before OR after
	// the flags: first absorb any leading flags, then take the next
	// non-flag as the slug, then absorb any trailing flags.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: do requires a task ref")
		return 2
	}
	query := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	explicitHarness, err := parseHarnessName(*harnessFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	injectionText, rc := loadInjectionText(fs, *withInstr, *withFile)
	if rc != 0 {
		return rc
	}
	if injectionText != "" && *here {
		fmt.Fprintln(os.Stderr, "error: --with/--with-file cannot be used with --here (no session is spawned to inject into)")
		return 2
	}

	if *here {
		return cmdDoHere(query, *force, explicitHarness)
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	// Live-session guard: if this task's session_id is already running
	// in another claude process (e.g., the user has a tab open for it),
	// try to focus that tab. If the focus succeeds, exit 0 — the user
	// gets switched to the existing tab. If the focus path can't find
	// the tab (different terminal app, different zellij session, etc.)
	// or itself errors, fall back to refusing the spawn so the user
	// knows to switch manually or pass --force. The ps check is
	// best-effort: ps failures fall through silently rather than block.
	//
	// Duplicate detection: if more than one claude process is running
	// the same session UUID (possible via prior --force, or a manual
	// `claude --resume <uuid>` in another tab), warn before focusing.
	// Both processes write to the same session jsonl and can race —
	// the user almost certainly wants to know.
	// Pick the harness for this spawn. If the task has been opened
	// before, task.harness is set and binding; otherwise detect from
	// the current process's ambient harness env (so `flow do` from
	// inside codex picks codex), falling back to claude.
	h, err := harnessForSpawn(task, explicitHarness, *fresh)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	launchOpts := harness.LaunchOpts{
		SkipPermissions: *dangerSkip,
		Inject:          injectionText,
	}
	sessionCtx := harness.SessionContext{WorkDir: task.WorkDir, Env: os.Environ()}
	expectedSessionID := ""
	if hasSessionID(task.SessionID) {
		expectedSessionID = task.SessionID.String
	}
	expectedHarness := ""
	if task.Harness.Valid {
		expectedHarness = task.Harness.String
	}

	if task.Status == "done" && injectionText == "" {
		fmt.Fprintf(os.Stderr,
			"error: task %q is done; edit its status back to backlog or in-progress to reopen it\n",
			task.Slug)
		return 1
	}

	if !*force && task.SessionID.Valid && task.SessionID.String != "" {
		if live, err := h.LiveSessionIDs(); err == nil {
			if n := live[strings.ToLower(task.SessionID.String)]; n > 0 {
				if n > 1 {
					fmt.Fprintf(os.Stderr,
						"warning: %d %s processes are running session %s — both write to the same transcript and may race; close duplicates if unintended\n",
						n, h.Binary(), task.SessionID.String)
				}
				focused, ferr := spawner.FocusSession(task.SessionID.String, h.Binary())
				if focused {
					fmt.Printf("Already open: %s — switched to existing tab\n", task.Slug)
					return 0
				}
				if ferr != nil {
					fmt.Fprintf(os.Stderr, "warning: focus attempt failed: %v\n", ferr)
				}
				fmt.Fprintf(os.Stderr,
					"error: task %q has a live %s session (%s) running elsewhere — switch to that tab, or pass --force to open another\n",
					task.Slug, h.Binary(), task.SessionID.String)
				return 1
			}
		}
	}

	var prompt string
	var prepared harness.PreparedSession
	snapshotNeedsBootstrap := !hasSessionID(task.SessionID) || *fresh
	if snapshotNeedsBootstrap {
		if task.WorkDir == "" {
			fmt.Fprintf(os.Stderr, "error: task %q has no work_dir\n", task.Slug)
			return 1
		}
		p, rc := bootstrapPromptForTask(db, task)
		if rc != 0 {
			return rc
		}
		prompt = p
		prepared, err = h.PrepareFreshSession(sessionCtx, prompt, launchOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: prepare session: %v\n", err)
			return 1
		}
	}

	// Step 2: atomic status flip inside a transaction. Per spec §6 this
	// commit is the durability boundary for the DB bind. Failures before
	// commit roll back with the tx; post-commit fresh bootstrap/spawn
	// failures make a guarded best-effort rollback of the fresh bind,
	// while resume spawn failures preserve the existing session.
	tx, err := db.Begin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: begin tx: %v\n", err)
		return 1
	}
	// If we don't commit by the end, rollback.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Re-read inside the tx so we see the freshest status.
	var curStatus string
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE slug = ?`, task.Slug).Scan(&curStatus); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read task: %v\n", err)
		return 1
	}
	if curStatus == "done" {
		if injectionText == "" {
			fmt.Fprintf(os.Stderr,
				"error: task %q is done; edit its status back to backlog or in-progress to reopen it\n",
				task.Slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "--with on done task %q: reopening as in-progress\n", task.Slug)
	}

	// Decide bootstrap vs resume based on the row we re-read inside the tx.
	// Fresh bootstrap means: either the task has no session_id, or --fresh
	// was passed. In both cases we use the session prepared before the
	// transaction and claim it in the DB via the status-flip UPDATE below.
	var curSessionID, curHarness sql.NullString
	if err := tx.QueryRow(`SELECT session_id, harness FROM tasks WHERE slug=?`, task.Slug).Scan(&curSessionID, &curHarness); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read session binding: %v\n", err)
		return 1
	}
	needsBootstrap := !hasSessionID(curSessionID) || *fresh
	var sessionID string
	if needsBootstrap && !snapshotNeedsBootstrap {
		fmt.Fprintf(os.Stderr, "error: task %q session changed while preparing; retry flow do\n", task.Slug)
		return 1
	}
	if !needsBootstrap && snapshotNeedsBootstrap {
		fmt.Fprintf(os.Stderr, "error: task %q changed concurrently; retry flow do\n", task.Slug)
		return 1
	} else if needsBootstrap {
		sessionID = prepared.SessionID
	} else {
		if curSessionID.String != expectedSessionID || nullStringValue(curHarness) != expectedHarness {
			fmt.Fprintf(os.Stderr, "error: task %q session changed while preparing; retry flow do\n", task.Slug)
			return 1
		}
		sessionID = curSessionID.String
	}

	now := flowdb.NowISO()
	sessionStarted := now
	// 'done' is reachable here only via the --with auto-reopen path above.
	const statusFilter = "status IN ('backlog','in-progress','done')"
	if needsBootstrap {
		// Persist the harness name alongside session_id so future
		// `flow do` invocations read the same adapter — even if
		// they're issued from a different ambient harness or no
		// harness at all.
		//
		// The compare-and-swap predicate below is evaluated against
		// the transaction snapshot, so a concurrent session mutation
		// cannot be overwritten silently. `flow do --fresh` is the
		// explicit lane for replacing an existing fresh bind.
		//
		// Note on cwd: bootstrap spawns the new tab with
		// cwd=task.WorkDir, so the harness writes its transcript
		// under that encoded path. The "session_id is bound to
		// work_dir" invariant holds by construction here — no
		// extra column needed; future resumes spawn at work_dir
		// and the transcript will be found.
		expectedPlaybookSlug := ""
		if task.PlaybookSlug.Valid {
			expectedPlaybookSlug = task.PlaybookSlug.String
		}
		res, err := tx.Exec(
			`UPDATE tasks SET status='in-progress',
				 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
				 session_id=?, session_started=?,
				 harness=?,
			 updated_at=?
			 WHERE slug=?
				   AND `+statusFilter+`
				   AND archived_at IS NULL
				   AND work_dir=?
				   AND kind=?
				   AND ((? = '' AND playbook_slug IS NULL) OR playbook_slug = ?)
				   AND ((? = '' AND (harness IS NULL OR harness = '')) OR harness = ?)
				   AND updated_at=?
				   AND ((? = '' AND (session_id IS NULL OR session_id = '')) OR session_id = ?)`,
			now, sessionID, now, string(h.Name()), now, task.Slug,
			task.WorkDir, task.Kind, expectedPlaybookSlug, expectedPlaybookSlug,
			expectedHarness, expectedHarness, task.UpdatedAt, expectedSessionID, expectedSessionID,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
			return 1
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			fmt.Fprintf(os.Stderr, "error: task %q changed concurrently; retry flow do\n", task.Slug)
			return 1
		}
	} else {
		res, err := tx.Exec(
			`UPDATE tasks SET status='in-progress',
			 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
			 updated_at=?
			 WHERE slug=? AND `+statusFilter+`
			   AND archived_at IS NULL
			   AND ((? = '' AND (session_id IS NULL OR session_id = '')) OR session_id = ?)
			   AND ((? = '' AND (harness IS NULL OR harness = '')) OR harness = ?)`,
			now, now, task.Slug, expectedSessionID, expectedSessionID, expectedHarness, expectedHarness,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
			return 1
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			fmt.Fprintf(os.Stderr, "error: task %q changed concurrently; retry flow do\n", task.Slug)
			return 1
		}
	}
	// Re-select to capture the canonical view.
	row := tx.QueryRow(`SELECT `+flowdb.TaskCols+` FROM tasks WHERE slug = ?`, task.Slug)
	fresh2, err := flowdb.ScanTask(row)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: re-select task: %v\n", err)
		return 1
	}
	task = fresh2
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "error: commit: %v\n", err)
		return 1
	}
	committed = true

	if *fresh && hasSessionID(curSessionID) {
		fmt.Printf("--fresh: discarding old session %s\n", curSessionID.String)
	}
	if task.WorkDir == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no work_dir\n", task.Slug)
		return 1
	}

	// Look up project (may be nil).
	var project *flowdb.Project
	if task.ProjectSlug.Valid {
		p, err := flowdb.GetProject(db, task.ProjectSlug.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: get project: %v\n", err)
			return 1
		}
		project = p
	}

	// Spawn the tab via the active harness adapter.
	//
	// The skill on disk (e.g. ~/.claude/skills/flow/SKILL.md for the
	// claude harness) is whatever was last installed via
	// `flow skill install` / `flow skill update`. To refresh it after
	// upgrading flow, the user runs `flow skill update` manually.
	var command string
	if needsBootstrap {
		if err := h.BootstrapFreshSession(sessionCtx, sessionID, prompt, launchOpts); err != nil {
			rollbackFreshSessionBind(db, task.Slug, sessionID, sessionStarted, expectedHarness, string(h.Name()))
			fmt.Fprintf(os.Stderr, "error: bootstrap session: %v\n", err)
			return 1
		}
		command = prepared.LaunchCommand
	} else {
		// Resume path: the UUID we already have in the DB is what the
		// harness used when it first wrote its transcript.
		command = h.ResumeCmd(sessionID, launchOpts)
	}
	// Env propagation. Flow never injects harness session-id vars
	// (the harness exports those; flow only reads them). It does
	// propagate explicit parent context needed for the child to see
	// the same data/config roots as the allocating process.
	var spawnEnv map[string]string
	addSpawnEnv := func(key string) {
		if value := os.Getenv(key); value != "" {
			if spawnEnv == nil {
				spawnEnv = map[string]string{}
			}
			spawnEnv[key] = value
		}
	}
	addSpawnEnv("FLOW_ROOT")
	addSpawnEnv("HOME")
	if h.Name() == harness.NameCodex {
		addSpawnEnv("CODEX_HOME")
	}
	if err := spawner.SpawnTab(buildTabTitle(project, task), task.WorkDir, command, spawnEnv); err != nil {
		if needsBootstrap {
			// Spawn failed after the fresh bind. Undo both the
			// session_id pre-allocation, harness pin, and status
			// flip so the next `flow do` retries bootstrap fresh.
			// The session-id invariant (in-progress requires
			// session_id) makes "preserve status, drop session_id"
			// illegal — rolling status back to backlog is the only
			// consistent recovery. The user's next `flow do` will
			// flip fresh.
			//
			// The WHERE clause guards against a concurrent `flow do`
			// having mutated session_id between commit and now —
			// only roll back if we still own the session.
			rollbackFreshSessionBind(db, task.Slug, sessionID, sessionStarted, expectedHarness, string(h.Name()))
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Post-spawn bookkeeping, outside the main tx.
	now2 := flowdb.NowISO()
	if !needsBootstrap {
		if _, err := db.Exec(
			`UPDATE tasks SET session_last_resumed = ? WHERE slug = ?`,
			now2, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: record resume: %v\n", err)
			return 1
		}
	}
	if _, err := db.Exec(
		`UPDATE workdirs SET last_used_at = ? WHERE path = ?`,
		now2, task.WorkDir,
	); err != nil {
		fmt.Fprintf(os.Stderr, "warning: bump workdir last_used_at: %v\n", err)
	}

	if needsBootstrap {
		fmt.Printf("Spawned %s (session %s)\n", task.Slug, sessionID)
	} else {
		fmt.Printf("Resumed %s (session %s)\n", task.Slug, sessionID)
	}
	return 0
}

func hasSessionID(id sql.NullString) bool {
	return id.Valid && id.String != ""
}

func nullStringValue(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

func rollbackFreshSessionBind(db *sql.DB, slug, sessionID, sessionStarted, priorHarness, boundHarness string) {
	res, err := db.Exec(
		`UPDATE tasks SET
			session_id        = NULL,
			session_started   = NULL,
			status            = 'backlog',
			status_changed_at = NULL,
			harness           = CASE WHEN ? = '' THEN NULL ELSE ? END,
			updated_at        = ?
		 WHERE slug=?
		   AND session_id=?
		   AND status='in-progress'
		   AND session_started=?
		   AND harness=?`,
		priorHarness, priorHarness, flowdb.NowISO(), slug, sessionID, sessionStarted, boundHarness,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback fresh session bind: %v\n", err)
		return
	}
	if affected, err := res.RowsAffected(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback fresh session bind: rows affected: %v\n", err)
	} else if affected == 0 {
		fmt.Fprintf(os.Stderr, "warning: rollback fresh session bind: no matching in-progress bind for task %q session %s\n", slug, sessionID)
	}
}

func bootstrapPromptForTask(db *sql.DB, task *flowdb.Task) (string, int) {
	playbookSlug := ""
	isFirstRun := false
	if task.PlaybookSlug.Valid {
		playbookSlug = task.PlaybookSlug.String
		var runCount int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM tasks WHERE playbook_slug = ? AND kind = 'playbook_run' AND archived_at IS NULL`,
			playbookSlug,
		).Scan(&runCount); err != nil {
			fmt.Fprintf(os.Stderr, "warning: count playbook runs: %v\n", err)
		}
		isFirstRun = runCount <= 1
	}
	return buildBootstrapPromptForKindV2(task.Slug, task.Kind, playbookSlug, isFirstRun), 0
}

// buildBootstrapPromptForKind dispatches to the right prompt variant
// based on task kind. For kind='playbook_run' the playbook variant is
// used; otherwise the regular task variant. Empty kind (legacy rows
// that somehow didn't migrate) falls through to the regular variant.
//
// The bootstrap prompt is intentionally shell-safe — no single/double
// quotes, backticks, or dollar signs — because it gets shell-quoted
// as a single positional argument to a harness command.
//
// The session id is bound by `flow do` before the interactive tab opens,
// so there is no self-registration step here. The session loads context
// in order: task brief + task updates, then (if any) project brief +
// project updates, then repo instruction files in the work_dir. The flow
// skill enforces this sequence too; the
// bootstrap prompt is a backup in case the skill isn't auto-activated.
// Kept for callers (and tests) that don't track first-run state. New
// callers should use buildBootstrapPromptForKindV2 to opt into the
// first-run variant when relevant.
func buildBootstrapPromptForKind(slug, kind, playbookSlug string) string {
	return buildBootstrapPromptForKindV2(slug, kind, playbookSlug, false)
}

// buildBootstrapPromptForKindV2 is the kind-aware dispatcher with first-
// run awareness for playbook runs. When isFirstRun=true on a playbook
// run, a richer "capture-aggressive" prompt is emitted that nudges the
// session to harvest scripts, edge cases, and decision rules back into
// the live playbook brief / sidecar files.
func buildBootstrapPromptForKindV2(slug, kind, playbookSlug string, isFirstRun bool) string {
	if kind == "playbook_run" {
		return buildPlaybookRunBootstrapPrompt(slug, playbookSlug, isFirstRun)
	}
	return buildTaskBootstrapPrompt(slug)
}

// buildTaskBootstrapPrompt is the prompt for regular tasks.
func buildTaskBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references — load on demand when relevant, not eagerly.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
			"4. Read repo instruction files in your work_dir (AGENTS.md, CLAUDE.md, or equivalent) and any nested instruction files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}

// buildPlaybookRunBootstrapPrompt is the prompt for playbook-run tasks.
// Adds an explicit `flow show playbook <slug>` context-load step and
// frames the run's brief as an authoritative snapshot — the session
// must execute against that snapshot, not re-read the live playbook
// brief (which may drift between runs).
func buildPlaybookRunBootstrapPrompt(runSlug, playbookSlug string, isFirstRun bool) string {
	base := fmt.Sprintf(
		"You are running playbook `%s` as run `%s`. Do ALL of the following in order before executing anything:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works.\n"+
			"2. Run: flow show playbook %s. This shows the playbook's definition and recent runs — context only, not your instructions. Note any files listed under other: — they're sidecar references you can Read on demand if relevant; do not eagerly load them.\n"+
			"3. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files under other: are references for THIS run; load on demand when relevant. The brief is your authoritative instructions for this run — it was snapshotted from the playbook at the moment this run started. Execute against this, not the live playbook brief.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief and every file under updates:. Files under other: are on-demand references.\n"+
			"5. Read repo instruction files in your work_dir (AGENTS.md, CLAUDE.md, or equivalent) and any nested instruction files under subdirectories you will modify.\n"+
			"6. Only then begin executing your brief.\n"+
			"\n"+
			"While executing: if the user adjusts the playbook's procedure during this run (e.g. 'let's always do X', 'change the approach for...', 'this step should also...'), pause and ask via AskUserQuestion whether to persist the change to the playbook's live brief.md so future runs benefit. Options: 'Persist to playbook' (Edit playbooks/%s/brief.md), 'Just this run' (no change to live playbook), 'Both — persist + log a note in playbooks/%s/updates/'. The run's own brief.md is a frozen snapshot — never edit it to change future behavior; that's what the live playbook brief is for. See flow skill §4.13 for the full pattern.",
		playbookSlug, runSlug, playbookSlug, playbookSlug, playbookSlug,
	)

	if !isFirstRun {
		return base
	}

	firstRunAddendum := fmt.Sprintf(
		"\n"+
			"\n"+
			"⚡ THIS IS THE FIRST RUN OF THIS PLAYBOOK ⚡\n"+
			"\n"+
			"The brief was written aspirationally; this run is where the actual procedure crystallizes. Be MORE proactive than usual about capturing back to the live playbook. Specifically:\n"+
			"\n"+
			"- When you write a script, command, or settle on a concrete decision rule that wasn't in the brief: don't wait for the user to ask. Pause and AskUserQuestion whether to capture it. Three capture targets:\n"+
			"    • 'Add to playbook brief' — append/edit the relevant section of playbooks/%s/brief.md so future runs see it inline\n"+
			"    • 'Save as sidecar file' — write to playbooks/%s/<topic>.md (e.g. decision-tree.md, sample-script.md, edge-cases.md). These get surfaced under `other:` in flow show playbook for future runs to load on demand\n"+
			"    • 'Just this run' — apply locally, don't change the playbook (rare; usually means it's run-specific)\n"+
			"- When you discover an edge case or signal worth watching: AskUserQuestion whether to add it to the 'Signals to watch for' section of the live brief.\n"+
			"- Before flow done at the end of the run, AskUserQuestion: 'Capture anything from this run back to the playbook before closing?' Options: 'Yes — walk me through what to capture' / 'No, close out as-is'. The 'walk me through' path: list candidate captures (scripts produced, decisions made, edge cases hit, commands you ended up using) and offer per-item via AskUserQuestion.\n"+
			"\n"+
			"After this run, the playbook should be substantially more concrete than the aspirational brief it started with. That's the point. Treat capture-back as a primary deliverable of the first run, not an afterthought.",
		playbookSlug, playbookSlug,
	)

	return base + firstRunAddendum
}

// buildBootstrapPrompt is a backwards-compat shim for old callers that
// pass only a slug. Now points at the regular-task variant. Tests still
// call this to verify the regular variant.
func buildBootstrapPrompt(slug string) string {
	return buildTaskBootstrapPrompt(slug)
}

// buildTabTitle returns a short iTerm tab title. Project-scoped tasks get
// "<project-slug>/<task-slug>"; floating tasks get just "<task-slug>".
// Titles longer than 30 runes are truncated with a trailing ellipsis.
func buildTabTitle(project *flowdb.Project, task *flowdb.Task) string {
	raw := task.Slug
	if project != nil {
		raw = project.Slug + "/" + task.Slug
	}
	const maxLen = 30
	runes := []rune(raw)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return raw
}

// findTask resolves a user-supplied ref to exactly one non-archived task.
// Exact alias match first, then exact slug match.
func findTask(db *sql.DB, query string) (*flowdb.Task, int) {
	t, err := ResolveTask(db, query, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	return t, 0
}

// cmdDoHere is the `--here` branch of `flow do`. Instead of spawning
// a new tab with a fresh harness session, it binds the current harness
// session (discovered via that harness's session env var) to the named
// task and flips the task to in-progress.
//
// Safety:
//   - Refuses if not running inside the selected harness session.
//   - Refuses if the target task already has a different session_id
//     bound. The constraint guards against silent overwrites that
//     would orphan the prior session. --force overrides.
//   - No-op (idempotent) if the target task is already bound to this
//     same session.
//   - Refuses if the target task is `done`. The user should reopen
//     it explicitly via `flow update task <slug> --status in-progress`
//     first.
//
// The DB write is the only side effect — no terminal spawn, no env
// var injection. Subsequent `flow do <slug>` from elsewhere will
// resume this session via `claude --resume`.
func cmdDoHere(query string, force bool, explicit harness.Name) int {
	// --here only makes sense from inside a harness session. Probe
	// ambient explicitly — defaultHarness's claude fallback would
	// mask the "user isn't in any harness" case.
	var h harness.Harness
	var sid string
	if explicit != "" {
		var err error
		h, err = harnessByName(string(explicit))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		sid = os.Getenv(h.SessionIDEnvVar())
		if sid == "" {
			fmt.Fprintf(os.Stderr,
				"error: --here with --harness %s requires $%s to be set\n",
				explicit, h.SessionIDEnvVar())
			return 1
		}
	} else {
		ambient := ambientHarnessResultForEnv()
		if len(ambient.Matches) > 1 {
			fmt.Fprintf(os.Stderr,
				"error: --here sees multiple harness session env vars (%s); pass --harness to choose one\n",
				strings.Join(ambient.Matches, ", "))
			return 1
		}
		h = ambient.Harness
		sid = ambient.Value
	}
	if h == nil {
		var probed []string
		for _, hh := range allHarnesses() {
			probed = append(probed, "$"+hh.SessionIDEnvVar())
		}
		fmt.Fprintf(os.Stderr,
			"error: --here requires running inside a known harness session; none of %s is set\n",
			strings.Join(probed, ", "))
		return 1
	}
	if err := h.ValidateSessionID(sid); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: $%s is not a valid session id (%v)\n",
			h.SessionIDEnvVar(), err)
		return 1
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	if task.Status == "done" {
		fmt.Fprintf(os.Stderr,
			"error: task %q is done; reopen it first via `flow update task %s --status in-progress` (after which --here is unnecessary — the prior session_id is preserved)\n",
			task.Slug, task.Slug)
		return 1
	}

	// If the task has a harness pinned and it differs from the
	// session this --here would attach, --force is required to
	// switch. The switch is destructive in the soft sense — the
	// prior harness's transcript file stays on disk but flow no
	// longer tracks it (close-out sweep, transcript renderer, and
	// resume path all now point at the new harness). Without
	// --force we refuse so the user makes the swap deliberately.
	if task.Harness.Valid && task.Harness.String != "" && task.Harness.String != string(h.Name()) {
		if !force {
			fmt.Fprintf(os.Stderr,
				"error: task %q is pinned to harness %q but this session is %q — pass --force to switch harnesses (the prior harness's transcript history will no longer be tracked by flow)\n",
				task.Slug, task.Harness.String, h.Name())
			return 1
		}
		fmt.Fprintf(os.Stderr,
			"warning: --force switching task %q from harness %q to %q; prior transcript is orphaned from flow's view\n",
			task.Slug, task.Harness.String, h.Name())
	}

	// Check 1: is THIS session already bound to a different task? Binding
	// it to the target would either orphan the prior task or violate the
	// partial unique index on session_id. --force does NOT override this:
	// a session_id can belong to at most one task by construction, and
	// the user must explicitly release the prior binding (or open the
	// target in a new tab) — silent rebinding loses the original
	// transcript's task association.
	priorBinding, lookupErr := flowdb.TaskBySessionID(db, sid)
	if lookupErr == nil && priorBinding.Slug != task.Slug {
		fmt.Fprintf(os.Stderr,
			"error: this %s session is already bound to task %q. binding it to %q would orphan %q's transcript and is rejected by the session_id uniqueness invariant. --force does not override this.\n"+
				"  to start work on %q in a separate session: flow do %s\n",
			h.Name(), priorBinding.Slug, task.Slug, priorBinding.Slug, task.Slug, task.Slug)
		return 1
	}

	if task.SessionID.Valid && task.SessionID.String != "" {
		if task.SessionID.String == sid {
			// Already bound to this same session — idempotent no-op.
			fmt.Printf("%s already bound to this session (%s)\n", task.Slug, sid)
			return 0
		}
		if !force {
			fmt.Fprintf(os.Stderr,
				"error: task %q is already bound to session %s — pass --force to overwrite (this orphans the prior session)\n",
				task.Slug, task.SessionID.String)
			return 1
		}
	}

	// Invariant validation. Any task with session_id has work_dir
	// == the cwd that session was created at — because the
	// harness's on-disk transcript path is keyed by (cwd, sid),
	// and future `flow do <slug>` resumes spawn at work_dir
	// (GH #59).
	//
	// h.ValidateSession is the honest check: claude's impl stats
	// the expected jsonl path on disk. Comparing os.Getwd() to
	// work_dir would be fooled by chained-cd from inside a claude
	// Bash invocation (the subprocess cwd has nothing to do with
	// where the actual jsonl was written). Codex's impl will
	// no-op since its sessions are sid-only.
	if err := h.ValidateSession(task.WorkDir, sid); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: can't bind this session to task %q — the %s transcript isn't where work_dir says it should be:\n"+
				"  %v\n"+
				"this means %s was started in a different directory than task.work_dir, OR work_dir is set wrong.\n"+
				"pick one of:\n"+
				"  - open it in a new tab (recommended):           flow do %s\n"+
				"  - point work_dir at where %s actually runs: flow update task %s --work-dir <real-cwd>\n"+
				"    (allowed because the new work_dir must match the session's real on-disk location)\n",
			task.Slug, h.Name(), err, h.Binary(), task.Slug, h.Binary(), task.Slug)
		return 1
	}

	now := flowdb.NowISO()
	// Also writes harness — for a previously-unpinned task this
	// is the first bind; for a same-harness --here it's a no-op
	// write; for a --force harness switch it persists the swap
	// alongside the new session_id.
	res, err := db.Exec(
		`UPDATE tasks SET
			session_id      = ?,
			session_started = COALESCE(session_started, ?),
			status          = 'in-progress',
			status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
			harness         = ?,
			updated_at      = ?
		WHERE slug = ?`,
		sid, now, now, string(h.Name()), now, task.Slug,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: bind session: %v\n", err)
		return 1
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		fmt.Fprintf(os.Stderr, "error: task %q not updated\n", task.Slug)
		return 1
	}
	fmt.Printf("Bound %s to this session (%s)\n", task.Slug, sid)
	return 0
}

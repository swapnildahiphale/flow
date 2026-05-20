# Teams morning digest

## What
On each run, fetch Microsoft Teams channel and chat activity since the last
watermark (or 14 days back on first run), classify discussions by topic at the
channel level, hydrate context from the persistent topic store, and produce a
structured morning digest for situational catch-up — what topics were discussed,
what decisions were made, where conversations are heading.

## Why
The user works IST; the US team works during their nighttime. By morning, many
channel discussions have happened that the user should be aware of for situational
awareness — not necessarily action items, but context they'd otherwise miss. The
existing teams-triage routine handles **action items** (what you need to do). This
playbook fills the complementary gap: **situational awareness** (what happened,
what's moving, what to know going into standup).

## Where
work_dir: /Users/Swapnil/workspace/swapnil/flow

## Each run does

### State file locations

All state lives under `~/.flow/playbooks/teams-morning-digest/`:
- `watermark.md` — ISO 8601 UTC timestamp of the last successful run (absent = first run)
- `channels-excluded.md` — user-maintained blocklist (one `- <display name>` per line)
- `topics.md` — MEMORY.md-style index: one line per known topic, fast scan
- `topics/<slug>.md` — per-topic files, lazy-loaded on demand
- `digests/YYYY-MM-DD.md` — daily output, written each run

### 1. Detect first-run mode

Read `~/.flow/playbooks/teams-morning-digest/watermark.md`.

- **First run** if: file is absent, OR file is empty, OR `topics.md` is absent or empty.
- **Normal run**: use the timestamp from `watermark.md` as `window_start`.
- **First run**: set `window_start` = 14 days ago at 09:00 IST, converted to UTC.
  Formula: subtract 14 days from today's date, set time to 03:30 UTC (= 09:00 IST).
- `window_end` = current time (UTC).

If first-run: emit a visible banner in the digest header:
```
⚡ FIRST RUN — 14-day lookback active. All channels surfaced so you can populate
   channels-excluded.md before the next run.
```

If first-run is expected to take a while (>500 messages likely), note this upfront.

### 2. Load the exclusion list

Read `~/.flow/playbooks/teams-morning-digest/channels-excluded.md`.

Parse: lines matching `^- ` — strip the `- ` prefix; the remainder is the channel
display name to exclude. Comparison is case-insensitive substring match.
If the file is absent: no exclusions; continue.

### 3. Fetch messages from Teams

Call `mcp__claude_ai_Microsoft_365__chat_message_search` with:
- `query`: `"*"` (wildcard — broad scope; channel/content filtering happens inline).
- `afterDateTime`: `window_start` as ISO 8601 UTC string.
- `limit`: 25.

**Pagination**: if a page returns exactly 25 results, increment `offset` by 25 and
fetch again. Repeat until a page returns fewer than 25, or you reach 500 total messages
(safety cap — emit a note in the digest if the cap fires).

**On MCP error or timeout:**
- Retry once after a 5-second pause.
- If still failing: write a partial digest (step 11) with a `FETCH FAILED: <error>` banner
  in the header. Do NOT advance the watermark (so the next run retries the same window).
  Skip steps 4–14. Jump to step 15 (release, exit).

### 4. Apply the createdDateTime gate

The MCP's `afterDateTime` filters on `lastModifiedDateTime`, not `createdDateTime` —
reactions, edits, and metadata touches resurface older messages.

Drop any message whose `createdDateTime` is earlier than `window_start`.
Empirically this is ~5–10% of results.

### 5. Group messages by channel / chat

**MCP response shape note (verified by Tier 2 smoke test):** the response does NOT
include a human-readable channel display name. Each message carries:
- `chatId` — opaque GUID (e.g. `19:meeting_<base64>@thread.v2`, or
  `19:<hex>@thread.v2`, or `19:<uuid>_<uuid>@unq.gbl.spaces` for DMs)
- `chatUri` — same identifier wrapped in a `teams://` URL
- `subject` — always null in observed responses; do not rely on it

**Grouping strategy:**

Group all gate-passing messages by `chatId` as the canonical channel key.
Preserve arrival order within each group. Each chatId yields one "channel" group
for the rest of the pipeline.

**Synthesizing a display name for each chatId** (used by the digest in step 11):

The chatId is opaque, so derive a short human label from the *content* of the
messages once classification has run (step 7) — typically the dominant topic
slug plus a "chat" suffix (e.g. `eks-security-groups chat`, `AVA platform chat`).
This label is just for digest readability; the chatId remains the stable
identifier in topic-file frontmatter (`channels:` list).

**DM detection:**

If `chatId` ends with `@unq.gbl.spaces`, this is a 1:1 direct message, not a
group chat. Label it as `DM: <other-participant>` using the `from.displayName`
of the most recent message (best available proxy for the other side of the DM).
DMs go through the same topic classification as group chats — there is no
separate "DMs section" in the digest; topics span both.

**Exclusion list:**

Apply the exclusion list (step 2). Match each chat's *synthesized display name*
(from step 7's classification, available by the time we filter for output) and
its chatId against any entry. Exclusion uses case-insensitive substring matching.
If a chat's display name is unstable across runs (because it derives from content),
the user can also exclude by chatId verbatim — the brief preserves chatId
in `topics/<slug>.md` frontmatter so users can copy it.

Record per-chatId message counts for the digest header.

### 6. Noise pre-filter (per channel)

For each channel group, identify obvious noise messages before LLM classification:
- CI/CD build notifications (bot-posted, contains "BUILD", "PIPELINE", "DEPLOY" keywords, or sender is a bot/service account)
- "X joined the team" / "X left the team"
- Emoji-only messages (no text content)
- Calendar invites, meeting reminders, auto-replies
- Reaction-only events (if the API surfaces them)

Bucket these as `noise`. Do **not** topic-classify them. What remains is the
"substantive" message list for each channel.

Keep a per-channel noise count for the digest noise-summary section.

### 7. Per-channel topic classification

For each channel with at least one substantive message, classify inline using a
**two-pass approach** to keep token cost bounded as the topic store grows.

**Pass 1 — index scan.**
Read `topics.md` once (already loaded, cache). For each channel's substantive
messages, propose candidate topic slugs based solely on the one-line index
entries. Most cases resolve here: a clear new topic gets a new slug; a clear
existing topic gets matched by slug.

**Pass 2 — frontmatter disambiguation (only when needed).**
If Pass 1 leaves ambiguity (the discussion could plausibly belong to 2+
existing topics, OR matches by slug but the one-liner is too thin to confirm
the fit), `Read` the candidate topic files with `limit: 15` — this grabs only
the frontmatter (slug, summary, current-focus, channels, key-people,
last-seen). Use `summary:` to make the final call. Do NOT read the full body
at this stage; that happens in step 8 once a topic is confirmed.

**Drift detection** (applies to existing topics matched in Pass 1 or 2):
compare today's discussion narrative against the matched topic's `summary:`
(if loaded) or its one-line index entry. If the discussion fits the slug but
**diverges meaningfully** from the summary (e.g. `jenkins-security` topic now
discussing k8s migration, not security incidents), set `drifted_from_summary: true`
on the classification output. Drifted topics are queued for the step 15
resolution prompt — never silently rewrite `summary:`.

**Classification output (per topic):**
- `topic_slug`: existing slug or new lowercase-kebab 2–4 word slug
- `is_new`: true if not in topics.md
- `brief_summary`: 1–2 sentences — what happened with this topic today
- `current_focus_line`: one line — where the discussion is heading right now
- `proposed_summary`: only when `is_new` — 1–2 sentences, the stable definition
  of what this topic fundamentally is (e.g. "Migration of the HIX platform
  from VM-based Jenkins to EKS-hosted runners.")
- `key_excerpts`: 2–3 most salient message quotes (truncated to ~120 chars each)
- `mentioned_people`: names / @handles that appeared in the messages
- `has_high_importance`: true if ANY message in this topic carries
  `importance: "high"` in the MCP response. Surface these prominently in the
  digest (step 11 prefixes high-importance excerpts with a `⚡` marker).
- `drifted_from_summary`: true | false (default false; true only for existing
  topics whose narrative has clearly moved off the original `summary:`)

Mark messages as part of topic `noise` only if they do not fit any meaningful topic.

**New topic slug rules:**
- Lowercase, kebab-case, 2–4 words.
- Descriptive of the discussion content, not the channel name.
- Examples: `hix-deploy-eks`, `artifactory-creds-rotation`, `callai-api-launch`.
- Accept the LLM's proposed slug on first appearance; the user can rename the file later.

A single channel may contribute to multiple topics.
A single topic may span multiple channels (common for cross-team discussions).

### 8. Load context for existing topics

For each `topic_slug` that appears in `topics.md` (load topics.md once, cache):
- Read `~/.flow/playbooks/teams-morning-digest/topics/<slug>.md`.
- Use the "Context" and "Key history" sections to enrich the digest narrative for
  this topic. Don't include the raw topic file in the digest — use it as background.

This is lazy-load: only read files for topics that appeared in step 7.

### 9. Hydrate and create new topic files

For each new topic (`is_new = true` from step 7):

1. **Attempt context hydration (optional):** if the MCP returned a resource URL for
   the channel, call `mcp__claude_ai_Microsoft_365__read_resource` with that URL to
   fetch up to 3 days of channel history. Use this to write a richer initial Context.
   If `read_resource` fails or no URL is available, proceed with only the messages
   already fetched — do not block the run.

2. Create `~/.flow/playbooks/teams-morning-digest/topics/<slug>.md`:

```markdown
---
slug: <slug>
first-seen: YYYY-MM-DD
last-seen: YYYY-MM-DD
summary: <from classification's proposed_summary — 1–2 sentences, the STABLE
          definition of what this topic fundamentally is. Rarely rewritten;
          only changes on an explicit rename in step 15.>
current-focus: <from classification's current_focus_line — one line, where
                the discussion is right now. OVERWRITTEN every active run.>
channels:
  - <channel-name>
key-people:
  - <names from key_excerpts if identifiable>
---

## Context
<2–3 sentence longer-form background, derived from classification and any
hydration. What is the thing, why does it matter to this team? This is
narrative context for human readers; the classifier uses `summary:` instead.>

## Key history
- YYYY-MM-DD: <Initial entry summarising today's messages>

## Open threads
- <any pending decision or unanswered question visible in today's messages; else "(none yet)">

## Last activity
YYYY-MM-DD in #<channel-name>: <one-line summary>
```

**Why three time horizons** (`summary:` / `current-focus:` / `Key history`):
- `summary:` is **stable** — what this topic IS. Cheap for the classifier
  to read; only rewritten on explicit user action (rename/split in step 15).
- `current-focus:` is **mutable** — where the discussion is THIS run.
  Overwritten every active run by step 12.
- `Key history` is **append-only** — the full chronology of dated entries.
  Never edited; always grows.

The classifier scans `summary:` + `current-focus:` from frontmatter (cheap)
to make routing decisions; the body is only read when a topic is confirmed
active and needs full context.

3. Append to `topics.md`:
```
- [<slug>](topics/<slug>.md) — <one-line description> | last-seen: YYYY-MM-DD
```

### 10. Detect candidate action items

For each substantive message across all channels, flag as a **candidate action item** if:
- Message is a DM or @mention directed at the user (body contains "Swapnil", "@Swapnil",
  or metadata shows it is a direct message).
- Message contains an explicit ask: "can you", "please", "could you", "need you to",
  "waiting on you", "blocking on".
- Message contains a deadline: "by EOD", "before the release", "by Thursday", "ASAP".

For each candidate: check `flow list tasks --status in-progress` and
`flow list tasks --status backlog` (run these commands once, cache results) and
compare against the candidate to see if a matching task exists. Label as
`[tracked as <slug>]` or `[not tracked]`.

**Surface only. Never auto-create.** Collect into `action_items_list` for step 11.

### 11. Compose and write the digest

Compute all timestamps in IST for display (UTC + 5:30). Display format: `YYYY-MM-DD HH:mm IST`.

Write `~/.flow/playbooks/teams-morning-digest/digests/<YYYY-MM-DD>.md` with
this exact structure. Also echo the full content to terminal.

```markdown
# Teams morning digest — <YYYY-MM-DD>

> **Window:** <window_start IST> → <window_end IST>
> **Messages reviewed:** <total after createdDateTime gate>  
> **Channels touched:** <non-excluded channels with at least one substantive message>  
> **Topics active:** <topics seen this run that existed before>  
> **Topics new:** <new topics created this run>

---

## Active topics

### <Topic display name> (`<slug>`)
_Channels: #<ch1>, #<ch2> | People: <name1>, <name2>_

<2–4 sentence narrative summary. What happened today with this topic?
What changed vs. prior state (use Key history context if loaded)?
What decision was made or what is still open?>

**Excerpts:**
- "<quote1>" — <Sender>, <time IST>
- "<quote2>" — <Sender>, <time IST>

(Prefix any excerpt from an `importance: "high"` message with `⚡` so it stands
out. Topics where `has_high_importance: true` may also benefit from a `⚡` next
to the topic title.)

[→ Topic details](~/.flow/playbooks/teams-morning-digest/topics/<slug>.md)

---

## New topics this run

<For each new topic:>
- **`<slug>`** — <one-line description> (first seen in #<channel>)

<If none:>
_(none)_

---

## Noise summary

<For each channel where all or most messages were noise:>
- **#<channel>**: <N> noise messages — <brief: "CI builds, 2 join events">

<If no noise or it was negligible:>
_(nothing notable)_

---

## Action items detected

<For each candidate action item:>
- **[tracked as `<slug>`]** / **[not tracked]** — _<Sender>_ in #<channel>, <time IST>
  > "<excerpt>"

<If none:>
_(none detected)_

---

## Candidate KB updates

<List durable facts about the org, products, processes, or business that the digest revealed:>
- `org.md`: <fact>
- `processes.md`: <fact>

<If none:>
_(none)_
```

### 12. Update existing topic files

For each topic that was **not new** this run but had activity (appeared in step 7):
1. Read the topic file.
2. **Append** a new dated entry to "Key history" — after the last existing entry:
   ```
   - YYYY-MM-DD: <one-line summary of what happened today>
   ```
3. **Update** "Last activity" line (the entire last line of the file — overwrite).
4. **Update** frontmatter:
   - `last-seen: YYYY-MM-DD` (today).
   - `current-focus:` — **overwrite** with the classification's
     `current_focus_line` from step 7. This is the mutable field; it should
     reflect today's discussion direction, not yesterday's.
   - Add any new channels to `channels:` if not already present.
   - Add any new people to `key-people:` if applicable.
5. **Do NOT touch `summary:`.** It is the stable identity of the topic and
   only changes via the explicit step 15 drift-resolution prompt.

DO NOT edit or rewrite any prior "Key history" entries. The history list is
append-only. New topic files were already written in step 9 — do not
re-process them here.

**Drift queue:** if step 7 flagged this topic with `drifted_from_summary: true`,
add it to the `drifted_topics` list to be surfaced in step 15. Do not act on
the drift here — the user owns the rename/split call.

### 13. Update topics.md index

For each topic that appeared this run:
- Find its line in `topics.md` and update the `last-seen: YYYY-MM-DD` date in-place.

After updating, scan all lines: mark any topic whose `last-seen` is 30+ days ago with `⚠ stale`
appended to its line (if not already marked). Do NOT remove stale lines.

### 14. Advance the watermark

Write `window_end` (UTC, ISO 8601) as a single line to
`~/.flow/playbooks/teams-morning-digest/watermark.md`.

Do this **only after** the digest file has been successfully written to disk.
If the digest write failed for any reason, do not advance the watermark.

### 15. End-of-run interactive prompts

**Drifted topics** (if any were flagged with `drifted_from_summary: true` in step 7):

For each drifted topic, show the user:
- The topic's current `summary:` (what it was about).
- A one-line description of where today's discussion went.

Use `AskUserQuestion` (header: "Topic drift") with options:
- **"Rename topic"** — prompt for a new slug + new `summary:`. Rewrite the
  topic file's frontmatter (`slug:`, `summary:`); rename the file on disk;
  update the corresponding line in `topics.md`. Add a `Key history` entry:
  `- YYYY-MM-DD: Renamed from <old-slug> — <reason>`. Existing chronology
  is preserved.
- **"Split into new topic"** — create a new topic file (full step 9 path)
  with a fresh slug and summary capturing the new direction. In the old
  topic's `Key history`, append: `- YYYY-MM-DD: Discussion forked to
  [<new-slug>](<new-slug>.md) — future activity tracked there.` Mark the
  old topic with `⚠ forked` in `topics.md`. Today's run continues to log
  in the old topic; future runs will classify into the new one.
- **"Keep as-is"** — do nothing. Drift is acknowledged but the topic
  identity remains useful. The flag is not persisted; future runs
  re-evaluate.

**Candidate KB updates** (if any were identified in step 11):

Use `AskUserQuestion` (header: "KB updates?"):
- "Approve all" — append each to the matching `~/.flow/kb/<file>.md` in scoop-mode
  format: `- YYYY-MM-DD — <paraphrase>`.
- "Review one by one" — AskUserQuestion per item with "Add it" / "Skip this one".
- "Skip all" — do nothing; the candidates are preserved in the digest file.

**Untracked action items** (if any `[not tracked]` items were detected in step 10):

Use `AskUserQuestion` (header: "Create tasks?"):
- "Create tasks for untracked items" — for each untracked item, run the §4.2
  task-intake interview from the flow skill. Pre-fill the What field from the
  action-item excerpt.
- "Skip — I'll handle manually" — stop here.

**After all prompts: exit WITHOUT calling `flow done`.**
Daily digest runs should not trigger the close-out KB sweep — it's expensive and
adds noise to the KB for routine runs. The run-task accumulates in backlog and can
be archived manually or via a future cleanup routine.

## Out of scope
- Sending, replying, or reacting on Teams (read-only, always).
- Auto-creating flow tasks for detected action items — surface only; user confirms.
- Per-message topic classification — channels are the unit, not individual messages.
- Scheduled / automated execution — manual trigger only for v1.
- Flow-UI integration for digest viewing — future task once digest format stabilizes.
- Auto-merging of overlapping topics — manual cleanup only.
- Auto-deletion of stale topics — manual archival only.
- Cross-watermark coordination with teams-triage — both fetch independently; acceptable for v1.

## Signals to watch for
- **MCP timeout on first run**: 14-day lookback may pull 500+ messages. If MCP timeouts
  cluster, re-run — the watermark isn't advanced on failure so the run is retryable.
  Consider running during off-peak hours for the first run.
- **Topic explosion (>50 topics in week 1)**: classification is too granular. Tune the
  step 7 prompt to merge closely related topics more aggressively.
- **Noise ratio >80% on any channel**: that channel likely belongs in channels-excluded.md.
  The digest surfaces high-noise channels explicitly — use that list to decide.
- **Action-item false positives**: if the section is full of noise, tighten the detection
  heuristics in step 10 by requiring both an explicit ask AND a user mention together.
- **Watermark drift after multi-day skip**: the catchup run may be slow but is correct —
  the window just covers the gap. No manual watermark reset needed.
- **Topics.md growing stale**: scan `⚠ stale` markers periodically and archive or merge
  inactive topics manually (edit topics.md, move the file to a backup location).
- **Drift prompts firing every run**: if the same topic keeps getting flagged as drifted
  but the user keeps picking "Keep as-is", the `summary:` is too narrow. Tighten the
  step 7 drift heuristic, or prompt the user to rename — the persistent flag is a
  signal that the stable definition no longer matches reality.
- **Classifier reading too many topic files (Pass 2 frequency)**: if Pass 2 fires for
  most channels, `topics.md` one-liners are not descriptive enough. Rewrite them to
  be more disambiguating (manual edit, or fold into a future refinement pass).
- **Channel display names diverging across runs**: because display names are
  synthesized from content (step 5), the same chatId may get different labels
  across runs as discussion shifts. This is fine for digest readability but means
  display names are NOT stable identifiers — use chatId for exclusion-list entries
  and `channels:` frontmatter, never the synthesized label alone.
- **DM volume spikes**: DM topics can dominate the digest if a single 1:1
  discussion gets lengthy. The brief intentionally classifies DMs alongside
  group chats — if you find you want DMs separated, add `unq.gbl.spaces`-suffixed
  chatIds to `channels-excluded.md` for the noisy ones.

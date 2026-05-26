# [$TICKET_KEY] $TICKET_SUMMARY

## What
Investigate root cause for $TICKET_KEY and propose a resolution.
DO NOT auto-remediate.

## Ticket
- URL: $TICKET_URL
- Reporter: $REPORTER
- Priority: $PRIORITY
- Components: $COMPONENTS
- Created: $CREATED
- Description (verbatim, truncated to 2000 chars):
  > $TICKET_DESCRIPTION

## Investigation playbook

Read `~/.flow/oc-investigator/config.yaml` first; honor `enable_*` and
`output.*` toggles.

Use the Jira skill (loaded into this session) for all Jira operations:
fetching the full ticket and comments, searching past tickets, posting
the final comment. Use available domain skills (kubectl, repo tooling)
as needed for the other phases.

PHASE 1 (sequential, main session):
  Fetch full ticket and comment history for $TICKET_KEY. Extract:
  affected service name, affected env, error signatures, timestamps.

PHASES 2–4 (parallel — dispatch as subagents in a SINGLE message):
  • prior-tickets subagent:
      Search past OC tickets (last 30d) for matching error signatures
      or affected component. Return at most $PRIOR_TICKETS_LIMIT most
      relevant. Skip the subagent entirely if enable_prior_tickets=false.
  • repo-context subagent:
      Locate the affected service's repo, scan recent commits/PRs and
      relevant code paths. Skip if enable_repo_context=false.
  • k8s-context subagent:
      Pull logs/events for affected pods on the named env. Skip if
      enable_k8s_context=false or env is not named in the ticket.
  Each subagent reports full findings — no length cap. Include evidence
  pointers (file paths, line numbers, log timestamps, commit hashes,
  prior ticket keys) so the main session can cite specifics in the
  synthesis.

PHASE 5 (synthesis, main session):
  Build root-cause hypothesis with evidence; flag weak/missing evidence.
  Draft proposed resolution in whatever shape fits:
    • Code change (PR-shaped — files + direction)
    • Manual ops steps (commands, restarts, config flips)
    • Infra/config tweak (pool size, resource bump, liquibase unlock)
    • Escalation note (which team + why)
  Note risks / blast radius for any ops steps.

PHASE 6 (parallel outputs):
  • If output.jira_comment=true: post a concise comment on $TICKET_KEY
    (<300 words) — symptom → root cause → resolution. ASCII diagram if
    it clarifies.
    Jira-comment hygiene (strict — these rules are non-negotiable):
      - NEVER use the word "flow" in the comment. Not "flow task",
        not "flow update", not "flow session", not "see flow" — the
        tool that produced this investigation is invisible to Jira
        readers and must stay invisible.
      - NEVER reference any local-only paths: no `~/.flow/...`, no
        `/Users/...`, no "more details at <local path>", no laptop
        file or directory names. Jira readers don't have your
        filesystem.
      - NEVER mention the spawned session, the runner, the
        LaunchAgent, automation, or that this comment was
        auto-generated. The comment must read as a standalone
        investigation summary authored by the on-call engineer.
      - Cite shared-system evidence only: Jira keys, Confluence pages,
        Bitbucket commits/PRs, Argo CD URLs, kubectl resource names,
        Grafana dashboard links, log timestamps. Anything a teammate
        could open from their own machine.
  • If output.flow_update=true: save a flow update at
    `~/.flow/tasks/$TICKET_KEY_LC/updates/YYYY-MM-DD-investigation.md`
    with full detail, alternatives considered, code/log excerpts.
    (This file IS local-only — full detail lives here, not in Jira.)

STOP. No auto-remediation, no ticket transitions, no pages.

## Out of scope
- Applying any fix
- Closing or transitioning the ticket
- Paging anyone

---
*Load the flow skill and the Jira skill before starting. Both are
discoverable via the standard skill-loading mechanism.*

# ADR-6003: The ticket is the record of each run

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes what Graft writes on a request's ticket, what an agent
reports, how a stage that failed is tried again, or how the finished work
is shown to its approvers; and the admin who sets the program file.

## Purpose

Settles where the record of a request's work lives, how it is written as
the work goes, how it shows the finished work, and how a stage that failed
tells the next attempt what it hit.

## Context

Graft wrote the plan into the ticket's description and left a few
one-off comments ("Trying again...", "Round 2 of the plan is ready...",
"Released: ..."). Everything else a run did lived in Graft's own log on
its volume, which the people who decide and review the work do not read.
To check that a finished form matched what was asked and approved, an
approver had to sign in to the app, find the screen, and set up data. A
stage that failed and was tried again started from nothing, and could walk
into the same wall.

An agent holds no forge credential (ADR-2006, `AGENTS.md`), so it cannot
write the ticket itself. The forges notify a ticket's people of a new
comment, and of an edit of none.

## Decision

- **One comment a run, edited in place.** A run is one pass of one stage
  from its start: the investigation of round 1, each revision, each build,
  each landing, and each release. Each gets exactly one comment on the
  ticket, written when the run starts (or when its ticket is written, for
  the first investigation) and edited as it goes, until it ends. A plan's
  ask is the end of its round's run, and the gate is a step of the build's.
  A stage held or run again after a restart goes on in its run.
- **What it shows.** At the top, short: what the run is, its round and
  attempt, whether it runs, ended, or stopped, how long, its agent prompts
  and turns, its agent session, and what it is doing now; its budget and
  spend only where `ticket.cost` shows dollars (ADR-6004). Then what proves each criterion
  of the plan, the screenshots, why it stopped, and what comes next. Every
  step, Graft's and the agent's, is folded below it: clones, steps,
  findings, tests and their results, commits with their SHAs, the gate, and
  each pipeline and its state.
- **The agent writes files; Graft writes the forge.** The agent reports
  in a folder beside its clone: progress as JSON lines, and screenshots
  with a manifest (`graft report` writes both, and calls nothing). Graft
  reads them through the workspace, as data, strictly and capped; sweeps
  every line for secrets; and makes every comment, edit, and upload with
  its own token.
- **Edits are throttled.** A run's comment is edited only when a step
  waits, and at most once in 30 seconds; its start, its end, and its
  failure are written at once. Only the comment's creation notifies anyone.
- **Screenshots of the finished work, as proof.** A plan says which screens
  its build changes and how they are shown (`build.ui`). Its builder takes
  before screenshots with the repository's own end-to-end tooling, in the
  sandbox, with the repository's test or demo data; its prover takes them
  after, and maps each criterion of the plan to a screenshot or the named
  test that proves it. A build lands only when every criterion is proven
  and a change people see has a screenshot after it, or an approver waived
  it (ADR-6005).
- **Never production data.** Screenshots come only from the sandbox's dev
  loop with fixtures; the manifest must say what data they show. A check
  that looks at an environment after a release uses Test with demo data,
  never production.
- **Every upload is checked.** A screenshot is taken only when its own
  bytes are a PNG, JPEG, GIF, or WebP picture, under 5 MB, at most 12 a run
  and 25 MB together, named once, in the screenshots folder; and when OCR
  reads it and the sweep finds no secret in it. One that fails is left off,
  and the comment says which and why.
- **A failure is written into its run's comment.** The cause, where it
  stopped, what it tried, what its agent said stopped it, the end of what
  it reported, and how to retry. No separate failure comment.
- **The next attempt reads what the last hit.** The failures of a stage's
  runs since its last success go into the next attempt's prompt. A retry's
  comment links the attempt it retries and carries its failure, folded; a
  revision's comment says which round it revises and what it answers.
- **A program setting.** `ticket.runs` and `ticket.screenshots`, both on
  unless the program file says otherwise. Off, Graft writes the comments
  it wrote before.
- **The catalog and the page link each run.** The `change_request` record
  carries `runs`, and the page lists them under Details. The plan stays in
  the ticket's description.

## What this means for the code

`store.Run` is the record, one row a run in the `runs` table, and
`Doc.Runs` lists them. `runs.go` in `internal/desk` opens, picks up, and
ends a run (`begin`, `after`, `finishRun`), writes its comment
(`flush`, `due`, `editEvery`), and hands failures on (`failed`,
`earlierFailures`, `failuresBrief`). `render.go` is the comment.
`report.go` reads the agent's report and takes its screenshots
(`readReport`, `gather`, `checkShots`). `forge.Forge` gained
`EditComment` and `Attach`; `CommentIssue` answers the comment it wrote.
`cmd/graft/report.go` is the agent's `graft report`. A retry of a build
gives the builder its rounds back.

## Verification

In `internal/desk/runs_test.go`:
`TestEachRunKeepsOneCommentWrittenOnceThenEdited`,
`TestARunsCommentIsEditedAtMostOnceAWindowAndAtOnceAtItsEdges`,
`TestAFailedBuildIsWrittenIntoItsRunsCommentAndItsRetryIsToldWhatItHit`,
`TestAScreenshotThatDoesNotPassIsLeftOffTheTicket`,
`TestAChangePeopleSeeLandsOnlyWithAScreenshotAfterItOrAWaiver`,
`TestAProgramThatTurnsRunsOffWritesTheCommentsItDidBefore`, and
`TestTheAgentNeverSeesTheForgeToken`. In `internal/forge/gitlab`:
`TestARunsCommentIsWrittenOnceAndThenEdited` and
`TestAFileIsAttachedThroughTheProjectsUploads`. In `cmd/graft`:
`TestTheReportIsWrittenBesideTheCloneInTheShapesGraftReads`. In
`internal/program`: `TestEachRunWritesOnTheTicketUnlessTheProgramSaysNot`.

## Consequences

- A ticket holds a comment a run, and its people are notified once a run.
- What a run spent is the runtime's report, kept by ADR-6004; the comment
  shows it only where `ticket.cost` is on.
- GitHub's API takes no file for a comment, so on GitHub the screenshots
  are checked and named, not shown, and the comment says so.
- Each screenshot costs one OCR read in the sandbox, and a plan that
  changes screens may cost a builder round when the evidence set is
  missing.
- A run's steps since its last edit are lost from the comment if Graft
  stops in that window; the next edit writes what the store kept.

## Related

- [ADR-6001](6001-a-failed-stage-waits-for-a-retry.md)
- [ADR-6002](6002-an-agent-runs-budget-is-its-stages.md)
- [ADR-6004](6004-each-agent-run-keeps-what-it-spent.md)
- [ADR-5006](5006-a-revision-revises-the-round-before.md)
- [ADR-2006](2006-an-approvers-files-join-the-next-round.md)
- Platform standard 2026.1119.1418:
  `the-runner-gates-the-agent-s-work-and-returns-failures-with-` (the gate
  holds the evidence set, and a failure goes back with its evidence),
  `route-verdicts-by-failure-class-the-same-failure-twice-is-a-` (a stopped
  run records what it tried and what it printed, and the next attempt reads
  it), `one-structured-review-pass-that-executes-every-criterion-and`
  (every criterion shows its proof), `no-silent-failures`,
  `production-data-never-reaches-a-lower-environment-unmasked`, and
  `every-commit-traces-to-a-work-item-that-existed-first`. New ground: the
  work item as the live record of each agent run, and screenshots of the
  finished work as acceptance proof on it.

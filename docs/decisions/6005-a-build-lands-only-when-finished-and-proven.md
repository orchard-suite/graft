# ADR-6005: A build lands only when it is finished and proven

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes how a build is judged before it lands, what the builder or
the prover is told, or how a stalled build is retried; and the admin who
sets the program file and a repository's `graft.yaml`.

## Purpose

Settles when a build may land: only when its builder says, in a signal
code reads, that the work is finished, and every criterion of its plan
carries proof or an approver's waiver. Settles what happens to work that
is not finished, and how proof gets a budget that building cannot spend.

## Context

A builder ran to its budget after 57 turns. The wind-down committed two
commits, and the agent wrote that the work was not finished: a test suite
never ran, generated files were not regenerated, and two of its browser
tests failed. Its commit bodies said so too. The gate checked only a clean
tree, the commit rules, and `make check`, which that repository does not
have. A second round reworded two subjects and deleted a stray
`__pycache__`, the gate passed, and Graft landed the unfinished work on
main. No criterion had any proof: proofs were asked for only where a plan
named screens, and the builder that would have proved them had spent the
whole budget building.

Words in an agent's prose are data and decide nothing (`AGENTS.md`), so
Graft could not read "not finished" from them. The wind-down is the run
that reached its budget; `runAgent` reported it as a run that ended well.

## Decision

- **The builder says it is finished, in a file.** `graft report done`
  writes that it is; `graft report left` writes one thing left, with why.
  Graft reads `finish.json` strictly. Anything left makes the work
  unfinished whatever else it says. A builder that stops without saying
  either is asked by the gate, as a gate problem, within its rounds.
- **A run stopped at its budget or its time limit is never finished**,
  whatever its wind-down says. `runAgent` now answers that it wound a run
  down.
- **Every criterion carries proof, or an approver's waiver.** A proof is
  the test that shows it or a screenshot that shows it. A change people
  see, by the plan's screens or by the kinds of file it changes, needs at
  least one screenshot after it that passes Graft's checks; a reason
  there is none does not stand in for one. Screenshots say what data they
  show. With `ticket.screenshots` off, screenshots are asked of nobody and
  tests still prove each criterion.
- **Proof has a budget of its own.** After the gate passes on work its
  builder says is finished, a prover runs in its own session, in the same
  sandbox, with `limits.budget.prove` (a fifth of `spend_usd` unless set).
  It proves only what is still unproven, changes no tracked file, and
  commits nothing; what lands is the builder's commit as the gate passed
  it. A builder that proved everything as it went needs no prover. A
  prover that finds a criterion does not hold says so with `left`, and the
  build is unfinished.
- **Unfinished work stalls, and is kept on the request.** The build stage
  stalls (ADR-6001). The work's commits cross to the runner's clone, as
  `refs/graft/agent`, and never to the forge. The run's comment says
  exactly what is left and why, and up to which commit the work is kept.
  `store.Unfinished` keeps the list, the part left (the build, or only its
  proof), the kept commit, and what was already proven.
- **A retry goes on from that work.** The builder is given the kept commits
  and the list. Where only proof is left, the builder does not run again:
  the prover does, and what earlier attempts proved stands.
- **An approver may waive a proof** by commenting `/graft waive <number>
  <why>` or `/graft waive screenshots <why>` on the ticket, then retrying.
  The person who asked never waives. The comment shows each waiver, by whom
  and why.
- **The builder runs the repository's own checks in its loop.** It is told
  the repository's commit rules (from release-damnit's config, where the
  repository uses it) and to run `release-damnit check-commits
  origin/main..HEAD`, and the repository's own check: `check` in its
  `graft.yaml`, else `make check`. The gate runs that same check.
- **Build output does not fail the gate.** Before it asks for a clean tree,
  the gate removes untracked `__pycache__`, `TestResults`, test reports,
  and coverage output, and says what it removed. Nothing tracked is
  touched.

## What this means for the code

`internal/desk/finish.go`: `Finish`, `readFinish`, `buildLeft`,
`continueBrief`, `gaps`, `seenChange`, `prove`, `proveBrief`,
`sweepLitter`, `checkCmd`, `checksBrief`, `prepare`, and the waiver
(`readWaiver`, `waive`). `build.go`: `build`, `builder`, and `proven`.
`report.go`: `gather` and `checkShots` replace `takeShots` and the gate's
`evidenceProblem`. `runAgent` answers `wound`. `takeCommits` takes a named
commit. `store.Unfinished`, `store.Waiver`, `Request.Unfinished`,
`Request.Waived`, `Run.Waived`, `store.SpendProve`.
`program.Budget.Prove`; `program.RepoFile.Check`. The agent's
`graft report done` and `left`. The catalog's build cost holds the
prover's.

## Verification

In `internal/desk/finish_test.go`:
`TestABuildStoppedAtItsBudgetKeepsItsWorkOnTheRequestAndNeverLands`,
`TestABuildLandsOnlyWhenItsBuilderSaysItIsFinished`,
`TestTheBuilderIsToldTheRepositorysRulesAndTheGateClearsBuildOutput`,
`TestBuildOutputIsKnownByTheFolderItSitsIn`,
`TestAWaiverNamesACriterionOrTheScreenshotsAndSaysWhy`, and
`TestTheSandboxGetsTheBrowserTheRepositorysPlaywrightWants`. In
`internal/desk/runs_test.go`:
`TestAChangePeopleSeeLandsOnlyWithAScreenshotAfterItOrAWaiver`. In
`internal/program`: `TestEachAgentStageHasABudget` and
`TestARepositoryNamesItsOwnCheck`. In `cmd/graft`:
`TestTheBuilderSaysItIsFinishedOrWhatIsLeftInTheFinishFile`.

## Consequences

- Every build costs a prover run unless its builder proved each criterion
  itself; the prover's spend counts in the build's cost.
- A builder that does its work but forgets to say so costs a gate round.
- A proof by test is the agent's word on which test proves a criterion; the
  gate runs the repository's check, which runs it only where the check
  holds that test. A repository whose check cannot run its browser tests in
  the sandbox still proves those criteria by screenshot or by waiver.
- Kept work lives in the runner's clone on Graft's data volume. A volume
  lost loses it; a retry then starts from main and says so.
- A change people see is known by its plan's screens, or by the kinds of
  file it changes. A change to a stylesheet nobody can reach in the sandbox
  waits for a waiver.

## Related

- [ADR-6001](6001-a-failed-stage-waits-for-a-retry.md)
- [ADR-6002](6002-an-agent-runs-budget-is-its-stages.md)
- [ADR-6003](6003-the-ticket-is-the-record-of-each-run.md)
- [ADR-6006](6006-only-a-green-pipeline-is-a-release.md)
- Platform standard 2026.1119.1752:
  `the-runner-gates-the-agent-s-work-and-returns-failures-with-` (the agent's
  word is never taken: here its word that it is finished is required as
  well as the gate),
  `one-structured-review-pass-that-executes-every-criterion-and` (every
  criterion shows its proof),
  `hold-every-builder-to-one-bar-and-count-a-check-only-where-i` (the
  repository's own check, named in its file, is the gate's),
  `stop-a-failed-agent-stage-for-a-retry-never-end-the-request`,
  `keep-a-request-as-one-unit-asked-landed-and-undone` (main holds only
  what was finished),
  `agents-deliver-their-own-verified-work-except-infrastructure`,
  `make-the-work-item-the-live-record-of-every-agent-run`, and
  `no-silent-failures`. New ground: a finish signal the builder must give,
  proof as its own budgeted run, and an approver's waiver of one proof.

# ADR-5002: A plan is a brief for its approvers and a build plan for its builder

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes what the investigator writes, how a plan is shown on the
ticket, in the hub, or on Graft's page, or what the builder is given.

## Purpose

Settles what a plan is, and which part each reader reads.

## Context

A plan was one document in six parts: what was understood, the changes by
path, what is left alone, the tests, the size, and the tiers. It was written
for the builder, and the approvers read the same thing on the ticket and in
the hub: a wall of file-by-file changes. An approver could not tell, in the
time they had, what the request was, what would change for the people who
use the system, what was left out, and what only they could settle.

## Decision

- Every plan is two documents, written by the investigator in one
  `plan.json`: `brief` and `build`.
- **The brief** is for the approvers: what was asked and the problem under
  it, what will change as people will see it, what will not and why, the
  judgment calls only they can settle with a recommendation, the claims
  checked and what was found, how the change fits and what could break,
  and a recommendation in one line. It names behaviour, never files, and
  is read in two minutes.
- **The build plan** is for the builder: the kind of change, the problem it
  solves, the steps, what must hold when it is done, the tests that fail
  first, the rules it keeps, and every file it changes. It is the contract
  the fences hold the build to.
- The brief leads everywhere a person decides: the ticket's description,
  the top of the plan's run in the hub (a `markdown` input, shown open),
  and the request's details on Graft's page. The build plan is folded
  beneath it in each.
- Code holds the brief to its shape, as it holds the verdict: every part
  present, a recommendation of approve, approve with changes, or decline,
  no file the build changes named where an approver reads what will
  happen, every claim found true, false, or partly backed by evidence that
  exists, at most 6,000 characters, and, on a round that answers its
  approvers, a list of what changed.

## What this means for the code

`store.Plan` is `Brief` and `Build`. `checkPlan`, `checkBrief`, and
`checkBuild` in `internal/desk/investigate.go` are the one check of a plan.
`briefText` and `buildText` in `internal/desk/brief.go` are the one way each
is written as Markdown. The lane action's inputs put `brief` first and fold
`plan`. A plan kept before this record is its build plan, with no brief.

## Verification

- `TestThePlanIsABriefForItsApproversAndABuildPlanForItsBuilder` and
  `TestABriefIsHeldToItsShape` in `internal/desk`.
- `TestALanesRunLeadsWithTheBriefAndFoldsTheBuildPlan` in `internal/hub`.
- `TestAPlanKeptBeforeBriefsBecomesItsBuildPlanAndItsNoteItsThread` in
  `internal/store`.

## Consequences

- An approver decides from the brief alone, and opens the build plan only
  when they want the detail.
- The investigator writes more, and a plan that does not hold to the shape
  goes back to it once with the reason.
- The run's `reason` in the estate's approvals inbox begins with the brief.

## Related

- [ADR-5003](5003-a-request-is-evidence.md)
- [ADR-2005](2005-approvers-answer-a-plan-as-rounds.md)
- Platform standard 2026.1107.1839:
  `an-exploration-dossier-that-separates-facts-from-hypotheses`,
  `run-implementation-unattended-from-the-specification-alone`.

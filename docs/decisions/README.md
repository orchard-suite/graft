# Decisions

This folder is Graft's record of decisions: one decision per record, and
the reasons behind it.

| Prefix | Category |
|---|---|
| 0xxx | Governance |
| 1xxx | Infrastructure |
| 2xxx | Security |
| 3xxx | Data |
| 4xxx | Compute |
| 5xxx | Development |
| 6xxx | Operations |

## Infrastructure

| Record | Title | Status | Last updated |
|---|---|---|---|
| [1001](1001-package-caches-outlive-a-sandbox.md) | Package caches outlive a sandbox; nothing else does | Accepted | 2026-11-19 |

## Security

| Record | Title | Status | Last updated |
|---|---|---|---|
| [2001](2001-lanes-held-by-members.md) | A lane is held by the members its program names | Accepted | 2026-11-18 |
| [2002](2002-plan-runs-asked-by-graft.md) | A plan's run is asked by Graft for the person | Accepted | 2026-11-18 |
| [2003](2003-deciders-read-what-they-decide.md) | Whoever decides a request's plan reads the request | Accepted | 2026-11-18 |
| [2004](2004-only-a-clear-defect-ships-undecided.md) | Only a clear defect ships without a decision | Accepted | 2026-11-18 |
| [2005](2005-approvers-answer-a-plan-as-rounds.md) | A plan's approvers answer it, and each answer is its next round | Accepted | 2026-11-19 |
| [2006](2006-an-approvers-files-join-the-next-round.md) | An approver's files on the ticket join the next round | Accepted | 2026-11-18 |

## Development

| Record | Title | Status | Last updated |
|---|---|---|---|
| [5001](5001-app-paths-are-context.md) | An intake app's paths are where to start, not a fence | Accepted | 2026-11-18 |
| [5002](5002-a-plan-is-a-brief-and-a-build-plan.md) | A plan is a brief for its approvers and a build plan for its builder | Accepted | 2026-11-18 |
| [5003](5003-a-request-is-evidence.md) | A request is evidence of a problem, not a specification | Accepted | 2026-11-18 |
| [5004](5004-the-builder-reads-what-the-plan-answers.md) | The builder reads what its plan answers | Accepted | 2026-11-18 |
| [5005](5005-graft-names-the-ticket-in-each-commit.md) | Graft names the ticket in each commit | Accepted | 2026-11-18 |
| [5006](5006-a-revision-revises-the-round-before.md) | A revision revises the round before; it does not start again | Accepted | 2026-11-19 |

## Operations

| Record | Title | Status | Last updated |
|---|---|---|---|
| [6001](6001-a-failed-stage-waits-for-a-retry.md) | A failed stage waits for a retry, and throws nothing away | Accepted | 2026-11-19 |
| [6002](6002-an-agent-runs-budget-is-its-stages.md) | An agent run's budget is its stage's, and it writes before it runs out | Accepted | 2026-11-19 |
| [6003](6003-the-ticket-is-the-record-of-each-run.md) | The ticket is the record of each run | Accepted | 2026-11-19 |
| [6004](6004-each-agent-run-keeps-what-it-spent.md) | Each agent run keeps what the runtime says it spent | Accepted | 2026-11-19 |
| [6005](6005-a-build-lands-only-when-finished-and-proven.md) | A build lands only when it is finished and proven | Accepted | 2026-11-19 |
| [6006](6006-only-a-green-pipeline-is-a-release.md) | Only a green pipeline is a release | Accepted | 2026-11-19 |

## Adding and changing a record

Copy `0000-template.md` to the next number in its category; numbers are
never reused. A record is Proposed, Accepted, Deprecated, or Superseded by
another. Strengthen a record in place while its decision stands; write a
new one and mark the old Superseded when the decision is reversed. Update
this index in the same commit.

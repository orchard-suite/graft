# ADR-6001: A failed stage waits for a retry, and throws nothing away

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes what happens when a stage of a request fails, how the
person who asked is told, or how an approver tries a stage again.

## Purpose

Settles one rule for every failure of a request's stages: what stands,
where the request waits, who hears, and how it is tried again.

## Context

A round of a plan that answered its approvers failed when its investigator
ran past its budget. Graft ended the whole request as failed. Round 1's
plan, still waiting in the hub, could no longer be built, so the hub and
Graft disagreed about the request. The person who asked was told "a
person has been told" while the program held its notices and nobody was.
The only way on was to send the request again, which made a new request, a
new ticket, and a new approval.

Every stage already runs again from its start after a restart, so every
stage can be run twice.

## Decision

- **Nothing is thrown away.** A stage that fails keeps everything the
  request held, and records the stage, the round, the cause in plain words,
  and how many times it failed (`Stall`).
- **A failed revision leaves the round before standing.** While the round
  before still waits in the hub, a failed round puts the request back to
  waiting for that decision, with the run untouched. The approvers' notes
  stay open. Findings the failed round already wrote are kept, and a retry
  asks about them.
- **Any other stage stalls.** From the investigation on, a failed stage
  waits as `stalled`. An approved run stays live, so a retry builds under
  what it granted.
- **The ticket always says so.** The cause and how to retry are commented
  on the ticket, which Graft writes first when there is none, whatever
  the program does with its notices. The owner gets the `request_filed`
  notice, held where notices are held.
- **An approver retries, on the same request.** By the ticket comment
  `/graft retry` or the page's **Retry** button, judged against the
  request's approvers by the hub at that moment, never the person who
  asked. A request that failed before this was decided is retried the same
  way when it holds what a failed revision leaves.
- **Failed is the rest.** A stage before the investigation, or a stall no
  ticket could be written for, ends the request as failed. The person is
  told a person knows only when the ticket or a sent notice told one.

## What this means for the code

`stop` in `internal/desk/stall.go` takes every stage's error from `Run`.
`standBack`, `stall`, and `fail` are the three outcomes; `Retryable` and
`Retry` the way back. `store.Stall` is the record, `store.StateStalled` the
waiting state. `ending` in `answer.go` tells the owner before the person so
the answer can say whether anyone was told. `ask` leaves the notes open when
the hub does not take a round.

## Verification

In `internal/desk/stall_test.go`:
`TestARoundThatFailsLeavesTheRoundBeforeStandingAndSaysSoOnTheTicket`,
`TestARetryAsksAboutTheRoundAFailedAskAlreadyWrote`,
`TestARequestThatFailedBeforeStallsWereKeptIsRetriedFromItsTicket`,
`TestAFailedInvestigationStallsOnItsTicketAndAnApproverRetriesIt`, and
`TestTheAnswerSaysAPersonWasToldOnlyWhenOneWas`. In `internal/web`:
`TestOnlyAnApproverRetriesAStalledRequestFromThePage`.

## Consequences

- A stalled request waits until an approver retries it; nothing gives up
  on it by itself, as a plan that waits for a decision does not.
- Each retry spends another agent run's budget. A person asks each one, so
  retries are bounded by the people who ask them.
- A comment reaches a request that ended failed only through the forge's
  webhook; the sweep reads only requests that wait.

## Related

- [ADR-2005](2005-approvers-answer-a-plan-as-rounds.md)
- [ADR-6002](6002-an-agent-runs-budget-is-its-stages.md)
- Platform standard 2026.1107.1839:
  `assume-every-part-will-fail-contain-it-outvote-it-or-fail-sa` (a failure
  stays local and fails to a state a person resumes from),
  `route-verdicts-by-failure-class-the-same-failure-twice-is-a-` (retries
  are bounded: here by the approvers who ask them), and
  `one-approvals-inbox-as-a-catalog-record-every-system-pushes-` (the hub's
  run stays the one record of the decision).

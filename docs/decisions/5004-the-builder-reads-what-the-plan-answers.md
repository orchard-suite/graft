# ADR-5004: The builder reads what its plan answers

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes what the build agent is given.

## Purpose

Settles what the builder reads besides the plan.

## Context

The builder saw the plan only, never the request or the files the person
sent. Now a plan may take several rounds, and its approvers say on the
ticket and in the hub what they want. A builder that sees only the final
build plan cannot tell what a step was for when the step leaves a choice,
and cannot see a picture of what the person meant.

## Decision

- The builder is given the approved plan, brief and build plan, with the
  request as the person sent it, the files they sent, and every note its
  approvers left, round by round.
- All of it but the plan is data, inside the brief's data markers. Where
  the request and the plan differ, the plan as approved wins: it already
  answers what the approvers said.
- The files sit beside the builder's clone, never inside it, so no commit
  takes one by accident; the builder commits one only where the plan says
  to use it.
- The plan stays the contract: the fences still judge the diff against the
  build plan.

## What this means for the code

`buildBrief` in `internal/desk/build.go` writes it. `build` stages the
person's files in the session's box at `in/`, beside the clone.

## Verification

`TestTheBuilderGetsTheRequestItsFilesTheApprovedPlanAndTheThread` in
`internal/desk`.

## Consequences

- The person's words reach a second agent. They were already swept for
  secrets and the files read for them at intake; the builder holds no
  credential, and what it builds is held to the plan.

## Related

- [ADR-5002](5002-a-plan-is-a-brief-and-a-build-plan.md)
- [ADR-2005](2005-approvers-answer-a-plan-as-rounds.md)

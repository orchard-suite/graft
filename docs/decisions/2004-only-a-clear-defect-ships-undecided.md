# ADR-2004: Only a clear defect ships without a decision

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes which plans wait for their approvers, and the platform
admin who marks a lane `small_fix`.

## Purpose

Settles what may ship with no person deciding it, in a lane whose plans
wait.

## Context

In a lane marked `small_fix`, any request the investigator called a defect
was built first and shipped unasked when the fences judged the diff small.
A defect whose only cure changes what people see the system do, or a
request the person framed as something new, could ship that way. A change
in behaviour needs judgment: what serves one person can add complexity for
everyone.

## Decision

- In a lane whose plans wait, every plan waits for its approvers' decision
  on its brief, except a clear defect in a lane the program file marks
  `small_fix`.
- A defect is clear when three judges agree: the screen did not take the
  request for something new, the investigator's verdict is a defect, and
  its plan is a fix (`kind: fix`), which only makes the system do what it
  was already meant to do. A fix whose cure changes behaviour is a
  feature.
- A clear defect is built first and ships unasked only when the fences
  judge what was built a small fix, as before. Anything else is asked.
- A lane with `approval: false` still ships every plan unasked: it is the
  platform's own lane. A plan that asks for a tool tier waits in every
  lane.

## What this means for the code

`clearDefect` in `internal/desk/plan.go` is the one answer to whether a
request is built before anyone is asked.

## Verification

- `TestAFeatureOrAChangeInBehaviourAlwaysWaitsForADecision` in
  `internal/desk`: a feature, a defect planned as a feature, a defect the
  screen took for something new, and a defect in a lane without
  `small_fix` each wait, with approvers, and nothing is built.
- `TestASmallFixShipsWithNoPlanApproval` and
  `TestThePlatformLaneShipsWithNoGate`.

## Consequences

- Fewer requests ship unasked; every one that changes behaviour gets a
  person's judgment.
- The program file's keys are unchanged: `approval` and `small_fix` per
  lane say it.

## Related

- [ADR-2002](2002-plan-runs-asked-by-graft.md)
- Platform standard 2026.1107.1839:
  `risk-trip-wires-derived-from-evidence-independent-of-size`,
  `decide-the-feature-flag-at-planning-operator-releases-after-`.

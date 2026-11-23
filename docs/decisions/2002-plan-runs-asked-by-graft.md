# ADR-2002: A plan's run is asked by Graft for the person

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes how Graft asks the hub about a plan, and the admin who
sets the lane actions' access.

## Purpose

Settles who asks a plan's run in the hub, and who decides whether it
waits.

## Context

Graft asked each plan's run in the requester's name. That failed for a
requester the hub let see nothing, and it made the hub's notices about the
run (approved, declined, failed) reach the requester, past Graft's own
notices and `notices: hold`.

## Decision

- Graft asks each plan's run as itself, with its machine token, on
  behalf of the program. The run names the requester as `requestedFor`:
  data, shown on the run and on the `change_request` record
  (`requested_by`), never the asker. The requester needs no access in the
  hub, never decides the run, and the hub tells them nothing of it.
  Whatever they hear comes from Graft.
- Whether a plan waits is the lane's `approval` in the program file, not a
  hub rule. A lane whose plans wait names the approvers on the run, and the
  hub holds it for one of them. A lane with `approval: false` names none
  and ships unasked. A plan that asks for a tool tier names the approvers
  in every lane.
- Graft still checks a decision before it builds: never the requester,
  the run must carry the approvers it named, and the decider must be one,
  by `lets-in`. A plan that waits that the hub lets go ahead with nobody's
  decision is never built: the run ends failed and the request waits for a
  person.

## What this means for the code

`hub.AskPlan` sends `requestedFor` and never `for`. `desk.approvers` is the
one answer to whether a plan's run names approvers. The lane action's
access in the hub names Graft's machine token.

## Verification

- `TestAPlanIsAskedByGraftForThePersonWithItsApprovers` and
  `TestAPlanThatGoesAheadUnaskedNamesNoApprovers` in `internal/hub`.
- `TestThePlatformLaneShipsWithNoGate` and
  `TestAPlanThatWaitsIsNeverBuiltWhenTheHubLetsItGoUnasked` in
  `internal/desk`.
- The hub's own tests of `requestedFor` (Orrery 1.133.0).

## Consequences

- The requester's sight of the hub plays no part in a plan, and the hub
  stays silent toward them.
- It needs a hub that keeps `requestedFor` (Orrery 1.133.0 or later).
- A tool tier an approver granted reaches the requester's tool calls
  through the run, but the lane's own tiers still need a hub rule that
  lets the person ask the lane.

## Related

- [ADR-2001](2001-lanes-held-by-members.md)
- Platform standard 2026.1107.1839:
  `one-approvals-inbox-as-a-catalog-record-every-system-pushes-`,
  `extend-the-existing-actions-engine-every-run-goes-to-the-led`.

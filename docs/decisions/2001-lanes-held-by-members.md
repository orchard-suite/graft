# ADR-2001: A lane is held by the members its program names

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes how Graft decides which lane a request enters, and the
platform admin who writes a program file.

## Purpose

Settles who holds a lane: the lane's own configuration in the program
file, never what the person may see in the catalog hub or a cloud.

## Context

A lane was held where the hub let the person ask the lane's action on a
repository. The hub lists no target on a blueprint the person may not
see, so a person who could not see repositories held no lane, and an
intake app's request for them was refused. The fallback lane only picked
among lanes already held, so it never caught them. Opening repositories
to everyone was rejected: repositories stay invisible to staff.

## Decision

- A lane belongs to the program and is held on every repository of it, or
  on none.
- Each lane but the fallback names its `members` in the program file, as
  the hub's grants (by team, department, field, or address). Graft asks
  the hub whether they let a person in (`POST /v1/access/lets-in`), judged
  on the person's own record.
- The fallback lane names no members: everyone a port vouches for who
  holds no other lane holds it. A request from an intake app is never
  refused for want of a lane.
- A person who holds several lanes enters the highest. A lane above
  another names its members for more, so the platform's own people work in
  their own lane even when a lower lane's grant (a department) names them
  too. The lowest-lane rule, and the hub exceptions it needed, are gone.

## What this means for the code

`program.Held` and `program.Choose` are the one answer to which lanes a
person holds and which they enter. `hub.Lanes` supplies only the hub's
verdict on each lane's grants. Nothing reads the hub's action targets to
decide a lane.

## Verification

- `TestAPersonTheHubShowsNothingIsTakenFromAnAppInTheUserLane` in
  `internal/web`: the real desk and hub client, against a hub that lets the
  person into nothing, answer 201 in the user lane, and the hub is asked
  nothing but `lets-in`.
- `TestALaneIsHeldByItsMembersAndTheFallbackByEveryoneElse` in
  `internal/program`.

## Consequences

- Lane membership is written where the rest of the program's process is,
  and a lane no longer needs a person to see a repository.
- The lane action's access in the hub now carries only what the hub still
  owns: that Graft may ask the lane, and the lane's tool tiers. A lane's
  tiers still reach a person's own tool calls only where a hub rule lets
  them ask the lane.
- A program file that names a non-fallback lane without members no longer
  loads.

## Related

- [ADR-2002](2002-plan-runs-asked-by-graft.md)
- Platform standard 2026.1107.1839:
  `read-program-teams-from-the-identity-provider-s-groups-not-f`.

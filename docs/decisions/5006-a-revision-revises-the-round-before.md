# ADR-5006: A revision revises the round before; it does not start again

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes what a round that answers the approvers is given, or what
it must write.

## Purpose

Settles what the investigator of a later round starts from.

## Context

A round after the first was given the round before's brief and build plan
and the notes, but not its verdict or the evidence it kept, and its prompt
read as a first investigation. It re-read everything from zero, spent more
than the first round on the same budget, and ran out.

## Decision

- **It is told it is a revision.** The prompt opens by saying this round
  revises the one before, that what that round found stands unless a note
  changes it, and that it must not investigate again from zero.
- **It gets the whole round before.** Its verdict, as the investigator
  writes one; its brief and build plan; the whole thread, with the files
  each note sent; and every piece of evidence kept so far, by number, to
  cite where it still holds.
- **It still checks.** Every claim of fact a note makes is checked as the
  request's are (ADR-5003). An approver's note directs the plan and lifts
  no rule.
- **It may keep the verdict.** A round that answers the approvers writes
  `verdict.json` only when the verdict changes; without it, the round
  before's verdict stands. It always writes the plan, with what changed.
- **Each round keeps its verdict.** An earlier round is kept with the
  verdict it stood on, so a round that fails can put it back (ADR-6001).

## What this means for the code

`investigateBrief` and `roundBrief` in `internal/desk`; `readFindings`
takes a missing verdict on a revision as the one before; `store.Round`
holds `Verdict`.

## Verification

`TestARevisionStartsFromTheRoundBefore` in `internal/desk/stall_test.go`.

## Consequences

- A revision costs what its notes change, not what the request does.
- A note that undoes what the round before found must say so, or the
  investigator keeps it.

## Related

- [ADR-2005](2005-approvers-answer-a-plan-as-rounds.md)
- [ADR-5003](5003-a-request-is-evidence.md)
- Platform standard 2026.1107.1839:
  `loop-a-program-tier-child-by-child-re-reading-state-from-dis` (each step
  starts from the state kept, not from recall).

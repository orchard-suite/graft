# ADR-2005: A plan's approvers answer it, and each answer is its next round

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes how a plan is revised, what counts as an approver's word on
it, or how the ticket and the hub show its rounds; and the admin who sets
up the forge's webhook.

## Purpose

Settles how approvers iterate on a plan before deciding it, and whose
words count.

## Context

A plan could be sent back in the hub with a note, and the next plan was a
new run that revised the old one. Approvers also read the ticket and want
to answer the plan there, as a conversation, and the inbox showed each
round as an unrelated item.

## Decision

- **One approval that gains rounds.** A plan is one ask of its approvers.
  Each answer is planned into the next round, which revises the run of the
  round before: the hub supersedes a run that still waits, and keeps every
  round of the ask as one approval in its inbox (Orrery 1.134.0).
- **Two ways to answer, one result.** Send back with a note in the hub, or
  a comment on the request's ticket. Both become a note in the request's
  thread, and the next round answers it.
- **Only the approvers' words count.** A comment counts only from someone
  the plan's approvers let in, as the hub judges them when it is read. Its
  author's sign-in address comes from the program file's `forge.person`
  template, else the address the forge shows on their profile. Graft's
  own comments, a bot's, and anyone else's change nothing, and the reason
  is logged.
- **The forge's word is a nudge.** A webhook on note events, checked by a
  shared secret on a port of its own, says only that a ticket was
  commented on; Graft reads the comments from the forge with its own
  credential. A sweep reads each waiting plan's ticket every two minutes
  as well.
- **What changed leads the round.** A round after the first opens with
  what changed since the round before, quoting the notes it answers. The
  ticket's description becomes the round, and Graft comments that it is
  ready, where to decide it, and the round before, folded.
- **A question is answered, and the round stands.** When every new note
  is from the ticket, the investigator may answer instead of planning
  again: Graft posts the answer on the ticket and the run still waits.
  Code decides from what the investigator wrote: a reply keeps the round,
  a plan is the next round.
- **A decision made while a round is written** is taken as made: a decline
  ends the request, a note sent back is planned in too, and an approval of
  the round before is ended unused.
- **The limit.** `limits.plan_rounds` still bounds the rounds. A comment at
  the last round is answered that the round stands; a send back there ends
  the request for a conversation.
- **A decline tells the person why.** The decider's note goes to the person
  who asked as the reason, held while the program holds its notices.

## What this means for the code

`internal/desk/rounds.go` is the thread, the comment intake (`listen`,
`commenter`), the reply, and how the next round is asked (`prior`). The
forge crossing reads an issue's comments and writes its description
(`Comments`, `UpdateIssue`, `Email`). `internal/web/hook.go` is the forge's
port. `forge.person` in the program file maps a username to an address.

## Verification

In `internal/desk`: `TestAnApproversCommentOnTheTicketPlansTheNextRound`,
`TestASendBackInTheHubPlansTheNextRoundTheSameWay`,
`TestOnlyOneApprovalWaitsHoweverManyRoundsAPlanTakes`,
`TestAQuestionOnTheTicketIsAnsweredThereAndTheRoundStands`,
`TestAnApprovalOfARoundBeingRevisedDoesNotCount`, and
`TestADeclineTellsThePersonWhy`. In `internal/web`:
`TestTheForgesPortTakesACommentOnATicketOnlyWithTheHooksSecret`. In the
hub: Orrery's own tests of a round that revises a run still waiting, and of
one approvals record an ask.

## Consequences

- It needs Orrery 1.134.0 or later: an older hub refuses a run that
  revises one still waiting.
- A forge username maps to an address only as safely as the forge's
  usernames are the identity provider's: set `forge.person` only where
  they are.
- Each comment from an approver costs an investigation; a question costs
  one too, and is answered by an agent that read the code.

## Related

- [ADR-2002](2002-plan-runs-asked-by-graft.md)
- [ADR-5002](5002-a-plan-is-a-brief-and-a-build-plan.md)
- [ADR-5006](5006-a-revision-revises-the-round-before.md): what a round
  that answers the approvers starts from.
- [ADR-6001](6001-a-failed-stage-waits-for-a-retry.md): a round that
  fails leaves the round before standing.
- Platform standard 2026.1107.1839:
  `one-approvals-inbox-as-a-catalog-record-every-system-pushes-`,
  `refresh-from-webhooks-a-sweep-and-conditional-requests-behin`.

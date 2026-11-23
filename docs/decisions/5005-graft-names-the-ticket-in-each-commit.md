# ADR-5005: Graft names the ticket in each commit

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes the gate, the builder's brief, or how Graft takes a
change back out.

## Purpose

Settles who puts the ticket's reference on a request's commits, and where
in the message it goes.

## Context

Every commit of a request names its ticket. A person reading main finds
the request from it, and the revert tells the request's commits apart by
it. The builder was told to add `#<number>` to each message, and the gate
sent a round back when one did not.

A model does not reliably do what it is told three times. A build failed
after three rounds spent on the reference alone. The repository allowed
it: its commit rules hold a subject to 51 characters, so `#2` often did
not fit in the subject, but a line in the body was fine. The builder kept
neither.

## Decision

- Graft names the ticket itself, in the gate, before it judges the
  messages. Every commit of the builder's whose message does not name the
  ticket is written again with the trailer `Refs: #<number>` under its
  message. A commit after it is written again on top of it, its message as
  it was.
- The subject is never touched, so a repository's subject rules are never
  broken by Graft.
- Each new commit keeps its tree, its parents, its author, and its date.
  Graft is the committer, as for a rebase. Only the builder's own
  commits above the gate's base are written again: none of them is on the
  forge yet.
- The repository's commit-msg hook judges each new message, as it judged
  the builder's. When it refuses one, nothing is written, and the round
  goes back to the builder to name the ticket in a form the hook takes.
- The gate then checks, as before, that every commit names the ticket,
  and the repository's commit rules judge the messages that will land.
- The builder is told Graft names the ticket, and to leave it out.
- The revert finds the request's commits by `#<number>` anywhere in their
  messages, which the trailer is.

It is a rewrite in the gate, not a hook in the sandbox. A hook Graft
installs in the builder's clone is the builder's to remove, and a
repository that points git at its own hooks (husky sets `core.hooksPath`)
never runs it. The gate runs in the workspace, after the builder stops,
on what the builder left; it is the one place that sees every commit.

## What this means for the code

`nameTicket` and `ticketTrailer` in `internal/desk/ticket.go`; `gate` in
`internal/desk/build.go` calls it before the commit checks. `namesTicket`
is the one reading of whether a message names the ticket, for the gate and
for `revert` in `internal/desk/land.go`.

## Verification

`TestGraftNamesTheTicketUnderEachCommitAndKeepsTheSubject`,
`TestAHookThatRefusesTheTrailerSendsTheBuilderToNameTheTicketItself`, and
`TestARedPipelinePastTheLastRoundIsTakenBackOut` in `internal/desk`.

## Consequences

- The builder's commits get new hashes in its clone when Graft names
  the ticket. Nothing has read those hashes yet: they cross to the runner
  after the gate.
- A signed commit loses its signature when written again. A sandbox holds
  no signing key, so the builder's commits are not signed there.

## Related

- [ADR-5004](5004-the-builder-reads-what-the-plan-answers.md)

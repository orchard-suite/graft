# ADR-2006: An approver's files on the ticket join the next round

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes what Graft reads from a ticket's comments, how a request
keeps its files, or how a file reaches an agent; and the admin who sets up
the forge's token.

## Purpose

Settles how a picture or a PDF an approver attaches to a comment reaches
the plan and the build, and what Graft may fetch to get it.

## Context

Approvers answer a plan by commenting on its ticket (ADR-2005). Graft
took only a comment's words. An approver who had the file the plan needed,
such as the company's logo for a page, could only describe it, and the
planner could not use it. A comment's link to a file is written by a person:
fetching whatever it names would let a comment send Graft, with its
token, anywhere.

## Decision

- **Only an approver's comment.** The files of a comment that counts as an
  approver's note are taken. Nobody else's comment is read for files.
- **Only the forge's own uploads, through its API.** The forge adapter
  names a comment's files: on GitLab, a link `/uploads/<32 hex>/<name>`,
  relative or under the project's own address on the forge, and nothing
  else. Graft reads each by its secret and name through the forge's API
  for that project (`GET /projects/:id/uploads/:secret/:filename`), with
  its own token, and never fetches the address in the comment. A read is
  capped at the largest file a request keeps, and a byte. The token is
  never sent to another host a redirect names. GitHub's API reads no
  comment's file: there, the ticket is told so.
- **One set of limits.** An approver's file is held to the rules of a
  person's: its kind from its own bytes, pictures and PDFs alone, and 5
  files, 10 MiB each, 25 MiB in all, counted across the request. A file
  that breaks one is left out, the ticket says which and why, and the round
  goes on.
- **Swept before anything reads it.** A file taken waits, unread, in the
  request's `incoming` until the next round starts. That round sweeps it
  first, in a sandbox of its own, as a person's file is swept. A file that
  looks like it holds a secret, or cannot be read, is deleted, and the
  ticket is told which file and what kind of secret, never the secret. It
  costs that file alone: the request is not quarantined, since the person
  who asked did not send it.
- **Words are swept too.** An approver's comment that looks like it holds a
  secret is left out of the round, with its files, and the ticket is told
  which comment. A note sent back in the hub is planned with the secret cut
  out, since its run already ended.
- **Marked as the approver's.** A file keeps who sent it (`from`, `by`) and
  the round they answered with it. The briefs name it as a file that
  approver sent for the plan to use; the ticket, the plan's run, the round's
  comment, and Graft's page list it with who and which round.
- **Committed only by the plan.** The builder gets every file beside its
  clone, never inside it, and puts one in the repository only where the
  build plan says which file goes to which path.

## What this means for the code

`forge.Comment.Uploads` and `Forge.Upload` are the crossing; the GitLab
adapter's `uploads` is the one place a link is judged. `listen` and
`takeFiles` in `internal/desk/rounds.go` take an approver's files,
`attach.Take` holds them to the limits, and `sweepIncoming` in
`internal/desk/attachments.go` sweeps them at the start of the round.
`store.Doc.Incoming` holds what is not swept yet; nothing stages it.

## Verification

In `internal/desk`: `TestAnApproversFileOnTheTicketIsPlannedWithAndBuiltFrom`,
`TestOnlyAnApproversCommentHasItsFilesRead`,
`TestAFileOverALimitOrOfAnotherKindIsLeftOutAndTheRoundGoesOn`,
`TestASecretInAnApproversFileDropsThatFileAlone`,
`TestASecretInAnApproversCommentLeavesItOutOfTheRound`, and
`TestASecretInANoteSentBackIsRemovedBeforeAnyAgentReadsIt`. In
`internal/forge/gitlab`: `TestCommentsNameOnlyTheProjectsOwnUploads`,
`TestAnUploadIsReadThroughTheProjectsAPIAndCapped`, and
`TestTheTokenNeverFollowsARedirectToAnotherHost`. In `internal/attach`:
`TestTakeHoldsOneMoreFileToTheRequestsLimits`.

## Consequences

- It needs GitLab 17.4 or later, whose API reads an upload by its secret.
  On an older GitLab every such file is left out and the ticket says so.
- Each round that takes files costs one more sandbox, for the sweep.
- An approver's file is kept with the request's other files and dropped
  with them after the program's `context_days`, counted from when the
  request was made.
- OCR is not perfect: a secret too small or blurred to read reaches the
  planner, who is told not to copy one.

## Related

- [ADR-2005](2005-approvers-answer-a-plan-as-rounds.md)
- [ADR-5004](5004-the-builder-reads-what-the-plan-answers.md)
- Platform standard 2026.1107.1839:
  `a-secrets-sweep-on-every-imported-item-with-quarantine-and-a`. Graft
  deletes a swept file that holds a secret rather than holding it for a
  person, as it already does for a person's files.

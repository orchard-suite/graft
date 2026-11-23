# ADR-6006: Only a green pipeline is a release

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes how Graft watches a landed change, what it calls
released, or what happens when a pipeline is not green; and the admin who
reads the tickets.

## Purpose

Settles what counts as released, which pipeline is the release in a
repository that releases in two pipelines, and where a request waits when
its pipeline is not green.

## Context

A landed change's push pipeline failed two jobs, and its release job was
then cancelled. The pipeline ended `failed`, but Graft read a cancelled
run as superseded, not red, and posted "Released". Nothing was released by
that pipeline. A GitLab pipeline that waits for a click was read as
neutral, which was not red either.

In a repository that releases with release-damnit, the push pipeline's
release job writes a release commit on main, and that commit's own
pipeline, or its tag's, is what tags and deploys. A green push pipeline is
not yet a release there.

## Decision

- **Only success is green.** A run that failed, timed out, was cancelled or
  skipped, or waits for a click released nothing. A run still cancelling is
  still going.
- **The release commit's pipeline is the release.** In a repository that
  releases with release-damnit, once the landed commit's pipelines are
  green, Graft looks on main for the first commit after it whose subject
  is a release commit's (`chore: release`, `chore(release):`), and watches
  every pipeline of that commit (its push pipeline or its tag's). Released
  means those are green. Where none follows, the landed commit's own
  pipeline is the release, as before. A commit with no pipeline within
  three minutes has nothing more to watch.
- **A pipeline that is not green stalls the request at watching**
  (ADR-6001). The run's comment links each run that is not green and how
  it ended, and each failed job by name and stage, with its page.
- **A retry reads the pipeline again first.** Green now, it is released.
  Still failing, the builder is given each failed job's log to fix forward,
  with its rounds back, and the fix is built, proven (ADR-6005), landed, and
  watched again. A pipeline with no failed job (cancelled, waiting) is run
  again on the forge by a person; the comment says so.
- **Nothing goes back to the builder unasked.** A red pipeline no longer
  goes back to the builder by itself, and no longer leads to a revert: a
  person decides, by the retry.

## What this means for the code

`forge.Run.Green` replaces `Red`; GitLab's `manual` is `completed` with
conclusion `manual`, and `canceling` is still running.
`forge.Forge.FailedJobs` replaces `FailedLog` and answers `forge.Job`s with
their pages. In `internal/desk/land.go`: `watch`, `settle`,
`releaseCommit`, `isReleaseCommit`, and `notReleased`.
`Request.Release` keeps the release commit being watched; landing clears
it.

## Verification

In `internal/desk/finish_test.go`:
`TestARedPipelineReleasesNothingAndARetryFixesItForward`,
`TestACancelledPipelineStallsUntilItIsRunAgain`, and
`TestOnlyTheReleaseCommitsGreenPipelineIsARelease`. In `internal/forge`:
`TestOnlySuccessIsGreen`; in `internal/forge/gitlab`:
`TestRunsAndFailedJobs`.

## Consequences

- A red main waits for a person. Until one retries, the repository's main
  holds a change whose pipeline is not green, and the ticket says so.
- A run that only needs running again costs a person a click on the forge
  and a retry, and no agent run.
- A workflow that ends skipped on every push stalls each request until a
  person retries it; such a workflow should not run on the push.
- A release commit is known by release-damnit's subjects. A repository
  that releases another way has its landed commit's pipeline as its
  release.

## Related

- [ADR-6001](6001-a-failed-stage-waits-for-a-retry.md)
- [ADR-6005](6005-a-build-lands-only-when-finished-and-proven.md)
- Platform standard 2026.1119.1752:
  `land-to-remote-main-and-let-ci-release`,
  `automated-per-project-releases-from-conventional-commits`,
  `stop-a-failed-agent-stage-for-a-retry-never-end-the-request`,
  `written-is-not-working-observed-local-gated-hands-on-then-gr`, and
  `no-silent-failures`. It departs from
  `the-runner-gates-the-agent-s-work-and-returns-failures-with-` in one
  place: that rule sends a red pipeline back to the agent by itself, within
  the rounds; here a red pipeline stalls, and a person's retry sends it
  back. New ground: the second pipeline of a two-step release as the
  release.

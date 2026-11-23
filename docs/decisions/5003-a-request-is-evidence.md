# ADR-5003: A request is evidence of a problem, not a specification

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes the investigator's brief or what a plan must say about the
request it answers.

## Purpose

Settles how the planner treats what a person asked for.

## Context

The planner took a request's words as truth. A request said a file had to
come from a named person; the plan passed that on as a dependency, though
the file was in the repository's history all along. A request for ten
changes to one form became ten one-off rules. Requests from the people who
use a system name what got in their way; they do not know the system, and
building each one as asked grows the system one special case at a time.

## Decision

- Every claim of fact in a request (a file exists or not, someone must
  provide something, something used to work, the system behaves a certain
  way) is checked before the plan relies on it: in the code, in the
  repository's history, and, where a tool reaches it, the running system.
  Each check is kept as evidence and recorded in the brief, true or false.
  A claim that could not be checked is recorded as such, with why.
- For a feature or any change in behaviour, the planner fits the change to
  how the system already does like things, prefers one general way over a
  case per field, page, or person, names what the change adds, and
  proposes the simplest change that solves the problem for everyone who
  uses it.
- The planner may decline, defer, or narrow parts of a request, in words
  the person who asked would accept, and puts what only the approvers can
  settle to them with its recommendation.

## What this means for the code

The investigator's brief (`investigateBrief` in
`internal/desk/investigate.go`) says so, with the commands that read the
history. The investigator's clone carries the repository's whole history.
`brief.checked` holds each check, and `checkBrief` refuses one that says
what it found without evidence that exists on the request.

## Verification

`TestAClaimInTheRequestIsCheckedAgainstTheRepositorysHistory` in
`internal/desk`: a request says a logo must come from a person; the logo is
in a commit a later commit removed; the investigator's clone holds that
history, and the brief on the ticket and on the run records the check,
found false, with the evidence that shows it.

## Consequences

- A plan pushes back. Approvers see what was declined and why, and what
  was checked.
- An investigation reads more, and costs more of its share of the spend
  cap.

## Related

- [ADR-5002](5002-a-plan-is-a-brief-and-a-build-plan.md)
- Platform standard 2026.1107.1839:
  `explore-read-only-classify-the-track-first-and-state-why`,
  `defect-track-runtime-evidence-first-no-design-questions-unti`,
  `question-the-operator-in-themed-pages-evidence-first-and-let`.

# ADR-2003: Whoever decides a request's plan reads the request

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes who reads a request on Graft's page, and the admin who
names a program's approvers.

## Purpose

Settles who may read a request, its steps, and its files on Graft's
page.

## Context

The page let two kinds of person read a request: the person who asked,
and anyone holding a lane above the program's lowest. A plan's approvers
are named apart from the lanes (`approvers` in the program file), so an
approver in only the lowest lane could decide a plan in the hub but not
open the request it came from, nor the files the person sent, which the
plan's run links to. They got 404 for both.

## Decision

- Three kinds of reader, and nobody else:
  - **The person who asked** reads their own request: what they sent, where
    it stands, the answer, how its plan was decided, and its files.
  - **Who runs a repository**, anyone holding a lane above the lowest,
    reads every request there whole: also the verdict, the plan, the
    steps, the evidence, and the log. Only they stop and start it.
  - **Who decides its plans**, anyone the repository's approvers (else the
    program's) let in, as the hub judges those grants on their own record,
    reads every request there whole, as who runs it does. Deciding plans
    does not let them stop or start it.
- Whether someone approves is asked of the hub with the same check that
  holds a decision to its approvers (`POST /v1/access/lets-in`). For
  reading, an answer is kept a minute; a decision is always checked
  afresh.
- Anyone else gets the same 404 a request that does not exist gets.
- A file is shown in place, as the kind its own bytes say it is, under a
  policy that runs nothing and loads nothing else.

## What this means for the code

- `reaches` in `internal/web/server.go` is the one place the page works
  out what a person may do in each repository (`runs`, `approves`), and
  `readerOf` the one place who they are to one request. Every request
  route reads through `readable`.
- `Approves` in `internal/desk/serve.go` names the grants; `Admits` in
  `internal/hub/hub.go` keeps the hub's answer a minute.
- The intake port is unchanged: an app reads only what it sent, for the
  person it names.

## Verification

`TestWhoeverDecidesAPlanReadsTheRequestAndItsFiles` in
`internal/web/server_test.go` runs the real desk and hub client against a
hub that lets in only the approvers: the approver reads the request, its
steps, and each file in place; the requester reads their own; a stranger
gets 404 for every part.

## Consequences

- A program's approvers see every request of the repositories they
  decide for, including the words and files a person sent, since they
  decide plans built from them.
- Each page call that reads a request asks the hub once a minute per
  person and set of approvers.

## Related

- [ADR-2001](2001-lanes-held-by-members.md): a lane is held by the members
  its program names.
- [ADR-2002](2002-plan-runs-asked-by-graft.md): a plan's run is asked by
  Graft for the person.

# ADR-5001: An intake app's paths are where to start, not a fence

| | |
|---|---|
| **Decision Date** | 2026-11-18 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-18 |

## Status

Accepted

## Audience

Whoever changes what a request may change, and the admin who lists intake
apps.

## Purpose

Settles what an intake app's `paths` mean.

## Context

A request from an app with `paths` was filed when its plan or build
reached past them. A request is about the program: a problem a person
meets in one app may need a change in a service it calls, in the same
repository.

## Decision

- `paths` say where the app is built from. The investigator starts there,
  at the app's `tag` for the version the person ran. They never fence what
  a request may change.
- A request may change anything in its repository the fix needs. Only the
  protected paths are held to a lane.

## What this means for the code

No stage reads an app's `paths` to refuse a plan or a build. The brief
names them as where to start.

## Verification

`TestAnAppsRequestMayChangeCodeBeyondTheAppsPaths` and
`TestAnAppsRequestIsStillHeldToTheProtectedPaths` in `internal/desk`.

## Consequences

A request works in the one repository its app reports against. A change
that spans repositories is still two requests.

## Related

[ADR-2001](2001-lanes-held-by-members.md)

# ADR-1001: Package caches outlive a sandbox; nothing else does

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

The estate that writes the sandbox Pod manifest, and whoever changes the
image or what a sandbox may keep.

## Purpose

Settles what one agent sandbox may leave for the next: the package
caches, in one folder, and nothing else.

## Context

Every sandbox started cold. A .NET restore took more than eight minutes,
and a test suite never finished inside its run. The image shipped one
Chromium build, for Playwright 1.65.1; a repository pinned to 1.62.1
wanted another, and its builder spent turns linking a copy under the name
its Playwright expected.

A sandbox is a pod per session, so no session reads what another left
(design, Fences). A package cache is something one session leaves for the
next.

## Decision

- **One cache folder, `/cache`.** The image points every package cache
  there: NuGet's packages and HTTP cache, npm's cache, Playwright's
  browsers, Go's modules and build cache, uv's and pip's caches. The estate
  mounts one volume there, in the engine's container and the workspace's
  alike, and may keep it across sandboxes. Without a volume each container
  has its own, empty.
- **Nothing else outlives a sandbox.** The clone, the report, the handle,
  and the engine's sessions stay in the sandbox's own volumes and go with
  it.
- **The browser is the repository's.** The image holds the system
  libraries Chromium needs, and no browser. Before a build, the workspace
  reads which Playwright versions the repository pins (the lockfile's,
  else the manifest's exact version) and installs the browser each wants
  into the cache, where the next sandbox finds it. A failed install is
  said on the ticket and stops nothing; the agent is told never to stand
  one browser build in for another.
- **One program's sandboxes share it.** The volume belongs to one Graft
  and no other workload.

## What this means for the code

`Dockerfile`: the cache environment and `/cache`, and Playwright's
`install-deps` in place of a browser. `prepare` in
`internal/desk/finish.go` installs the pinned browsers.

## Verification

`TestTheSandboxGetsTheBrowserTheRepositorysPlaywrightWants` in
`internal/desk`.

## Consequences

- A sandbox reads what earlier sandboxes downloaded. npm checks each
  package against its integrity on read; NuGet's extracted packages and
  Playwright's browsers are trusted as they lie. A session that wrote a
  bad file there could reach a later session's build. Such a session holds
  no credential, and what it could change still lands only as a diff the
  fences, the gate, the pipeline, and its plan's approvers judge.
- The volume must take writes from several pods at once: a ReadWriteMany
  volume, or one node's disk where every sandbox lands on that node.
- The volume grows with every version any repository pins. Clearing it is
  safe at any time a sandbox is not running; the next sandbox fetches again.
- The workspace needs the npm registry and Playwright's download host.

## Related

- [ADR-6005](6005-a-build-lands-only-when-finished-and-proven.md)
- Platform standard 2026.1119.1752:
  `give-a-sandboxed-agent-a-handle-on-one-request-never-a-hub-c` (no key
  enters the cache) and
  `environments-are-identical-in-architecture-with-named-except`. New
  ground: a cache kept across sandboxes as the one thing a sandbox leaves.

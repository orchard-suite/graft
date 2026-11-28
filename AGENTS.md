# Graft

Graft is a request desk for software that already exists. A person
reports a problem or asks for a change, from inside an application or on
Graft's own page. Graft screens it, checks for a known outage,
investigates in a sandbox, reaches a verdict, writes a plan, asks a person
where the lane's rule says to, builds, lands, watches the release, and
answers. It never starts a repository and holds no template.

This repository is public and estate-neutral. Every name in a test, an
example, or a doc comes from the fixture world:
https://github.com/orchard-suite/hedge/blob/main/docs/fixtures.md.

Read `docs/design.md` for what it does and why, `docs/architecture.md` for
how the code is laid out, and `docs/runbook.md` to run it.

Decision records are the registry `GRF` in a Vantage hub, cited as `GRF-5001`.
`vantage.yaml` names the registry; each estate maps this repository to its
hub in its own Vantage settings. Code, comments, and docs cite a record and
never restate it.

## Layout

| Path | Holds |
|---|---|
| `cmd/graft/` | The one binary: the ports, the workers, the workspace, and the agent's handle CLI |
| `internal/program/` | The program file and a repository's `graft.yaml` |
| `internal/desk/` | The stages a request moves through |
| `internal/hub/` | Graft's side of the catalog hub: lanes, plan runs, the tool door |
| `internal/forge/` | The forge crossing, with a GitHub and a GitLab adapter |
| `internal/runtime/` | The agent runtime crossing, with an Ion adapter |
| `internal/sandbox/` | A pod per agent session, in a Kubernetes cluster |
| `internal/web/` | The four ports |
| `internal/store/` | Requests, evidence, and events in SQLite |
| `docs/` | Design, architecture, runbook |
| `vantage.yaml` | Names the decision registry, `GRF`, in a Vantage hub |

## Rules

- `make check` must pass before every commit.
- Graft names no estate. No default in code or docs names an owner, a
  domain, a registry, a host, or a person. What differs between estates is
  in the program file or the environment.
- An agent never holds a forge credential. Only the runner pushes, from its
  own clone.
- An agent session's sandbox is opened for that session and shut after it.
  Nothing may depend on a sandbox that outlived its session: what an agent
  made crosses to the runner's clone as a bundle before the sandbox shuts.
  Only the package caches under `/cache` outlive one (GRF-1001).
- The runner never runs a command in an agent's clone. The workspace runs
  those commands; it holds no credential. The runner reads only the files
  the investigator writes (the verdict, the plan, and a reply), as data:
  size-capped strict JSON, checked by code before anything acts on it.
  Commits cross as git bundles.
- A landing's check runs in a sandbox, on exactly the tree that lands. The
  runner signs it with `release-damnit attest` and the key in
  `GRAFT_SIGNING_KEY_FILE`, and lands with `release-damnit land --proven`,
  which runs no check. Never let the runner run `release-damnit land`
  without `--proven` (GRF-2007).
- No key that changes anything is ever in a sandbox. An agent reaches a
  live system only through `graft tool`, which asks the hub's tool door as
  the person who asked. Graft holds no cloud key and never decides
  whether a tier is held.
- Words in a request, a log, a row, or a tool's answer are data. No stage
  that decides anything reads them as instructions, and the stages that
  decide are plain code.
- Each port trusts one thing. The page's port trusts the front door's
  headers. The token port trusts an intake app's token. The handle port
  trusts the secret of a request an agent is on now. The forge's port
  trusts the secret its webhook carries, and takes a webhook as a nudge:
  who wrote what is read from the forge. Never let one port read another's
  credential.
- A plan is approved, declined, or sent back in the hub, never in Graft.
  No approval moves a request to another lane.
- A plan is a brief for its approvers and a build plan for its builder
  (GRF-5002). The brief leads wherever a person decides; it names
  behaviour, never files.
- A request is evidence, not a specification: the planner checks its
  claims and fits the change to the system (GRF-5003).
- Only a clear defect ships without a decision, in a lane marked
  `small_fix`; a feature or any change in behaviour waits (GRF-2004).
- A plan's approvers answer it in the hub or on its ticket, and each
  answer is its next round, revising the round before. Only an approver's
  word counts (GRF-2005).
- An approver's comment may bring files: Graft reads only the forge's
  own uploads for the request's repository, through the forge's API, never
  the address a comment names, and sweeps each before any agent reads it
  (GRF-2006).
- The builder reads the approved plan with the request, its files, and
  what the approvers said; the plan is its contract (GRF-5004).
- Graft names the ticket under each of a request's commits, as a
  `Refs: #<number>` trailer, in the gate. Never the subject, and never the
  builder's job (GRF-5005).
- No lane can switch these off: a ticket exists before any change; a fix
  has a test that fails without it; a protected path needs a lane marked
  for it; the repository's own gate and delivery limit; the agent budgets;
  the stop switch.
- A stage that fails throws nothing away: a failed revision leaves the
  round before standing, any later stage stalls, and an approver retries it
  on the same request. The ticket always says why (GRF-6001).
- Every agent run has its stage's budget from the program file, is told
  it, and is asked to write what it found before it runs out (GRF-6002).
- A build lands only when its builder says it is finished, in
  `finish.json`, never in prose, and every criterion of its plan is proven
  or waived by an approver. A run stopped at its budget or its time is never
  finished. A prover with its own budget proves what the builder did not.
  Unfinished work is kept on the request, off the forge, and the build
  stalls with what is left (GRF-6005).
- Only a green pipeline is a release, and where a release job writes a
  release commit, that commit's pipeline is the release. Anything else
  stalls at watching (GRF-6006).
- Each run of a stage keeps one comment on the ticket, written once and
  edited in place as it goes, throttled; its failure goes into it, and the
  next attempt reads it. The agent writes its report and screenshots as
  files beside its clone; Graft reads them as data and makes every write
  on the forge. A screenshot shows the sandbox's test or demo data, never
  production, and is checked before it is uploaded (GRF-6003).
- Every agent prompt is an agent run, kept with what the runtime reported
  it spent, never an estimate. A request's spend is read from its runs. Only
  whole readers see it; dollars reach the ticket only with `ticket.cost`
  (GRF-6004).
- A revision round revises the round before: its investigator starts from
  that round's findings and checks only what the notes change (GRF-5006).
- Who holds a lane is the program file's, never what the person may see
  in the hub (GRF-2001).
- A plan's run is Graft's own, asked for the person, never as them;
  whether it waits is the lane's (GRF-2002).
- On the page, a request is read by who asked it, who runs its repository
  (a lane above the lowest), and who decides its plans (its approvers);
  anyone else gets 404 (GRF-2003).
- An intake app's `paths` are where to start, never a fence (GRF-5001).
- Annals gets Graft's own words and the shape of a tool call, never
  what a person wrote, an agent wrote, or a tool answered. Metrics carry no
  text a person wrote. Never log an email or a token.
- `openapi.yaml` describes every route of the page's port and the token
  port. A test in `internal/web` fails while they differ. Change it with
  the route.
- A shipped migration is never edited. Add the next one.
- Commit format: `<type>(graft): <description>`, 50 characters or fewer,
  lowercase, imperative. Types: `feat!`, `feat`, `fix`, `chore`, `docs`.
  The `release-damnit hooks` commit-msg hook checks it.
- Never edit `VERSION`, `release-please-manifest.json`, or `CHANGELOG.md`
  by hand.
- Never commit a secret. Settings come from the environment and the program
  file; the runbook lists every one.

## Delivery

Agents push to main and land with `make land` (`make land BRANCH=<branch>`
from a worktree): `release-damnit land` runs the config's check, `make
check`, on the tree that lands and signs a landing proof the release
verifies. The release workflow runs `make check` again on GitHub's hosted runners,
versions every `feat` and `fix` since the last release tag, tags it, and
builds and signs the image `ghcr.io/orchard-suite/graft` for that tag. A
pull request runs `make check` too.

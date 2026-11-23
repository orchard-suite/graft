# Architecture

One Go binary, one SQLite file, one program file, one agent runtime. The
unit of work is a request. `docs/design.md` says what a request goes
through and why; this file says where that lives in the code.

```
an app's server ─token──▶ ┌──────────────────────────┐ ──▶ catalog hub
a person's page ─headers─▶│ graft (ports + workers)  │ ──▶ incident desk
the agent's CLI ─secret──▶│                          │ ──▶ Annals (audit)
the forge's hook ─secret─▶└──────────────────────────┘
                              │      │        │
                 git, REST    │      │        └─socket─▶ agent runtime (ion serve)
                              ▼      ▼ bundles, gate            │
                           forge   graft workspace ──▶ <work>/requests/<id>/repo
                              ▲
                 <repos>/requests/<id>  (the runner's clone)
```

## Parts

- `cmd/graft`: wiring. `serve` runs the four ports and the workers.
  `workspace` serves the agent's clones. `git-credential` answers git with
  the forge's credential. `tool` and `evidence` are the agent's handle.
- `internal/program`: the program file and a repository's `graft.yaml`,
  parsed strictly. An unknown key is an error, so a typo cannot switch a
  fence off.
- `internal/config`: the environment. Where to listen, where to keep
  things, and the addresses and credentials of what Graft talks to.
- `internal/store`: requests, their numbered evidence, their events, their
  runs, their agent runs and what each spent (`spend.go`), each
  repository's stop switch and last release, and the tool actions declared.
  A request is a few columns to filter by and one JSON document.
  Migrations run in order, counted by the database's `user_version`.
- `internal/desk`: the stages. `desk.go` runs a request through them.
  `intake.go` sweeps, matches, screens, and checks for incidents.
  `investigate.go` runs the investigator and checks its verdict and plan.
  `plan.go` decides what a verdict leads to and asks the hub about a plan.
  `brief.go` writes a plan's brief and build plan as Markdown. `rounds.go`
  takes what the approvers say of a plan, in the hub or on the ticket, and
  asks its next round.
  `stall.go` takes a stage that failed: a round that leaves the round
  before standing, a stall, or a failure, and the retry. `runs.go` keeps
  each run's record and its comment on the ticket, `render.go` writes the
  comment, and `report.go` reads what the agent reported and takes its
  screenshots. `spend.go` runs every agent prompt and keeps what it spent.
  `build.go` runs the builder, the gate, and the fences. `finish.go` is
  what a build must be to land: the builder's word that it is finished,
  the prover, the proof each criterion needs, the work kept when it is
  not, the waiver, the build output the gate clears, and the browsers a
  repository's Playwright wants. `land.go` lands, opens a review, watches
  the pipeline and the release pipeline after it, and takes a change back
  out.
  `tools.go` is the agent's handle on the repository's tools. `serve.go`
  takes requests in and schedules the work.
- `internal/hub`: the catalog hub. Lanes, plan runs, tool declarations, the
  tool door, and the request as a catalog record.
- `internal/forge`: the forge crossing: clone address and credential,
  issues and their comments with the files they link to, a comment written
  and edited, a file read through the forge's API or attached to it, pull requests, whether the main branch makes a
  review a must, and a commit's pipeline. `forge/github` and `forge/gitlab` are the adapters,
  both plain REST.
- `internal/ghapp`: signs in to GitHub as a GitHub App and mints one short
  token per repository.
- `internal/runtime`: the agent runtime crossing: run a prompt in a
  directory under a model, a turn limit, a time limit, and a spend limit,
  and answer what the run consumed (`Usage`); ask one question; stop a
  session. `runtime/ion` is the adapter.
- `internal/sweep`: finds secrets in what a person sent.
- `internal/attach`: the files a request keeps: those the person sends,
  and those its plan's approvers send on its ticket, each marked with who
  sent it. Which kinds are taken, read from their bytes, the limits on a
  request's files together (`Take`), and the files themselves on the data
  disk, one folder a request, beside the database and never in it.
  `desk/attachments.go` sweeps them and shows them to the investigator and
  the builder.
- `internal/web`: the four ports. `reaches` and `readerOf` are the one
  place the page decides who reads a request (ADR-2003). `hook.go` is the
  forge's port.
- `internal/auth`: who is calling. The page's port reads the front door's
  headers through the platform kit's `door`. The token port matches a
  bearer token against the intake token directory.
- `internal/workspace`: the agent's side. A server over the work directory
  that runs the gate's commands and moves files, and the runner's client.
- `internal/annals`: each step of a request as an Annals event.
- `internal/telemetry`: the requests as OTLP gauges. The spend counters
  are the desk's, sent with the server's own telemetry.
- `internal/shell`: runs commands with output copied to the request's log.

## Where an agent runs

Every agent session runs in a sandbox: the screening call, one
investigation, or one build with its rounds. A sandbox is an engine, where
the agent's commands execute, and a workspace, where the runner's checks on
the agent's work execute. It is opened when the session starts and shut
when it ends. Nothing in it is needed afterward: the agent's commits cross
to the runner's clone as a bundle before it shuts, and a round sent back
later starts in a new sandbox from the work so far. `GRAFT_SANDBOX`
picks one of two kinds.

- **`kubernetes`: a pod per session.** Graft makes a pod from a manifest
  the estate owns (`GRAFT_SANDBOX_POD_FILE`), named
  `graft-<request>-<stage>`, waits until it is ready, and deletes it when
  the session ends. It changes only the pod's name, its labels, and the
  workspace's token. The workspace listens on the pod's address and takes
  a bearer token made for that pod alone, which only Graft holds. Graft
  drives the agent through the workspace, so the runtime's CLI runs inside
  the pod, beside its engine. No session can read what another left, and
  ten requests are ten pods the cluster schedules. Graft itself is then
  one small pod: the ports and the workers.
- **`shared`: one pod for everything.** The engine and the workspace are
  containers beside Graft. Each session has its own directory there,
  removed when the session ends. One agent could read another session's
  files while both run, so this fits an estate where everyone who may ask
  is trusted with everything any request reads.

Graft keeps its state in one SQLite file, so it runs as one replica. A
restart loses no request: each stage runs again from its start.

## Three containers, one image

In the `shared` kind the engine, the workspace, and the runner are three
containers of one pod. In the `kubernetes` kind the engine and the
workspace are the sandbox pod's containers, and the runner is Graft's own
pod. Either way they are the same image.

- The engine container runs the agent runtime. The agents' shell commands
  execute there, with the toolchains the repositories need. It holds no
  forge credential and no key that changes anything. In a program marked
  `trusted`, the deployment may mount one read-only cloud key there.
- The workspace container runs `graft workspace` on the pod's loopback.
  Every command that reads an agent's clone runs there: the gate's git and
  the repository's check, the proof that a new test fails without its fix,
  the install of the browsers the repository's Playwright wants, and the
  git that moves commits in and out. It holds no credential.
- The runner container runs `graft`: the ports and the workers. It holds
  the forge credential, the hub's two tokens, and the tokens for the
  incident desk and Annals. It never runs a command in an agent's
  clone.

The engine and the workspace share the work volume. The runner cannot see
it; its database, its logs, and its own clones are on a volume only it
mounts.

Each request has two clones. The agent works in its own, in its sandbox at
`<work>/requests/<id>/repo`. The runner clones, pushes, and lands from its
own, at `<repos>/requests/<id>`. Commits cross between the two as git
bundles. A bundle is data, so no hook, git config, or Makefile an agent
wrote ever runs beside the forge credential. The investigator and the
builder each get a fresh clone: the builder never works where the
investigator did.

The runner reads only the files the investigator writes: the verdict and
the plan, or, for a round that answers a question, the reply, from
`.graft-out/` in the investigator's clone, through the workspace. It
reads at most 64 KB of each as strict JSON, and code checks every field
before anything acts on it. A file that does not pass goes back to the
investigator once with the reason; a second failure fails the request.

The runner checks that the engine answers before every prompt. The ion CLI
starts an engine of its own when it finds none, and one started in the
runner's container would run the agent beside the credentials.

## Four ports

Each port trusts one thing and reads no other port's credential.

| Port | Default | Serves | Trusts |
|---|---|---|---|
| Page | `:8080` | `/api/...` and the page | The front door's identity headers. Only the front door may reach it. A change must also carry the page's own `Origin` |
| Token | `:8081` | `/v1/...` | An intake app's bearer token, matched against the token directory. The app says who is signed in to it |
| Handle | `127.0.0.1:8082` | `/tools`, `/tools/{id}/call`, `/evidence` | The secret of a request an agent is working on now. With a pod per session it listens on the pod's address, and only sandbox pods may reach it |
| Forge | `:8083` | `/hooks/gitlab` | The secret the forge's webhook carries, read from a file on every call. On only when that file is set. Only the forge may reach it |

The page shows a person their own requests: what they sent, the files
they sent with it, where it stands, where its plan is decided and how it
was, and the answer. Someone who holds a lane above the lowest sees every
request in the program's repositories, with the verdict, the plan, the
evidence, the steps, and the log, and may stop and start the repository.

An intake app sees only what it sent itself, for the person it names.

## The handle

An agent reaches a live system one way: `graft tool call <id>`. The CLI
is the same binary, on the engine container's path. It finds the handle
file, `.graft-handle`, beside the clone and never inside it, and calls
the handle port with the secret in it.

```
agent ── graft tool call ──▶ handle port ──▶ the hub's tool door ──▶ a runner ──▶ the system
           (secret)              (machine token, lent to the person)     (the runner's key)
```

- The secret is 32 random bytes. Graft keeps only its SHA-256, on the
  request. It works while the request is being investigated or built, and
  is replaced for each agent session.
- Graft names only tools that the repository's `graft.yaml` declares
  on its main branch. For each `run` tool it has put one hub action per
  environment tier the program names a runner for. For an `action` tool it
  names the hub action the file names.
- The hub decides. Graft sends the call with who it is for: the person,
  the lane's action, the repository's entity, and, during a build under an
  approved plan, that plan's run. The hub checks the tier on every call and
  runs the command on its own runner with that runner's key.
- The answer is kept as numbered evidence and printed with its number. A
  call the hub refuses is an answer too, so the agent reads why.
- A call that read production marks the request. Annals gets the shape
  of the call: which tool, which tier, which environment, how it ended.
  Never its inputs or its answer.

`graft evidence add <where> "<what it shows>"` keeps a code reference or
a test run as evidence the same way.

## The hub

Graft uses two hub credentials.

- A **source token** declares Graft's own actions and writes its
  entities. Its source needs the `actions` grant, and each runner that
  serves Graft's tools names that source in its `serves.sources`.
- A **machine access token** asks for runs, lends tool calls, reports, and
  checks grants. Its role holds `actions.ask`, `tools.lend`,
  `actions.report`, and `actions.list`.

A lane is the program's, and one hub action, `graft_<program>_<lane>`, on
the blueprint of the program's repositories. It has no form and a `caller`
backend.

- A person **holds a lane** when the lane's `members` in the program file
  let them in, as the hub judges those grants on the person's own record
  (`POST /v1/access/lets-in`). What the person may see in the hub plays no
  part. A person no lane names holds the fallback lane. Graft keeps each
  person's answer for a minute. The request enters the highest lane held.
- A **plan approval** is a run of that action that Graft asks as itself,
  with its machine token, for the person: `requestedFor` names them, and
  the plan is its inputs. The action's access lets Graft's token in; the
  person needs no access, sees nothing, never decides the run, and is told
  nothing of it by the hub. Whatever they hear comes from Graft. In a
  lane whose plans wait, the ask names the approvers and the run waits for
  one of them. In a lane with `approval: false` it names none and runs at
  once. Graft reads the run every 30 seconds.
  Approved, Graft reports it `running` and keeps it so while it builds,
  so the tiers the run was granted stay live; it reports it finished when
  the request ends.
- A run that asks for **more tiers** than the lane carries always waits,
  and only someone who holds those tiers approves it. Such a plan names
  the approvers in every lane. Once approved, a tool call lent to the
  person's request with the run carries those tiers, even when the person
  may not ask for the lane's action in the hub.
- A plan **answered** by its approvers (sent back with a note in the hub,
  or a comment on the ticket from someone the approvers let in) is planned
  again as its next round, and the new ask names the run of the round
  before as the one it revises: the hub supersedes it when it still waits,
  and keeps one approval. A plan asked again for any other reason, such as
  a build that reached past it, ends the old run first and asks afresh. A
  run decided while its next round was written is taken as decided: a
  decline ends the request, a note sent back is planned in too, and an
  approval of the round before ends unused.
- The run carries the **brief** as a `markdown` input, shown first and
  open on the run's page, the **build plan** as an `attachment`, folded
  until it is opened, and the person's **files** as links to Graft's
  page. The hub's approvals inbox shows them, the brief first.
- Graft names the plan's **approvers** on each ask that waits: the
  program's, or the repository's own, as the hub's grants. The hub lets only them decide the
  run: never the person who asked, and no admin by override. Graft checks
  again before it acts: the person who asked never decides, the run must
  carry the approvers Graft named, and the decider must be one of them,
  which Graft asks the hub as it judges them now
  (`POST /v1/access/lets-in`). A decision that fails is refused: the run
  ends failed with why, and the same round is asked again. A plan that
  waits that the hub lets go ahead with nobody's decision is never built:
  the run ends failed and the request waits for a person.
- The decision, who made it, when, and their note are kept on the request,
  shown on the page to the person who asked, and written to the catalog
  record.

Graft's save of an action never carries access. The admin sets it in the
hub: a grant that lets Graft's machine token ask the lane, and the tool
tiers the lane carries.

A request is also written to the catalog as a `change_request` entity. It
is a view; a hub that refuses it loses nothing. Besides its state and lane
it carries `requested_by` (who asked), `requested_at` (when), `updated_at`
(its last change in Graft), `ended_at` (when it ended; empty while it is
worked or waits), `round` (its plan's round), `plan` (the plan's run page),
`decision`, `decided_by`, `decided_at`, and what its agents spent:
`cost_usd`, `cost_screen_usd`, `cost_plan_usd`, `cost_build_usd`, and
`agent_runs` (below, Spend). The record is written again as each agent run
ends. At every start Graft writes
every request it holds, open or ended, in one incremental run of the scope
`change-requests`, two hundred a call: a record an earlier Graft wrote
gains what it lacked, and a record that already holds what it is sent
costs the hub nothing. A run that is not a snapshot never marks a record
missing. Graft also sends the hub a heartbeat as its source every 30
minutes, so a quiet day does not read as a stopped source.

## Comments on the ticket

A plan that waits has its ticket's comments read: at once when the forge's
webhook says one came, and every two minutes by a sweep besides. The
webhook only names the issue; Graft reads the comments from the forge
with its own credential, past the last one it read, and under a lock on the
request, so the sweep and a webhook never both act on one.

- Graft's own comments (by the user its forge token belongs to), a bot's,
  and a forge's own notes are passed over.
- A comment's author is a sign-in address by the program file's
  `forge.person` (`{username}@example.com`), else the address their forge
  profile shows. With neither, the comment is passed over.
- It counts only when the plan's approvers let that address in, as the hub
  judges it now (`POST /v1/access/lets-in`).
- Each comment passed over is logged with why: `ticket comment not taken`.
- A comment whose words look like they hold a secret is left out, with
  its files, and the ticket is told which comment and what kind of secret,
  never the secret. A note sent back in the hub is swept too: it is planned
  with the secret cut out.
- What counts joins the request's thread, and the request goes back to the
  investigator for the next round. When every new note is a comment, the
  investigator may answer with a reply instead, which Graft posts on the
  ticket; the round stands and still waits.
- A comment that is exactly `/graft retry`, from one of the request's
  approvers and not the person who asked, tries again the stage that
  failed (ADR-6001). It is read on a plan that waits and on a request that
  waits for a retry, and, through the webhook alone, on one that ended
  failed.
- The files a counted comment attaches come with it (ADR-2006). The forge
  adapter names a comment's uploads: on GitLab, a link
  `/uploads/<32 hex>/<name>`, relative or under the project's own address,
  and nothing else. Graft reads each through the forge's API by its secret
  and name, never from the link, at most 10 MiB and a byte, and holds it to
  the request's limits with `attach.Take`. A file taken waits in the
  request's `incoming`, unread by anyone, and its note names it. A file not
  taken is listed on the ticket with why, and the round goes on. The next
  round starts by sweeping `incoming` in a sandbox of its own, as a
  person's files are swept: a file that holds a secret, or cannot be read,
  is deleted and the ticket told; the rest join the request's files. On
  GitHub, whose API reads no comment's file, the ticket is told so.

## Runs on the ticket

A run is one pass of one stage from its start (ADR-6003). `Run` calls
`begin` before each stage and `after` once it returns: a stage of another
kind, a wait for a person, or an end closes the run well, and `stop` closes
it failed. The investigating and planning stages are one run, the round's;
the gate is a step of the build's. A run opened again after a restart, or a
stage held, goes on in its run.

- **The record.** `store.Run`, one row a run in `runs`: its kind, round,
  attempt, the run it retries or revises, its steps, the criteria of the
  plan and what proves each, its screenshots, its failure, and its comment.
  The request lists them in `Doc.Runs`.
- **The comment.** Written when the run opens, or when the ticket is
  written for a first investigation, by the worker alone. A ticker per open
  run reads the agent's report every 5 seconds and edits the comment when a
  step waits and the last edit is 30 seconds behind; the run's start, end,
  and failure are written at once. Every write keeps the run in the store.
  `note` puts Graft's own steps on the open run, and the stages add their
  clones, gate verdicts, commits, and pipelines.
- **The agent's report.** A folder beside the clone, `report/`, cleared at
  the start of each agent stage: `progress.jsonl`, one strict JSON entry a
  line (`step`, `found`, `test`, `stuck`), and `shots/` with
  `manifest.json` and the pictures. `graft report` writes them and calls
  nothing. Graft reads them through the workspace, at most 256 KB of
  progress and 64 KB of manifest, and sweeps every line for secrets.
- **Screenshots.** At the end of an investigation whose findings pass, and
  of a build whose gate passes, Graft reads each picture through the
  workspace, holds it to its kind by its bytes and to the limits (12 a run,
  5 MB each, 25 MB together, named once, in the folder), copies the bytes
  it will attach back into the box, reads them with OCR there, sweeps the
  text, and attaches what passes through the forge (`Attach`; on GitLab the
  project uploads API; a forge that takes none has each checked and named).
  What an earlier attempt of the round proved and showed is kept with the
  unfinished work, and counts.
- **Failures.** `failed` writes the cause, where the run stopped, what it
  tried, what its agent said stopped it, and how to retry into the run's
  comment, and ends it. `earlierFailures` hands the failures of a stage's
  runs since its last success to the next attempt's prompt. A retry gives
  the builder its rounds back, and goes on from the work an unfinished
  build kept.

## The gate and the fences

Before the builder starts, the workspace installs the browser each
Playwright version the repository pins wants into the cache (ADR-1001).
The builder is told the repository's commit rules and its check, and says
in `report/finish.json` that it is finished, or what is left (ADR-6005). A
round stopped at its budget or its time, or a builder that says what is
left, ends the build unfinished at once. Otherwise the runner checks,
through the workspace:

0. Build output the tests left untracked is removed (`sweepLitter`).
1. The tree is clean and no rebase is in progress.
2. There is at least one commit since the base.
3. Every commit message names the ticket. Graft names it itself first:
   each of the builder's commits whose message lacks `#<number>` is
   written again with the trailer `Refs: #<number>` under its message,
   keeping its subject, tree, author, and date (ADR-5005). The
   repository's commit-msg hook judges each new message; what it refuses
   goes back to the builder, to name the ticket in a form the hook takes.
4. The commit messages pass `release-damnit check-commits`, where the
   repository versions with release-damnit.
5. The repository's check passes: `check` in its `graft.yaml`, else
   `make check` where the root Makefile has one.
6. The builder said whether the work is finished.

Failing any sends the report back into the same session as the next
prompt, up to `GRAFT_BUILD_ROUNDS`; past it, the build is unfinished.

Then the proof. `gaps` lists each criterion with no test or screenshot and
no waiver, a change people see (by the plan's screens, or by the kinds of
file it changes) with no screenshot after it, and screenshots that do not
say what data they show. Where any is open, the prover runs, in a session
of its own (`graft-<request>-prove`) in the same sandbox, with
`limits.budget.prove`; what it commits is left out, since what lands is
the builder's commit as the gate passed it. Still open after it, or a
criterion the prover says does not hold, and the build is unfinished.

An unfinished build (`buildLeft`) takes its commits into the runner's
clone as `refs/graft/agent`, keeps `store.Unfinished` (the part left,
the list, the commit, and what was proven) on the request, and stalls the
build with the list on its comment. Nothing reaches the forge. A retry
seeds the next sandbox from that commit and hands the builder the list;
where only the proof is left, the builder is skipped.

Then the fences judge the diff, read in the runner's own clone:

- **The plan as a contract.** A protected path the plan did not name, or
  more than twice the plan's lines, means the plan no longer says what is
  shipped. Graft asks again with what was built; the new ask replaces
  the old. In a lane not marked `protected`, a protected path ends the
  request as filed.
- **A small fix.** A build nobody was asked about ships only when every
  mark holds: it adds at least one test file and edits or deletes none; the
  repository's check fails with only the new tests on the old code,
  and passed with the fix; no protected path; no dependency manifest; no
  migration or schema file; no more changed lines, outside the new tests,
  than `small_fix_lines`. Fail one and Graft asks about the plan, and
  lands what was built once a person approves.

## Landing and watching

Landing takes the agent's commits into the runner's clone and rebases them
onto the forge's main there, so a conflict becomes the agent's next prompt
instead of a failed request.

- A lane without `review` lands on main: `release-damnit land` where the
  repository uses it, a plain push otherwise.
- A lane with `review` pushes the branch `graft/<id>` and opens a pull
  request. Graft first checks that the forge itself keeps a change off
  main until a review; where it does not, the request ends as needing a
  person and nothing lands. Graft reads the pull request every 30
  seconds. Approved, it asks the forge to merge. Merged, it watches the
  commit main got. Closed, the request is declined.

Watching waits for every pipeline run the forge starts for the landed
commit (ADR-6006). Only success is green: failed, cancelled, skipped, and a
GitLab pipeline waiting for a click are not. Once every run is green, in a
repository that releases with release-damnit, Graft looks on main for
the first release commit after the landed one (`chore: release`,
`chore(release):`), keeps it as `Request.Release`, and watches its runs
too; they are the release. A run that is not green stalls the request at
`watching`, with each such run and each failed job (`FailedJobs`) linked
on the comment. A retry watches again: green, it is released; failed jobs,
their logs become the builder's next prompt, with the rule to fix forward.

When a stage sends the build back past the last round (a landing whose
rebase still conflicts), Graft writes one commit that undoes every commit of
the request on main, which it tells apart by the ticket they name (the
`Refs: #<number>` trailer, or the builder's own `#<number>`), and
lands that. A green revert ends the request as filed. A red revert stops
the repository, opens an incident on the application, and ends the request
as needing a person.

## What stops shipping

Two checks run before a build starts and before a landing, in every lane:

- **The stop switch.** One per repository. A person who helps run the
  repository flips it on the page. A critical open incident on the
  repository's service flips it until the incident closes. An incident that
  opens within an hour after Graft's own release there flips it and sends
  the request to a person.
- **Working hours.** With `hours.schedule` set, Graft builds and lands
  only while that on-call schedule has a person on. Reading and answering
  go on at any hour.

A request held by either stays where it is and is looked at again in a few
minutes.

## The suite

- **Catalog hub.** Above.
- **Incident desk.** Graft asks for open incidents on the repository's
  service and on what the catalog says it needs. It opens an incident only
  when a person must come. It records each release as a change, checks in
  with a heartbeat, reads the on-call schedule, and sends three kinds of
  notice, which it declares so each person can choose how to hear of them:
  `graft.report_answered`, `graft.fix_shipped`, and
  `graft.request_filed`.
- **Annals.** Every step is one event, under Graft on behalf of the
  person who asked, and each names the one before it as its cause. A
  down Annals loses events and stops nothing.
- **Cost reader.** Not Graft's code, and not needed for Graft's own
  record of spend. An estate may run its shipper beside the engine; each
  agent session is keyed `graft-<request>-investigate` or
  `graft-<request>-build`.

## Spend

Every prompt to the agent runtime is an agent run, and goes through
`prompt` (or `askModel`, for the screen) in `desk/spend.go`, which keeps what
it consumed (ADR-6004).

- **What the runtime says.** The ion CLI ends an attached prompt with its
  report of the run on its last line, once the engine closed the run:
  `Run: {...}` with the model, the cost, turns, time, and the provider's
  tokens, cache reads and writes among them. It does so after an error too,
  so a run stopped at its budget says what it spent. The adapter reads only
  the output's last line, so nothing an agent printed passes as the report,
  and leaves it out of a screen's answer. Without a report it takes the
  dollars of the stop line (`stopped`); without either the run's spend is
  `unknown`, and counts as nothing.
- **What is kept.** One row in `agent_runs` a prompt: its stage (`screen`,
  `investigate`, `revise`, `build`), plan round, the run on the ticket it
  belongs to, that run's attempt, its try within it, whether it is a
  wind-down, its session, and the figures. The open run adds it to its own
  tally (`Run.Agents`). A request's spend is read from its rows, never kept
  twice: `store.Spend` is its total and each stage of each round.
- **Where it shows.** The catalog record, written again as each run ends
  (`show`). The page's detail, for whole readers alone (`Full.Spend`). The
  ticket, only with `ticket.cost`: otherwise a run's comment says its
  prompts and turns, and no dollar, and causes name the program key that
  gives more room, never an amount.
- **Counted.** `graft.agent.spend` (US dollars) and `graft.agent.runs`,
  by `program`, `lane`, and `stage`; the runs also by `outcome` and `cost`.

Each agent run is also started with a limit: its stage's budget from the
program file, `limits.budget.investigate`, `.revise`, `.build`, or `.prove`
(ADR-6002), and a small fixed sum for the screening call. The prompt tells the agent its budget.

The runtime stops a run at its limit between turns, keeps the session, and
says so (`Error: budget exceeded:`), which the Ion adapter
reads as `runtime.ErrBudget`. So Graft starts the run with four fifths of
the budget, and a run that reaches it is sent one more prompt in the same
session, with the fifth held back and a few turns: write what you must now.
What it wrote is then read as any run's is, and taken when it passes; a run
that ran out with nothing that passes fails its stage with that cause.

## When a stage fails

`Run` hands every stage's error to `stop` in `stall.go`, with the stage.
The cause is kept in plain words on the request's `Stall`, with the stage a
retry runs again and how many times it failed.

- A round that answers the approvers (open notes, a plan, and the plan's
  run still `awaiting` in the hub) goes back to `awaiting_plan`. The plan
  and verdict of the round before are put back if the failed round had
  already replaced them, and the new round's findings are kept on the
  stall as its draft. The notes stay open.
- Any other stage from `investigating` on writes the ticket if there is
  none, comments the cause and how to retry, and waits as `stalled`. An
  approved run stays `running` in the hub, labelled as waiting for a retry.
- A stage before `investigating`, or a stall whose ticket cannot be written
  or commented, ends `failed`. The owner is told first, so the person's
  answer says a person knows only when the ticket or a sent notice told
  one.

A retry (`Retry`, from the page or the comment) checks the asker against the
request's approvers with the hub, as a decision is checked, puts the request
back at the stall's stage (asking about the draft, where there is one),
clears its answer and error, and lets a worker take it. `Retryable` also
reads a request that failed before stalls were kept, from what a failed
revision leaves on it.

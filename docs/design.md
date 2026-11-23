# Design: the request desk

Graft takes a request to change a repository that exists. A person says
what is wrong or what they want. Graft works out what is true, writes a
plan, and, when the plan is allowed, an agent makes the change and Graft
lands it and watches it ship.

This page is the design. `architecture.md` says how the running system is
laid out. `runbook.md` says how to run it.

## Words

| Word | Meaning |
|---|---|
| Request | What a person said, plus what the app saw when they said it. The unit of work. |
| Program | The wall. A set of repositories, their keys, and one sandbox. One Graft runs one program. |
| Lane | Who is asking. A lane sets how much a request may do with no person in the way. |
| Tier | How much power a tool takes: `look`, `read`, `data`, `act`. |
| Tool | One narrow thing a repository lends an agent: a command, or an action that already exists. |
| Door | Where a tool call is judged and run. The judge is the catalog hub. The hands are a hub runner. The handle is Graft's. |
| Key | One cloud identity. A runner holds exactly one. |
| Plan | Two documents: a brief its approvers decide on, and a build plan its builder follows. Every request that a change would answer gets one. |
| Round | One version of a plan put to its approvers. Each answer they give is planned into the next round. |
| Thread | What a plan's approvers said of it, in the hub or on the ticket, and the round that answered each. |
| Verdict | What the investigation found, with facts and guesses kept apart. |
| Stall | A stage that failed, kept so an approver can run it again on the same request. |
| Run | One pass of one stage from its start: an investigation, a revision, a build, a landing, a release. Its comment on the ticket is its record. |
| Agent run | One prompt Graft sends an agent runtime: the screen, an investigator's try, a builder round, the prover, a wind-down. Each keeps what the runtime said it spent. |
| Prover | The agent run, with a budget of its own, that proves each criterion of a finished build its builder left unproven: by a test, or a screenshot. |
| Unfinished | A build its builder did not say is finished, or that is not proven. Its work is kept on the request, off main, and a retry goes on from it. |

Graft is not a tracker, not an agent runtime, and not a chat. It starts
no repository. It changes a repository only when asked.

## A request, start to finish

| Stage | What happens | Who does it |
|---|---|---|
| Capture | An app's own server takes the report, and any pictures of the page or PDFs the person adds, and stamps who is signed in. The browser never says who. | The app |
| Intake | Store it. Sweep its words and its files for secrets. Match it against open requests. | Code |
| Screen | One cheap model call sorts it: bug, feature, question, junk, abuse. | Model |
| Incident check | Ask the incident desk whether an incident is open on the app or on what it depends on. If yes, link the request, answer "we know", and wait. | Code |
| Investigate | A fresh sandbox, cloned at the version the person was on. Read only. | Agent |
| Verdict | One of seven, below. Each fact cites numbered evidence. | Agent, checked by code |
| Plan | A brief and a build plan, below. The runner writes them on the ticket, never the agent. | Agent, checked by code |
| Decide | Plain code reads the plan, the lane, and the change. No model. Approvers answer the plan in the hub or on the ticket; each answer is its next round. | Code, then people |
| Build | An agent that builds the approved plan, given the request, its files, and what the approvers said, holds no credential, runs the repository's own checks, and says when it is finished. A prover proves each criterion it left unproven. | Agent, checked by code |
| Land and watch | Fences on the diff, the repository's own gate, the pipeline and the release pipeline after it, the rollout. Only a green pipeline is a release. | Code |
| Answer | One message per request, built from the verdict's fields. | Code |

A request ends in one of five outcomes: answered, filed, waiting on a
person, built and released, declined. A stage that fails ends none of
them: it waits for a retry (below).

### The ticket is the record

Each run keeps one comment on the request's ticket (ADR-6003): written when
the run starts, or when the ticket is written for a first investigation,
and edited in place as the run goes, at most every 30 seconds and at once
when it starts, ends, or fails. Only the comment's creation notifies
anyone. At the top it says what the run is, its round and attempt, how it
stands, how long it took, its agent prompts and turns, and what it is doing
now (dollars only where the program shows cost, below); then what
proves each criterion of the plan, the screenshots of the finished work,
why it stopped, and what comes next; and every step folded below:
Graft's (clones, the gate, commits with their SHAs, each pipeline and its
state) and the agent's (steps, findings, tests and their results, and what
stops it).

- **The agent writes files, never the forge.** It reports in a folder
  beside its clone, as progress lines and screenshots with a manifest, with
  `graft report`. Graft reads them as data, sweeps them for secrets, and
  makes every comment, edit, and upload with its own token.
- **Screenshots show the finished work.** A plan says which screens its
  build changes and how they are shown. The builder takes a before
  screenshot of each, and the prover an after one, with the repository's
  own end-to-end tooling, in the sandbox, with its test or demo data, and
  says what proves each criterion: a screenshot, or the named test where
  nothing is seen. A build lands only when every criterion is proven, and
  a change people see has a screenshot after it, or an approver waived it
  (below). An approver checks the finished work on the ticket, without
  signing in to anything.
- **Never production data.** The screenshots show only the sandbox's
  fixtures, and say which. A check that looks at an environment after a
  release uses Test with demo data, never production.
- **A failure is in its run's comment**, with where it stopped, what it
  tried, and what its agent said stopped it; the next attempt of that stage
  is told all of it. A retry's comment links the attempt it retries, and a
  revision's the round it revises.
- **The plan stays in the ticket's description.** The catalog's record of
  the request and Graft's page link every run's comment.

### What it cost

Every agent run keeps what the agent runtime said it spent (ADR-6004): its
dollars, model, tokens, turns, time, and how it ended, a run that failed or
was stopped at its budget too. Graft estimates nothing: a run the runtime
reported nothing of counts as unknown and adds no dollars. A request's
spend is the sum of its runs, the total and each stage of each round.

- **In the catalog**, on the request's record: what it spent in all, on the
  screen, on its plan, and on its build, and how many agent runs it took,
  current as each run ends. Who sees those fields is the hub's to say.
- **On Graft's page**, under Details, to the request's staff and
  approvers. Never to the person who asked.
- **Not on the ticket**, which every member of the project reads, unless
  the program file's `ticket.cost` says so.

### The seven verdicts

1. A real defect.
2. User error.
3. Works as designed.
4. A feature request.
5. A duplicate.
6. Not enough to act on.
7. An outage, not a code problem.

### The plan

A request is evidence of a problem, not a specification (ADR-5003). The
person knows what got in their way; they do not know the system. The
planner checks every claim of fact in the request (in the code, in the
repository's history, and, where a tool reaches it, the running system)
before it relies on one, and records each check. For a feature or any
change in behaviour it fits the change to how the system already does like
things, prefers one general way to a case per field or per person, names
what the change adds, and proposes the simplest change that serves everyone
who uses the system. It may decline, defer, or narrow parts of a request,
in words the person who asked would accept.

Every plan is two documents (ADR-5002):

**The brief**, for the approvers, read in two minutes. It names behaviour,
never files:

1. What was asked, quoting the person where it matters, and the problem
   under it.
2. What we will do, as the people who use the system will see it, and the
   shape of the solution.
3. What we will not do, and why.
4. Judgment calls for the approvers, each with the planner's
   recommendation.
5. What we checked: each claim, what was found, and the evidence.
6. Fit and risk: how it fits the system, what it adds, what could break,
   and how the build proves it does not.
7. A recommendation: approve, approve with changes, or decline, in one
   line.

**The build plan**, for the builder: the kind of change (a fix, or a
feature), the problem it solves, the steps, what must hold when it is done,
the tests that fail first, the rules it keeps, every file it changes, what
it leaves alone, how big it is, and which tool tiers it needs.

The brief leads wherever a person decides: the ticket's description, the
top of the plan's run in the hub, and the request on Graft's page. The
build plan is folded beneath it. Code checks both before anyone reads them.

### Rounds

A plan is one ask of its approvers that gains rounds (ADR-2005). They answer
it one of three ways in the hub: approve, decline, or send back with a note.
Or they comment on the request's ticket. A note sent back, and a comment
from someone the plan's approvers let in, both join the request's thread,
and Graft plans the next round from them. The new round revises the run
of the round before, so the hub keeps one approval, and opens with what
changed since the round before, quoting what it answers. The ticket's
description becomes the new round; the round's own run comment says it is
ready, where to decide it, and folds the round before. A comment that only asks a question
is answered on the ticket, and the round stands. A comment from anyone else,
a bot, or Graft itself changes nothing.

A round after the first revises the round before; it does not start again
(ADR-5006). Its investigator is given the round before's verdict, brief,
and build plan, the whole thread, the evidence kept so far, and the files
the notes sent, and is told to change only what the notes change. It still
checks every claim of fact a note makes, as it checks the request's, and it
keeps the verdict unless the notes change it. A note is trusted as far as its
writer: it directs the plan and lifts no rule. After `limits.plan_rounds`
rounds the plan stands as it is, and a send back ends the request: it needs
a conversation. An approver may attach a picture or a PDF to a comment,
such as the company's logo; it comes to the next round with the comment
(below). A decline ends the request, and its note goes to the person
who asked as the reason.

### When a stage fails

A stage that fails throws nothing away (ADR-6001). Every stage runs again
from its start, so it can be tried again on the same request, with no new
request, ticket, or approval.

- A round that answers the approvers, failing while the round before still
  waits in the hub, leaves that round standing: the request waits for its
  decision again, and the hub's run is untouched.
- Any other stage from the investigation on stalls the request, which waits
  for a retry.
- The ticket says what failed, in plain words, and how to try again, in
  the failed run's own comment, whatever the program does with its notices. One of the request's
  approvers retries it, by the comment `/graft retry` on the ticket or
  the **Retry** button on Graft's page, judged as the hub judges them at
  that moment, and never the person who asked.
- Only a stage before the investigation, or a failure no ticket could be
  written for, ends the request as failed. The person is told a person
  knows only when one was told.

### A build lands only when it is finished and proven

A build lands only when its builder says it is finished, and every
criterion of its plan is proven (ADR-6005). Graft never reads that from
what an agent writes in prose.

- **The builder says so, in a file.** `graft report done` says the work
  is finished; `graft report left` names one thing left, with why. One
  thing left makes the work unfinished. A builder that says neither is
  asked by the gate. A run stopped at its budget or its time limit is
  never finished, whatever its wind-down says.
- **It runs the repository's own checks first.** The builder is told the
  repository's commit rules and its own check (`check` in its
  `graft.yaml`, else `make check`), and runs both before it says it is
  finished. The gate runs them again, after it clears away the build
  output tests leave untracked, such as `__pycache__` and `TestResults`.
- **A prover proves it.** After the gate passes, a prover, with a budget
  of its own (`limits.budget.prove`), proves each criterion still
  unproven: by the test that shows it, or a screenshot. It changes no file
  that lands. A change people see, by the plan's screens or by the kinds
  of file it changes, needs a screenshot after it. A builder that proved
  everything as it went needs no prover.
- **Unfinished work stalls, kept off main.** Its commits are kept on the
  request, in Graft's own clone, and never pushed. The run's comment
  says exactly what is left and why. A retry goes on from that work, with
  the list; where only the proof is left, only the prover runs again.
- **An approver may waive a proof**, one criterion or the screenshots, by
  `/graft waive <number or screenshots> <why>` on the ticket, then
  `/graft retry`. The comment shows who waived what, and why. The person
  who asked never waives.

### Only a green pipeline is a release

A landed change is released only when its pipeline succeeds (ADR-6006). A
pipeline that failed, was cancelled or skipped, or waits for a click
released nothing. In a repository whose release job writes a release
commit on main after the landed one, that commit's pipeline is the
release, and it must succeed too.

- **Not green stalls the request at watching.** The run's comment links
  each run and each failed job, by name.
- **A retry reads the pipeline again.** Green now, it is released. Still
  failing, the builder is given each failed job's log to fix forward, and
  the fix is built, proven, landed, and watched again. A pipeline with no
  failed job is run again on the forge first.

**The approved plan is a contract.** If the build touches a protected
path the plan did not name, or grows well past the plan's size, the build
stops and Graft asks again. The new ask replaces the old one.

### Four kinds of change

| Kind | Stops at | Told after |
|---|---|---|
| Small fix | Nowhere, in a lane the admin marks `small_fix`, for a clear defect only | The person and the owner, once it ships |
| Bigger fix or feature | The plan | |
| Protected change | The plan, and only an architect lane passes | |
| Cannot fix | Becomes an incident when people are blocked, else a ticket | The owner |

Only a clear defect may be one (ADR-2004): the screen did not take the
request for something new, the investigator found a defect, and the plan
is a fix, which only makes the system do what it was already meant to do.
A fix whose cure changes what people see is a feature, and a feature, or
any change in behaviour, waits for the approvers in every lane whose plans
wait.

A machine decides whether a clear defect's change is a small fix, from the
change itself. All of these must hold:

- A new test fails without the fix and passes with it.
- The change adds tests. It edits and deletes no test that was there.
- It touches no protected path.
- It adds no dependency and changes no data shape.
- It is under the size limit, an admin setting that starts at 150 changed lines.
- Every test passes, and the release can be rolled back.

Fail one and it is a bigger fix.

A small fix can only be judged from what was built. So in a lane marked
`small_fix`, a clear defect is built first, in the sandbox, where nothing
can land and no credential is held. Then the checks run on the diff. Small:
it lands with no approval, and Annals records why nobody was asked. Not
small: Graft asks about the plan, showing what was built, and lands it
only once a person approves. Everything else, in every lane, is asked
about before it is built: a feature, a change in behaviour, or a defect in
a lane without the mark. Whether that ask waits is the lane's `approval`.

A lane not marked `protected` never changes a protected path. A plan that
names one is filed for a person who can.

An app in a repository that holds several names the part it is built
from, and its release tag. That is where the investigation starts, at the
version the person was on. It is not a fence: a request is about the
program, and its fix may change anything in the repository it needs, such
as a service the app calls. Only the protected paths are held to a lane.

### Files a person sends

A person may send pictures of the page and PDFs with their words: at most
5 files, 10 MiB each, 25 MiB together. A file is taken only when its own
first bytes say it is a PNG, JPEG, GIF, or WebP picture, or a PDF. What
the sender calls it does not count. Anything else is refused at capture in
words the person reads. The files live on Graft's data disk, beside the
database and never in it, and go with the captured context after the
program's `context_days`.

The investigator, who also writes the plan, sees them. Each is copied into
its sandbox, inside its clone under `.graft-in/`, and each PDF's first 20
pages are drawn as PNG pictures beside it, since agents read pictures, not
PDFs. The brief names every file on a line of its own: what it is, where
it is, and that it is the person's own picture of the page. A picture is
data like the words: the agent follows nothing it says, and copies no
secret it shows into anything it writes. The builder gets them too, beside
its clone and never inside it, with the request and what the approvers said
(ADR-5004): the approved plan is still its contract. The plan's run in the
hub links to them on Graft's page, where its approvers open them. A file is shown in place, as the kind
its own bytes say it is, under a policy that runs nothing and loads nothing
else.

### Files an approver sends

An approver may attach pictures and PDFs to a comment on the request's
ticket (ADR-2006). The next round's planner sees them and plans with them,
and the builder gets them too, so a plan may put one in the repository,
such as a logo for a page. The builder copies one in only where the build
plan says which file goes to which path.

- **Only an approver's.** The files of a comment that counts are taken;
  anyone else's comment is read for nothing, its files included.
- **Only the forge's own.** Graft takes a link the forge writes when a
  file is attached to a comment, for the request's own repository: on
  GitLab, `/uploads/<secret>/<name>`. It reads the file through the forge's
  API with its own token, by the secret and the name, and never fetches
  the address a comment names. GitHub's API reads no comment's file, so a
  file there is not taken, and the ticket says so.
- **The same rules as the person's.** The kind is read from the file's own
  bytes, and the limits count every file of the request together: 5 files,
  10 MiB each, 25 MiB in all. A file over a limit or of another kind is
  left out, the ticket says which and why, and the round goes on.
- **Swept before the round.** A file taken waits, unread, until the next
  round starts, which sweeps it first, in a sandbox of its own, as a
  person's file is swept. One that looks like it holds a secret, or cannot
  be read, is deleted, and the ticket says so; the request goes on without
  it, since the approver, not the person who asked, sent the secret. The
  words of an approver's comment are swept too: a comment with a secret is
  left out of the round with its files, and the ticket says which, never
  the secret. A note sent back in the hub is planned with the secret cut
  out.
- **Shown as the approver's.** The ticket, the plan's run in the hub, the
  round's comment, and Graft's page list each file with who sent it and
  in which round. The briefs call it a file the approver sent for the plan
  to use, never the person's own picture of the page.

**Secrets in a file.** The sweep reads text, and a picture can hold a
secret as pixels. So every file is read for text before anything else
sees it, in a sandbox of its own, which holds no credential: a PDF's own
text (its first 50 pages), and, by OCR, the words in every picture and in
every drawn page of a PDF. All of it goes through the same sweep as the
words, once as OCR read it and once with the spaces inside each line taken
out, since OCR splits a long token where its letters crowd. A hit
quarantines the whole request, as a secret in the words does: its files
are deleted at once, its context is dropped, no agent reads any of it, and
the person is told to change the secret. A file the sandbox cannot read
for text is dropped, since nothing can say it holds no secret, and the
request says so. The runner never opens a file a person sent: it moves the
bytes and reads back the text, as data. OCR is not perfect: a secret too
small or too blurred to read gets through to the investigator, who is told
not to copy it.

### Incidents

A report never opens an incident. An incident opens only when a person
must come.

| Case | What Graft does |
|---|---|
| A code bug it can fix | Fixes it. No incident. |
| The cause is outside the code | Opens an incident on that system. |
| A code bug it cannot fix, and people are blocked now | Opens an incident on the app. |
| A code bug it cannot fix, and nobody is blocked | Files a ticket and tells the owner. |
| Its own fix made things worse and putting it back did not help | Opens an incident on the app. |

The request stays open and links to the incident. When the incident
closes, Graft checks again and answers. A request that still cannot be
answered after its incident closed goes to a person: Graft never opens
incidents in a loop.

## Lanes

A lane is who is asking. The usual set, lowest first: `user`,
`stakeholder`, `qa`, `developer`, `architect`.

- **A lane belongs to the program**, and each program's lanes are set on
  their own. A lane's members are named in the program file, by team,
  department, or person, and the hub judges those grants on the person's
  own record. What a person may see in the hub, or in a cloud, plays no
  part: a person who can see no repository still holds a lane.
- The program's lowest lane may be the fallback, the lane for everyone
  else: anyone an intake app or the page vouches for who holds no other
  lane holds it. A request from an intake app is never refused for want
  of a lane.
- A person who holds several lanes enters the highest: a lane above
  another names its members for more, so the platform's own people work in
  their own lane. They may pick a lower one they hold on Graft's page.
- Nobody can ask for a lane they do not hold. No approval moves a request
  to another lane. The lane follows the writer from start to finish.
- An approval grants only the tool tiers the plan names, to that one
  request. An approver can grant only a tier they hold on that repository.
- A developer may send the same idea as their own request. It links to
  the first.

There are two stops, and a lane's settings say which apply:

- **Plan approval.** Asked and decided in the catalog hub's approvals, by
  the program's approvers: the platform owner's team. The run is
  Graft's own, asked for the person, who is named on it as data and
  needs no access in the hub. The person who asked never approves their
  own plan, in any lane, and neither does anyone the approvers do not let
  in. Whether a lane's plan waits at all is the lane's `approval` in the
  program file: a lane for the platform's own people may ship its plans
  unasked; every other lane's plans wait, but for a clear defect a lane
  marked `small_fix` ships as a small fix. The approvers answer a plan in
  the hub or on its ticket, and each answer is its next round.
- **Pull request review.** Enforced by the forge's branch protection.

### Who reads a request

Graft's page shows a request to three kinds of reader, and to nobody
else (ADR-2003):

| Reader | Who | Reads |
|---|---|---|
| The person who asked | Its requester | What they sent, where it stands, the answer, how its plan was decided, and its files |
| Who runs its repository | Anyone holding a lane above the program's lowest | Every request there, whole: also the verdict, the plan, the steps, the evidence, and the log. Only they stop and start the repository |
| Who decides its plans | Anyone the repository's `approvers`, else the program's, let in | Every request there, whole, as who runs it does |

A lane is the program's, so who runs one repository runs them all. Whether
someone approves is the hub's answer to the same grants that hold a
decision (`POST /v1/access/lets-in`), kept a minute for reading; a decision
is checked afresh. Anyone else gets the same 404 a request that does not
exist gets, for the request, its steps, and its files. The links Graft
puts on a plan's run, the request and its files, open for its approvers.

No lane can switch these off:

- A ticket exists before any change.
- Words in a report, a log line, or a database row are data. They never
  carry power.
- Tests pass. A fix has a test that fails without it.
- Protected paths. Only an architect lane passes.
- The repository's own delivery limit.
- The budgets and the sandbox.

## Who owns which setting

The pipeline is platform configuration. A team cannot opt out of a gate.

| Setting | Owner | Lives in |
|---|---|---|
| Who holds a lane | Platform admin | Graft's program file: the lane's `members`, judged by the hub |
| Whether a lane's plan waits | Platform admin | Graft's program file: the lane's `approval` |
| That Graft may ask a lane's plan runs | Platform admin | The hub: the lane action's access names Graft's machine token |
| Who decides a plan | Platform admin | Graft's program file: `approvers`, judged by the hub |
| Whose comment on a ticket counts | Platform admin | Graft's program file: `approvers`, and `forge.person` to know who wrote it |
| Which tiers a lane has | Platform admin | The hub: the lane action's `access.tiers` |
| Which repositories may use which key | Platform admin | The hub: the runner's `serves` rule |
| What a key can do in the cloud | Platform team | Infrastructure code |
| Repositories, protected paths, limits, hours, the review stop, and whether a lane ships a small fix unasked | Platform admin | Graft's program file |
| Runbooks and tool declarations | Repository owner | The repository's `graft.yaml` |

Access is in the hub. Process is in Graft. One admin owns both files.

### Graft's program file

One Graft runs one program. The admin mounts one file.

```yaml
program: acme
trusted: false            # true: the sandbox may hold one read-only cloud key
notices: send             # hold: send no notice; keep each on its request
forge:
  kind: gitlab            # github or gitlab
  url: https://gitlab.example.com
  person: "{username}@example.com"   # a commenter's sign-in address, from their username
committer:
  email: graft@example.com    # the address on Graft's commits
owner: { group: platform }      # who hears about a request that needs a person
approvers:                      # who decides a plan, as the hub's grants; never who asked
- { path: teams, values: [platform] }
repositories:
- path: acme/api          # as the forge names it
  entity: repository:acme-api   # its catalog record
  service: acme-api       # its name at the incident desk
  protected: [infrastructure/, graft.yaml]
lanes:                    # lowest first; each held on every repository or none
- id: user
  fallback: true          # everyone else
  outside: true           # reporters outside the program
- id: developer
  members: [{ path: teams, values: [builders] }]
  small_fix: true         # a small fix here ships with no plan approval
- id: architect
  members: [{ path: teams, values: [platform] }]
  approval: false         # its plans ship unasked
  protected: true         # may pass a protected path, at the plan
  review: true            # lands through a pull request
ticket:
  runs: true              # each run keeps one comment on the ticket, edited as it goes
  screenshots: true       # a run that changes what people see ends with screenshots on it
  cost: false             # true: a run's comment shows its budget and what its agents spent
limits:
  small_fix_lines: 150
  spend_usd: 5            # the base each agent stage's budget is a share of
  budget: { investigate: 2, revise: 2, build: 3, prove: 1 }   # the most one agent run may spend
  working: 2              # requests worked at once
  plan_rounds: 3
  requests_per_hour: 10   # per person
  context_days: 30        # how long captured context is kept
runners:                  # a tool's runs_as name, per environment tier
  reader:   { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
  operator: { production: acme-operator-prod, nonproduction: acme-operator-nonprod }
intake:                   # which app may report against which repository
- app: acme-web
  repository: acme/web
  environment: production
  paths: [apps/web/, shared/web/]   # the part it is built from: where the investigation starts
  tag: web-v{version}     # its release tag; empty, v{version} or {version}
hours:
  schedule: platform      # ships only while this on-call schedule has a person on
incidents:
  needs: [depends_on]     # catalog relations also checked for an open incident
```

`docs/runbook.md` describes every key.

### A repository's file

`graft.yaml` at the repository root. It is always a protected path, and
Graft reads it only from the main branch.

```yaml
runbooks: docs/runbooks/
check: [make, check]        # the repository's own verify command; the builder and the gate run it
tools:
- id: queue-depth
  tier: read
  runs_as: reader
  run: [az, servicebus, queue, show, --name, orders, --query, messageCount]
- id: restart-api
  tier: act
  action: restart_service   # a hub action that already exists
```

A tool names a key by a plain word. The program file maps the word to a
runner. The hub decides whether this repository may use that runner. A
`run` tool may take inputs, declared in the hub's own words for an
action's inputs.

## Tools

| Tier | What it is | Default |
|---|---|---|
| Look | Read the code and run the app in the sandbox | Every lane |
| Read | Read live systems: health, logs, metrics, config | A lane the admin gives it to |
| Data | Read real rows | Off in every lane |
| Act | Change a live system | A lane the admin gives it to |

Rules that hold for every tool:

- Lend a tool, not a key. A free command is still a tool.
- The store enforces read-only, never the tool.
- The server picks the system. The agent picks only the question.
- An infrastructure apply always waits for a person.
- Each answer is kept as numbered evidence on the request.
- Annals gets the shape of a call, never its values.
- A run that read production has no open internet, and sends no free
  text back to an outside reporter.
- A read-only cloud key may sit in the sandbox only in a program the
  admin marks trusted. No key that changes anything is ever in a sandbox.

### One tool call

```
the agent, in the sandbox
   |  runs `graft tool call <id>`, which holds only the request's handle
   v
Graft (the messenger)
   |  is the tool declared, on the main branch?
   |  sends the call to the hub, lent to the person who asked
   |  keeps the answer as numbered evidence
   v
the hub (the judge)
   |  does the runner it names serve this repository at this tier?
   |  does this person's lane, or their approved plan, carry the tier?
   |  writes the call to the activity log, which feeds Annals
   v
the runner that holds the key (the hands)
   |  runs the command, under limits on time and size
   v
the answer goes back the same way
```

Graft holds no cloud key and makes none of the hub's checks. It declares
each repository's tools to the hub, read from the main branch. No hub
credential enters the sandbox: the agent's command carries a secret that
names one request and works only while an agent is on it, and Graft
calls the hub with its own token, saying whose request the call serves.
The hub works out the tiers on every call.

## What Graft asks of the catalog hub

These are the hub's contracts that Graft builds on.

| Need | Contract |
|---|---|
| A lane | An action named `graft_<program>_<lane>`, on the repository blueprint, with a `caller` backend and no form. Graft declares it. The admin's access on it names Graft's machine token, and the lane's tool tiers. |
| Does a person hold a lane | `POST /v1/access/lets-in` with the lane's `members`: whether the hub's grants let that person in, judged on their own record. |
| A plan approval | A run of the lane's action on the repository, asked by Graft's machine token for the person with `requestedFor`, carrying `tiers`, the brief as a `markdown` input shown first, the build plan as an `attachment`, links to the person's files, and, on a round after the first, `revises` naming the run of the round before. The person needs no access and is told nothing by the hub. |
| The three answers | `approve` and `decline` with an optional note, and `return` with a note. A returned run ends `returned`. The run keeps the note, who decided, and when. An approver's comment on the ticket is Graft's, not the hub's. |
| A round revises the round before | `revises` names a run sent back, or one that still waits, which the hub then ends as superseded (Orrery 1.134.0). |
| Who decides a plan | The program's `approvers` (or a repository's own), named on each run that waits as the hub's grants. The hub holds such a run for them and lets only them decide it: never the person it was asked for, no admin by override. |
| Graft holds the hub to it | Before it builds, Graft refuses a decision by the person who asked, one on a run not held to its approvers, and one by someone the approvers do not let in (`POST /v1/access/lets-in`). It ends the run failed with why, keeps nothing of the decision, and asks the same round again. |
| One inbox of approvals | The hub writes each plan that waits into the estate's approvals blueprint, with the brief first, the decision, and the people who may decide it: one record however many rounds it takes, showing the newest round. The approval links to the request's `change_request` by a relation rule on the run's address, which the request keeps in its `plan` field. |
| The newer ask replaces the older | `serial: [request]` on the lane's action. |
| A tool | An action with `tool: { tier }`. A `read` or `data` tool runs on a runner and answers in the call. |
| Which repositories may use a key | `serves` on a runner source: tiers, and a filter over entities. |
| A call lent to a person | `POST /v1/tools/{id}/call` with `as`: the person, the lane action, the entity, and an approved plan run when there is one. The caller's role holds `tools.lend`. The call gets the lane's tiers where the hub lets the person ask the lane, plus the tiers the run Graft asked for them was granted. |
| A run that stays live while Graft builds | Graft reports the approved run `running`, and finished when the request ends. |
| A request as a catalog record | A `change_request` entity that Graft's source writes, with when it was asked, last changed, and ended, its plan's round, and what its agents spent, so the grid sorts by them. Graft writes every request it holds again at start, in one incremental run. |
| Who sees what a request cost | A field class in the hub's access policy, matching `cost_*` on `change_request`. Graft's offer carries no access. |
| Health of an app and what it needs | The entity's status and its relations. |

## What Graft asks of the rest of the suite

| Member | What Graft does |
|---|---|
| Annals | Posts every step, chained to the request, under the person who asked. Graft still ships when Annals is down. |
| The incident desk | Asks for open incidents before code work. Declares and sends its own three notice kinds: report answered, fix shipped, and request filed. Records each release as a change. Sends a heartbeat. Opens the rare incident. Reads the on-call schedule for its working hours. A program that holds its notices (`notices: hold`) sends none: each is kept on its request as an event that says what would have gone out, and to whom, and is never sent later. |
| The cost reader | Optional. Graft keeps each agent run's spend itself (ADR-6004); an estate that counts agent spend across its machines may still run its shipper beside the engine. |

Agents are never on call. Graft ships only while a person is. Read-only
work may run at night under the agent budgets.

## Fences

- Sign-in and a rate limit at capture.
- A secrets sweep at intake, of the words and of every file, with
  quarantine.
- The verdict and the plan are structured. Code checks them.
- The builder holds no credential, and the diff is held to the approved
  plan.
- Each agent session runs in a sandbox of its own, shut when the session
  ends. In a cluster that is a pod per session, so no session reads what
  another left, but for the package caches the estate keeps in one folder
  (ADR-1001).
- A build lands only when its builder says it is finished and each
  criterion is proven (ADR-6005); only a green pipeline is a release
  (ADR-6006).
- The fences judge the diff, not what the agent says about it.
- The repository's own gate and pipeline. There is no review gate of
  Graft's own.
- A new feature ships switched off.
- A stop switch per repository. An open critical incident on a service
  flips it for that service.
- A loop guard: an incident that opens soon after Graft's own release
  goes to a person.
- A budget for every agent run, by stage, set in the program file
  (ADR-6002). The agent is told it, and asked to write what it found
  before it runs out.
- Form values are stripped from captured context, and context is deleted
  on a timer.

## Crossings

Graft reaches each of these through an adapter, and names no estate:

- **The forge.** GitHub and GitLab. One interface: clone address and
  credential, issues, pull requests, branch rules, pipeline state.
- **The agent runtime.** One interface: run a prompt in a directory with
  a model, a turn limit, a time limit, and a spend limit, and answer what
  the run consumed; and stop a session.

## Built to the standard

This design builds to these rules of the platform standard, version
2026.1107.1839, and, for the record of each run (ADR-6003) and what each
agent run spent (ADR-6004), 2026.1119.1418, and, for a finished and proven
build (ADR-6005), the release (ADR-6006), and the caches (ADR-1001),
2026.1119.1752:

- `self-service-cloud-operations-run-as-a-narrow-machine-identi`
- `extend-the-existing-actions-engine-every-run-goes-to-the-led`
- `operate-through-audited-tools-build-through-git`
- `one-ledger-of-what-every-agent-did-shape-defined-first`
- `one-approvals-inbox-as-a-catalog-record-every-system-pushes-`
- `ownership-lives-in-the-catalog-never-in-a-resource-tag`
- `send-every-product-s-notifications-through-one-delivery-serv`
- `set-rigor-by-what-a-failure-costs-in-a-few-named-classes`
- `agents-deliver-their-own-verified-work-except-infrastructure`
- `an-incident-desk-holds-every-alert-as-an-incident-with-an-ow`
- `let-the-incident-assistant-read-and-write-but-never-act-with`
- `production-data-never-reaches-a-lower-environment-unmasked`
- `decide-what-a-managed-identity-represents-before-naming-it`
- `keep-every-estate-on-its-own-side-of-a-seam-with-a-closed-li`
- `a-secrets-sweep-on-every-imported-item-with-quarantine-and-a`
- `every-commit-traces-to-a-work-item-that-existed-first`
- `an-exploration-dossier-that-separates-facts-from-hypotheses`
- `run-implementation-unattended-from-the-specification-alone`
- `explore-read-only-classify-the-track-first-and-state-why`
- `defect-track-runtime-evidence-first-no-design-questions-unti`
- `question-the-operator-in-themed-pages-evidence-first-and-let`
- `risk-trip-wires-derived-from-evidence-independent-of-size`
- `decide-the-feature-flag-at-planning-operator-releases-after-`
- `refresh-from-webhooks-a-sweep-and-conditional-requests-behin`
- `the-runner-gates-the-agent-s-work-and-returns-failures-with-`
- `route-verdicts-by-failure-class-the-same-failure-twice-is-a-`
- `one-structured-review-pass-that-executes-every-criterion-and`
- `no-silent-failures`
- `show-agent-cost-in-tiers-of-detail-enforced-by-the-server`
- `keep-a-field-s-budget-access-rule-and-lock-as-the-estate-s-o`
- `report-cost-per-conversation-without-splitting-it-and-treat-`
- `cost-per-workload-host-agent-and-job`
- `keep-metric-labels-to-fixed-sets-and-time-work-by-task-name`
- `hold-every-builder-to-one-bar-and-count-a-check-only-where-i`
- `keep-a-request-as-one-unit-asked-landed-and-undone`
- `stop-a-failed-agent-stage-for-a-retry-never-end-the-request`
- `written-is-not-working-observed-local-gated-hands-on-then-gr`
- `land-to-remote-main-and-let-ci-release`

It departs from `the-runner-gates-the-agent-s-work-and-returns-failures-with-`
in one place: a red pipeline stalls for a person's retry, which sends it
back to the builder, instead of going back by itself (ADR-6006).

It departs from one rule in two places:

- `self-service-cloud-operations-run-as-a-narrow-machine-identi` says
  one identity per operation. A program has one operator key per
  environment tier, with a custom role that lists only the operations
  its tools use.
- The same rule gives each environment tier its own identity. In a
  trusted program, the sandbox's read key reads both tiers. It is
  read-only and sees no secret and no real data.

New to the standard: a tool as a kind of its own beside an action, a tool
call lent to the person a request is for, a runner that holds a line open,
a lane held per program by members the program file names, a plan's run
asked by the product for a person who stays data on it, a plan for every
request, a small fix built first and judged from its diff, a plan as a
brief for its deciders and a build plan for its builder, one approval
that gains rounds as its deciders answer it on the ticket or in the hub,
the work item as the live record of each agent run, screenshots of the
finished work as the proof of each criterion on it, a product that keeps
the runtime's own figure for each agent run of a work item, a finish signal
the builder must give, proof as an agent run with its own budget, an
approver's waiver of one proof, the second pipeline of a two-step release
as the release, and package caches kept across sandboxes.

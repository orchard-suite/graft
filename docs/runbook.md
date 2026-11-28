# Runbook

## Where it runs

As one pod, one replica. With `GRAFT_SANDBOX=shared` that pod has three
containers from one image: the agent runtime's engine, `graft workspace`,
and `graft`. With `GRAFT_SANDBOX=kubernetes` it has `graft` alone,
and each agent session gets a pod of its own (A pod per session, below).
The manifests live with the estate that runs it, not in this repository.
One Graft serves one program; a second program is a second Graft.

## Release

Land on `main` with `make land`, which runs `make check` and then
`release-damnit land`. The release workflow runs `make check` again, versions
every `feat` and `fix` since the last release tag, commits the version, tags
it, and calls the build workflow, which pushes the image to
`ghcr.io/orchard-suite/graft`, signs it with cosign keyless, and attests its
SBOM. A pull request runs `make check` too. A change to Markdown or `docs/` alone
cuts no release.

## The program file

`GRAFT_PROGRAM_FILE` names it. The platform admin owns it. Graft reads
it once at start and refuses to start on a key it does not know.

```yaml
program: acme
trusted: false            # true: the deployment may mount one read-only cloud key in the sandbox
notices: send             # hold: send no notice; keep each on its request instead
forge:
  kind: gitlab            # github or gitlab
  url: https://gitlab.example.com     # required for gitlab; github defaults to https://github.com
  person: "{username}@example.com"    # a ticket commenter's sign-in address, from their forge username; left out, the address on their forge profile
committer:
  name: Graft             # the default
  email: graft@example.com          # required: the address on Graft's commits
owner: { group: platform }            # who hears about requests that need a person: group, person, or chain
approvers:                # who decides a plan: the hub's grants on a person; never the person who asked
- { path: teams, values: [platform] }
repositories:
- path: acme/api          # as the forge names it
  entity: repository:acme-api         # its catalog record
  service: acme-api       # its name at the incident desk; the entity's key when left out
  protected: [infrastructure/]        # graft.yaml is always protected
  owner: { person: lead@example.com } # overrides the program's owner
  approvers: [{ values: [lead@example.com] }]   # overrides the program's approvers
ticket:
  runs: true              # each run keeps one comment on the ticket, edited as it goes; the default
  screenshots: true       # a run that changes what people see ends with screenshots on it; the default; needs runs
  cost: false             # true: each run's comment shows its budget and what its agents spent; off by default
lanes:                    # lowest first; each is the program's, held on every repository or none
- id: user
  fallback: true          # everyone a port vouches for who holds no other lane; names no members
  outside: true           # reporters outside the program: fixed words only once production was read
- id: developer
  members:                # who holds it: the hub's grants on a person, never what they may see
  - { path: teams, values: [builders] }
  small_fix: true         # a clear defect whose build the fences judge a small fix ships with no plan approval
- id: architect
  members: [{ path: teams, values: [platform] }]
  approval: false         # its plans go ahead unasked; left out, they wait for the approvers
  protected: true         # may change a protected path, when the plan names it
  review: true            # lands through a pull request
limits:
  small_fix_lines: 150
  spend_usd: 5            # the base the budgets below are shares of, where they are left out
  budget:                 # the most one agent run may spend, in US dollars
    investigate: 2        # the first round's investigator; two fifths of spend_usd when left out
    revise: 2             # a round that answers the approvers; investigate's when left out, and never less
    build: 3              # each builder run; three fifths of spend_usd when left out
    prove: 1              # the prover of each build; a fifth of spend_usd when left out
  working: 2              # requests worked at once
  plan_rounds: 3          # rounds a plan may take; at the last, it stands as it is
  requests_per_hour: 10   # per person
  context_days: 30        # how long captured context is kept
runners:                  # a tool's runs_as name, per environment tier
  reader:   { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
  operator: { production: acme-operator-prod, nonproduction: acme-operator-nonprod }
intake:                   # which app may report against which repository
- app: acme-web
  repository: acme/web
  environment: production
  paths: [apps/web/, shared/web/]   # the part it is built from: where the investigation starts, never a fence
  tag: web-v{version}     # its release tag; empty, v{version} or {version}
hours:
  schedule: platform      # builds and lands only while this on-call schedule has a person on
incidents:
  needs: [depends_on]     # catalog relations whose targets are also checked for an open incident
```

A path under `protected` or an intake app's `paths` that ends in `/`
covers everything under it; any other is one file.

An intake app with `paths` is one app in a repository that holds several.
The investigator starts there, and reads the code at the app's `tag` for
the version the person was on. A request may change anything else in the
repository its fix needs, such as a service the app calls; only the
protected paths are held to a lane.

### Lanes

A lane is the program's. Its `members` say who holds it, as grants in the
hub's shape (`{ path, field, values }`), which Graft asks the hub to judge
on the person's own catalog record (`POST /v1/access/lets-in`). What the
person may see in the hub plays no part, so a lane never depends on a
repository being visible to them. Every lane but the fallback names
members.

- **Anyone else** an intake app or the page vouches for holds the fallback
  lane. An intake app's request is never refused for want of a lane.
- **Several lanes.** A person enters the highest lane they hold: a lane
  above another names its members for more. On Graft's page they may
  pick a lower one they hold.
- **Whether a plan waits** is the lane's `approval`. Left out, the lane's
  plans wait for the approvers; `approval: false` ships them unasked, with
  no gate, and allows no `small_fix` (there is no wait to skip). The
  fallback lane always waits. A plan that asks for a tool tier waits for
  the approvers in every lane: only an approver grants a tier.
- **What may skip the wait** is the kind of change, in a lane marked
  `small_fix` alone (GRF-2004). A clear defect (the screen did not take the
  request for something new, the investigator found a defect, and the plan
  only makes the system do what it was meant to) is built first, and ships
  unasked when the fences judge it a small fix. A feature, a fix that
  changes what people see the system do, and everything in a lane without
  the mark wait for the approvers' decision on the brief.

  | Lane | A feature or a change in behaviour | A clear defect, built small | Any other defect |
  |---|---|---|---|
  | `approval: false` | Ships unasked | Ships unasked | Ships unasked |
  | Waits, `small_fix: true` | Waits | Ships unasked | Waits |
  | Waits | Waits | Waits | Waits |
- A lane whose plans wait needs approvers: the program's, or every
  repository's own.

### Budgets

Each agent run is held to the budget of its stage (GRF-6002). A run is one
prompt: the investigator's first try, its second try with the reason the
first did not pass, and each builder round after a gate report are a run
each, with a budget each. The screen is a fixed few cents.

| Key | Default | Applies to |
|---|---|---|
| `limits.budget.investigate` | Two fifths of `spend_usd`: $2.00 | The investigator of a request's first round |
| `limits.budget.revise` | `investigate`'s, and never less | The investigator of a round that answers the approvers |
| `limits.budget.build` | Three fifths of `spend_usd`: $3.00 | Each builder run |
| `limits.budget.prove` | A fifth of `spend_usd`: $1.00 | The prover of a build its builder says is finished, apart from the builder's budget (GRF-6005) |
| `limits.spend_usd` | $5.00 | Only the base the defaults above are shares of |

Graft tells the agent its budget, and holds a fifth of it back. When a
run reaches the rest, the runtime stops it between turns, and Graft asks
the same session, with the fifth held back and at most 8 turns, to write
what it must now: the investigator its verdict and plan from what it found,
the builder a commit of what is done and a list of what is left, the prover
the proofs it has. An investigator's files that pass Graft's checks are
taken even from a run that ran out entirely. A builder that reached its
budget is never finished: its commits are kept on the request and the
build stalls (below, **A build that is not finished**). The ticket names the
key above that gives it more room.

### Runs on the ticket

Each run of a request keeps one comment on its ticket (GRF-6003): the
investigation of round 1, each revision, each build, each landing, and each
release. The comment is written when the run starts, or when the ticket is
written for a first investigation, and edited as it goes: at most once in
30 seconds while steps come, and at once when the run starts, ends, or
fails. GitLab and GitHub notify a ticket's people of a new comment, not of
an edit, so a ticket's people hear once a run. The plan stays in the
ticket's description.

| Key | Default | Means |
|---|---|---|
| `ticket.runs` | `true` | Each run keeps one comment, edited in place. `false` writes the one-off comments Graft wrote before 6.4 |
| `ticket.screenshots` | `true` | A run whose plan changes what people see ends with before-and-after screenshots on its comment. Needs `ticket.runs` |
| `ticket.cost` | `false` | Each run's comment shows what its agents spent and its budget a prompt. Off, the comment shows no dollar at all: every member of the project reads the ticket |

- **What the comment shows.** What the run is, its round and attempt, how
  it stands, how long it took, how many agent prompts and turns it took,
  its agent session, and what it does now; with `ticket.cost`, also what
  its agents spent and its budget a prompt. Then each criterion of the plan with its proof, the
  screenshots, why it stopped, and what comes next. Every step folded
  below: clones, the agent's steps, findings, and tests, the gate, commits
  with their SHAs, and each pipeline and its state. A retry's comment links
  the attempt it retries; a revision's, the round it revises.
- **A failure** is written into its run's comment: the cause, where it
  stopped, what it tried, what its agent said stopped it, and how to retry.
  The next attempt of that stage is told all of it.
- **Screenshots.** A plan that changes what people see names the screens
  and how its builder shows them. The builder takes them with the
  repository's own end-to-end tooling (its Playwright setup, for one) and
  its test or demo data, in the sandbox: the builder before the change, the
  prover after it, with what proves each criterion. A build lands only when
  each criterion is proven and a change people see has a screenshot after
  it, or an approver waived it (GRF-6005). Graft takes a
  picture only when its bytes are a PNG, JPEG, GIF, or WebP, under 5 MB, at
  most 12 a run and 25 MB together, and when OCR in the sandbox finds no
  secret in it; it attaches what passes with its own token, and the comment
  lists what it left off and why. On GitHub, whose API takes no file for a
  comment, the screenshots are checked and named, not shown, and the
  comment says so.
- **Never production data.** Screenshots come only from the sandbox, with
  the repository's fixtures or demo data, and say which. Nothing in a
  sandbox signs in to a live environment. A check that looks at an
  environment after a release uses Test with demo data, never production.
- **Where to find them.** The catalog's `change_request` record lists every
  run in its `runs` field, each linked to its comment, and so does a
  request's **Details** on Graft's page.
- **The forge token** needs nothing new on GitLab: `api` writes and edits
  a note and uploads a file. The agent never holds it.

### What a request cost

Every prompt Graft sends an agent (the screen, each investigator try, each
builder round, the prover, each wind-down) is an agent run, kept with what the agent
runtime said it spent (GRF-6004): dollars, model, tokens in and out, cache
reads and writes, turns, time, and how it ended. A run that failed, timed
out, or was stopped at its budget is kept too. The figure is Ion's own, from
its model catalog's prices; Graft estimates nothing. A run Ion reported
nothing of (a timeout, a lost stream) is `unknown` and adds no dollars. This
needs the ion 3.8 CLI, which the image carries.

- **On the page.** A request's **Details** show its spend: the total, each
  stage of each round, and every agent run, folded. Only who runs its
  repository and who decides its plans get it. The person who asked never
  does, on the page or through an intake app: the server leaves it out.
- **In the catalog.** Graft offers `change_request` these fields and keeps
  them current as each agent run ends. All are numbers; the dollars are US
  dollars to the cent, shown as money.

  | Field | Title | Holds |
  |---|---|---|
  | `cost_usd` | Cost | Everything the request's agents spent so far |
  | `cost_screen_usd` | Cost: screen | The one call that sorted it |
  | `cost_plan_usd` | Cost: plan | Every investigation and revision, and their retries |
  | `cost_build_usd` | Cost: build | Every builder round and prover, gate and pipeline repairs and retries included |
  | `agent_runs` | Agent runs | How many agent runs it took |

  A request no agent ran for has none of them. Requests worked before 6.5
  have no cost: Ion never kept a session's spend by the key Graft names it
  by, so none could be filled in.
- **On the ticket.** Only with `ticket.cost` (above).
- **Counted.** `graft.agent.spend` and `graft.agent.runs` (Metrics,
  below).

#### Who sees the cost fields in the hub

Graft's offer carries no access: the hub drops any it sends, and a field
already on the blueprint keeps the access an admin gave it. So the cost
fields are seen by whoever sees `change_request`. To hold them closer, as
before sharing the blueprint wider, an admin adds a field class to the hub's
access policy. Every field it matches is seen by its audiences alone, on
every entity, and the next field Graft adds under `cost_` is caught too:

1. In the hub, open **Security**. On **Overview**, under **Classes**, press
   **New class**:
   - id `agent-cost`, title `Agent cost`;
   - match `cost_*`, blueprints `change_request`;
   - audiences: who may see what agents spent, such as the platform's own.
2. Read the preview: it names everyone who gains or loses each field. Save.

The same change by the API, as an admin, is a `POST /v1/posture/policy` of
the whole policy with the class added under `classes` and `"save": true`:

```json
{ "id": "agent-cost", "title": "Agent cost", "audiences": ["platform"],
  "match": ["cost_*"], "blueprints": ["change_request"], "include": [], "exclude": [] }
```

A field in a class takes no rule of its own. To put `agent_runs` in the
class too, add `change_request.agent_runs` to its `include`. An estate whose
money class matches `*_cents` does not catch these fields, which hold
dollars, not cents; add them there instead to hold them with the rest of
the money.

### Holding notices

`notices: hold` stops every notice Graft sends: the answer to the person,
the shipped note, and the owner's "needs a person". Nothing goes to the
incident desk or any channel behind it. Each notice is kept on its request
instead, as one event:

```
notice held: graft.report_answered to person a@example.com
About your request: Orders page is blank

<the text it would have sent>
```

The incident checks, the incidents Graft opens, its releases recorded as
changes, and its heartbeat all go on as before: only notices are held.
Graft logs `notices=hold` at start, beside `trusted` and `sandbox`, and
warns once that notices are held.

Turning it back to `send` (or leaving the key out) sends the notices that
come after. The held ones are never sent: the owner decides what to tell
people. To read them, open a request's **Details** on the page and read its
steps, or ask `GET /api/requests/{id}/events` and keep the messages that
start `notice held: `. Whoever runs the repository or decides its plans
may.

## The repository file

`graft.yaml` at a repository's root. Graft reads it from the main
branch only, at start, every five minutes, and after each release. It is
always a protected path. A file that does not parse changes nothing: the
tools declared before stay.

```yaml
runbooks: docs/runbooks/
check: [make, check]      # the repository's own verify command, as argv
tools:
- id: queue-depth
  title: Queue depth
  tier: read              # read, data, or act
  runs_as: reader         # an entry of the program's runners
  run: [az, servicebus, queue, show, --name, "{{ inputs.queue }}", --query, messageCount]
  timeout: 30s
  inputs:
    queue: { type: string, required: true }
- id: restart-api
  tier: act
  action: restart_service # a hub action that already exists
```

For each `run` tool, Graft puts one hub action per environment tier the
program names a runner for, declared from the repository's entity. It
removes the hub action of a tool that is gone.

`check` is the command that verifies a change in this repository: the
builder is told to run it before it says the work is finished, the gate
runs it on what was built, and a small fix's new test must fail it on the
code as it was. Left out, it is `make check` where the root Makefile has
that target, and the gate runs none otherwise. Give it in a repository
with no such target, or the gate checks only the tree and the commits.

## What the admin sets in the hub

Graft declares; the admin grants. None of this is in Graft's files.

- **A source** for Graft, with the `actions` grant, registered for the
  `change_request` blueprint. Its token is `ORRERY_PUSH_TOKEN`. Graft
  sends the hub a heartbeat as this source every 30 minutes, so give it a
  cadence of an hour or more; a request that moves counts too.
- **A machine access token** whose role holds `actions.ask`, `tools.lend`,
  `actions.report`, and `actions.list`. It is `GRAFT_ORRERY_TOKEN`.
- **Each lane action's access**, on `graft_<program>_<lane>` that Graft
  declares at start: a grant that names Graft's machine token, so
  Graft may ask the lane's plan runs as itself, and leaves out
  `approval` (the program file says whether a plan waits):

  ```json
  { "grants": [{ "values": ["<the machine token's name>"] }] }
  ```

  Add `tiers` for the tool tiers that come with the lane, and a rule for
  the people whose own tool calls should carry them: a lane's tiers are
  lent to a person only where a rule lets them ask the lane. Who holds a
  lane is the program file's, never this rule's.
- **Each runner's `serves` rule**: its tiers, Graft's source under
  `sources`, and the repositories it may be declared from.

### Plans decided in the hub

A plan is read and decided on its run's page in the hub, and in the
estate's approvals inbox when the hub writes one.

- **Who asks it.** Graft, as its machine token, for the person who asked:
  the run names them as `requestedFor` (shown as "Asked by graft for
  them"), and they need no access in the hub. The hub tells them nothing
  of the run; what they hear comes from Graft, which `notices: hold`
  holds.
- **Whether it waits.** The lane's `approval` in the program file. A plan
  that waits names the approvers, and the hub holds it for one of them. A
  hub that lets such a plan go ahead with nobody's decision is not
  trusted: the run ends failed and the request waits for a person.
- **Who decides.** The program's `approvers`, or a repository's own: grants
  in the hub's shape, judged on each person's catalog record. The hub lets
  only them decide it: not the person who asked, not someone the lane's
  rules trust, not an admin. Graft checks again before it builds: a
  decision by the person who asked, by someone the approvers do not let
  in, or on a run the hub did not hold to them, is refused. In a lane with
  `approval: false` a run that waits anyway (the hub's own rule) is
  decided by the hub's rules, never by the person who asked.

  ```yaml
  approvers:              # the Platform team decides every plan
  - { path: teams, values: [platform] }
  ```
- **What they read.** The run leads with the plan's brief, open: what was
  asked, what will change for the people who use the system, what will
  not and why, the judgment calls for them, what was checked, fit and
  risk, and a recommendation. The build plan is folded beneath it, and the
  person's files are linked.
- **Rounds.** Each answer is the plan's next round: a send back with a
  note, or a comment on the ticket (below). The new round's run revises
  the one before, which the hub supersedes when it still waits, so the
  inbox holds one approval an ask, showing its newest round. This needs
  Orrery 1.134.0 or later.
- **The machine token** must hold `actions.ask`, which also lets Graft
  check a lane's member and a decider (`POST /v1/access/lets-in`).
- **The inbox.** With an approvals blueprint named under Access
  (`PUT /v1/access/approvals`), the hub writes each plan that waits there,
  with the brief first in `reason`, the requester, and the people who may
  decide it: one record an ask, keyed by its first round and titled with
  its round from the second on. Give the blueprint a `round` number field
  to keep the round in a field of its own. To tie each approval to its request, give that blueprint a
  relation to `change_request` and a relation rule that matches
  `target.plan == source.url`: the request's `plan` field holds its run's
  page, and so does the approval's `url`.
- **The record.** Graft offers `change_request` the fields
  `requested_by`, `requested_at` (Requested at), `updated_at` (Updated
  at: the request's last change in Graft, not the record's last write,
  which is the hub's own Last update), `ended_at` (Ended at; empty while
  the request is worked or waits), `round` (Plan round), `plan`,
  `decision`, `decided_by`, `decided_at`, and what its agents spent (above,
  What a request cost), and fills them as the request moves, the plan is
  decided, and each agent run ends. The hub adds a field it lacks to the
  blueprint it holds, and changes none it has.
- **At start** Graft writes every request it holds to the catalog again,
  in one incremental run (scope `change-requests`), so every record holds
  what its request holds now. It runs beside the ports and holds nothing
  up: 300 requests took 64 ms on a hub in a test, and 30 ms when nothing
  had changed, plus the network's time for four calls. The log says
  `requests written to the catalog` with how many; a hub that does not
  take them is tried again a minute later.

## Comments on the ticket

An approver may answer a plan by commenting on the request's ticket, as
well as in the hub (GRF-2005). A comment counts while the plan waits for a
decision, and only from someone the plan's approvers let in.

- **Who wrote it.** Graft reads a comment's author as a sign-in address
  by the program file's `forge.person`, a template on the forge username
  (`"{username}@example.com"`). Set it only where the forge's usernames come
  from the same sign-in as the hub's people, or anyone who can pick a
  username could speak as an approver. Left out, Graft takes the address
  the author shows on their forge profile, which GitLab and GitHub let a
  user set only to an address they confirmed; a comment whose author shows
  none does not count.
- **What happens.** A change becomes the plan's next round: the ticket's
  description becomes that round, the round's run comment says it is
  ready, with a link to decide it and the round before folded, and the run
  in the hub revises the one before. A comment that only asks a question is
  answered on the ticket in a comment of its own, and the round stands. Graft's own comments, a bot's, and
  anyone else's change nothing.
- **Retry.** A comment that is exactly `/graft retry` (any case) tries
  again the stage that failed (below, **When a stage fails**). It counts
  from the request's approvers, never from the person who asked.
- **Waive.** A comment that starts `/graft waive`, then a criterion's
  number or `screenshots`, then why, lets an unfinished build land without
  that proof (GRF-6005). It counts from the request's approvers, never from
  the person who asked, and only while a build waits for its proof.
  Graft says on the ticket what it took; the build goes on at the next
  retry.
- **How Graft hears.** A sweep reads the ticket of each plan that waits,
  and of each request that waits for a retry, every two minutes. The
  forge's webhook makes it at once, and is the only way a comment reaches
  a request that ended `failed`:

### The GitLab webhook

On each repository (Settings, Webhooks):

| Setting | Value |
|---|---|
| URL | Graft's forge port, at `/hooks/gitlab`, as GitLab reaches it, such as `https://graft-hooks.example.com/hooks/gitlab` |
| Secret token | The secret in `GRAFT_HOOK_SECRET_FILE` |
| Trigger | Comments, and nothing else |
| SSL verification | On |

Graft answers `202` for a comment on a request's ticket, `200` for any
other event it leaves alone, and `401` for a wrong secret. The webhook
names only the issue: Graft reads the comments from GitLab with its own
token. The file is read on every call, so a rotated secret works at once:
change the file, then the webhook. The forge's port runs only while
`GRAFT_HOOK_SECRET_FILE` is set, and only for a GitLab program; a GitHub
program relies on the sweep.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `GRAFT_PROGRAM_FILE` | required | The program file |
| `GRAFT_AUTH_MODE` | required | `proxy` trusts the front door's identity headers; `none` signs every request in as `local@localhost` (local only) |
| `GRAFT_LISTEN` | `:8080` | The page's port |
| `GRAFT_API_LISTEN` | `:8081` | The token port |
| `GRAFT_INTAKE_TOKEN_DIR` | | A directory with one file per intake app, named for the app, holding its token. Read on every request. Unset, the token port is off |
| `GRAFT_HANDLE_LISTEN` | `127.0.0.1:8082` | The handle port. Loopback only |
| `GRAFT_HANDLE_URL` | `http://<GRAFT_HANDLE_LISTEN>` | How the agent's CLI reaches the handle port from the engine's container |
| `GRAFT_HOOK_LISTEN` | `:8083` | The forge's port, where GitLab's webhook says a ticket was commented on |
| `GRAFT_HOOK_SECRET_FILE` | | A file holding the secret the forge's webhook carries. Read on every call. Unset, the forge's port is off and the sweep alone reads tickets. GitLab only |
| `GRAFT_PUBLIC_URL` | | Where a person opens Graft. It is the origin a change on the page must come from, and the link in notices, tickets, and the hub |
| `GRAFT_DATA_DIR` | `data` | The database and the request logs. The runner's alone |
| `GRAFT_REPO_DIR` | `repos` | The runner's own clones, the ones it pushes from. The runner's alone |
| `GRAFT_WORK_DIR` | `work` | The agents' clones, as the engine and the workspace see them. The runner only needs the paths to exist |
| `GRAFT_SANDBOX` | `shared` | Where an agent session runs: `shared` (the engine and the workspace beside Graft) or `kubernetes` (a pod per session) |
| `GRAFT_SANDBOX_POD_FILE` | | With `kubernetes`: the Pod manifest every sandbox starts from. Required |
| `GRAFT_SANDBOX_NAMESPACE` | Graft's own | With `kubernetes`: where the pods are made |
| `GRAFT_SANDBOX_PORT` | `8090` | With `kubernetes`: the workspace's port in the pod |
| `GRAFT_SANDBOX_START_WAIT` | `5m` | With `kubernetes`: how long a pod may take to be ready |
| `GRAFT_WORKSPACE_URL` | `http://127.0.0.1:8090` | With `shared`: where the runner reaches the workspace |
| `GRAFT_WORKSPACE_LISTEN` | `127.0.0.1:8090` | Where `graft workspace` listens. Graft sets it in a sandbox pod |
| `GRAFT_WORKSPACE_TOKEN` | | The bearer token `graft workspace` asks for. Graft sets it in a sandbox pod; never set it yourself |
| `GRAFT_FORGE_TOKEN_FILE` | | A file holding the forge token: a GitLab project or group access token, or a GitHub token. Read on every use. Set this or the GitHub App, not both |
| `GRAFT_GITHUB_APP_ID` | | A GitHub App, for a GitHub forge |
| `GRAFT_GITHUB_APP_KEY_FILE` | | The App's private key (PEM) |
| `GRAFT_ORRERY_URL` | required | The catalog hub's machine port |
| `GRAFT_ORRERY_TOKEN` | required | The machine access token |
| `ORRERY_PUSH_TOKEN` | required | The source token |
| `GRAFT_ORRERY_PUBLIC_URL` | | Where a person opens the hub, for the link to a plan's run |
| `GRAFT_DISPATCH_URL` | | The incident desk's API port. Unset, no incident is checked or opened and nobody is told |
| `GRAFT_DISPATCH_TOKEN` | | A token the incident desk issued Graft. Required when `hours.schedule` is set |
| `GRAFT_ANNALS_URL` | | The API of Annals, the audit log. Unset, no event is posted |
| `GRAFT_ANNALS_TOKEN` | | The token Annals issued for source `graft` |
| `GRAFT_SCREEN_MODEL` | `claude-haiku-5-5` | Model for the screening call |
| `GRAFT_INVESTIGATE_MODEL` | `claude-sonnet-5-5` | Model for the investigator |
| `GRAFT_BUILD_MODEL` | `claude-sonnet-5-5` | Model for the builder |
| `GRAFT_INVESTIGATE_TIMEOUT` | `45m` | Wall clock for the investigator |
| `GRAFT_INVESTIGATE_TURNS` | `150` | Model turns for the investigator |
| `GRAFT_BUILD_TIMEOUT` | `2h` | Wall clock per builder round |
| `GRAFT_BUILD_TURNS` | `400` | Model turns per builder round |
| `GRAFT_BUILD_ROUNDS` | `3` | Builder rounds before the request is filed, or taken back out once it has landed |
| `GRAFT_PIPELINE_WAIT` | `30m` | How long to wait for a landed commit's pipeline |
| `GRAFT_OTLP_ENDPOINT` | | OTLP/HTTP receiver for the requests' metrics. Unset, none are sent |
| `GRAFT_METRICS_EVERY` | `30s` | How often they are sent |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | | OTLP/HTTP receiver for the server's own telemetry, set up by the platform kit: a span and `http.server.request.duration` per route and port, build info, and the process's CPU and memory |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. Logs are JSON lines with `trace_id` |

The Ion engine is found through its own variables (`ION_DATA_DIR`,
`ION_SOCKET_PATH`).

## A pod per session

Each sandbox is a pod of its own; only the package caches outlive one
(GRF-1001, below).

With `GRAFT_SANDBOX=kubernetes`, the estate writes one Pod manifest and
mounts it at `GRAFT_SANDBOX_POD_FILE`. Graft makes one pod from it for
each agent session and deletes it after.

```yaml
apiVersion: v1
kind: Pod
metadata:
  labels: { app: graft-sandbox }
spec:
  automountServiceAccountToken: false    # a sandbox holds no cluster credential
  securityContext: { runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000 }
  volumes:
  - { name: work, emptyDir: {} }
  - { name: engine, emptyDir: {} }
  containers:
  - name: engine                         # the agent runtime
    image: registry.example.com/graft:5.1.0
    command: [/usr/bin/tini, --, ion, serve]
    env:
    - { name: ION_DATA_DIR, value: /engine }
    - { name: ION_SOCKET_PATH, value: /engine/engine.sock }
    volumeMounts:
    - { name: work, mountPath: /work }
    - { name: engine, mountPath: /engine }
    readinessProbe: { exec: { command: [ion, health] }, periodSeconds: 2 }
  - name: workspace                      # must have this name
    image: registry.example.com/graft:5.1.0
    command: [/usr/bin/tini, --, graft, workspace]
    env:
    - { name: GRAFT_WORK_DIR, value: /work }
    - { name: ION_DATA_DIR, value: /engine }
    - { name: ION_SOCKET_PATH, value: /engine/engine.sock }
    volumeMounts:
    - { name: work, mountPath: /work }
    - { name: engine, mountPath: /engine }
```

- The container named `workspace` runs `graft workspace`. Graft gives
  it its port and its token. It shares the engine's socket, because the
  agent is started from there.
- `GRAFT_WORK_DIR` in the pod must equal Graft's own.
- The pod is ready when its probes say so. Give the engine a readiness
  probe, or the first prompt may find no engine.
- The engine's model credential, its resource limits, its node pool, and a
  read-only cloud key for a trusted program are the estate's to add here.
- Graft's service account needs `create`, `get`, and `delete` on `pods`
  in the sandbox namespace, and nothing else.
- Set `GRAFT_HANDLE_LISTEN` to `:8082` and `GRAFT_HANDLE_URL` to a
  Service for that port, so the agent's `graft tool` reaches Graft.
- Network policy: only Graft's pod reaches a sandbox's workspace port.
  A sandbox reaches Graft's handle port, the model, the forge for reads,
  the package registries its builds need, and Playwright's download host.
  It reaches no other pod.
- **The package caches.** The image points every package cache at
  `/cache`: `NUGET_PACKAGES` and `NUGET_HTTP_CACHE_PATH`,
  `npm_config_cache`, `PLAYWRIGHT_BROWSERS_PATH`, `GOMODCACHE` and
  `GOCACHE`, `UV_CACHE_DIR`, and `PIP_CACHE_DIR`. Mount one volume at
  `/cache` in the engine's container and the workspace's, so the browser
  the workspace installs is the one the agent runs, and keep it across
  sandboxes with a PersistentVolumeClaim so each request starts warm.
  Several sandboxes write it at once, so it is ReadWriteMany, or on one
  node's disk where every sandbox runs on that node. Give it to this
  Graft alone. Clearing it is safe while no sandbox runs. Set none of
  those variables in the manifest: the image's are the ones Graft and
  its docs mean.

  ```yaml
  spec:
    volumes:
    - { name: cache, persistentVolumeClaim: { claimName: graft-sandbox-cache } }
    containers:
    - name: engine
      volumeMounts:
      - { name: cache, mountPath: /cache }
    - name: workspace
      volumeMounts:
      - { name: cache, mountPath: /cache }
  ```

## The forge credential

- **GitLab.** A project or group access token in
  `GRAFT_FORGE_TOKEN_FILE`, with the `api` and `write_repository` scopes
  and a role that may push to the main branch of each repository, or, for
  a lane that lands through review, open and merge merge requests. The
  same token reads the files approvers attach to a ticket's comments,
  through the project uploads API (`GET /projects/:id/uploads/:secret/:filename`),
  which needs GitLab 17.4 or later and any role in the project. On an older
  GitLab each such file is left out, and the ticket says it could not be
  read. When GitLab redirects a read to its object store, the token is not
  sent there.
- **GitHub.** A GitHub App installed on each repository, with Contents,
  Issues, and Pull requests (write), and Actions, Checks, and
  Administration (read). Or a token in `GRAFT_FORGE_TOKEN_FILE` with the
  same reach.

Git asks for the credential through `graft git-credential`, so
release-damnit's pushes get it too. It answers only for the program's forge
and only for the program's repositories.

## What the deployment must add

Graft cannot do these itself.

- **Network.** The page's port admits the front door alone. The token port
  admits the intake apps alone. The forge's port admits the forge alone,
  for `/hooks/gitlab`. The handle port is loopback. The engine's
  container needs the model provider and nothing else that is not asked
  for: a run that read production must have no open internet, and that is
  the pod's network policy.
- **The cost reader, if the estate wants one.** Graft keeps each agent
  run's spend itself. An estate that also counts agent spend across its
  machines may run its cost reader's shipper as a sidecar that shares the
  engine's data volume. Sessions are keyed `graft-<request>-investigate`,
  `graft-<request>-build`, and `graft-<request>-prove`.
- **A trusted program's read key.** With `trusted: true`, mount one
  read-only cloud sign-in in the engine's container. Never a key that
  changes anything.
- **The intake tokens.** Mount a secret at `GRAFT_INTAKE_TOKEN_DIR` with
  one key per intake app. Give each app its own token and nothing else.
- **A browser for screenshots.** The image carries the system libraries
  Chromium needs, and no browser. Before each build the workspace installs
  the browser each Playwright version the repository pins wants (the
  lockfile's version, else the manifest's exact one) with that version's
  own `npx playwright@<version> install chromium`, into
  `PLAYWRIGHT_BROWSERS_PATH` (`/cache/ms-playwright`). Kept on the cache
  volume, it is fetched once a version. The workspace's container needs
  the npm registry and Playwright's download host for it. The browser runs
  the app inside the sandbox against the repository's fixtures; it never
  reaches a live environment.

## Sign-in

With `GRAFT_AUTH_MODE=proxy`, the front door signs people in and sets
`X-Forwarded-Email` after removing any copy the client sent. Every `/api/`
request without it gets 401, and the page reloads so the front door can
sign the person in again. Anyone the front door lets through may open the
page; what they may ask is by the lanes they hold, and what they see is in
**Reading a request**, below.

## Metrics

With `GRAFT_OTLP_ENDPOINT` set, one series per request changed in the
last day, labelled `request_id`, `repo`, and `lane`:

| Metric | Is 1 while |
|---|---|
| `graft_request_active` | The request is being worked or waits |
| `graft_request_waiting` | It waits on a person: a plan to decide, a pull request to review, a retry. Also labelled `state` |
| `graft_request_failed` | A stage of it failed: it waits for a retry, or it ended because Graft could not work it |

Beside the server's own telemetry (`OTEL_EXPORTER_OTLP_ENDPOINT`), two
counters of what agents spent, by `program`, `lane`, and `stage` (`screen`,
`investigate`, `revise`, `build`):

| Metric | Counts |
|---|---|
| `graft.agent.spend` | US dollars agent runs spent, as the runtime reported |
| `graft.agent.runs` | Agent runs, also by `outcome` (`done`, `capped`, `timeout`, `failed`) and `cost` (`reported`, `stopped`, `unknown`) |

No metric carries anything a person wrote. Graft also checks in at the
incident desk as the heartbeat `graft`, every minute, expected within
five, and at the catalog hub as its source, every 30 minutes.

## Files

A request may carry up to 5 files, 10 MiB each and 25 MiB together:
pictures of the page (PNG, JPEG, GIF, WebP) and PDFs, judged by their
bytes. They are kept under `$GRAFT_DATA_DIR/attachments/<request>/`, one
file an id, and deleted after `context_days`, with the captured context.

At intake each file is read for text in a sandbox: `pdftotext` for a PDF's
own text, `pdftoppm` to draw its pages, and `tesseract` to read the words
in every picture and page. The image ships all three. What they read is
swept for secrets as the words are, and never written to the log. A hit
quarantines the request and deletes its files. A file the tools cannot
read is dropped, and the request's steps say which.

The investigator gets the files in `.graft-in/` inside its clone, with
each PDF's first 20 pages drawn as PNGs, and one line in its brief for
each. Whoever may read the request opens a file from the page (Reading a
request, below); the plan's run in the hub links to them there, so its
approvers open them from the run. A picture or a PDF is shown in the
browser, as the kind its own bytes say it is, with `nosniff` and a policy
under which nothing runs and nothing loads but the file.

## Reading a request

The page shows each request's state and answer, the files sent with it,
and, once a plan was asked about, where it is decided and how it was: by
whom, when, and with what note. Three kinds of reader see a request
(GRF-2003):

- **The person who asked** sees their own.
- **Who runs the repository**, anyone holding a lane above the lowest,
  sees every request there and opens **Details** for the verdict, the
  plan, what its agents spent, and links to the evidence, the steps, the log, the ticket, the
  pull request, and the plan's run in the hub. Only they stop and start the
  repository.
- **Who decides its plans**, anyone the repository's `approvers` (else the
  program's) let in, sees every request there and its **Details**, as who
  runs it does. Nothing more is set for this: the program file's
  `approvers` already name them.

Anyone else signed in gets 404 for the request, its steps, and its files.
A link to one request is the page's address with `#<id>`; the plan's run
carries it as "The request in Graft", and the page opens it there. Each
read logs the call's `reader` (`requester`, `staff`, `approver`, or
`none`) and counts it: `graft.request.reads` by `reader`, and a refused
one as `graft.refusals` with `reason="not_a_reader"`.

| State | Means |
|---|---|
| `received`, `screening`, `checking`, `investigating`, `planning`, `building`, `landing`, `watching` | A worker is on it, or will be |
| `holding` | It waits for an incident to close |
| `awaiting_plan` | A person must decide the plan, in the hub, or answer it on the ticket |
| `awaiting_review` | A pull request waits for review |
| `stalled` | A stage failed, and waits for one of the request's approvers to retry it; nothing it held is lost |
| `answered` | The person got an explanation |
| `filed` | A ticket holds it for a person |
| `waiting` | It needs a person: a conversation, an unprotected main branch, a loop guard, a failed revert |
| `released` | The change is built and released |
| `declined` | Junk, abuse, a declined plan, or a closed pull request |
| `quarantined` | It held a secret; nothing read it |
| `failed` | Graft could not work it, and nobody could be told on its ticket, or it failed before the investigation; the log says why |

A request in `building` or `landing` with a hold is waiting for working
hours or for the repository to be started; **Details** says which.

## When something is wrong

- **Nobody holds a lane.** The page says so. Check the lane action's
  access rule in the hub, and that the rule names the person rather than
  leaning on an admin's override.
- **A plan never moves.** Open its run in the hub. `awaiting` waits for a
  decider; if nobody can decide it, the lane's rule names no one who may.
- **A decision was refused.** The steps say `decision refused:`, who
  decided, and why: they asked for the change themselves, the approvers do
  not let them in, or the hub did not hold the run to the approvers (an
  older hub). Graft asked the same round again. Only an approver decides
  it. An admin who must stop it withdraws the run.
- **An approver gets "no such request".** The hub does not let them in by
  the repository's `approvers`, else the program's: check the grants
  against their catalog record. An answer is kept a minute.
- **A file was dropped.** The steps name it: the sandbox could not read it
  for text, so no agent sees it. A request fails in `received` when the
  tools themselves are missing from the image.
- **A tool call is refused.** The agent's log shows why: the tier is not
  held (the lane's `tiers`, or the plan's run), or no runner serves the
  tool there (the runner's `serves` rule, or the runner is down).
- **Tools are missing.** The log line `tools not synced` has the reason: a
  `graft.yaml` that does not parse, a `runs_as` the program does not
  name, or a runner whose `serves.sources` does not name Graft's source.
- **A request stalled in `investigating`.** The verdict or the plan did
  not pass the checks twice (a brief missing a part, a check of a claim
  with no evidence, a brief that names a file, or one too long to read in
  two minutes), or the investigator ran past its budget with nothing that
  passes. The ticket and the log say which. Retry it (below).
- **A comment on the ticket changed nothing.** The log line
  `ticket comment not taken` says why: Graft's own, a bot's, no
  sign-in address known for its author (`forge.person`, or their forge
  profile), or its author is not one of the plan's approvers. A comment
  counts only while the plan waits; one on a plan already decided is
  passed over.
- **The webhook gets 401.** Its secret is not the one in
  `GRAFT_HOOK_SECRET_FILE`. Comments are still read by the sweep.
- **A request stalled in `building`.** The gate failed after the last
  round, or the builder ran past its budget and what it left did not pass.
  The build run's comment on the ticket says why, what it tried, and the
  end of the gate's report; the log ends with all of it. A retry starts
  the builder again from main with its rounds back, and tells it what the
  failed attempt hit.
- **A run's comment did not change.** The log says `run comment not
  edited` with the forge's answer; the next step tries again. A screenshot
  left off is listed on the comment with why.
- **A build waits on its evidence set.** The plan names screens, and the
  builder did not end with screenshots and a proof for each criterion, or
  say why it could not. The gate's report says which. Its approved run stays running in
  the hub, labelled as waiting for a retry, so a retry builds under what it
  granted.
- **Nothing ships.** The repository is stopped (the page shows why), or
  nobody is on the `hours.schedule` schedule.
- **Any stage fails with `workspace:`.** The workspace container is down.
- **A stopped repository after a failed revert.** A person fixes main, then
  starts the repository on the page.

### When a stage fails

Nothing a request held is thrown away (GRF-6001). Every stage runs again
from its start, so a stage that failed can be tried again on the same
request: no new request, ticket, or approval.

- **A round that answers the approvers** fails while the round before still
  waits in the hub: the request goes back to `awaiting_plan` on that round,
  and its run is left as it was. The approvers may still decide it. The
  notes the failed round answered stay open, and the next try, or the next
  comment, plans from them. Findings the failed round already wrote are
  kept, and a retry asks about them without writing them again. The person
  who asked hears nothing: what they wait on has not changed.
- **Any other stage from the investigation on** stalls the request
  (`stalled`), and writes its ticket first when it has none.
- **Either way** the ticket says what failed, in plain words, and how to
  try again, whatever `notices` says; the owner gets a `graft.request_filed`
  notice, held where notices are held.
- **Before the investigation**, or where the ticket cannot be written, the
  request ends `failed`. The person who asked is told a person knows only
  when the ticket or a sent notice told one. The stage stays retryable from
  the page.

To retry, one of the request's approvers (the repository's `approvers`,
else the program's, as the hub judges them at that moment, and never the
person who asked):

- comments exactly `/graft retry` on the request's ticket, or
- opens the request's **Details** on Graft's page and presses **Retry**
  (`POST /api/requests/{id}/retry`).

A request that ended `failed` before Graft kept the stage that failed is
retried the same way when it holds what a failed revision leaves: a ticket,
the plan's run, and an approver's note no round answered. It goes back to
writing that round from the note. Its ticket comment reaches it only
through the webhook; the page's button needs nothing.

A request that was filed, declined, or answered is sent again, by the
person or by someone in a higher lane.

### A build that is not finished

A build lands only when its builder says it is finished and every
criterion of its plan is proven (GRF-6005). Otherwise the build stalls,
and its comment has a **What is left** list.

| What the comment says | Why | What to do |
|---|---|---|
| The builder ran past its budget | A run stopped at its budget is never finished | Raise `limits.budget.build`, or retry as it is: the next attempt goes on from the kept work |
| The builder hit its time limit | The same, for `GRAFT_BUILD_TIMEOUT` | Raise it, or retry |
| The builder said what is left | It said `graft report left` | Read the list; retry, or change the plan |
| The build did not pass the gate after N rounds | The repository's check or commit rules still fail | Read the gate's lines; retry |
| The prover found the work does not meet its plan | A criterion does not hold | Retry: the builder is given what fails |
| Not every criterion is proven | No test or screenshot shows one | Retry, raise `limits.budget.prove`, or waive it |
| No screenshot after the change | A change people see has none that passed | Make the screen reachable in the sandbox, or waive the screenshots |

- **The work is kept.** Its commits are in the runner's clone, under
  `refs/graft/agent` in `GRAFT_REPO_DIR/requests/<id>`, never on the
  forge. The comment names the last one.
- **A retry goes on from it.** The builder is given those commits and the
  list. Where only the proof is left, only the prover runs, and what
  earlier attempts proved stands.
- **Build output** the tests leave untracked (`__pycache__`,
  `TestResults`, `test-results`, `playwright-report`, coverage) is cleared
  by the gate, which says what it removed. Anything else untracked fails
  the gate, as before.

### A pipeline that is not green

Only a pipeline that succeeded is a release (GRF-6006). A run that failed,
was cancelled or skipped, or waits for a click stalls the request at
`watching`, and its comment links each such run and each failed job.

- **Two pipelines.** Where the repository releases with release-damnit,
  its push pipeline's release job writes a release commit (`chore:
  release ...`) on main. Graft then watches that commit's pipelines too;
  released means they succeeded. The log says `the release job wrote
  <sha>`.
- **Retry.** A retry reads the pipeline again. Green now (a person ran it
  again on the forge), the request is released. Still failing, the builder
  is given each failed job's log to fix forward, with its rounds back. A
  pipeline with no failed job (cancelled, waiting for a click) is run again
  on the forge first.

## State

The SQLite database, the request logs, and the files people sent are the
only state. What matters
is on the forge: the tickets, the commits, and the pull requests. A
request's clones are removed when it ends. Captured context and the files
a person sent are dropped after `context_days`.

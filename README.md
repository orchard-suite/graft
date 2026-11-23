# Graft

A request desk for software that already exists. A person reports a problem
or asks for a change. Graft looks into it and answers: with an explanation,
a ticket, or a tested change that is released.

A graft joins a new shoot to a tree that already grows, so the tree keeps
its roots and gains the change.

## What happens to a request

1. **Capture.** An application's own server sends what a signed-in person
   wrote, with where they were, to Graft's token port. Or a person writes
   it on Graft's page. The request enters the highest lane the person
   holds in the program: a lane's members are named in the program file,
   and everyone else is in the fallback lane. What the person may see in
   the catalog hub plays no part.
2. **Intake.** Graft sweeps the words for secrets. A hit is quarantined:
   nothing reads it. Then it matches the request against open ones.
3. **Screen.** One cheap model call sorts it: bug, feature, question, junk,
   or abuse. Junk and abuse end here.
4. **Incident check.** If the incident desk has an open incident on the
   service or on something it needs, the request links to it, the person is
   told we know, and the request waits until it closes.
5. **Investigate.** An agent gets a fresh clone at the version the person
   was on. It holds no credential. It reaches live systems only through the
   tools the repository declares, lent through the hub. It takes the request
   as evidence of a problem: it checks each claim the request makes, in the
   code, its history, and the running system, and fits the change to the
   system for everyone who uses it. It writes a verdict, with every fact tied
   to numbered evidence, and, when a change would answer it, a plan in two
   documents: a brief its approvers read in two minutes, and a build plan
   its builder follows.
6. **Decide.** Plain code reads the verdict. It answers, files a ticket,
   opens an incident, or goes on to a change.
7. **Plan approval.** A lane with `approval: false` ships its plans with no
   approval. In any other lane a small, clear defect fix, where the lane
   allows it, ships unasked; a feature or any change in behaviour is asked
   about in the hub as Graft's own run, for the person, and waits for the
   program's approvers. They read the brief, then approve, decline, or send
   it back with a note, or comment on the ticket: each answer is the plan's
   next round, and the hub keeps one approval. The person who asked never
   decides it.
8. **Build.** A second agent builds the approved plan, given the request,
   its files, and what the approvers said. The gate checks its commits;
   fences judge the diff against the plan.
9. **Land and watch.** Graft lands the commits from its own clone, or
   opens a pull request in a lane that lands through review. It watches the
   pipeline. A red run goes back to the agent. Past the last round, Graft
   takes its own commits back out.
10. **Answer.** The person gets one message.

The ticket is the record of the work. Each run (an investigation, a round,
a build, a landing, a release) keeps one comment there, edited as it goes:
its steps, tests, commits, and pipelines, why it stopped if it did, and,
when it changes what people see, before-and-after screenshots from the
sandbox that prove each criterion of the plan.

`docs/design.md` has the whole design. `docs/architecture.md` says how the
code is laid out.

## Settings

How Graft works is in two files:

- The **program file** belongs to the platform admin: the forge, the
  repositories and their protected paths, the lanes (who holds each and
  whether its plans wait), who approves a plan, the limits, the hours,
  what each run writes on the ticket, and which app may report against
  which repository. One Graft serves one
  program.
- **`graft.yaml`** at a repository's root holds that repository's
  runbooks and tool declarations. Graft reads it from the main branch
  only.

Which tool tiers a lane carries, and that Graft may ask each lane's
action, are access rules in the catalog hub, not here.

`docs/runbook.md` lists every setting and every environment variable.

## Develop

```bash
make check                 # lint and tests
make                       # ./graft for this machine
make image                 # the image, for linux/amd64
```

The tests need git and no network. `make run` serves the page with sign-in
off; it needs a program file and the hub's and the forge's settings.

## Operate

`docs/runbook.md`. The deployment's manifests live with the estate that runs
it, not here.

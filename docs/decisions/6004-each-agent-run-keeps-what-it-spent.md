# ADR-6004: Each agent run keeps what the runtime says it spent

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

Whoever changes how Graft starts an agent, what it writes on the ticket,
the catalog record, or the page; and the admin who sets the program file
and shares the `change_request` blueprint in the hub.

## Purpose

Settles where a request's agent spend comes from, where it is kept, and who
reads it.

## Context

Graft held each agent run to a budget (ADR-6002) but knew what a run spent
only when the runtime stopped it: the ion CLI wrote `Error: budget exceeded:
$2.0150 >= $2.0000` and nothing for a run that finished. The engine knew
every run's cost, turns, and tokens on the event that closes the run, and
the CLI dropped it. Ion keeps a stored conversation by its own id, never by
the session key Graft names (`graft-<request>-<stage>`), and in a
cluster each session's engine goes with its pod, so what the runtime spent
before could not be read back.

Spend is sensitive. Every member of a forge project reads its tickets, and a
person who asked for a change should not read what it cost.

## Decision

- **The runtime reports; Graft estimates nothing.** Ion 3.8 ends every
  attached prompt with its report of the run, on stderr, once the engine
  closed the run: `Run: {"model", "costUsd", "turns", "durationMs",
  "inputTokens", "outputTokens", "cacheReadTokens", "cacheCreationTokens",
  "reason"}`, after an error too, so a run stopped at its budget says what it
  spent. Graft reads only the output's last line as the report. Without
  one, the stop line's dollars are taken, marked `stopped`. Without either,
  the run's spend is `unknown` and counts as nothing; it is never guessed.
- **Every prompt is an agent run, kept.** The screen, each investigator try,
  each builder round, and each wind-down is one row in `agent_runs`: its
  stage, plan round, the run on the ticket it belongs to, its attempt and
  try, its session, model, spend and where the figure came from, tokens,
  turns, time, and how it ended. A run that fails, times out, or is capped
  is kept like any other.
- **A request's spend is read from its rows.** Its total, and each stage of
  each round (the screen, round 1's investigation, round 2's revision, the
  build of round 2), with no figure stored twice. Each run on the ticket
  keeps its own tally of prompts, turns, and dollars, as it keeps its steps.
- **The catalog record carries it.** `cost_usd`, `cost_screen_usd`,
  `cost_plan_usd` (every investigation and revision), `cost_build_usd`
  (every builder round, gate and pipeline repairs and retries included), all
  US dollars to the cent, and `agent_runs`. The record is written again as
  each agent run ends. Every cost field starts `cost_`, so one field class
  in the hub's access policy holds them all; an offer carries no access, so
  who sees them is the hub admin's to set.
- **The page shows it to whole readers only.** Its staff and its approvers
  get the spend with the request's detail; the person who asked and the
  intake app never do. The server leaves it out, not the page.
- **Dollars stay off the ticket by default.** `ticket.cost`, off unless the
  program file turns it on. Off, a run's comment says its prompts and turns
  and no dollar: no budget, no spend, and no cause or error that names an
  amount; the cause names the program key that gives more room. On, the
  comment adds what the run spent and its budget.
- **Counted.** `graft.agent.spend` (US dollars) and `graft.agent.runs`,
  by program, lane, and stage, and the runs also by how they ended and where
  their spend came from.
- **No backfill.** No earlier session's spend can be tied to a request, so
  none is filled in.

## What this means for the code

`runtime.Usage`, answered by `Run` and `Ask`; `usage`, `lastReport`, and
`answer` in `internal/runtime/ion/usage.go`. `store.AgentRun`, `store.Spend`,
`SpendOf`, and the `agent_runs` table in `internal/store/spend.go`;
`store.Tally` on `store.Run`. `prompt`, `askModel`, and `spent` in
`internal/desk/spend.go` are the one way the desk runs an agent. `hub.Spend`
and `costField` in `internal/hub`. `Full.Spend` in `internal/web`.
`program.Ticket.Cost`. The image carries the ion 3.8.0 engine.

## Verification

In `internal/runtime/ion`: `TestAFinishedRunSaysWhatItSpent`,
`TestACappedRunStillSaysWhatItSpent`,
`TestAFailedRunWithNoReportSpendsNothingKnown`,
`TestAnAgentCannotWriteItsOwnReport`, `TestTheAnswerLeavesTheReportOut`,
and `TestWhatIon380PrintsIsRead`, on output the published 3.8.0 engine
printed. In `internal/sandbox`: `TestTheAgentRunsInsideThePod`. In
`internal/desk`: `TestAnAgentRunStoppedAtItsBudgetStillRecordsWhatItSpent`,
`TestAFailedAgentRunStillRecordsWhatItSpent`,
`TestARequestsSpendAddsUpAcrossRoundsAndRetries`,
`TestTheCatalogRecordShowsTheSpendAsEachRunEnds`, and
`TestTheTicketShowsNoCostUnlessTheProgramSaysSo`. In `internal/hub`:
`TestARequestRecordCarriesWhatItsAgentsSpent`. In `internal/web`:
`TestThePersonWhoAskedNeverSeesWhatItCost`. In `internal/program`:
`TestTheTicketShowsNoCostUnlessTheProgramSaysSo`. In Ion:
`TestAFinishedRunEndsWithItsReport`,
`TestARunStoppedAtItsBudgetStillReportsWhatItSpent`, and
`TestTranslateRunUsage_TaskComplete`.

## Consequences

- Spend is the runtime's price for the model, from its own catalog: a list
  price, not the bill. A bill, where there is one, is the truth for totals.
- A run that times out, or whose stream is lost, is closed by nobody the CLI
  hears, so its spend is unknown; the page counts such runs.
- Requests worked before this release show no cost.
- The comment no longer names a budget's dollars unless `ticket.cost` is on.

## Related

- [ADR-6002](6002-an-agent-runs-budget-is-its-stages.md)
- [ADR-6003](6003-the-ticket-is-the-record-of-each-run.md)
- [ADR-2003](2003-deciders-read-what-they-decide.md)
- Platform standard 2026.1119.1418:
  `show-agent-cost-in-tiers-of-detail-enforced-by-the-server` (the server
  leaves spend out for a reader who may not see it),
  `keep-a-field-s-budget-access-rule-and-lock-as-the-estate-s-o` (the cost
  fields' access is the hub's),
  `report-cost-per-conversation-without-splitting-it-and-treat-` (each part
  is the sum of whole runs; nothing is split), `cost-per-workload-host-agent-and-job`,
  and `keep-metric-labels-to-fixed-sets-and-time-work-by-task-name`. New
  ground beside `measure-agent-cost-per-conversation-from-the-agents-own-tran`:
  a product keeps the runtime's own figure for each agent run of a work item,
  rather than an estimate from transcripts.

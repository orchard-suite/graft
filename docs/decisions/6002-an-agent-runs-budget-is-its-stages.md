# ADR-6002: An agent run's budget is its stage's, and it writes before it runs out

| | |
|---|---|
| **Decision Date** | 2026-11-19 |
| **Decision Maker** | The Orchard authors |
| **Last Updated** | 2026-11-19 |

## Status

Accepted

## Audience

The admin who sets what agents may spend, and whoever changes how Graft
starts an agent run.

## Purpose

Settles how much one agent run may spend, who sets it, and what happens to
its work when it reaches the limit.

## Context

Every run was held to a share of `spend_usd`: two fifths to investigate and
three fifths to build, the same for a first round and for a revision that
widened the work. The runtime stops a run at its limit between turns and
says so; Graft read that as a crash and threw away what the agent had
found. Ion offers a hard limit per prompt, and a wind-down warning for the
turn limit, but no soft spend limit and no signal of spend as it goes. A
later prompt to the same session continues its conversation with a limit of
its own, and a prompt sent while a run closes is queued behind it.

## Decision

- **A budget per stage, in the program file.** `limits.budget.investigate`
  for a first round, `.revise` for a round that answers the approvers, and
  `.build` for the builder, in US dollars per run. Left out, they are two
  fifths, the same as `investigate`, and three fifths of `spend_usd`, so a
  program file from before spends as it did. A revision never gets less
  than a first round: a file that says so is refused at start.
- **The agent knows its budget.** The prompt says what the run may spend,
  and when Graft will ask for its files.
- **A fifth is held back to write.** The run starts with four fifths. One
  that reaches it is sent one more prompt in the same session, with the
  fifth and a few turns: write what you must now, from what you found.
- **What passes is taken.** Whatever an investigator wrote is checked as
  any run's is, and taken when it passes, even from a run that ran out
  entirely. One that ran out with nothing that passes stops its stage
  (ADR-6001), and the ticket names the key that gives it more room. A
  builder that reached its budget is never finished: its wind-down's
  commits are kept on the request, not landed (ADR-6005).
- **Proof has its own budget.** `limits.budget.prove` is the prover's, so a
  builder that runs out never spends it (ADR-6005).

## What this means for the code

`limits.Budget` in `internal/program`. `runtime.ErrBudget`, read by the Ion
adapter from the CLI's `Error: budget exceeded:` line. `budget`, `spends`,
and `runAgent` in `internal/desk/investigate.go` serve the investigator and
the builder alike.

## Verification

`TestEachAgentStageHasABudget` in `internal/program`;
`TestAPromptThatEndsSaysHow` in `internal/runtime/ion`;
`TestARunStoppedAtItsBudgetKeepsWhatItWrote` and
`TestARoundThatFailsLeavesTheRoundBeforeStandingAndSaysSoOnTheTicket` in
`internal/desk`.

## Consequences

- A run's work is cut a fifth short of its budget, and the wind-down's last
  turn may pass the budget by one turn's cost, as any run's may.
- The budget is per run, so the investigator's second try and each builder
  round after a gate report have their own: a request's spend is bounded by
  the budgets, `limits.plan_rounds`, `GRAFT_BUILD_ROUNDS`, and the
  retries people ask for.
- The adapter reads a line of the CLI's output; an Ion that words it
  otherwise is read as a crash until the adapter learns the new words.

## Related

- [ADR-6001](6001-a-failed-stage-waits-for-a-retry.md)
- [ADR-5006](5006-a-revision-revises-the-round-before.md)
- Platform standard 2026.1107.1839: no rule covers an agent run's spend
  limit or a wind-down before it. New ground.

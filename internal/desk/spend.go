package desk

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/store"
)

// Every prompt Graft sends an agent runtime is one agent run, and is kept
// with what the runtime said it consumed (ADR-6004): its spend, model,
// tokens, turns, time, and how it ended. A run that failed, timed out, or
// was stopped at its budget is kept too. Graft never estimates a figure
// the runtime did not give.

// The spend counters, sent wherever the platform kit sends the server's
// telemetry. Their labels are fixed sets: the program, the lane, the stage,
// how a run ended, and where its spend came from; never a request or a
// person.
var (
	spendUSD, _ = otel.Meter("graft").Float64Counter("graft.agent.spend",
		metric.WithUnit("USD"),
		metric.WithDescription("US dollars Graft's agent runs spent, as the agent runtime reported, by program, lane, and stage."))
	agentRuns, _ = otel.Meter("graft").Int64Counter("graft.agent.runs",
		metric.WithDescription("Agent runs Graft made, by program, lane, stage, how each ended, and whether the runtime reported its spend."))
)

// agentPart is what one agent prompt serves: its stage and plan round, its
// try within its run, and whether it is the wind-down of a run that reached
// its budget.
type agentPart struct {
	stage    string
	round    int
	try      int
	windDown bool
}

// prompt runs one prompt of an agent and keeps what it consumed.
func (d *Desk) prompt(ctx context.Context, r *store.Request, p agentPart, s runtime.Session, log io.Writer) error {
	use, err := d.box(r).RT.Run(ctx, s, log)
	d.spent(ctx, r, p, s, use, err, log)
	return err
}

// askModel asks a model one question and keeps what it consumed.
func (d *Desk) askModel(ctx context.Context, r *store.Request, p agentPart, s runtime.Session, log io.Writer) (string, error) {
	out, use, err := d.box(r).RT.Ask(ctx, s)
	d.spent(ctx, r, p, s, use, err, log)
	return out, err
}

// spent keeps one agent run: in the store, on the open run's tally, in the
// counters, and on the catalog record, which shows the request's spend as
// each run ends.
func (d *Desk) spent(ctx context.Context, r *store.Request, p agentPart, s runtime.Session, use runtime.Usage, runErr error, log io.Writer) {
	a := agentRunOf(r.ID, p, s, use, runErr)
	if lv := d.running(r.ID); lv != nil {
		lv.change(func(run *store.Run) {
			a.Run, a.Attempt = run.N, run.Attempt
			if run.Agents == nil {
				run.Agents = &store.Tally{}
			}
			run.Agents.Add(a)
		})
	}
	ctx = context.WithoutCancel(ctx)
	if err := d.store.AddAgentRun(ctx, &a); err != nil {
		d.log.Error("agent run not kept", "request", r.ID, "stage", a.Stage, "error", err)
	}
	d.log.Info("agent run kept", "request", r.ID, "stage", a.Stage, "round", a.Round, "run", a.Run, "try", a.Try, "wind_down", a.WindDown,
		"model", a.Model, "cost", a.Cost, "usd", a.CostUSD, "turns", a.Turns, "input_tokens", a.InputTokens, "output_tokens", a.OutputTokens,
		"cache_read_tokens", a.CacheReadTokens, "cache_creation_tokens", a.CacheCreationTokens, "ms", a.DurationMs, "outcome", a.Outcome)
	fmt.Fprintf(log, "agent run: %s, %s, %d turns, %s, ended %s, spend %s\n", a.Stage, a.Model, a.Turns, took(use.Duration), a.Outcome, spendWords(a))
	attrs := []attribute.KeyValue{attribute.String("program", d.prog.Name), attribute.String("lane", r.Lane), attribute.String("stage", a.Stage)}
	spendUSD.Add(ctx, a.CostUSD, metric.WithAttributes(attrs...))
	agentRuns.Add(ctx, 1, metric.WithAttributes(append(attrs, attribute.String("outcome", a.Outcome), attribute.String("cost", a.Cost))...))
	d.show(ctx, r)
}

// agentRunOf is one agent prompt as it is kept.
func agentRunOf(request string, p agentPart, s runtime.Session, use runtime.Usage, runErr error) store.AgentRun {
	a := store.AgentRun{Request: request, Stage: p.stage, Round: p.round, Try: p.try, WindDown: p.windDown, Session: s.Key,
		Model: use.Model, CostUSD: use.CostUSD, InputTokens: use.InputTokens, OutputTokens: use.OutputTokens,
		CacheReadTokens: use.CacheReadTokens, CacheCreationTokens: use.CacheCreationTokens, Turns: use.Turns,
		DurationMs: use.Duration.Milliseconds(), Outcome: outcomeOf(runErr)}
	if a.Model == "" {
		// The runtime runs the model it is asked for; it named none.
		a.Model = s.Model
	}
	switch use.From {
	case runtime.FromRun:
		a.Cost = store.CostReported
	case runtime.FromStop:
		a.Cost = store.CostStopped
	default:
		a.Cost, a.CostUSD = store.CostUnknown, 0
	}
	return a
}

// outcomeOf is how an agent run ended.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return store.AgentDone
	case errors.Is(err, runtime.ErrBudget):
		return store.AgentCapped
	case errors.Is(err, runtime.ErrTimeout):
		return store.AgentTimeout
	}
	return store.AgentFailed
}

// spendWords says what an agent run spent, and where the figure came from,
// for the request's own log.
func spendWords(a store.AgentRun) string {
	switch a.Cost {
	case store.CostUnknown:
		return "not reported"
	case store.CostStopped:
		return fmt.Sprintf("$%.4f (from the runtime's stop at its budget)", a.CostUSD)
	}
	return fmt.Sprintf("$%.4f", a.CostUSD)
}

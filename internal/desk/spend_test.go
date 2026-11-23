package desk

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/store"
)

// priced makes the runtime report what each prompt spent, by what it was:
// the screen, a wind-down, an investigation, or a builder round, and more
// for a run it stopped at its budget. A builder run that fails still
// reports what it spent.
func (fx *fixture) priced() {
	fx.rt.usage = func(s runtime.Session, err error) runtime.Usage {
		u := runtime.Usage{From: runtime.FromRun, Model: "claude-sonnet-5-5", Duration: time.Minute, InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 9000}
		switch {
		case s.Key == "":
			u.Model, u.CostUSD, u.Turns = "claude-haiku-5-5", 0.01, 1
		case strings.HasPrefix(s.Prompt, "Graft stopped you"):
			u.CostUSD, u.Turns = 0.25, 4
		case errors.Is(err, runtime.ErrBudget):
			u.CostUSD, u.Turns = 1.60, 30
		case strings.HasSuffix(s.Key, "-investigate"):
			u.CostUSD, u.Turns = 0.50, 10
		case err != nil:
			u.CostUSD, u.Turns = 0.30, 6
		default:
			u.CostUSD, u.Turns = 1.00, 20
		}
		return u
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAnAgentRunStoppedAtItsBudgetStillRecordsWhatItSpent pins that a run
// the runtime stopped at its budget, and the wind-down after it, are each
// kept with the runtime's figures, on the run that made them.
func TestAnAgentRunStoppedAtItsBudgetStillRecordsWhatItSpent(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram)
	fx.priced()
	fx.findings(feature(), featurePlan("a.txt"))
	write := fx.rt.investigate
	first := true
	fx.rt.investigate = func(dir, p string) error {
		if err := write(dir, p); err != nil {
			return err
		}
		if first {
			first = false
			return runtime.ErrBudget
		}
		return nil
	}
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	runs, err := fx.st.AgentRuns(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("%d agent runs, want the screen, the capped run, and its wind-down: %+v", len(runs), runs)
	}
	capped, wound := runs[1], runs[2]
	if capped.Outcome != store.AgentCapped || capped.Cost != store.CostReported || !near(capped.CostUSD, 1.60) || capped.Turns != 30 || capped.WindDown ||
		capped.Stage != store.SpendInvestigate || capped.Round != 1 || capped.Run != 1 || capped.Try != 1 || capped.Model != "claude-sonnet-5-5" || capped.CacheReadTokens != 9000 {
		t.Errorf("the capped run: %+v", capped)
	}
	if !wound.WindDown || wound.Outcome != store.AgentDone || !near(wound.CostUSD, 0.25) || wound.Run != 1 {
		t.Errorf("the wind-down: %+v", wound)
	}
	run := fx.runsOf(r)[0]
	if a := run.Agents; a == nil || a.Prompts != 2 || a.Turns != 34 || !near(a.USD, 1.85) {
		t.Errorf("the run's tally: %+v", run.Agents)
	}
}

// TestAFailedAgentRunStillRecordsWhatItSpent pins that a run that fails is
// kept, with what the runtime reported, and one the runtime reported
// nothing of is kept as unknown, counting no dollars.
func TestAFailedAgentRunStillRecordsWhatItSpent(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram)
	fx.priced()
	fx.rt.investigate = func(string, string) error { return errors.New("the engine went away") }
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateStalled {
		t.Fatalf("state %s", r.State)
	}
	runs, _ := fx.st.AgentRuns(context.Background(), r.ID)
	if len(runs) != 2 || runs[1].Outcome != store.AgentFailed || !near(runs[1].CostUSD, 0.50) || runs[1].Cost != store.CostReported {
		t.Fatalf("the failed run: %+v", runs)
	}

	fx = newFixtureOf(t, roundsProgram)
	fx.rt.investigate = func(string, string) error { return errors.New("the engine went away") }
	r = fx.work(fx.submit("user", "please add a second line to the orders file"))
	runs, _ = fx.st.AgentRuns(context.Background(), r.ID)
	sp := store.SpendOf(runs)
	if len(runs) != 2 || runs[1].Cost != store.CostUnknown || runs[1].CostUSD != 0 || runs[1].Model != "i" || runs[1].Outcome != store.AgentFailed || sp.Unknown != 2 || sp.TotalUSD != 0 {
		t.Fatalf("a run the runtime reported nothing of: %+v, spend %+v", runs, sp)
	}
}

// TestARequestsSpendAddsUpAcrossRoundsAndRetries walks one request through
// a capped first round, a revision, a build that fails, its retry, and a
// gate repair, and pins its total and each stage and round, in the store
// and on its catalog record.
func TestARequestsSpendAddsUpAcrossRoundsAndRetries(t *testing.T) {
	ctx := context.Background()
	fx := newFixtureOf(t, roundsProgram)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	fx.priced()
	fx.findings(feature(), featurePlan("a.txt"))
	finds := fx.rt.investigate
	investigations := 0
	fx.rt.investigate = func(dir, p string) error {
		investigations++
		if err := finds(dir, p); err != nil {
			return err
		}
		if investigations == 1 {
			return runtime.ErrBudget
		}
		return nil
	}
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "Keep the old line too.")
	r = fx.poll(r)
	if r.PlanRound != 2 {
		t.Fatalf("round %d", r.PlanRound)
	}
	builds := 0
	fx.rt.build = func(dir, _ string) error {
		builds++
		switch builds {
		case 1:
			return errors.New("the engine went away")
		case 2:
			// A round the gate sends back: the repository's check fails.
			write(t, dir, "a.txt", "two\n")
			run(t, dir, "git", "add", "-A")
			run(t, dir, "git", "commit", "-q", "-m", "feat: add the second line")
			return nil
		}
		return fx.commitTwo(dir)
	}
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateStalled {
		t.Fatalf("the failed build: state %s", r.State)
	}
	if _, err := fx.d.Retry(ctx, r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateReleased {
		t.Fatalf("state %s", r.State)
	}

	sp, err := fx.st.Spend(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The screen, round 1's capped run and wind-down, round 2, the failed
	// build, the retry's two rounds, and its prover.
	if sp.Runs != 8 || !near(sp.TotalUSD, 0.01+1.60+0.25+0.50+0.30+1.00+1.00+1.00) || sp.Unknown != 0 {
		t.Fatalf("spend: %d runs, $%v, %d unknown", sp.Runs, sp.TotalUSD, sp.Unknown)
	}
	want := []store.SpendPart{
		{Stage: store.SpendScreen, Round: 0, USD: 0.01, Runs: 1, Turns: 1},
		{Stage: store.SpendInvestigate, Round: 1, USD: 1.85, Runs: 2, Turns: 34},
		{Stage: store.SpendRevise, Round: 2, USD: 0.50, Runs: 1, Turns: 10},
		{Stage: store.SpendBuild, Round: 2, USD: 2.30, Runs: 3, Turns: 46},
		{Stage: store.SpendProve, Round: 2, USD: 1.00, Runs: 1, Turns: 20},
	}
	if len(sp.Parts) != len(want) {
		t.Fatalf("parts: %+v", sp.Parts)
	}
	for i, w := range want {
		p := sp.Parts[i]
		if p.Stage != w.Stage || p.Round != w.Round || !near(p.USD, w.USD) || p.Runs != w.Runs || p.Turns != w.Turns {
			t.Errorf("part %d: %+v, want %+v", i, p, w)
		}
	}
	var attempts []int
	for _, a := range sp.AgentRuns {
		if a.Stage == store.SpendBuild {
			attempts = append(attempts, a.Attempt*10+a.Try)
		}
	}
	if len(attempts) != 3 || attempts[0] != 11 || attempts[1] != 21 || attempts[2] != 22 {
		t.Errorf("the builds' attempts and tries: %v, want attempt 1 try 1, then attempt 2 tries 1 and 2", attempts)
	}

	rec := fx.hub.records[r.ID].Spend
	// The build's cost holds its prover's.
	if rec.AgentRuns != 8 || !near(rec.TotalUSD, sp.TotalUSD) || !near(rec.ScreenUSD, 0.01) || !near(rec.PlanUSD, 2.35) || !near(rec.BuildUSD, 3.30) {
		t.Errorf("the catalog record's spend: %+v", rec)
	}
}

// TestTheCatalogRecordShowsTheSpendAsEachRunEnds pins that the record is
// written again as each agent run ends, so its cost is current while the
// request is still worked, and that every record written at start carries
// what its request spent.
func TestTheCatalogRecordShowsTheSpendAsEachRunEnds(t *testing.T) {
	ctx := context.Background()
	fx := newFixtureOf(t, roundsProgram)
	fx.priced()
	fx.findings(feature(), featurePlan("a.txt"))
	var mu sync.Mutex
	var seen []float64
	write := fx.rt.investigate
	fx.rt.investigate = func(dir, p string) error {
		mu.Lock()
		seen = append(seen, fx.hub.records[fx.only().ID].Spend.TotalUSD)
		mu.Unlock()
		return write(dir, p)
	}
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if len(seen) != 1 || !near(seen[0], 0.01) {
		t.Errorf("while the investigator ran, the record showed %v; want the screen's $0.01", seen)
	}
	if rec := fx.hub.records[r.ID].Spend; !near(rec.TotalUSD, 0.51) || rec.AgentRuns != 2 {
		t.Errorf("once it ran: %+v", rec)
	}
	fx.hub.records = nil
	if err := fx.d.ShowAll(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := fx.hub.records[r.ID].Spend; !near(rec.TotalUSD, 0.51) || !near(rec.PlanUSD, 0.50) || rec.AgentRuns != 2 {
		t.Errorf("written at start: %+v", rec)
	}
}

// TestTheTicketShowsNoCostUnlessTheProgramSaysSo pins that by default no
// dollar reaches the ticket: no budget, no spend, and no cause that names
// one, while each run still says its prompts and turns. With ticket.cost on,
// each run's spend and budget are there.
func TestTheTicketShowsNoCostUnlessTheProgramSaysSo(t *testing.T) {
	capped := func(fx *fixture) *store.Request {
		fx.priced()
		fx.rt.investigate = func(string, string) error { return runtime.ErrBudget }
		return fx.work(fx.submit("user", "please add a second line to the orders file"))
	}
	fx := newFixtureOf(t, roundsProgram)
	r := capped(fx)
	if r.State != store.StateStalled {
		t.Fatalf("state %s", r.State)
	}
	said := strings.Join(append(append([]string(nil), fx.forge.comments...), fx.forge.issues...), "\n")
	if strings.Contains(said, "$") {
		t.Errorf("the ticket shows dollars by default:\n%s", said)
	}
	if !strings.Contains(said, "2 agent prompts, 34 turns") || !strings.Contains(said, "limits.budget.investigate") {
		t.Errorf("the ticket lacks the run's prompts and turns, or the key that gives it more room:\n%s", said)
	}

	fx = newFixtureOf(t, strings.Replace(roundsProgram, "program: acme", "program: acme\nticket: { cost: true }", 1))
	capped(fx)
	said = strings.Join(fx.forge.comments, "\n")
	if !strings.Contains(said, "2 agent prompts, 34 turns, $1.85 spent") || !strings.Contains(said, "budget $2.00 an agent prompt") {
		t.Errorf("with ticket.cost on, the run's spend and budget are missing:\n%s", said)
	}
}

// only is the one request the fixture holds.
func (fx *fixture) only() *store.Request {
	fx.t.Helper()
	all, err := fx.st.List(context.Background(), store.Filter{})
	if err != nil || len(all) != 1 {
		fx.t.Fatalf("%d requests: %v", len(all), err)
	}
	return all[0]
}

// runsOf are the request's runs, as the store keeps them.
func (fx *fixture) runsOf(r *store.Request) []store.Run {
	fx.t.Helper()
	runs, err := fx.st.Runs(context.Background(), r.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return runs
}

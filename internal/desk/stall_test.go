package desk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/store"
)

// widened is an approver's comment that answers round 1 and widens it.
const widened = "Quantities become a number and a unit picked from a list, with a migration for the data there is."

// reread reads the request as it is kept.
func (fx *fixture) reread(r *store.Request) *store.Request {
	fx.t.Helper()
	got, err := fx.st.Get(context.Background(), r.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return got
}

// toldThePerson says a notice went to the person who asked.
func (fx *fixture) toldThePerson() bool {
	for _, k := range fx.inc.kinds() {
		if strings.HasSuffix(k, ">a@example.com") {
			return true
		}
	}
	return false
}

func TestARoundThatFailsLeavesTheRoundBeforeStandingAndSaysSoOnTheTicket(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.forge.say(r.Ticket.Number, "lead", widened)
	runs := 0
	fx.rt.investigate = func(string, string) error {
		// It reaches its budget, and reaches it again winding down, having
		// written nothing.
		runs++
		return runtime.ErrBudget
	}
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 || r.Run != "run1" || len(fx.hub.asks) != 1 {
		t.Fatalf("state %s round %d run %s asks %d", r.State, r.PlanRound, r.Run, len(fx.hub.asks))
	}
	if run := fx.hub.runs["run1"]; run.State != orrery.RunAwaiting {
		t.Errorf("round 1's run in the hub: %s", run.State)
	}
	if r.Stall == nil || r.Stall.Stage != store.StateInvestigating || r.Stall.Round != 1 || !strings.Contains(r.Stall.Cause, "ran past its budget") {
		t.Fatalf("stall: %+v", r.Stall)
	}
	if open := openNotes(r); len(open) != 1 || open[0].Text != widened {
		t.Errorf("the note the failed round answered stays open for the next try: %+v", r.Thread)
	}
	if runs != 2 || !strings.Contains(fx.rt.prompts[len(fx.rt.prompts)-1], "Graft stopped you: this run has reached its budget") {
		t.Errorf("the investigator is told to write what it has once it reaches its budget: %d runs", runs)
	}
	said := fx.lastComment()
	for _, want := range []string{"**Round 2 of the plan could not be written.** The investigator ran past its budget", "Round 1 stands as it was, and still waits for a decision. [Decide it in the hub](https://hub.example.com/actions/runs/run1)",
		"comments `/graft retry` here", "presses **Retry** on [the request in Graft](https://graft.example.com/#" + r.ID + ")", "raise `limits.budget.revise`"} {
		if !strings.Contains(said, want) {
			t.Errorf("the ticket is not told %q:\n%s", want, said)
		}
	}
	if fx.toldThePerson() {
		t.Errorf("the person who asked was told of a round that failed: %v", fx.inc.kinds())
	}
	if !strings.Contains(strings.Join(fx.inc.kinds(), " "), KindFiled+">platform") {
		t.Errorf("the owner is not told: %v", fx.inc.kinds())
	}
	if fx.hub.shown[r.ID] != store.StateAwaitingPlan {
		t.Errorf("the catalog shows %s", fx.hub.shown[r.ID])
	}

	// The person who asked may not retry it, even where the approvers let
	// them in; one of the approvers may.
	fx.hub.members["a@example.com"] = true
	fx.forge.say(r.Ticket.Number, "a", "/graft retry")
	if err := fx.d.Listen(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if r = fx.reread(r); r.State != store.StateAwaitingPlan {
		t.Fatalf("a retry by the person who asked moved it to %s", r.State)
	}
	fx.findings(feature(), featurePlan("a.txt"))
	fx.forge.say(r.Ticket.Number, "lead", "  /Graft   Retry ")
	if err := fx.d.Listen(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || r.Run != "run2" || fx.hub.asks[1].Revises != "run1" || r.Stall != nil {
		t.Fatalf("retried: state %s round %d run %s revises %q stall %+v", r.State, r.PlanRound, r.Run, fx.hub.asks[1].Revises, r.Stall)
	}
	if n := r.Thread[0]; n.Answered != 2 || n.Text != widened {
		t.Errorf("round 2 answers the note: %+v", n)
	}
}

func TestARetryAsksAboutTheRoundAFailedAskAlreadyWrote(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.forge.say(r.Ticket.Number, "lead", widened)
	fx.hub.askErr = errors.New("the hub is down")
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 || r.Run != "run1" || len(r.Earlier) != 0 || len(r.Plan.Brief.Changed) != 0 {
		t.Fatalf("round 1 stands: state %s round %d run %s earlier %d changed %v", r.State, r.PlanRound, r.Run, len(r.Earlier), r.Plan.Brief.Changed)
	}
	s := r.Stall
	if s == nil || s.Stage != store.StatePlanning || s.Draft == nil || s.Draft.N != 2 || len(s.Draft.Plan.Brief.Changed) == 0 || s.Draft.Verdict == nil {
		t.Fatalf("round 2's findings are kept: %+v", s)
	}
	if !strings.Contains(fx.lastComment(), "Graft could not finish asking about the plan: ask about the plan: the hub is down") {
		t.Errorf("the ticket: %s", fx.lastComment())
	}
	fx.hub.askErr = nil
	investigated := len(fx.rt.prompts)
	got, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com")
	if err != nil || got.State != store.StatePlanning {
		t.Fatalf("retry: %v %+v", err, got)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || r.Run != "run2" || len(r.Earlier) != 1 || r.Earlier[0].Verdict == nil {
		t.Fatalf("round 2 asked: state %s round %d run %s earlier %+v", r.State, r.PlanRound, r.Run, r.Earlier)
	}
	if len(fx.rt.prompts) != investigated {
		t.Errorf("the retry investigated again: %d prompts, was %d", len(fx.rt.prompts), investigated)
	}
	if !strings.Contains(fx.lastComment(), "**Round 2 of the plan is ready for a decision.**") {
		t.Errorf("the ticket: %s", fx.lastComment())
	}
}

// TestARequestThatFailedBeforeStallsWereKeptIsRetriedFromItsTicket is a
// request that failed writing its second round before Graft kept a stall:
// it holds the approver's open note, round 1's waiting run, and its ticket.
func TestARequestThatFailedBeforeStallsWereKeptIsRetriedFromItsTicket(t *testing.T) {
	fx, r := waitingFeature(t)
	ctx := context.Background()
	r.Thread = append(r.Thread, store.Note{Round: 1, From: store.FromTicket, By: "lead@example.com", At: "2026-11-18T20:00:00Z", Text: widened, Ref: "32"})
	r.State, r.Error, r.Answer = store.StateFailed, "investigator: ion prompt exited 1", fixedFailed+fixedTold+"\n\nTicket: "+r.Ticket.URL
	if err := fx.st.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	if s := Retryable(fx.reread(r)); s == nil || s.Stage != store.StateInvestigating || s.Round != 1 || s.Cause != "investigator: ion prompt exited 1" {
		t.Fatalf("retryable: %+v", s)
	}
	var prompt string
	fx.findings(feature(), featurePlan("a.txt"))
	write := fx.rt.investigate
	fx.rt.investigate = func(dir, p string) error {
		prompt = p
		return write(dir, p)
	}
	fx.forge.say(r.Ticket.Number, "lead", RetryCommand)
	said := len(fx.forge.comments)
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = fx.reread(r); r.State != store.StateInvestigating || r.Answer != "" || r.Error != "" {
		t.Fatalf("the retry opens it again: state %s answer %q error %q", r.State, r.Answer, r.Error)
	}
	if len(fx.forge.comments) != said {
		t.Errorf("the retry wrote a comment of its own; the run that tries again says so: %s", fx.lastComment())
	}
	r = fx.work(r)
	if c := fx.lastComment(); !strings.HasPrefix(c, "### Round 2 of the plan: a revision") || !strings.Contains(c, "Tries again what failed, as lead@example.com asked on the ticket.") {
		t.Errorf("the run that tries again: %s", c)
	}
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || fx.hub.asks[1].Revises != "run1" || r.Thread[0].Answered != 2 {
		t.Fatalf("round 2: state %s round %d revises %q thread %+v", r.State, r.PlanRound, fx.hub.asks[1].Revises, r.Thread)
	}
	if !strings.Contains(prompt, widened) || !strings.Contains(prompt, "This is round 2: a revision") {
		t.Errorf("round 2 is not planned from the approver's note:\n%s", prompt)
	}
	if rec := fx.hub.records[r.ID]; rec.State != store.StateAwaitingPlan || !rec.EndedAt.IsZero() {
		t.Errorf("the catalog shows %s, ended %v", rec.State, rec.EndedAt)
	}
}

func TestARevisionStartsFromTheRoundBefore(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.forge.say(r.Ticket.Number, "lead", widened)
	var prompt string
	fx.rt.investigate = func(dir, p string) error {
		prompt = p
		// It keeps the verdict, and writes only the plan.
		plan := featurePlan("a.txt", "b.txt")
		plan.Brief.Changed = []string{"Quantities take a unit, as the note asks."}
		raw, _ := json.Marshal(plan)
		write(t, dir, outDir+"/plan.json", string(raw))
		return nil
	}
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 {
		t.Fatalf("state %s round %d", r.State, r.PlanRound)
	}
	for _, want := range []string{
		"This is round 2: a revision.", "Do not investigate again from zero: what round 1 found stands unless a note changes it.",
		"Check what the notes change, and every claim of fact a note makes",
		widened,
		"The verdict of round 1:\n<<<\n{\n  \"verdict\": \"feature\",\n  \"summary\": \"The file lacks a line.\"",
		"The brief of round 1:\n<<<\n## What was asked",
		"The build plan of round 1:\n<<<\n",
		"The evidence kept so far, by number:\n<<<\n1: a.txt:1\n>>>",
		"Write `.graft-out/verdict.json` only if the verdict changes: without it, round 1's verdict stands.",
		"## Budget\n\nThis run may spend $2.00. At $1.60 Graft stops you",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the revision's brief lacks %q:\n%s", want, prompt)
		}
	}
	if r.Verdict == nil || r.Verdict.Summary != "The file lacks a line." || len(r.Earlier) != 1 || r.Earlier[0].Verdict == nil {
		t.Errorf("round 1's verdict stands, and is kept with round 1: %+v %+v", r.Verdict, r.Earlier)
	}
	if len(r.Plan.Build.Changes) != 2 {
		t.Errorf("round 2's plan: %+v", r.Plan.Build.Changes)
	}
}

func TestARunStoppedAtItsBudgetKeepsWhatItWrote(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram)
	fx.findings(feature(), featurePlan("a.txt"))
	write := fx.rt.investigate
	fx.rt.investigate = func(dir, p string) error {
		// Its files are whole, and it reached its budget all the same.
		if err := write(dir, p); err != nil {
			return err
		}
		return runtime.ErrBudget
	}
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 {
		t.Fatalf("state %s round %d", r.State, r.PlanRound)
	}
	if !strings.Contains(fx.rt.prompts[0], "## Budget\n\nThis run may spend $2.00. At $1.60 Graft stops you and asks for what you must write, at once.") {
		t.Errorf("the investigator is not told its budget:\n%s", fx.rt.prompts[0])
	}
	if len(fx.rt.prompts) != 2 || !strings.Contains(fx.rt.prompts[1], "Write `.graft-out/verdict.json`, and `.graft-out/plan.json` where the verdict needs a plan.") {
		t.Errorf("the wind-down: %d prompts", len(fx.rt.prompts))
	}
	if b := fx.rt.budgets["graft-"+r.ID+"-investigate"]; b < 0.39 || b > 0.41 {
		t.Errorf("the wind-down runs on the fifth held back: %v", b)
	}
}

func TestAFailedInvestigationStallsOnItsTicketAndAnApproverRetriesIt(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram)
	fx.rt.investigate = func(string, string) error { return errors.New("the engine went away") }
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateStalled || r.Ticket == nil || r.Stall == nil || r.Stall.Stage != store.StateInvestigating {
		t.Fatalf("state %s ticket %+v stall %+v", r.State, r.Ticket, r.Stall)
	}
	if said := fx.lastComment(); !strings.Contains(said, "**Graft stopped while investigating.** Graft could not finish investigating: investigator: the engine went away") ||
		!strings.Contains(said, "`/graft retry`") || strings.Contains(said, "limits.budget") {
		t.Errorf("the ticket: %s", said)
	}
	if fx.toldThePerson() || r.Answer != "" {
		t.Errorf("the person who asked was answered while it waits for a retry: %q %v", r.Answer, fx.inc.kinds())
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "a@example.com"); !errors.Is(err, ErrMayNotRetry) {
		t.Errorf("the person who asked retries: %v", err)
	}
	fx.findings(feature(), featurePlan("a.txt"))
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 || r.Stall != nil || len(fx.forge.issues) != 1 {
		t.Fatalf("state %s round %d stall %+v issues %d", r.State, r.PlanRound, r.Stall, len(fx.forge.issues))
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); !errors.Is(err, ErrNothingToRetry) {
		t.Errorf("a second retry: %v", err)
	}
}

func TestTheAnswerSaysAPersonWasToldOnlyWhenOneWas(t *testing.T) {
	for _, c := range []struct {
		name, program, want string
	}{
		{"notices held", strings.Replace(roundsProgram, "program: acme", "program: acme\nnotices: hold", 1), fixedFailed + fixedUntold},
		{"notices sent", roundsProgram, fixedFailed + fixedTold},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newFixtureOf(t, c.program)
			// The forge takes no ticket, so no approver can be told there.
			fx.forge.down = true
			fx.rt.investigate = func(string, string) error { return errors.New("the engine went away") }
			r := fx.submit("user", "please add a second line to the orders file")
			fx.d.Run(context.Background(), r.ID)
			r = fx.reread(r)
			if r.State != store.StateFailed || r.Answer != c.want {
				t.Fatalf("state %s answer %q", r.State, r.Answer)
			}
			if s := Retryable(r); s == nil || s.Stage != store.StateInvestigating {
				t.Errorf("a failed stage stays retryable from the page: %+v", s)
			}
		})
	}
}

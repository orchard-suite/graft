package desk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

// roundsProgram is the test program with the forge's usernames read as
// sign-in addresses, so a comment's author is known.
var roundsProgram = strings.Replace(testProgram, "forge: { kind: gitlab, url: https://gitlab.example.com }", `forge: { kind: gitlab, url: https://gitlab.example.com, person: "{username}@example.com" }`, 1)

// requestOf is the request whose handle the agent in dir holds.
func (fx *fixture) requestOf(dir string) (*store.Request, error) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), HandleFile))
	if err != nil {
		return nil, fmt.Errorf("the agent has no handle: %w", err)
	}
	var h Handle
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return fx.d.ByHandle(context.Background(), h.Secret)
}

func feature() store.Verdict {
	v := defect()
	v.Verdict = store.VerdictFeature
	return v
}

// waitingFeature is a feature in the user lane, asked of its approvers as
// round 1.
func waitingFeature(t *testing.T) (*fixture, *store.Request) {
	t.Helper()
	return waitingFeatureOf(t, roundsProgram)
}

// waitingFeatureOf is waitingFeature under another program file.
func waitingFeatureOf(t *testing.T, programFile string) (*fixture, *store.Request) {
	t.Helper()
	fx := newFixtureOf(t, programFile)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	fx.findings(feature(), featurePlan("a.txt"))
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 || r.Run != "run1" {
		t.Fatalf("state %s round %d run %s", r.State, r.PlanRound, r.Run)
	}
	return fx, r
}

func TestThePlanIsABriefForItsApproversAndABuildPlanForItsBuilder(t *testing.T) {
	fx, r := waitingFeature(t)
	brief := fx.hub.asks[0].Inputs["brief"].(string)
	build := fx.hub.asks[0].Inputs["plan"].(string)
	if !strings.HasPrefix(brief, "## What was asked\n\nThe orders page is blank") {
		t.Errorf("the run leads with the brief:\n%s", brief)
	}
	for _, part := range []string{"## What we will do", "## What we will not do, and why", "## Judgment calls for you", "## What we checked", "## Fit and risk", "## Recommendation\n\n**Approve.** A plain defect with a plain fix."} {
		if !strings.Contains(brief, part) {
			t.Errorf("the brief lacks %q:\n%s", part, brief)
		}
	}
	if strings.Contains(brief, "a.txt") {
		t.Errorf("the brief names a file:\n%s", brief)
	}
	if !strings.Contains(build, "**Steps.**\n\n1. Add the second line.") || !strings.Contains(build, "- `a.txt`: add the line") || !strings.Contains(build, "**Tests that fail first.**") {
		t.Errorf("the build plan:\n%s", build)
	}
	body := fx.forge.bodies[r.Ticket.Number]
	asked, folded := strings.Index(body, "## What was asked"), strings.Index(body, "<details><summary>The build plan</summary>")
	if !strings.HasPrefix(body, "**Round 1 of the plan.**") || asked < 0 || folded < asked || !strings.Contains(body, "[Decide it in the hub](https://hub.example.com/actions/runs/run1)") {
		t.Errorf("the ticket leads with the brief and folds the build plan:\n%s", body)
	}
}

func TestAClaimInTheRequestIsCheckedAgainstTheRepositorysHistory(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram)
	// The logo lived in the portal's first version, which a later commit removed.
	write(t, fx.seed, "portal/v1/wwwroot/logo-navbar.png", "\x89PNG the company logo")
	run(t, fx.seed, "git", "add", "-A")
	run(t, fx.seed, "git", "commit", "-q", "-m", "feat: the portal v1")
	run(t, fx.seed, "git", "rm", "-q", "-r", "portal/v1")
	run(t, fx.seed, "git", "commit", "-q", "-m", "chore: remove the portal v1")
	run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main")
	removed := run(t, fx.seed, "git", "rev-parse", "--short", "HEAD")
	fx.rt.investigate = func(dir, prompt string) error {
		// What the brief tells it: look in the history before passing a dependency on to a person.
		out := run(t, dir, "git", "log", "--all", "--diff-filter=D", "--name-only", "--format=%h", "--", "*logo*")
		if !strings.Contains(out, "portal/v1/wwwroot/logo-navbar.png") {
			return fmt.Errorf("the investigator's clone does not hold the history: %q", out)
		}
		commit := strings.Fields(out)[0]
		r, err := fx.requestOf(dir)
		if err != nil {
			return err
		}
		ctx := context.Background()
		if _, err := fx.d.AddEvidence(ctx, r, "a.txt:1", "the form's header"); err != nil {
			return err
		}
		n, err := fx.d.AddEvidence(ctx, r, commit+"^:portal/v1/wwwroot/logo-navbar.png", "the company logo was in the repository until "+commit+" removed the portal v1")
		if err != nil {
			return err
		}
		plan := featurePlan("portal/v2/logo.png")
		plan.Brief.Checked = []store.Check{{Claim: "\"Our designer can provide it\": the logo must come from a person", Found: store.CheckFalse, Evidence: []int{n},
			Detail: "The company logo is in the repository's history, in the portal's first version, removed with it in " + commit + ". The plan restores it from there."}}
		v, _ := json.Marshal(feature())
		p, _ := json.Marshal(plan)
		write(t, dir, outDir+"/verdict.json", string(v))
		write(t, dir, outDir+"/plan.json", string(p))
		return nil
	}
	r := fx.work(fx.submit("user", "On the order form, use the proper company logo. Our designer can provide it."))
	if r.State != store.StateAwaitingPlan {
		t.Fatalf("state %s", r.State)
	}
	if prompt := fx.rt.prompts[0]; !strings.Contains(prompt, "look before you pass a dependency on to a person") || !strings.Contains(prompt, "git log --all --diff-filter=D --name-only") {
		t.Errorf("the brief does not send the investigator to the history:\n%s", prompt)
	}
	check := fmt.Sprintf("- \"Our designer can provide it\": the logo must come from a person **Not so.** The company logo is in the repository's history, in the portal's first version, removed with it in %s. The plan restores it from there. (evidence 2: %s^:portal/v1/wwwroot/logo-navbar.png)", removed, removed)
	for where, text := range map[string]string{"the run": fx.hub.asks[0].Inputs["brief"].(string), "the ticket": fx.forge.bodies[r.Ticket.Number]} {
		if !strings.Contains(text, "## What we checked\n\n"+check) {
			t.Errorf("%s does not record the check:\n%s", where, text)
		}
	}
}

func TestABriefIsHeldToItsShape(t *testing.T) {
	repo := &program.Repository{Path: "acme/api", Protected: []string{"infra/"}}
	cases := []struct {
		name     string
		change   func(p *store.Plan)
		revising bool
		want     string
	}{
		{"whole", func(*store.Plan) {}, false, ""},
		{"no brief", func(p *store.Plan) { p.Brief = store.Brief{} }, false, "brief."},
		{"no recommendation", func(p *store.Plan) { p.Brief.Recommendation.Decision = "maybe" }, false, "is not approve"},
		{"a file where people read", func(p *store.Plan) { p.Brief.WillDo = "Change a.txt to hold two lines." }, false, "the brief names a.txt"},
		{"a check with no evidence", func(p *store.Plan) {
			p.Brief.Checked = []store.Check{{Claim: "the logo is missing", Found: store.CheckFalse, Detail: "it is in history"}}
		}, false, "cites no evidence"},
		{"a check of evidence that is not there", func(p *store.Plan) {
			p.Brief.Checked = []store.Check{{Claim: "the logo is missing", Found: store.CheckFalse, Detail: "it is in history", Evidence: []int{9}}}
		}, false, "cites evidence 9"},
		{"a claim it could not check", func(p *store.Plan) {
			p.Brief.Checked = []store.Check{{Claim: "it worked last year", Found: store.CheckUnknown, Detail: "no log reaches back that far"}}
		}, false, ""},
		{"a declined part with no reason", func(p *store.Plan) { p.Brief.WillNot = []store.Declined{{What: "a red banner"}} }, false, "needs what is left out and why"},
		{"a judgment call with no recommendation", func(p *store.Plan) { p.Brief.Calls = []store.Call{{Question: "Which forms?"}} }, false, "your recommendation"},
		{"too long to read in two minutes", func(p *store.Plan) { p.Brief.Fit = strings.Repeat("It fits. ", 800) }, false, "read in two minutes"},
		{"a later round that says nothing changed", func(*store.Plan) {}, true, "brief.changed says what changed"},
		{"a later round", func(p *store.Plan) { p.Brief.Changed = []string{"Keeps the old line, as asked."} }, true, ""},
		{"a first round that says what changed", func(p *store.Plan) { p.Brief.Changed = []string{"Something."} }, false, "only for a round that answers"},
		{"no steps", func(p *store.Plan) { p.Build.Steps = nil }, false, "build.steps"},
		{"no acceptance", func(p *store.Plan) { p.Build.Acceptance = []string{" "} }, false, "build.acceptance"},
		{"no kind", func(p *store.Plan) { p.Build.Kind = "chore" }, false, "is not fix or feature"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fixPlan("a.txt", "infra/main.tf")
			c.change(p)
			err := checkPlan(p, repo, 2, c.revising)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want %q", err, c.want)
			case c.want == "" && strings.Join(p.Build.Protected, ",") != "infra/main.tf":
				t.Errorf("protected %v", p.Build.Protected)
			}
		})
	}
}

func TestAFeatureOrAChangeInBehaviourAlwaysWaitsForADecision(t *testing.T) {
	cases := []struct {
		name, lane, screen string
		verdict            store.Verdict
		plan               *store.Plan
	}{
		{"a feature in a lane that ships small fixes", "developer", "feature", feature(), featurePlan("a.txt", "tests/two.sh")},
		{"a defect whose fix changes behaviour", "developer", "bug", defect(), featurePlan("a.txt", "tests/two.sh")},
		{"a defect the screen took for something new", "developer", "feature", defect(), fixPlan("a.txt", "tests/two.sh")},
		{"a small clear defect in a lane that does not ship small fixes", "user", "bug", defect(), fixPlan("a.txt", "tests/two.sh")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newFixture(t)
			fx.rt.screen = fmt.Sprintf(`{"sort": %q, "title": "Orders"}`, c.screen)
			fx.findings(c.verdict, c.plan)
			built := 0
			fx.rt.build = func(string, string) error { built++; return nil }
			r := fx.work(fx.submit(c.lane, "the orders page needs a second line"))
			if r.State != store.StateAwaitingPlan || built != 0 || len(fx.hub.asks) != 1 || len(fx.hub.asks[0].Approvers) == 0 || fx.hub.runs[r.Run].State != orrery.RunAwaiting {
				t.Fatalf("state %s built %d asks %+v", r.State, built, fx.hub.asks)
			}
		})
	}
	// Where the program file allows it, a small clear defect ships unasked (TestASmallFixShipsWithNoPlanApproval), and the
	// platform's own lane ships every plan unasked (TestThePlatformLaneShipsWithNoGate).
}

func TestAnApproversCommentOnTheTicketPlansTheNextRound(t *testing.T) {
	fx, r := waitingFeature(t)
	ctx := context.Background()
	ticket := r.Ticket.Number
	// Someone who is not an approver, a bot, and Graft itself: nothing changes.
	fx.forge.say(ticket, "stranger", "Please make it purple too.")
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := fx.st.Get(ctx, r.ID); got.State != store.StateAwaitingPlan || len(got.Thread) != 0 || got.Seen == 0 {
		t.Fatalf("a stranger's comment moved the plan: state %s thread %+v seen %d", got.State, got.Thread, got.Seen)
	}
	// An approver's comment is the next round.
	fx.forge.say(ticket, "lead", "Keep the old line, and add the new one under it.")
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || len(fx.hub.asks) != 2 || fx.hub.asks[1].Revises != "run1" {
		t.Fatalf("state %s round %d asks %+v", r.State, r.PlanRound, fx.hub.asks)
	}
	if prompt := fx.rt.prompts[1]; !strings.Contains(prompt, "lead@example.com, on the ticket, of round 1:\nKeep the old line, and add the new one under it.") ||
		!strings.Contains(prompt, "The build plan of round 1:") || strings.Contains(prompt, "make it purple") {
		t.Errorf("the second investigation:\n%s", prompt)
	}
	brief := fx.hub.asks[1].Inputs["brief"].(string)
	if !strings.HasPrefix(brief, "## What changed since round 1\n\n> Keep the old line, and add the new one under it.\nSaid by lead@example.com, [on the ticket](https://gitlab.example.com/acme/api/-/issues/1#note_2001).\n\n- Answers the approvers' note.") {
		t.Errorf("round 2 leads with what changed, quoting the comment:\n%s", brief)
	}
	if fx.hub.asks[1].Inputs["round"] != 2 {
		t.Errorf("round input %v", fx.hub.asks[1].Inputs["round"])
	}
	if body := fx.forge.bodies[ticket]; !strings.HasPrefix(body, "**Round 2 of the plan.**") || !strings.Contains(body, "## What changed since round 1") {
		t.Errorf("the ticket is round 2 now:\n%s", body)
	}
	said := fx.forge.comments[len(fx.forge.comments)-1]
	if !strings.HasPrefix(said, "### Round 2 of the plan: a revision") || !strings.Contains(said, "**Round 2 of the plan is ready for a decision.** [Decide it in the hub](https://hub.example.com/actions/runs/run2).") ||
		!strings.Contains(said, "> Keep the old line") || !strings.Contains(said, "<details><summary>Round 1, as it was</summary>") {
		t.Errorf("the reply on the ticket:\n%s", said)
	}
	if len(r.Thread) != 1 || r.Thread[0].Answered != 2 || r.Thread[0].By != "lead@example.com" || len(r.Earlier) != 1 || r.Earlier[0].N != 1 {
		t.Errorf("thread %+v earlier %+v", r.Thread, r.Earlier)
	}
	// Graft's own reply on the ticket is no comment of an approver's.
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := fx.st.Get(ctx, r.ID); got.State != store.StateAwaitingPlan || got.PlanRound != 2 {
		t.Errorf("Graft's own comment moved the plan: %s round %d", got.State, got.PlanRound)
	}
}

func TestASendBackInTheHubPlansTheNextRoundTheSameWay(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "Keep the old line too.")
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || fx.hub.asks[1].Revises != "run1" {
		t.Fatalf("state %s round %d asks %+v", r.State, r.PlanRound, fx.hub.asks)
	}
	brief := fx.hub.asks[1].Inputs["brief"].(string)
	if !strings.HasPrefix(brief, "## What changed since round 1\n\n> Keep the old line too.\nSaid by lead@example.com, [sending the plan back in the hub](https://hub.example.com/actions/runs/run1).") {
		t.Errorf("round 2 leads with what changed, quoting the note:\n%s", brief)
	}
}

func TestOnlyOneApprovalWaitsHoweverManyRoundsAPlanTakes(t *testing.T) {
	fx, r := waitingFeatureOf(t, strings.Replace(roundsProgram, "plan_rounds: 2", "plan_rounds: 3", 1))
	ctx := context.Background()
	fx.forge.say(r.Ticket.Number, "lead", "Keep the old line.")
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "And sort them.")
	r = fx.poll(r)
	if r.PlanRound != 3 || len(fx.hub.asks) != 3 {
		t.Fatalf("round %d asks %d", r.PlanRound, len(fx.hub.asks))
	}
	// Each round revises the round before, so the hub keeps one approval of the ask.
	for i, want := range []string{"", "run1", "run2"} {
		if got := fx.hub.asks[i].Revises; got != want {
			t.Errorf("ask %d revises %q, want %q", i+1, got, want)
		}
	}
	if n := fx.hub.waiting(); n != 1 {
		t.Errorf("%d runs wait for a decision, want 1", n)
	}
	if len(r.Thread) != 2 || r.Thread[0].Answered != 2 || r.Thread[1].Answered != 3 || r.Thread[1].From != store.FromHub {
		t.Errorf("thread %+v", r.Thread)
	}
}

func TestAQuestionOnTheTicketIsAnsweredThereAndTheRoundStands(t *testing.T) {
	fx, r := waitingFeature(t)
	ctx := context.Background()
	fx.rt.investigate = func(dir, prompt string) error {
		if !strings.Contains(prompt, "reply.json") {
			return fmt.Errorf("the brief offers no reply: %s", prompt)
		}
		write(t, dir, outDir+"/reply.json", `{"reply": "The old line stays: the plan only adds one."}`)
		return nil
	}
	fx.forge.say(r.Ticket.Number, "lead", "Does the old line stay?")
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 1 || len(fx.hub.asks) != 1 || r.Run != "run1" {
		t.Fatalf("a question moved the round: state %s round %d asks %d", r.State, r.PlanRound, len(fx.hub.asks))
	}
	said := fx.forge.comments[len(fx.forge.comments)-1]
	if !strings.Contains(said, "> Does the old line stay?") || !strings.Contains(said, "The old line stays: the plan only adds one.") || !strings.Contains(said, "Round 1 of the plan stands as it was") {
		t.Errorf("the reply:\n%s", said)
	}
	if len(r.Thread) != 1 || !r.Thread[0].Replied || r.Thread[0].Answered != 1 {
		t.Errorf("thread %+v", r.Thread)
	}
}

func TestAnApprovalOfARoundBeingRevisedDoesNotCount(t *testing.T) {
	fx, r := waitingFeature(t)
	ctx := context.Background()
	built := 0
	fx.rt.build = func(string, string) error { built++; return nil }
	fx.forge.say(r.Ticket.Number, "lead", "Keep the old line.")
	if err := fx.d.Listen(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	// Someone approves round 1 while round 2 is written.
	fx.hub.decide("run1", orrery.RunQueued, "lead@example.com", "")
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || built != 0 || fx.hub.asks[1].Revises != "" || fx.hub.runs["run1"].State != orrery.RunFailed {
		t.Fatalf("state %s round %d built %d asks %+v run1 %s", r.State, r.PlanRound, built, fx.hub.asks, fx.hub.runs["run1"].State)
	}
	if last := fx.hub.reports[len(fx.hub.reports)-1]; !strings.Contains(last.Summary, "does not count") {
		t.Errorf("the old run ends saying why: %+v", last)
	}
}

func TestTheBuilderGetsTheRequestItsFilesTheApprovedPlanAndTheThread(t *testing.T) {
	readers(t)
	fx := newFixtureOf(t, roundsProgram)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	fx.findings(feature(), featurePlan("a.txt"))
	var prompt string
	var inClone bool
	fx.rt.build = func(dir, p string) error {
		prompt = p
		_, err := os.Stat(filepath.Join(dir, inBeside))
		inClone = err == nil
		if _, err := os.Stat(filepath.Join(filepath.Dir(dir), inBeside, "1-portal.png")); err != nil {
			return fmt.Errorf("the person's file is not beside the clone: %w", err)
		}
		return fx.commitTwo(dir)
	}
	r, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: "acme-web", Repository: "acme/api", Environment: program.Production, Lane: "user",
		Text: "please add a second line to the orders file", Attachments: []attach.Upload{{Name: "portal.png", Data: shot}}})
	if err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "Keep the old line too.")
	r = fx.poll(r)
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateReleased {
		t.Fatalf("state %s", r.State)
	}
	for _, part := range []string{"<<<\nplease add a second line to the orders file\n>>>", "`../in/1-portal.png`", "lead@example.com, sending the plan back, of round 1:\nKeep the old line too.",
		"# The plan, round 2, as approved", "## What was asked", "# The build plan", "**Steps.**"} {
		if !strings.Contains(prompt, part) {
			t.Errorf("the builder's brief lacks %q:\n%s", part, prompt)
		}
	}
	if inClone {
		t.Error("the person's files are inside the builder's clone")
	}
}

func TestADeclineTellsThePersonWhy(t *testing.T) {
	fx := newFixtureOf(t, strings.Replace(roundsProgram, "program: acme", "program: acme\nnotices: hold", 1))
	fx.hub.members = map[string]bool{"lead@example.com": true}
	fx.findings(feature(), featurePlan("a.txt"))
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	fx.hub.decide(r.Run, orrery.RunDeclined, "lead@example.com", "One person's need; the export already gives this.")
	r = fx.poll(r)
	if r.State != store.StateDeclined || r.DecisionNote != "One person's need; the export already gives this." || !strings.HasSuffix(strings.Split(r.Answer, "\n\nTicket:")[0], "Why: One person's need; the export already gives this.") {
		t.Fatalf("state %s note %q answer %q", r.State, r.DecisionNote, r.Answer)
	}
	events, _ := fx.st.Events(context.Background(), r.ID)
	held := false
	for _, e := range events {
		held = held || (strings.HasPrefix(e.Message, "notice held: "+KindAnswered+" to person a@example.com") && strings.Contains(e.Message, "Why: One person's need"))
	}
	if !held || len(fx.inc.sent) != 0 {
		t.Errorf("the decline is held for the person, with why: held %v sent %d", held, len(fx.inc.sent))
	}
}

// commitTwo is a build that adds the second line and commits it. Graft
// names the ticket.
func (fx *fixture) commitTwo(dir string) error {
	write(fx.t, dir, "a.txt", "one\ntwo\n")
	run(fx.t, dir, "git", "add", "-A")
	run(fx.t, dir, "git", "commit", "-q", "-m", "feat: add the second line")
	return nil
}

func TestTheCatalogRecordOfARequestCarriesItsTimesAndItsRound(t *testing.T) {
	fx, r := waitingFeature(t)
	rec := fx.hub.records[r.ID]
	if !rec.RequestedAt.Equal(r.CreatedAt) || !rec.UpdatedAt.Equal(r.UpdatedAt) || rec.Round != 1 || !rec.EndedAt.IsZero() {
		t.Fatalf("a waiting request's record: %+v (request made %v, changed %v)", rec, r.CreatedAt, r.UpdatedAt)
	}
	fx.hub.decide(r.Run, orrery.RunDeclined, "lead@example.com", "Not now.")
	r = fx.poll(r)
	rec = fx.hub.records[r.ID]
	if r.State != store.StateDeclined || rec.EndedAt.IsZero() || !rec.EndedAt.Equal(r.UpdatedAt) || !rec.RequestedAt.Equal(r.CreatedAt) {
		t.Fatalf("an ended request's record: %+v", rec)
	}
}

func TestEveryRequestHeldIsWrittenToTheCatalogAtStart(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	// Requests a Graft before this one held, open and ended, whose records carry no time.
	open := fx.submit("user", "the orders page is blank when I open it")
	ended := fx.submit("user", "please add an export button to orders")
	ended.State = store.StateAnswered
	if err := fx.st.Save(ctx, ended); err != nil {
		t.Fatal(err)
	}
	fx.hub.records = map[string]hub.Request{}
	for _, r := range []*store.Request{open, ended} {
		fx.hub.records[r.ID] = hub.Request{ID: r.ID, State: r.State, Lane: r.Lane}
	}
	for range 2 {
		if err := fx.d.ShowAll(ctx); err != nil {
			t.Fatal(err)
		}
		for _, r := range []*store.Request{open, ended} {
			got, _ := fx.st.Get(ctx, r.ID)
			rec := fx.hub.records[r.ID]
			if !rec.RequestedAt.Equal(got.CreatedAt) || !rec.UpdatedAt.Equal(got.UpdatedAt) || rec.State != got.State {
				t.Errorf("%s: record %+v", got.State, rec)
			}
			if store.Ended(got.State) != !rec.EndedAt.IsZero() {
				t.Errorf("%s: ended at %v", got.State, rec.EndedAt)
			}
		}
	}
}

package desk

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	kit "github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/dispatch"
	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

// plan decides what a verdict leads to. It is plain code: no model reads
// the verdict here, and nothing an agent wrote picks the path. A plan may
// change anything in the repository the fix needs, wherever the person was
// when they asked; only the protected paths are held to a lane.
func (d *Desk) plan(ctx context.Context, r *store.Request, log io.Writer) error {
	repo, err := d.repo(r)
	if err != nil {
		return err
	}
	lane, err := d.lane(r)
	if err != nil {
		return err
	}
	v := r.Verdict
	if v == nil {
		return fmt.Errorf("planning with no verdict")
	}
	labels := map[string]string{"verdict": v.Verdict}
	switch v.Verdict {
	case store.VerdictUserError, store.VerdictDesigned, store.VerdictNotEnough:
		d.end(ctx, r, store.StateAnswered, d.say(r), annals.Step{Verb: "request.answered", Labels: labels})
		return nil
	case store.VerdictDuplicate:
		other, err := d.store.Get(ctx, v.DuplicateOf)
		if err != nil {
			return err
		}
		r.Links = append(r.Links, store.Link{Kind: "request", ID: other.ID, URL: d.requestURL(other)})
		d.end(ctx, r, store.StateAnswered, d.say(r), annals.Step{Verb: "request.answered", Labels: labels})
		return nil
	case store.VerdictOutage:
		_, key, _ := strings.Cut(v.System, ":")
		return d.raise(ctx, r, log, key, "Outage reported through "+r.App+": "+d.title(r), dispatch.Warning)
	}
	// A defect or a feature.
	if !v.Fixable {
		if v.Blocked {
			return d.raise(ctx, r, log, repo.Service, "People are blocked: "+d.title(r), dispatch.Critical)
		}
		if err := d.ticket(ctx, r, log); err != nil {
			return err
		}
		d.end(ctx, r, store.StateFiled, d.say(r), annals.Step{Verb: "request.filed", Labels: labels})
		return nil
	}
	if r.Plan == nil {
		return fmt.Errorf("a fixable %s came with no plan", v.Verdict)
	}
	// The runner writes the ticket, never the agent: a ticket exists before
	// any change.
	if err := d.ticket(ctx, r, log); err != nil {
		return err
	}
	if len(r.Plan.Build.Protected) > 0 && !lane.Protected {
		fmt.Fprintf(log, "the plan names protected paths %v and the %s lane may not pass them\n", r.Plan.Build.Protected, lane.ID)
		d.end(ctx, r, store.StateFiled, d.say(r)+"\n\nThe change touches protected paths, which this lane may not change. It is filed for a person who can.", annals.Step{Verb: "request.filed",
			Outcome: annals.Refused, Reason: "the plan names a protected path and the lane may not pass it", Labels: labels})
		return nil
	}
	// A small fix can only be judged from what was built. In a lane that
	// ships small fixes with no approval, a clear defect is built first,
	// where nothing can land, and asked about only if it turns out not small.
	// Anything else that changes what the system does waits for a decision.
	if clear, why := clearDefect(r); lane.SmallFix && r.Run == "" && !r.Built {
		if clear {
			d.record(ctx, r, "building first: a small fix in this lane ships with no plan approval", annals.Step{Verb: "request.planned", Labels: map[string]string{"path": "build-first"}})
			r.State = store.StateBuilding
			return nil
		}
		fmt.Fprintf(log, "not built first: %s; asking about the plan\n", why)
		d.log.Info("plan asked before anything is built: it is not a clear defect", "request", r.ID, "lane", r.Lane, "why", why)
	}
	return d.askPlan(ctx, r, log, "")
}

// clearDefect says whether a request is a defect clear enough to be built
// before anyone is asked, where its lane ships a small fix unasked: the
// screen did not take it for a request for something new, the investigator
// found a defect, and the plan only makes the system do what it was meant
// to. Otherwise why not. A clear defect still ships unasked only when the
// fences judge what was built a small fix.
func clearDefect(r *store.Request) (bool, string) {
	switch {
	case r.Verdict == nil || r.Verdict.Verdict != store.VerdictDefect:
		return false, "the investigator did not find a defect"
	case r.Plan == nil || r.Plan.Build.Kind != store.KindFix:
		return false, "its plan changes what the system does"
	case r.Sort == store.SortFeature:
		return false, "the person asked for something new"
	}
	return true, ""
}

// raise opens an incident for a request no code change of Graft's can
// answer, unless one is open on that service already, then holds the
// request on it. A request that was already held on an incident once goes
// to a person instead: Graft never opens incidents in a loop.
func (d *Desk) raise(ctx context.Context, r *store.Request, log io.Writer, service, title, severity string) error {
	for _, l := range r.Links {
		if l.Kind == "incident" {
			d.end(ctx, r, store.StateWaiting, fixedKnown, annals.Step{Verb: "request.waiting", Outcome: annals.Refused, Reason: "still not answerable after its incident closed"})
			return nil
		}
	}
	open, err := d.inc.OpenIncidents(ctx, service)
	if err != nil {
		return err
	}
	var inc *dispatch.Incident
	if len(open) > 0 {
		inc = &open[0]
	} else {
		desc := fmt.Sprintf("Graft request %s in %s. %s", r.ID, r.Repo, d.requestURL(r))
		if inc, err = d.inc.OpenIncident(ctx, dispatch.NewIncident{Title: clip(title, 200), Severity: severity, Service: service, Description: desc}); err != nil {
			return fmt.Errorf("open an incident on %s: %w", service, err)
		}
	}
	if inc == nil {
		// No incident desk to open one on: a person must pick this up.
		if err := d.ticket(ctx, r, log); err != nil {
			return err
		}
		d.end(ctx, r, store.StateWaiting, fixedKnown, annals.Step{Verb: "request.waiting", Outcome: annals.Refused, Reason: "it needs an incident and no incident desk is set"})
		return nil
	}
	fmt.Fprintf(log, "incident %d on %s\n", inc.ID, service)
	d.link(r, inc)
	d.known(ctx, r)
	r.State = store.StateHolding
	return nil
}

// ticket makes sure the request has a ticket on the forge. The runner
// writes it from the verdict and the plan's checked fields.
func (d *Desk) ticket(ctx context.Context, r *store.Request, log io.Writer) error {
	if r.Ticket != nil {
		return nil
	}
	issue, err := d.forge.CreateIssue(ctx, r.Repo, clip(d.title(r), 200), d.ticketBody(ctx, r, r.PlanRound+1))
	if err != nil {
		return fmt.Errorf("ticket: %w", err)
	}
	r.Ticket = &store.Ticket{Number: issue.Number, URL: issue.URL}
	fmt.Fprintf(log, "ticket %s\n", issue.URL)
	d.note(ctx, r, r.State, "ticket "+issue.URL)
	d.ticketed(ctx, r)
	return d.store.Save(ctx, r)
}

// ticketBody is the request's ticket. For a plan it leads with round's
// brief, with the files the person sent, and folds the build plan and what
// the investigation found beneath it. For anything else it is what the
// investigation found.
func (d *Desk) ticketBody(ctx context.Context, r *store.Request, round int) string {
	var b strings.Builder
	if p := r.Plan; p != nil {
		fmt.Fprintf(&b, "**Round %d of the plan.** Graft request `%s`, sent through `%s` in the `%s` lane.", round, r.ID, r.App, r.Lane)
		if u := d.requestURL(r); u != "" {
			fmt.Fprintf(&b, " [The request in Graft](%s).", u)
		}
		if r.RunURL != "" && r.PlanRound == round {
			fmt.Fprintf(&b, " [Decide it in the hub](%s).", r.RunURL)
		}
		b.WriteString(" An approver may answer the plan here: a comment of theirs is planned into the next round.\n\n")
		b.WriteString(d.planBrief(ctx, r, round))
		if links := d.fileLinks(r); links != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s", filesTitle(r), links)
		}
		fmt.Fprintf(&b, "\n<details><summary>The build plan</summary>\n\n%s\n</details>\n", buildText(&p.Build))
		if found := d.findings(ctx, r); found != "" {
			fmt.Fprintf(&b, "\n<details><summary>What the investigation found</summary>\n\n%s</details>\n", found)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "Graft request `%s`, sent through `%s` in the `%s` lane.\n\n", r.ID, r.App, r.Lane)
	if u := d.requestURL(r); u != "" {
		fmt.Fprintf(&b, "%s\n\n", u)
	}
	b.WriteString(d.findings(ctx, r))
	if links := d.fileLinks(r); links != "" {
		fmt.Fprintf(&b, "## %s\n\n%s\n", filesTitle(r), links)
	}
	return b.String()
}

// findings is the verdict as Markdown: what is true, its facts with the
// evidence each cites, and the guesses apart.
func (d *Desk) findings(ctx context.Context, r *store.Request) string {
	v := r.Verdict
	if v == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Verdict: %s\n\n%s\n\n", v.Verdict, strings.TrimSpace(v.Summary))
	titles := d.evidenceTitles(ctx, r)
	if len(v.Facts) > 0 {
		b.WriteString("Facts:\n\n")
		for _, f := range v.Facts {
			var cites []string
			for _, n := range f.Evidence {
				cites = append(cites, fmt.Sprintf("%d: %s", n, titles[n]))
			}
			fmt.Fprintf(&b, "- %s (evidence %s)\n", strings.TrimSpace(f.Text), strings.Join(cites, "; "))
		}
		b.WriteString("\n")
	}
	if len(v.Guesses) > 0 {
		b.WriteString("Guesses, not shown:\n\n")
		for _, g := range v.Guesses {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(g))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// askPlan asks the hub about the plan as its next round. why, when it is
// not empty, says why Graft asks again about a plan it built.
func (d *Desk) askPlan(ctx context.Context, r *store.Request, log io.Writer, why string) error {
	return d.ask(ctx, r, log, r.PlanRound+1, why)
}

// ask asks the hub about the plan as round round, as a run of the lane's
// action that Graft asks for the person. Whether it waits is the lane's:
// a lane whose plans wait names the approvers, who alone decide it. The run
// leads with the brief, and folds the build plan. A round after the first
// revises the run of the round before, which the hub keeps as one approval;
// a run of an earlier round that is still live is ended first. The ticket
// becomes this round, and a round after the first says so on it.
//
// A plan that asks for tool tiers names the approvers in every lane:
// nobody but an approver who holds a tier grants it.
func (d *Desk) ask(ctx context.Context, r *store.Request, log io.Writer, round int, why string) error {
	repo, err := d.repo(r)
	if err != nil {
		return err
	}
	revises, stop, err := d.prior(ctx, r, log)
	if err != nil || stop {
		return err
	}
	p := r.Plan
	// The notes this round answers are marked so its brief quotes them, and
	// left open again if the hub does not take the ask.
	open := openNotes(r)
	answer(r, round, false)
	size := fmt.Sprintf("about %d lines in %d files", p.Build.Size.Lines, p.Build.Size.Files)
	if r.Built && r.Size != nil {
		size = fmt.Sprintf("built: %d lines in %d files", r.Size.Lines, r.Size.Files)
	}
	inputs := map[string]any{"title": d.title(r), "round": round, "kind": p.Build.Kind, "size": size,
		"brief": clip(d.planBrief(ctx, r, round), maxOut), "plan": clip(buildText(&p.Build), maxOut)}
	if u := d.requestURL(r); u != "" {
		inputs["link"] = u
	}
	if links := d.fileLinks(r); links != "" {
		inputs["files"] = links
	}
	approvers, err := d.approvers(r)
	if err != nil {
		return err
	}
	run, err := d.hub.AskPlan(ctx, hub.PlanAsk{Lane: r.Lane, Entity: repo.Entity, Person: r.Person, Request: r.ID, Inputs: inputs, Tiers: p.Build.Tiers, Revises: revises, Approvers: approvers})
	if err != nil {
		unanswer(r, open)
		return fmt.Errorf("ask about the plan: %w", err)
	}
	next := round > r.PlanRound
	r.PlanRound = round
	r.Run, r.RunURL = run.ID, d.hub.RunURL(run.ID)
	r.Decision, r.DecidedBy, r.DecidedAt, r.DecisionNote = "", "", "", ""
	fmt.Fprintf(log, "plan round %d asked as run %s (%s), revising %q, held to %d approver grant(s)\n", r.PlanRound, run.ID, run.State, revises, len(run.Approvers))
	if len(approvers) == 0 {
		d.log.Info("plan asked with no approvers: its lane ships unasked", "request", r.ID, "lane", r.Lane, "run", run.ID, "state", run.State, "round", round)
	} else {
		d.log.Info("plan asked of its approvers", "request", r.ID, "lane", r.Lane, "run", run.ID, "state", run.State, "round", round, "revises", revises)
	}
	d.record(ctx, r, fmt.Sprintf("plan round %d asked in the hub", r.PlanRound), annals.Step{Verb: "request.plan_asked",
		Labels: map[string]string{"round": fmt.Sprint(r.PlanRound), "kind": p.Build.Kind, "built": fmt.Sprint(r.Built), "revises": fmt.Sprint(revises != "")}})
	d.retitle(ctx, r, log)
	if lv := d.running(r.ID); lv != nil {
		// The round's own run says it is ready, where it is decided, what it
		// answers, and the round before.
		if run.State == orrery.RunAwaiting {
			closing := d.roundComment(ctx, r, why)
			lv.change(func(run *store.Run) { run.Closing = closing })
		}
	} else if next && round > 1 {
		d.comment(ctx, r, log, d.roundComment(ctx, r, why))
	}
	r.State = store.StateAwaitingPlan
	return d.decided(ctx, r, log, run)
}

// retitle writes the ticket again as the round now asked. The ticket is a
// view of the request: one the forge does not take is logged and nothing
// more.
func (d *Desk) retitle(ctx context.Context, r *store.Request, log io.Writer) {
	if r.Ticket == nil {
		return
	}
	if err := d.forge.UpdateIssue(ctx, r.Repo, r.Ticket.Number, d.ticketBody(ctx, r, r.PlanRound)); err != nil {
		fmt.Fprintf(log, "the ticket was not written again: %v\n", err)
		d.log.Warn("ticket not written again", "request", r.ID, "error", err)
	}
}

// pollPlan reads a plan that waits again: its ticket's comments, when the
// sweep is due, and its run.
func (d *Desk) pollPlan(ctx context.Context, r *store.Request) error {
	m := d.locks.of(r.ID)
	m.Lock()
	defer m.Unlock()
	r, err := d.store.Get(ctx, r.ID)
	if err != nil || r.State != store.StateAwaitingPlan {
		return err
	}
	d.mu.Lock()
	due := d.now().Sub(d.listened[r.ID]) >= listenEvery
	d.mu.Unlock()
	if due {
		// A forge that does not answer never keeps the run from being read.
		if err := d.listen(ctx, r); err != nil {
			d.log.Warn("ticket comments not read", "request", r.ID, "error", err)
		}
		if r.State != store.StateAwaitingPlan {
			return nil
		}
	}
	run, err := d.hub.Run(ctx, r.Run)
	if err != nil {
		return err
	}
	state, asked := r.State, r.Run
	if err := d.decided(ctx, r, io.Discard, run); err != nil {
		return err
	}
	if r.State == state && r.Run == asked {
		return nil
	}
	return d.save(ctx, r)
}

// approvers are who decide the request's plan: the repository's approvers
// when the lane's plans wait, or the plan asks for a tool tier; none when
// the lane ships its plans unasked.
func (d *Desk) approvers(r *store.Request) ([]program.Grant, error) {
	repo, err := d.repo(r)
	if err != nil {
		return nil, err
	}
	lane, err := d.lane(r)
	if err != nil {
		return nil, err
	}
	if !lane.Waits() && (r.Plan == nil || len(r.Plan.Build.Tiers) == 0) {
		return nil, nil
	}
	return d.prog.ApproversOf(repo), nil
}

// refusal says why Graft does not take the hub's decision of a plan, or
// empty when it does. The hub decides first; Graft checks the decider
// itself before anything is built: the person who asked never decides
// their own plan, and where Graft named approvers, the hub must have held
// the run to them and the decider must be one of them, as the hub's catalog
// says now.
func (d *Desk) refusal(ctx context.Context, r *store.Request, run *hub.PlanRun) (string, error) {
	if run.DecidedBy == "" {
		return "", nil
	}
	if strings.EqualFold(run.DecidedBy, r.Person) {
		return fmt.Sprintf("%s asked for this change, and the person who asked never decides their own plan", run.DecidedBy), nil
	}
	approvers, err := d.approvers(r)
	if err != nil {
		return "", err
	}
	if len(approvers) == 0 {
		return "", nil
	}
	if !sameGrants(run.Approvers, approvers) {
		return "the hub did not hold the plan to the approvers Graft named, so its decision does not count", nil
	}
	ok, err := d.hub.LetsIn(ctx, run.DecidedBy, approvers)
	if err != nil {
		return "", fmt.Errorf("check the plan's decider: %w", err)
	}
	if !ok {
		return fmt.Sprintf("%s is not one of the plan's approvers", run.DecidedBy), nil
	}
	return "", nil
}

// sameGrants says two lists of grants name the same people the same way.
func sameGrants(a, b []program.Grant) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Field != b[i].Field || strings.Join(a[i].Values, "\x00") != strings.Join(b[i].Values, "\x00") {
			return false
		}
	}
	return true
}

// decided moves a request by what its plan's run has come to. A run that
// still waits moves nothing. A decision Graft does not take moves
// nothing either: the run ends, and the plan is asked about again in the
// same round, for someone who may decide it.
func (d *Desk) decided(ctx context.Context, r *store.Request, log io.Writer, run *hub.PlanRun) error {
	if run.State == orrery.RunAwaiting {
		return nil
	}
	if stop, err := d.unheld(ctx, r, log, run); err != nil || stop {
		return err
	}
	why, err := d.refusal(ctx, r, run)
	if err != nil {
		return err
	}
	if why != "" {
		d.log.Warn("plan decision refused: decided by someone who may not decide it", "request", r.ID, "run", run.ID, "lane", r.Lane, "approvers", len(run.Approvers))
		fmt.Fprintf(log, "decision refused: %s\n", why)
		if !run.Finished() {
			if err := d.hub.Report(ctx, run.ID, orrery.ActionReport{State: orrery.RunFailed, Summary: "Graft refused this decision: " + why + ".", Link: d.requestURL(r)}); err != nil {
				return fmt.Errorf("end the refused run: %w", err)
			}
		}
		d.record(ctx, r, "decision refused: "+why, annals.Step{Verb: "request.plan_decided", Outcome: annals.Refused,
			Reason: "decided by someone who may not decide it", Labels: map[string]string{"round": fmt.Sprint(r.PlanRound), "decision": "refused"}})
		r.Run = ""
		return d.ask(ctx, r, log, r.PlanRound, "")
	}
	r.DecidedBy, r.DecidedAt, r.DecisionNote = run.DecidedBy, run.DecidedAt, clip(run.Note, 4000)
	switch run.State {
	case orrery.RunQueued, orrery.RunRunning:
		// Approved, or the hub's rule asked nobody. The run stays running
		// while the build is on, so what it granted stays live.
		if run.State == orrery.RunQueued {
			if err := d.hub.Report(ctx, run.ID, orrery.ActionReport{State: orrery.RunRunning, Label: "Building", Link: d.requestURL(r)}); err != nil {
				return fmt.Errorf("start the plan's run: %w", err)
			}
		}
		approval := &kit.Approval{Required: run.DecidedBy != "", By: run.DecidedBy}
		if at, err := time.Parse(time.RFC3339, run.DecidedAt); err == nil && approval.Required {
			approval.At = at.UTC().Truncate(time.Second)
		}
		msg := fmt.Sprintf("plan round %d may go ahead: the hub's rule asked nobody", r.PlanRound)
		if run.DecidedBy != "" {
			r.Decision = store.DecisionApproved
			msg = fmt.Sprintf("plan round %d approved by %s at %s", r.PlanRound, run.DecidedBy, run.DecidedAt) + noted(run.Note)
		}
		d.record(ctx, r, msg, annals.Step{Verb: "request.plan_decided", Approval: approval,
			Labels: map[string]string{"round": fmt.Sprint(r.PlanRound), "decision": "approved"}})
		r.State = store.StateBuilding
		if r.Built {
			r.State = store.StateLanding
		}
		return nil
	case orrery.RunReturned:
		d.sentBack(ctx, r, run)
		return nil
	default:
		// Declined, cancelled, or ended some other way: the run is over.
		msg := fmt.Sprintf("plan round %d ended %s", r.PlanRound, run.State)
		if run.State == orrery.RunDeclined {
			r.Decision = store.DecisionDeclined
			msg = fmt.Sprintf("plan round %d declined by %s at %s", r.PlanRound, run.DecidedBy, run.DecidedAt) + noted(run.Note)
		}
		d.note(ctx, r, r.State, msg)
		r.Run = ""
		d.end(ctx, r, store.StateDeclined, declinedWith(run.Note), annals.Step{Verb: "request.declined", Outcome: annals.Refused,
			Reason: "the plan's run ended " + run.State, Labels: map[string]string{"round": fmt.Sprint(r.PlanRound)}})
		return nil
	}
}

// unheld stops a request whose plan waits for its approvers when the hub
// let the run go ahead with nobody's decision: nothing is built, the run
// ends failed, and a person picks it up. Asking again would meet the same
// hub.
func (d *Desk) unheld(ctx context.Context, r *store.Request, log io.Writer, run *hub.PlanRun) (bool, error) {
	if run.DecidedBy != "" || (run.State != orrery.RunQueued && run.State != orrery.RunRunning) {
		return false, nil
	}
	approvers, err := d.approvers(r)
	if err != nil || len(approvers) == 0 {
		return false, err
	}
	d.log.Warn("plan went ahead with nobody's decision though it waits for its approvers; stopping", "request", r.ID, "run", run.ID, "lane", r.Lane)
	fmt.Fprintln(log, "the hub let the plan go ahead with nobody's decision, and this plan waits for its approvers; stopping")
	if err := d.hub.Report(ctx, run.ID, orrery.ActionReport{State: orrery.RunFailed, Summary: "Graft stopped: this plan waits for its approvers, and the hub let it go ahead with nobody's decision.", Link: d.requestURL(r)}); err != nil {
		return false, fmt.Errorf("end the unheld run: %w", err)
	}
	r.Run = ""
	d.ending(ctx, r, store.StateWaiting, annals.Step{Verb: "request.waiting", Outcome: annals.Refused,
		Reason: "the hub did not hold a plan that waits for its approvers", Labels: map[string]string{"round": fmt.Sprint(r.PlanRound)}}, failedAnswer)
	return true, nil
}

// declinedWith is what the person who asked is told of a plan declined:
// the decider's note is the reason.
func declinedWith(note string) string {
	if note = strings.TrimSpace(note); note == "" {
		return fixedDeclined
	}
	return fixedDeclined + "\n\nWhy: " + clip(note, 2000)
}

// noted is a decider's note as the end of a line on the page.
func noted(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return ": " + clip(strings.TrimSpace(note), 4000)
}

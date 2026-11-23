package desk

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/orchard-suite/platformkit/v3/dispatch"
	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

// The kinds of notice Graft sends through the incident desk.
const (
	KindAnswered = "graft.report_answered"
	KindShipped  = "graft.fix_shipped"
	KindFiled    = "graft.request_filed"
)

// Kinds declares Graft's notices to the incident desk, in the words a
// person reads when they choose how to hear of each.
var Kinds = []dispatch.Kind{
	{Kind: KindAnswered, Title: "Your request was answered", Description: "Graft answered something you reported or asked for.", Weight: dispatch.Notify},
	{Kind: KindShipped, Title: "A change shipped", Description: "Graft built and released a change, for a request you made or in a repository you own.", Weight: dispatch.Notify},
	{Kind: KindFiled, Title: "A request needs a person", Description: "Graft filed a ticket, or stopped and needs a person, in a repository you own.", Weight: dispatch.Notify},
}

// Fixed words. A request that read production for a reporter outside the
// program is answered in these alone, so nothing an agent read there can
// ride back out in its own words.
const (
	fixedJunk        = "This did not read as a report or a request, so nothing was done with it. Send it again with more detail if that is wrong."
	fixedAbuse       = "This was declined."
	fixedQuarantined = "Your message looked like it held a secret (a password, a key, or a token), so nobody and nothing read it. Change that secret now, then send your message again without it."
	fixedKnown       = "Thank you. This looks like a problem we already know about and are working on. You will hear back here when it is fixed."
	fixedFailed      = "Something went wrong on our side while working on this."
	fixedDeclined    = "This was looked at and a person decided not to go ahead with it."
	fixedConversing  = "This needs a conversation before it can go ahead."
	// fixedTold says a person knows. It is said only when a notice or the
	// request's ticket told one.
	fixedTold = " A person has been told and will pick it up."
	// fixedUntold is said when nothing told a person.
	fixedUntold = " Nobody has been told yet: send it again later, or tell the people who run this application."
)

// failedAnswer is what the person hears of a request Graft could not
// finish: that a person knows only when one was told.
func failedAnswer(told bool) string {
	if told {
		return fixedFailed + fixedTold
	}
	return fixedFailed + fixedUntold
}

// conversingAnswer is what the person hears of a plan sent back past its
// last round.
func conversingAnswer(told bool) string {
	if told {
		return fixedConversing + fixedTold
	}
	return fixedConversing + " The people who decide it have it on its ticket."
}

var fixedVerdict = map[string]string{
	store.VerdictDefect:    "Thank you. This is a real problem.",
	store.VerdictUserError: "We looked into this and the app did what it should. The steps you took do not do what you expected.",
	store.VerdictDesigned:  "We looked into this. The app works this way on purpose.",
	store.VerdictFeature:   "Thank you. This is a request for something new.",
	store.VerdictDuplicate: "Thank you. Someone already reported this, and it is being worked on.",
	store.VerdictNotEnough: "We could not find the problem from what you sent. Send it again with what you did, what you saw, and what you expected.",
	store.VerdictOutage:    fixedKnown,
}

// say is what the person is told about a verdict: the investigator's own
// answer, or fixed words when the request read production for someone
// outside the program.
func (d *Desk) say(r *store.Request) string {
	if r.Verdict == nil {
		return ""
	}
	if lane := d.prog.Lane(r.Lane); lane != nil && lane.Outside && r.ReadProduction {
		return fixedVerdict[r.Verdict.Verdict]
	}
	if a := strings.TrimSpace(r.Verdict.Answer); a != "" {
		return clip(a, 2000)
	}
	return fixedVerdict[r.Verdict.Verdict]
}

// end stops a request: it keeps the answer, tells the person and whoever
// else should hear, closes what the request held open, and records it.
func (d *Desk) end(ctx context.Context, r *store.Request, state, answer string, step annals.Step) {
	d.ending(ctx, r, state, step, func(bool) string { return answer })
}

// ending is end with an answer that may say whether the owner was told:
// where the owner hears of a request, they hear first.
func (d *Desk) ending(ctx context.Context, r *store.Request, state string, step annals.Step, say func(ownerTold bool) string) {
	ctx = context.WithoutCancel(ctx)
	r.State, r.HandleHash, r.Feedback = state, "", ""
	d.unhold(r)
	owner := d.owner(r)
	person := []dispatch.Target{{Person: r.Person}}
	ownerTold := false
	switch state {
	case store.StateFiled, store.StateWaiting, store.StateFailed, store.StateQuarantined:
		ownerTold = d.tell(ctx, r, KindFiled, "owner", "Needs a person: "+d.title(r), d.forOwner(r), owner)
	}
	answer := say(ownerTold)
	r.Answer = answer
	if r.Ticket != nil && answer != "" && state != store.StateReleased {
		answer += "\n\nTicket: " + r.Ticket.URL
		r.Answer = answer
	}
	d.record(ctx, r, state, step)
	if err := d.store.Save(ctx, r); err != nil {
		d.log.Error("request not saved", "request", r.ID, "error", err)
	}
	d.show(ctx, r)
	d.log.Info("request ended", "request", r.ID, "repo", r.Repo, "state", state, "lane", r.Lane)

	switch state {
	case store.StateReleased:
		d.tell(ctx, r, KindShipped, "end", "Shipped: "+d.title(r), answer, append(person, owner...))
	default:
		d.tell(ctx, r, KindAnswered, "end", "About your request: "+d.title(r), answer, person)
	}
	// The plan's run in the hub ends with the request.
	if r.Run != "" {
		report := orrery.ActionReport{State: orrery.RunFailed, Summary: "The request ended " + state + "."}
		if state == store.StateReleased {
			report = orrery.ActionReport{State: orrery.RunSucceeded, Summary: "Released as " + shortSHA(r.Commit) + "."}
		}
		report.Link = d.requestURL(r)
		if err := d.hub.Report(ctx, r.Run, report); err != nil {
			d.log.Debug("run not reported", "request", r.ID, "run", r.Run, "error", err)
		}
	}
	d.dropClones(ctx, r)
}

// forOwner is what the owner is told about a request that needs a person.
func (d *Desk) forOwner(r *store.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Request %s in %s ended %s.", r.ID, r.Repo, r.State)
	if r.Verdict != nil {
		fmt.Fprintf(&b, " Verdict: %s.", r.Verdict.Verdict)
	}
	if len(r.Secrets) > 0 {
		fmt.Fprintf(&b, " It held what looked like a secret (%s); nothing read it.", strings.Join(r.Secrets, ", "))
	}
	if r.Error != "" {
		b.WriteString(" Graft's log has why.")
	}
	return b.String()
}

// owner is who the incident desk tells about the request's repository.
func (d *Desk) owner(r *store.Request) []dispatch.Target {
	repo := d.prog.Repository(r.Repo)
	if repo == nil {
		return nil
	}
	return ownerTarget(d.prog.OwnerOf(repo))
}

func ownerTarget(o *program.Owner) []dispatch.Target {
	if o == nil {
		return nil
	}
	return []dispatch.Target{{Person: o.Person, Group: o.Group, Chain: o.Chain}}
}

// tell sends one notice through the incident desk, and says whether it
// went. A desk that does not take it is logged; a notice never stops a
// request. While the program holds its notices, nothing goes out: the
// notice is kept on the request as an event that says what would have been
// sent, and to whom.
func (d *Desk) tell(ctx context.Context, r *store.Request, kind, what, title, text string, to []dispatch.Target) bool {
	if len(to) == 0 {
		return false
	}
	if d.prog.Holding() {
		d.note(ctx, r, r.State, fmt.Sprintf("notice held: %s to %s\n%s\n\n%s", kind, targets(to), clip(title, 200), clip(text, 3500)))
		d.log.Info("notice held", "request", r.ID, "kind", kind, "to", len(to))
		return false
	}
	ev := dispatch.Event{
		Kind:   kind,
		Key:    "request:" + r.ID + ":" + what,
		Title:  clip(title, 200),
		Text:   clip(text, 3500),
		Facts:  []dispatch.Fact{{Name: "Repository", Value: r.Repo}, {Name: "Lane", Value: r.Lane}},
		Labels: map[string]string{"repository": r.Repo, "lane": r.Lane, "state": r.State},
		To:     to,
	}
	if repo := d.prog.Repository(r.Repo); repo != nil {
		bp, key, _ := strings.Cut(repo.Entity, ":")
		ev.Entity = &dispatch.Ref{Blueprint: bp, Key: key}
	}
	if u := d.requestURL(r); u != "" {
		ev.Links = append(ev.Links, dispatch.Link{Title: "Open the request", URL: u})
	}
	if r.Ticket != nil {
		ev.Links = append(ev.Links, dispatch.Link{Title: "Ticket", URL: r.Ticket.URL})
	}
	if err := d.inc.Send(ctx, ev); err != nil {
		d.log.Warn("notice not sent", "request", r.ID, "kind", kind, "error", err)
		return false
	}
	return true
}

// targets names who a notice is for, in the incident desk's words.
func targets(to []dispatch.Target) string {
	var out []string
	for _, t := range to {
		switch {
		case t.Person != "":
			out = append(out, "person "+t.Person)
		case t.Group != "":
			out = append(out, "group "+t.Group)
		case t.Chain != "":
			out = append(out, "chain "+t.Chain)
		}
	}
	return strings.Join(out, ", ")
}

// dropClones removes the runner's clone once the request has ended, and
// shuts any sandbox still open for it. The agent's clone went with its box.
func (d *Desk) dropClones(ctx context.Context, r *store.Request) {
	d.shut(ctx, r)
	if err := os.RemoveAll(d.repoDir(r)); err != nil {
		d.log.Error("clone not removed", "request", r.ID, "error", err)
	}
}

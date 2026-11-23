package desk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// A stage that fails throws nothing away (ADR-6001). Every stage runs again
// from its start, so a failed one can be tried again:
//
//   - A round that answers the approvers, while the round before still waits
//     in the hub, leaves that round standing: the request waits for its
//     decision again, and its approvers may retry the new round.
//   - Any other stage from the investigation on stops the request as
//     stalled, where its approvers may retry it.
//   - Either way the ticket says what failed, in plain words, and how to try
//     again, whatever the program does with its notices.
//
// Only a stage before the investigation, or a failure nobody could be told
// of, ends the request as failed.

// RetryCommand is the whole of a comment on a request's ticket that asks
// Graft to try a stage that failed again.
const RetryCommand = "/graft retry"

// ErrNothingToRetry is a retry asked of a request where no stage waits to be
// tried again.
var ErrNothingToRetry = errors.New("nothing on this request waits to be tried again")

// ErrMayNotRetry is a retry asked by someone who may not ask it: only the
// request's approvers retry it, and never the person who asked.
var ErrMayNotRetry = errors.New("only the approvers of this request's plans retry it, and never the person who asked")

// stopped is a stage's failure with its cause in plain words, for the ticket
// and the page, the program key that would give it more room, if any, and
// more to show on its run's comment, as Markdown: what is left of a build,
// or the pipeline's failed jobs.
type stopped struct {
	cause string
	key   string
	more  string
	err   error
}

func (e *stopped) Error() string { return e.err.Error() }
func (e *stopped) Unwrap() error { return e.err }

// stageWords says what a stage does, for a sentence.
var stageWords = map[string]string{
	store.StateInvestigating: "investigating",
	store.StatePlanning:      "asking about the plan",
	store.StateBuilding:      "building",
	store.StateLanding:       "landing the change",
	store.StateWatching:      "watching the release",
}

// retries says a stage is one a person may try again: from the
// investigation on, once the request's words are swept and sorted.
func retries(stage string) bool {
	_, ok := stageWords[stage]
	return ok
}

// causeOf is why a stage failed, in plain words, and what more its comment
// shows.
func causeOf(stage string, err error) (cause, key, more string) {
	var st *stopped
	if errors.As(err, &st) {
		return st.cause, st.key, st.more
	}
	return fmt.Sprintf("Graft could not finish %s: %s", stageWords[stage], clip(firstLine(err.Error()), 300)), "", ""
}

// stop takes a stage that failed. A round that answers its approvers while
// the round before still waits leaves that round standing; any other stage
// a person may retry stalls; the rest end the request as failed.
func (d *Desk) stop(ctx context.Context, r *store.Request, stage string, err error, log io.Writer) {
	ctx = context.WithoutCancel(ctx)
	r.Error = shell.Tail(err.Error(), 2000)
	if !retries(stage) {
		d.fail(ctx, r, err)
		return
	}
	cause, key, more := causeOf(stage, err)
	tries := 1
	if r.Stall != nil {
		tries = r.Stall.Tries + 1
	}
	r.Stall = &store.Stall{Stage: stage, Round: r.PlanRound, Cause: cause, At: d.now().UTC(), Tries: tries}
	d.log.Warn("stage failed", "request", r.ID, "stage", stage, "round", r.PlanRound, "tries", tries, "error", err)
	if d.standing(ctx, r, stage) {
		d.standBack(ctx, r, key, log)
		return
	}
	d.stall(ctx, r, key, more, log)
}

// standing says the stage was writing a round that answers the approvers,
// and the round before still waits for their decision in the hub.
func (d *Desk) standing(ctx context.Context, r *store.Request, stage string) bool {
	if (stage != store.StateInvestigating && stage != store.StatePlanning) || r.Plan == nil || r.Run == "" || len(openNotes(r)) == 0 {
		return false
	}
	run, err := d.hub.Run(ctx, r.Run)
	if err != nil {
		d.log.Warn("the plan's last run not read; the failed round stalls instead of standing back", "request", r.ID, "run", r.Run, "error", err)
		return false
	}
	if run.State != orrery.RunAwaiting {
		d.log.Info("the plan's last run no longer waits; the failed round stalls", "request", r.ID, "run", run.ID, "state", run.State)
		return false
	}
	return true
}

// drafted says the request holds the next round's findings, written but
// never asked about: the round before is the last item of Earlier.
func drafted(r *store.Request) bool {
	n := len(r.Earlier)
	return n > 0 && r.Earlier[n-1].N == r.PlanRound && r.PlanRound > 0
}

// standBack puts the round that still waits back where its approvers decide
// it, keeps the notes the failed round answered open for the next try, and
// says so on the ticket. Findings the failed round already wrote are kept on
// the stall, and a retry asks about them.
func (d *Desk) standBack(ctx context.Context, r *store.Request, key string, log io.Writer) {
	s := r.Stall
	next := r.PlanRound + 1
	if drafted(r) {
		n := len(r.Earlier)
		was := r.Earlier[n-1]
		s.Draft = &store.Round{N: next, Plan: *r.Plan, Verdict: r.Verdict}
		s.Stage = store.StatePlanning
		r.Plan = &was.Plan
		if was.Verdict != nil {
			r.Verdict = was.Verdict
		}
		r.Earlier = r.Earlier[:n-1]
	} else {
		s.Stage = store.StateInvestigating
	}
	r.State, r.HandleHash = store.StateAwaitingPlan, ""
	d.unhold(r)
	var b strings.Builder
	fmt.Fprintf(&b, "**Round %d of the plan could not be written.** %s.\n\nRound %d stands as it was, and still waits for a decision.", next, sentence(s.Cause), r.PlanRound)
	if r.RunURL != "" {
		fmt.Fprintf(&b, " [Decide it in the hub](%s).", r.RunURL)
	}
	b.WriteString(" The comments round " + fmt.Sprint(next) + " answers are kept, and the next try plans from them.\n\n")
	b.WriteString(d.howToRetry(r, "round "+fmt.Sprint(next), key))
	posted := d.failed(ctx, r, log, b.String(), s.Cause, key)
	d.record(ctx, r, fmt.Sprintf("plan round %d could not be written: %s; round %d stands", next, s.Cause, r.PlanRound),
		annals.Step{Verb: "request.round_failed", Outcome: annals.Failed, Reason: "the next round could not be written; the round before stands", Labels: map[string]string{"round": fmt.Sprint(next), "stage": s.Stage}})
	if !posted {
		d.log.Warn("the ticket was not told a round could not be written", "request", r.ID)
	}
	d.told(ctx, r, fmt.Sprintf("Round %d of the plan of request %s in %s could not be written: %s. Round %d stands and waits for a decision; its approvers may retry.", next, r.ID, r.Repo, s.Cause, r.PlanRound))
	if err := d.save(ctx, r); err != nil {
		d.log.Error("request not saved", "request", r.ID, "error", err)
	}
}

// stall stops the request where its approvers may retry the stage that
// failed, and says so on its ticket, which it writes first when it has
// none. A request whose ticket cannot be written ends as failed: then
// nobody would hear of the stall.
func (d *Desk) stall(ctx context.Context, r *store.Request, key, more string, log io.Writer) {
	s := r.Stall
	if err := d.ticket(ctx, r, log); err != nil {
		d.log.Warn("no ticket for a stalled request; it fails instead", "request", r.ID, "error", err)
		d.fail(ctx, r, errors.New(r.Error))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Graft stopped while %s.** %s.\n\nNothing is lost: the request keeps everything it had", stageWords[s.Stage], sentence(s.Cause))
	if r.Decision == store.DecisionApproved {
		fmt.Fprintf(&b, ", and round %d of the plan stays approved", r.PlanRound)
	}
	b.WriteString(".\n\n")
	b.WriteString(more)
	b.WriteString(d.howToRetry(r, stageWords[s.Stage], key))
	if !d.failed(ctx, r, log, b.String(), s.Cause, key) {
		d.fail(ctx, r, errors.New(r.Error))
		return
	}
	r.State, r.HandleHash, r.Feedback = store.StateStalled, "", ""
	d.unhold(r)
	if r.Run != "" && r.Decision == store.DecisionApproved {
		// The approved run stays live, so a retry builds under what it granted.
		if err := d.hub.Report(ctx, r.Run, orrery.ActionReport{State: orrery.RunRunning, Label: "Stalled: waiting for a retry", Link: d.requestURL(r)}); err != nil {
			d.log.Debug("stalled run not reported", "request", r.ID, "run", r.Run, "error", err)
		}
	}
	d.record(ctx, r, fmt.Sprintf("stalled while %s: %s", stageWords[s.Stage], s.Cause),
		annals.Step{Verb: "request.stalled", Outcome: annals.Failed, Reason: "a stage failed; it waits for a retry", Labels: map[string]string{"stage": s.Stage, "round": fmt.Sprint(r.PlanRound)}})
	d.told(ctx, r, fmt.Sprintf("Request %s in %s stopped while %s: %s. Its approvers may retry it on its ticket.", r.ID, r.Repo, stageWords[s.Stage], s.Cause))
	if err := d.save(ctx, r); err != nil {
		d.log.Error("request not saved", "request", r.ID, "error", err)
	}
}

// howToRetry tells an approver how to try again, and which setting gives a
// run that hit its budget more room.
func (d *Desk) howToRetry(r *store.Request, what, key string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "To try %s again, one of the plan's approvers comments `%s` here", what, RetryCommand)
	if u := d.requestURL(r); u != "" {
		fmt.Fprintf(&b, ", or presses **Retry** on [the request in Graft](%s)", u)
	}
	b.WriteString(".")
	if key != "" {
		fmt.Fprintf(&b, " To give it more room first, raise `%s` in Graft's program file.", key)
	}
	if u := r.Unfinished; u != nil && u.Part == store.PartProve {
		fmt.Fprintf(&b, " To let it land without a proof it lacks, an approver comments `%s <criterion number> <why>`, or `%s screenshots <why>`, before the retry.", WaiveCommand, WaiveCommand)
	}
	return b.String()
}

// told tells the repository's owner that a request stopped and waits for a
// person. The ticket already says so to its approvers.
func (d *Desk) told(ctx context.Context, r *store.Request, text string) {
	d.tell(ctx, r, KindFiled, fmt.Sprintf("stall:%d", r.Stall.Tries), "Needs a retry: "+d.title(r), text, d.owner(r))
}

// fail ends a request that Graft itself could not work. The ticket, when
// there is one, says why. The person is told only what happened: that a
// person knows, only when the ticket or a notice told one.
func (d *Desk) fail(ctx context.Context, r *store.Request, err error) {
	ctx = context.WithoutCancel(ctx)
	stage := r.State
	d.log.Error("request failed", "request", r.ID, "stage", stage, "error", err)
	r.Error = shell.Tail(err.Error(), 2000)
	posted := false
	if r.Ticket != nil {
		cause := fmt.Sprintf("It failed while %s: %s", doing(stage), clip(firstLine(err.Error()), 300))
		posted = d.failed(ctx, r, io.Discard, "**Graft could not finish this request.** "+cause+".", cause, "")
	} else if lv := d.running(r.ID); lv != nil {
		// No ticket to write on: the run ends with the request.
		d.finishRun(ctx, r, lv, store.RunFailed, sentence(clip(firstLine(err.Error()), 300))+".")
	}
	d.ending(ctx, r, store.StateFailed, annals.Step{Verb: "request.failed", Outcome: annals.Failed, Reason: "Graft could not finish the " + stage + " stage"},
		func(ownerTold bool) string { return failedAnswer(posted || ownerTold) })
}

// doing is a stage for a sentence, whether a person may retry it or not.
func doing(stage string) string {
	if w, ok := stageWords[stage]; ok {
		return w
	}
	return "working on the " + stage + " stage"
}

// sentence is a cause as a sentence, with a capital and no closing stop.
func sentence(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ".")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Retryable is what a retry of the request would run again, or nil. A
// request that failed before Graft kept its stalls is read from what it
// holds: a round that answered its approvers fails with its notes still
// open, its plan asked, and its ticket written.
func Retryable(r *store.Request) *store.Stall {
	switch r.State {
	case store.StateStalled, store.StateAwaitingPlan, store.StateFailed:
	default:
		return nil
	}
	if r.Stall != nil {
		return r.Stall
	}
	if r.State != store.StateFailed || r.Ticket == nil || r.Run == "" || r.Plan == nil || len(openNotes(r)) == 0 {
		return nil
	}
	stage := store.StateInvestigating
	if drafted(r) {
		stage = store.StatePlanning
	}
	return &store.Stall{Stage: stage, Round: r.PlanRound, Cause: clip(firstLine(r.Error), 300), At: r.UpdatedAt, Tries: 1}
}

// Retry runs the stage of a request that failed again, as by asked: the
// request's approvers, as the hub judges them now, and never the person
// who asked.
func (d *Desk) Retry(ctx context.Context, id, by string) (*store.Request, error) {
	m := d.locks.of(id)
	m.Lock()
	defer m.Unlock()
	r, err := d.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := d.retry(ctx, r, by, "the page", io.Discard); err != nil {
		return nil, err
	}
	return r, nil
}

// retry checks who asks, then puts the request back at the stage that
// failed. Findings a failed round wrote are asked about without writing
// them again. A request that had ended is open again.
func (d *Desk) retry(ctx context.Context, r *store.Request, by, via string, log io.Writer) error {
	s := Retryable(r)
	if s == nil {
		return ErrNothingToRetry
	}
	ok, err := d.mayRetry(ctx, r, by)
	if err != nil {
		return err
	}
	if !ok {
		d.log.Info("retry refused: not one of the request's approvers, or the person who asked", "request", r.ID)
		return ErrMayNotRetry
	}
	was := r.State
	what := stageWords[s.Stage]
	if (s.Stage == store.StateInvestigating || s.Stage == store.StatePlanning) && r.Plan != nil && len(openNotes(r)) > 0 {
		what = fmt.Sprintf("writing round %d of the plan", r.PlanRound+1)
	}
	r.Stall = s
	s.RetriedBy, s.Via = by, via
	if s.Draft != nil {
		d.newRound(r, s.Draft.Verdict, &s.Draft.Plan)
		s.Draft = nil
	}
	r.State, r.Error, r.Answer = s.Stage, "", ""
	if s.Stage == store.StateBuilding {
		// A retry gives the builder its rounds back. It goes on from the
		// work an unfinished build kept on the request, with what was left;
		// a build that kept none starts again from main.
		r.Rounds, r.Feedback = 0, ""
	}
	d.unhold(r)
	if r.Run != "" && r.Decision == store.DecisionApproved && was == store.StateStalled {
		if err := d.hub.Report(ctx, r.Run, orrery.ActionReport{State: orrery.RunRunning, Label: "Building", Link: d.requestURL(r)}); err != nil {
			d.log.Debug("retried run not reported", "request", r.ID, "run", r.Run, "error", err)
		}
	}
	d.log.Info("retry taken", "request", r.ID, "stage", s.Stage, "from", was, "via", via)
	d.record(ctx, r, fmt.Sprintf("%s asked to try again, on %s: %s", by, via, what), annals.Step{Verb: "request.retried", Labels: map[string]string{"stage": s.Stage, "from": was}})
	if err := d.store.SaveIf(ctx, r, was); err != nil {
		return err
	}
	d.show(ctx, r)
	if !d.prog.Ticket.RunsOn() {
		// With run comments on, the run that tries again says so itself.
		d.comment(ctx, r, log, fmt.Sprintf("Trying again, as %s asked: %s.", by, what))
	}
	return nil
}

// mayRetry says the person may retry the request: its approvers let them
// in, as the hub judges them now, and they did not ask for it.
func (d *Desk) mayRetry(ctx context.Context, r *store.Request, by string) (bool, error) {
	if strings.EqualFold(by, r.Person) {
		return false, nil
	}
	grants, err := d.approverGrants(r)
	if err != nil {
		return false, err
	}
	if len(grants) == 0 {
		return false, nil
	}
	ok, err := d.hub.LetsIn(ctx, by, grants)
	if err != nil {
		return false, fmt.Errorf("check who asks to retry: %w", err)
	}
	return ok, nil
}

// MayRetry says the person may retry the request now, as the hub judged
// them within the last minute: for the page to offer it. The retry itself
// is judged afresh.
func (d *Desk) MayRetry(ctx context.Context, r *store.Request, person string) (bool, error) {
	if Retryable(r) == nil || strings.EqualFold(person, r.Person) {
		return false, nil
	}
	repo := d.prog.Repository(r.Repo)
	if repo == nil {
		return false, nil
	}
	return d.Approves(ctx, person, repo)
}

// isRetry says a comment is the retry command and nothing else.
func isRetry(body string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(body), " "), RetryCommand)
}

// approverGrants are who may retry the request: its repository's approvers,
// else the program's.
func (d *Desk) approverGrants(r *store.Request) ([]program.Grant, error) {
	repo, err := d.repo(r)
	if err != nil {
		return nil, err
	}
	return d.prog.ApproversOf(repo), nil
}

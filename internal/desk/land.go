package desk

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	kit "github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/dispatch"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// pipelineStart is how long a landed commit may go without any pipeline
// run before Graft decides none was triggered for it.
var pipelineStart = 3 * time.Minute

// forgeMisses is how many reads of a commit's pipeline in a row may fail
// before a watch gives up. A forge drops a request now and then, and one
// dropped read is no reason to fail a request whose commit has landed.
const forgeMisses = 10

// reviewBranch is where a change that lands through a pull request is pushed.
func reviewBranch(r *store.Request) string { return "graft/" + r.ID }

// land puts the agent's commits, which the build stage took into the
// runner's clone, on the forge from there.
// Graft rebases first, so a conflict goes back to the agent instead of
// failing the request. A lane marked for review pushes a branch and opens a
// pull request, and the forge's own branch rules hold it until a person has
// reviewed it. Any other lane lands on main with the repository's own
// landing: release-damnit land where the repository uses it, a plain push
// otherwise.
func (d *Desk) land(ctx context.Context, r *store.Request, log io.Writer) error {
	// Taking a change back out never waits: it is how a bad release ends.
	if !r.Reverting {
		if ok, err := d.ready(ctx, r, log); err != nil || !ok {
			return err
		}
	}
	if err := d.ensureRepo(ctx, r, log); err != nil {
		return err
	}
	dir := d.repoDir(r)
	if _, err := d.git(ctx, dir, log, "merge-base", "--is-ancestor", "refs/remotes/origin/main", "HEAD"); err != nil {
		if _, err := shell.Run(ctx, log, shell.Cmd{Dir: dir, Env: d.committer(), Args: append(append([]string{"git"}, d.gitFlags()...), "rebase", "--quiet", "refs/remotes/origin/main")}); err != nil {
			if _, abortErr := d.git(ctx, dir, log, "rebase", "--abort"); abortErr != nil {
				return fmt.Errorf("rebase failed and could not be aborted: %v", abortErr)
			}
			if r.Reverting {
				return fmt.Errorf("the revert conflicts with main: %w", err)
			}
			return &backToAgent{why: "the commits conflict with main", prompt: conflictBrief(err.Error())}
		}
	}
	if r.First == "" {
		first, err := d.rev(ctx, r, log, "refs/remotes/origin/main")
		if err != nil {
			return err
		}
		r.First = first
	}
	lane, err := d.lane(r)
	if err != nil {
		return err
	}
	if lane.Review {
		return d.openReview(ctx, r, log)
	}
	before, err := d.rev(ctx, r, log, "refs/remotes/origin/main")
	if err != nil {
		return err
	}
	args := []string{"git", "push", "origin", "HEAD:main"}
	if landsWithReleaseDamnit(dir) {
		args = []string{"release-damnit", "land"}
	}
	d.step(r, store.StepDid, "Landing on main with `"+strings.Join(args, " ")+"`.", nil)
	// Landing rebases again when main moves meanwhile, and a rebase writes
	// commits: they carry Graft as their committer.
	if _, err := shell.Run(ctx, log, shell.Cmd{Dir: dir, Env: d.committer(), Args: args}); err != nil {
		return err
	}
	head, err := d.rev(ctx, r, log, "HEAD")
	if err != nil {
		return err
	}
	d.commitSteps(ctx, r, log, before+"..HEAD", true)
	return d.landed(ctx, r, log, head, "landed on main")
}

// landed records the commit main got. A round sent back from the pipeline
// starts from it.
func (d *Desk) landed(ctx context.Context, r *store.Request, log io.Writer, sha, how string) error {
	r.Commit, r.Base, r.Release = sha, sha, ""
	d.record(ctx, r, how+" as "+shortSHA(sha), annals.Step{Verb: "request.landed", Change: &kit.Change{BeforeHash: r.First, AfterHash: sha},
		Labels: map[string]string{"reverting": fmt.Sprint(r.Reverting)}})
	r.State = store.StateWatching
	return nil
}

// openReview pushes the commits to the request's branch and opens a pull
// request for them. The forge must itself keep a change off main until a
// review: a review lane on a repository whose main branch the forge does
// not protect that way refuses to land.
func (d *Desk) openReview(ctx context.Context, r *store.Request, log io.Writer) error {
	enforced, err := d.forge.ReviewEnforced(ctx, r.Repo)
	if err != nil {
		return fmt.Errorf("read the main branch's rules: %w", err)
	}
	if !enforced {
		fmt.Fprintf(log, "the forge does not make a review a must on %s's main branch\n", r.Repo)
		d.end(ctx, r, store.StateWaiting, d.say(r)+"\n\nThe change is built, but this repository's main branch does not require a review on the forge, so Graft did not land it. A person has been told.",
			annals.Step{Verb: "request.waiting", Outcome: annals.Refused, Reason: "the lane lands through review and the forge does not protect the main branch"})
		return nil
	}
	dir, branch := d.repoDir(r), reviewBranch(r)
	head, err := d.rev(ctx, r, log, "HEAD")
	if err != nil {
		return err
	}
	if _, err := d.git(ctx, dir, log, "push", "--quiet", "--force", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return err
	}
	if r.PR == nil {
		subject, err := d.git(ctx, dir, log, "log", "-1", "--format=%s", "HEAD")
		if err != nil {
			return err
		}
		body := fmt.Sprintf("Graft request `%s`.", r.ID)
		if r.Ticket != nil {
			body += fmt.Sprintf(" Ticket #%d.", r.Ticket.Number)
		}
		if r.Plan != nil && !r.Reverting {
			body += "\n\n" + roundText(r.Plan, d.evidenceTitles(ctx, r), r.PlanRound, nil)
		}
		pr, err := d.forge.OpenPR(ctx, r.Repo, branch, "main", clip(strings.TrimSpace(subject.Output), 200), body)
		if err != nil {
			return fmt.Errorf("open a pull request: %w", err)
		}
		r.PR = &store.PR{Number: pr.Number, URL: pr.URL}
	}
	r.PR.Head = head
	d.record(ctx, r, "waiting for review: "+r.PR.URL, annals.Step{Verb: "request.review_asked", Approval: &kit.Approval{Required: true}})
	r.State = store.StateAwaitingReview
	return nil
}

// pollReview reads the pull request again. Merged, the request watches the
// commit main got. Closed, it is declined. Approved, Graft asks the forge
// to merge it; the forge's own rules may still refuse.
func (d *Desk) pollReview(ctx context.Context, r *store.Request) error {
	pr, err := d.forge.PR(ctx, r.Repo, r.PR.Number)
	if err != nil {
		return err
	}
	switch {
	case pr.State == forge.PRMerged:
		f, err := d.openLog(r.ID)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := d.ensureRepo(ctx, r, f); err != nil {
			return err
		}
		if err := d.forge.DeleteBranch(ctx, r.Repo, reviewBranch(r)); err != nil {
			fmt.Fprintf(f, "could not delete %s: %v\n", reviewBranch(r), err)
		}
		r.PR = nil
		// The runner's main becomes what the forge merged, so a round sent
		// back from the pipeline builds on it.
		if _, err := d.git(ctx, d.repoDir(r), f, "checkout", "--quiet", "-B", "main", pr.MergeSHA); err != nil {
			return err
		}
		if err := d.landed(ctx, r, f, pr.MergeSHA, "merged after review"); err != nil {
			return err
		}
		return d.save(ctx, r)
	case pr.State == forge.PRClosed:
		if err := d.forge.DeleteBranch(ctx, r.Repo, reviewBranch(r)); err != nil {
			d.log.Debug("branch not deleted", "request", r.ID, "error", err)
		}
		d.end(ctx, r, store.StateDeclined, fixedDeclined, annals.Step{Verb: "request.declined", Outcome: annals.Refused, Reason: "its pull request was closed without merging"})
		return nil
	case pr.Approved && pr.Head == r.PR.Head:
		if err := d.forge.MergePR(ctx, r.Repo, r.PR.Number, r.PR.Head); err != nil {
			d.note(ctx, r, r.State, "approved, but the forge did not merge it yet: "+clip(err.Error(), 200))
		}
	}
	return nil
}

// watch waits for every pipeline run the forge starts for the landed
// commit, then, where the repository's own release job wrote a release
// commit after it, for every run of that one: its pipeline is the release
// (GRF-6006). Only success releases anything. A run that failed, was
// cancelled or skipped, or waits for a click stalls the request here, its
// failed jobs on the ticket; a retry reads the pipeline again and hands
// what still fails to the builder.
func (d *Desk) watch(ctx context.Context, r *store.Request, log io.Writer) error {
	for {
		sha := r.Commit
		if r.Release != "" {
			sha = r.Release
		}
		runs, err := d.settle(ctx, r, log, sha)
		if err != nil {
			return err
		}
		var bad []forge.Run
		for _, run := range runs {
			fmt.Fprintf(log, "%s %s: %s\n", run.Name, run.URL, run.Conclusion)
			if !run.Green() {
				bad = append(bad, run)
			}
		}
		if len(bad) > 0 {
			fmt.Fprintf(log, "not released: %d of %d pipeline runs of %s are not green\n", len(bad), len(runs), shortSHA(sha))
			d.log.Info("pipeline not green; nothing released", "request", r.ID, "sha", shortSHA(sha), "runs", len(runs), "not_green", len(bad))
			return d.notReleased(ctx, r, log, sha, bad)
		}
		if r.Release != "" {
			d.log.Info("release pipeline green", "request", r.ID, "release", shortSHA(r.Release))
			return d.green(ctx, r, log)
		}
		rel, subject, err := d.releaseCommit(ctx, r, log)
		if err != nil {
			return err
		}
		if rel == "" {
			d.log.Info("pipeline green and no release commit follows; it is the release", "request", r.ID, "sha", shortSHA(sha), "runs", len(runs))
			return d.green(ctx, r, log)
		}
		fmt.Fprintf(log, "the release job wrote %s (%s); watching its pipeline\n", shortSHA(rel), subject)
		d.log.Info("pipeline green; watching the release commit's pipeline", "request", r.ID, "release", shortSHA(rel))
		r.Release = rel
		d.step(r, store.StepCommit, "The release job wrote "+clip(subject, 100)+"; its pipeline is the release.", nil)
		if err := d.store.Save(ctx, r); err != nil {
			return err
		}
	}
}

// settle waits until every pipeline run of a commit is over, and looks
// once more for runs those started. It answers the runs, or none when no
// run started within pipelineStart.
func (d *Desk) settle(ctx context.Context, r *store.Request, log io.Writer, sha string) ([]forge.Run, error) {
	start := d.now()
	deadline := start.Add(d.cfg.PipelineWait)
	settled, misses := false, 0
	d.step(r, store.StepDid, "Watching every pipeline of "+shortSHA(sha)+": "+d.forge.CommitURL(r.Repo, sha), nil)
	shown := map[string]string{}
	for {
		runs, err := d.forge.Runs(ctx, r.Repo, sha)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			misses++
			if misses >= forgeMisses {
				return nil, fmt.Errorf("the forge failed %d reads in a row: %w", misses, err)
			}
			fmt.Fprintf(log, "could not read the pipeline (%d of %d), reading again: %v\n", misses, forgeMisses, err)
			if err := sleep(ctx, watchEvery); err != nil {
				return nil, err
			}
			continue
		}
		misses = 0
		d.pipelineSteps(r, runs, shown)
		switch {
		case len(runs) == 0 && d.now().Sub(start) > pipelineStart:
			fmt.Fprintf(log, "no pipeline ran for %s\n", shortSHA(sha))
			return nil, nil
		case len(runs) == 0:
			fmt.Fprintf(log, "waiting for a pipeline to start for %s\n", shortSHA(sha))
		case !allDone(runs):
			settled = false
			for _, run := range runs {
				fmt.Fprintf(log, "%s %s: %s %s\n", run.Name, run.URL, run.Status, run.Conclusion)
			}
		case !settled:
			// Every run is done. Look once more, for runs these started.
			settled = true
		default:
			for _, run := range runs {
				d.note(ctx, r, r.State, fmt.Sprintf("%s %s: %s", run.Name, run.URL, run.Conclusion))
			}
			return runs, nil
		}
		if d.now().After(deadline) {
			return nil, fmt.Errorf("the pipeline for %s did not finish within %s", shortSHA(sha), d.cfg.PipelineWait)
		}
		if err := sleep(ctx, watchEvery); err != nil {
			return nil, err
		}
	}
}

// releaseCommit is the commit a repository's own release job wrote on main
// after the landed commit, in a repository that releases with
// release-damnit: the first commit since it whose subject is a release
// commit's. Its pipeline is the release. Empty where none follows.
func (d *Desk) releaseCommit(ctx context.Context, r *store.Request, log io.Writer) (sha, subject string, err error) {
	if !landsWithReleaseDamnit(d.repoDir(r)) {
		return "", "", nil
	}
	if err := d.ensureRepo(ctx, r, log); err != nil {
		return "", "", err
	}
	res, err := d.git(ctx, d.repoDir(r), log, "log", "--reverse", "--ancestry-path", "--format=%H%x00%s", r.Commit+"..refs/remotes/origin/main")
	if err != nil {
		return "", "", err
	}
	for _, l := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		h, subj, ok := strings.Cut(l, "\x00")
		if ok && isReleaseCommit(subj) {
			return h, subj, nil
		}
	}
	return "", "", nil
}

// isReleaseCommit says a subject is a release commit's, as release-damnit
// writes and passes one.
func isReleaseCommit(subject string) bool {
	return strings.HasPrefix(subject, "chore: release") || strings.HasPrefix(subject, "chore(release):")
}

// notReleased is a commit whose pipeline is not green: nothing was
// released. Each failed job goes on the run with its page. On a retry, what
// still fails goes back to the builder with each failed job's log, to fix
// forward; otherwise the request stalls here.
func (d *Desk) notReleased(ctx context.Context, r *store.Request, log io.Writer, sha string, bad []forge.Run) error {
	var report, jobs strings.Builder
	failed := 0
	var ends []string
	for _, run := range bad {
		end := run.Conclusion
		if end == "" {
			end = run.Status
		}
		ends = append(ends, run.Name+" ended "+end)
		fmt.Fprintf(&jobs, "- [%s](%s) ended %s\n", plain(run.Name), run.URL, plain(end))
		fmt.Fprintf(&report, "%s ended %s: %s\n", run.Name, end, run.URL)
		list, err := d.forge.FailedJobs(ctx, r.Repo, run)
		if err != nil {
			fmt.Fprintf(log, "the failed jobs of %s were not read: %v\n", run.Name, err)
			fmt.Fprintf(&jobs, "  - its failed jobs could not be read\n")
			continue
		}
		for _, j := range list {
			failed++
			name := j.Name
			if j.Stage != "" {
				name += " (" + j.Stage + ")"
			}
			fmt.Fprintf(&jobs, "  - job [%s](%s) failed\n", plain(name), j.URL)
			fmt.Fprintf(&report, "---- job %s ----\n%s\n\n", name, shell.Tail(j.Log, 6000))
			ok := false
			if lv := d.running(r.ID); lv != nil {
				lv.add(store.Step{At: d.now(), By: store.ByGraft, Kind: store.StepPipeline, Text: run.Name + ": job " + name + " failed", URL: j.URL, OK: &ok, Detail: redacted(tailLines(j.Log, detailMax))})
			}
		}
	}
	if r.Reverting {
		return d.revertFailed(ctx, r, log)
	}
	retried := r.Stall != nil && r.Stall.Stage == store.StateWatching && r.Stall.RetriedBy != ""
	if retried && failed > 0 {
		fmt.Fprintf(log, "a retry, and %d job(s) still fail; back to the builder to fix forward\n", failed)
		r.Rounds, r.Feedback, r.State = 0, pipelineBrief(sha, report.String()), store.StateBuilding
		d.note(ctx, r, store.StateWatching, fmt.Sprintf("the pipeline of %s still fails; back to the builder to fix it forward", shortSHA(sha)))
		return nil
	}
	var more strings.Builder
	fmt.Fprintf(&more, "#### The pipeline of [%s](%s)\n\n%s\n", shortSHA(sha), d.forge.CommitURL(r.Repo, sha), jobs.String())
	if failed == 0 {
		more.WriteString("No job failed: the pipeline was stopped or waits for a person. Run it again on the forge, then retry here; a retry reads it again.\n\n")
	} else {
		more.WriteString("A retry reads the pipeline again: run it again on the forge first if it only needs that. What still fails goes to the builder, with each failed job's log, to fix forward.\n\n")
	}
	return &stopped{cause: fmt.Sprintf("the pipeline of %s is not green, so nothing was released: %s", shortSHA(sha), strings.Join(ends, "; ")), more: more.String(),
		err: fmt.Errorf("the pipeline of %s is not green: %s", shortSHA(sha), strings.Join(ends, "; "))}
}

// pipelineSteps puts each pipeline run of the commit watched on the run
// when it starts and each time its state moves.
func (d *Desk) pipelineSteps(r *store.Request, runs []forge.Run, shown map[string]string) {
	lv := d.running(r.ID)
	if lv == nil {
		return
	}
	for _, run := range runs {
		state := run.Status + " " + run.Conclusion
		if shown[run.ID] == state {
			continue
		}
		shown[run.ID] = state
		s := store.Step{At: d.now(), By: store.ByGraft, Kind: store.StepPipeline, Text: run.Name + ": " + strings.TrimSpace(strings.ReplaceAll(state, "completed ", "")), URL: run.URL}
		if run.Done() {
			ok := run.Green()
			s.OK = &ok
		}
		lv.add(s)
	}
}

// watchEvery is how often a watch reads the pipeline again.
var watchEvery = 20 * time.Second

func allDone(runs []forge.Run) bool {
	for _, r := range runs {
		if !r.Done() {
			return false
		}
	}
	return true
}

// green ends a request whose pipeline passed: released, or, for a revert,
// filed for a person with the change taken back out.
func (d *Desk) green(ctx context.Context, r *store.Request, log io.Writer) error {
	repo, err := d.repo(r)
	if err != nil {
		return err
	}
	url := d.forge.CommitURL(r.Repo, r.Commit)
	if r.Reverting {
		fmt.Fprintf(log, "the revert is green: %s\n", url)
		d.post(ctx, r, log, "Graft could not get this change through the pipeline and took its commits back out: "+url)
		d.end(ctx, r, store.StateFiled, d.say(r)+"\n\nA fix was built but did not pass the release pipeline, so it was taken back out. It is filed for a person.",
			annals.Step{Verb: "request.filed", Outcome: annals.Failed, Reason: "the change did not pass the pipeline and was reverted"})
		return nil
	}
	at := d.now()
	if err := d.inc.RecordChange(ctx, dispatch.Change{Service: repo.Service, Title: clip(d.title(r), 200), Kind: "deploy", URL: url, By: "graft", At: &at}); err != nil {
		fmt.Fprintf(log, "the change was not recorded at the incident desk: %v\n", err)
	}
	if err := d.store.AddRelease(ctx, r.Repo, r.ID, r.Commit); err != nil {
		return err
	}
	if r.Release != "" {
		d.post(ctx, r, log, "Released: "+d.forge.CommitURL(r.Repo, r.Release)+", the release of "+url)
	} else {
		d.post(ctx, r, log, "Released: "+url)
	}
	if r.Ticket != nil {
		if err := d.forge.CloseIssue(ctx, r.Repo, r.Ticket.Number); err != nil {
			fmt.Fprintf(log, "the ticket was not closed: %v\n", err)
		}
	}
	answer := d.say(r) + "\n\nIt is fixed and released."
	if r.Plan != nil && r.Plan.Build.Kind == store.KindFeature {
		answer = d.say(r) + "\n\nIt is built and released."
	}
	approval := &kit.Approval{Required: r.DecidedBy != "", By: r.DecidedBy}
	d.end(ctx, r, store.StateReleased, answer, annals.Step{Verb: "request.released", Approval: approval, Change: &kit.Change{BeforeHash: r.First, AfterHash: r.Commit},
		Labels: map[string]string{"small": fmt.Sprint(r.Small), "rounds": fmt.Sprint(r.Rounds)}})
	// What landed may have changed the repository's tools.
	if err := d.SyncTools(context.WithoutCancel(ctx), repo); err != nil {
		d.log.Warn("tools not refreshed after a release", "repo", r.Repo, "error", err)
	}
	return nil
}

// comment writes on the request's ticket, and says whether it did. A
// ticket is a view of the request, so a comment that fails is logged and
// stops nothing.
func (d *Desk) comment(ctx context.Context, r *store.Request, log io.Writer, body string) bool {
	if r.Ticket == nil {
		return false
	}
	if _, err := d.forge.CommentIssue(ctx, r.Repo, r.Ticket.Number, body); err != nil {
		fmt.Fprintf(log, "the ticket was not commented: %v\n", err)
		d.log.Warn("ticket not commented", "request", r.ID, "error", err)
		return false
	}
	return true
}

// giveUp is what happens when a stage sends the build back and the agent
// has no rounds left. Before anything landed, the request is filed for a
// person. After, Graft takes its own commits back out and lands that.
func (d *Desk) giveUp(ctx context.Context, r *store.Request, log io.Writer, back *backToAgent) error {
	if r.Commit == "" || r.Reverting {
		d.end(ctx, r, store.StateFiled, d.say(r)+"\n\nA fix could not be built. It is filed for a person.",
			annals.Step{Verb: "request.filed", Outcome: annals.Failed, Reason: back.why + " after the last agent round"})
		return nil
	}
	fmt.Fprintf(log, "%s after %d rounds; taking the commits back out\n", back.why, r.Rounds)
	return d.revert(ctx, r, log)
}

// revert writes one commit, in the runner's clone, that undoes every commit
// of this request on main, and sends the request back to landing. The
// request's commits are the ones since its first landing that name its
// ticket.
func (d *Desk) revert(ctx context.Context, r *store.Request, log io.Writer) error {
	if r.Ticket == nil {
		return fmt.Errorf("cannot tell this request's commits apart without a ticket")
	}
	if err := d.ensureRepo(ctx, r, log); err != nil {
		return err
	}
	dir := d.repoDir(r)
	if _, err := d.git(ctx, dir, log, "checkout", "--quiet", "-B", "main", "refs/remotes/origin/main"); err != nil {
		return err
	}
	res, err := d.git(ctx, dir, log, "log", "--format=%H%x00%B%x01", r.First+"..HEAD")
	if err != nil {
		return err
	}
	var mine []string
	for _, entry := range strings.Split(res.Output, "\x01") {
		sha, msg, ok := strings.Cut(strings.TrimSpace(entry), "\x00")
		if ok && namesTicket(msg, r.Ticket.Number) {
			mine = append(mine, sha)
		}
	}
	if len(mine) == 0 {
		return fmt.Errorf("found no commit of this request on main to take back out")
	}
	// Newest first, so each undo applies on top of the last.
	for _, sha := range mine {
		if _, err := d.git(ctx, dir, log, "revert", "--no-commit", sha); err != nil {
			_, _ = d.git(ctx, dir, io.Discard, "revert", "--abort")
			return fmt.Errorf("could not undo %s: %w", shortSHA(sha), err)
		}
	}
	msg := fmt.Sprintf("fix: take back graft request %s\n\nThe change did not pass the release pipeline.\n\n%s", r.ID, ticketTrailer(r.Ticket.Number))
	if _, err := shell.Run(ctx, log, shell.Cmd{Dir: dir, Env: d.committer(), Args: append(append([]string{"git"}, d.gitFlags()...), "commit", "--quiet", "-m", msg)}); err != nil {
		return err
	}
	r.Reverting, r.Feedback, r.PR = true, "", nil
	d.record(ctx, r, fmt.Sprintf("taking %d commits back out", len(mine)), annals.Step{Verb: "request.reverting", Outcome: annals.Failed, Reason: "the change did not pass the pipeline"})
	r.State = store.StateLanding
	return nil
}

// revertFailed is a revert whose own pipeline is red: putting it back did
// not help, so a person must come. An incident opens on the application.
func (d *Desk) revertFailed(ctx context.Context, r *store.Request, log io.Writer) error {
	repo, err := d.repo(r)
	if err != nil {
		return err
	}
	inc, err := d.inc.OpenIncident(ctx, dispatch.NewIncident{Title: clip("Graft's change and its revert both failed the pipeline: "+d.title(r), 200), Severity: dispatch.Critical, Service: repo.Service,
		Description: fmt.Sprintf("Graft request %s in %s. The change failed the release pipeline, and so did taking it back out. %s", r.ID, r.Repo, d.requestURL(r))})
	if err != nil {
		fmt.Fprintf(log, "the incident was not opened: %v\n", err)
	} else if inc != nil {
		d.link(r, inc)
	}
	if err := d.store.SetSwitch(ctx, store.Switch{Repo: r.Repo, Stopped: true, Reason: "request " + r.ID + " and its revert both failed the pipeline", By: "graft"}); err != nil {
		return err
	}
	d.ending(ctx, r, store.StateWaiting, annals.Step{Verb: "request.waiting", Outcome: annals.Failed, Reason: "the change and its revert both failed the pipeline"},
		func(ownerTold bool) string { return failedAnswer(ownerTold || inc != nil) })
	return nil
}

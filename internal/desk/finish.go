package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// A build lands only when it is finished and proven (GRF-6005). The builder
// says it is finished, or what is left, in a file Graft names, never in
// its prose. A run stopped at its budget or its time limit is never
// finished. A prover, with a budget of its own, proves each criterion of the
// plan the builder left unproven: a test, or a screenshot where people see
// the change. Work that is not finished or not proven keeps its commits on
// the request, never on the forge's main; the build stalls, its comment says
// what is left, and a retry goes on from there.

// FinishFile is the agent's word on whether its work is finished, in the
// report folder: what `graft report done` and `graft report left` write.
const FinishFile = "finish.json"

// Finish is what the builder says of its work, and what the prover adds
// when a criterion does not hold.
type Finish struct {
	// Done says the builder finished every step of the plan, tested and
	// committed it. Any Left makes the work unfinished whatever Done says.
	Done    bool     `json:"done"`
	Summary string   `json:"summary,omitempty"`
	Left    []string `json:"left,omitempty"`
}

// finished says the agent said the work is finished, and left nothing.
func (f *Finish) finished() bool { return f != nil && f.Done && len(f.Left) == 0 }

// readFinish reads the agent's finish file as data: nil when it wrote none.
func (d *Desk) readFinish(ctx context.Context, r *store.Request) (*Finish, error) {
	p := filepath.Join(d.reportDir(r), FinishFile)
	if _, err := d.box(r).WS.Run(ctx, io.Discard, shell.Cmd{Dir: d.home(r), Args: []string{"test", "-f", p}}); err != nil {
		return nil, nil
	}
	w := &capWriter{max: maxOut}
	if err := d.box(r).WS.Get(ctx, p, w); err != nil {
		return nil, err
	}
	if w.over {
		return nil, fmt.Errorf("%s is larger than %d bytes", FinishFile, maxOut)
	}
	var f Finish
	dec := json.NewDecoder(bytes.NewReader(w.buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s is not the JSON asked for: %v", FinishFile, err)
	}
	for i, l := range f.Left {
		f.Left[i] = clip(line(redacted(l)), stepMax)
	}
	f.Summary = clip(line(redacted(f.Summary)), stepMax)
	return &f, nil
}

// finishBrief tells the builder how to say it is finished, or what is left.
func (d *Desk) finishBrief() string {
	return `
## Say when it is finished

Graft lands nothing you have not said is finished. When every step of the plan is built, tested, and committed, and the repository's own check and the commit check pass, say so, once:

    graft report done "<what you built, in a line>"

If you stop before that, for any reason, say what is left, one line each, with why:

    graft report left "The Forms seed files are not regenerated: the generator needs the database the tests start"

Work you leave unfinished is kept on the request, never landed; the next attempt goes on from it with your list. A run stopped at its budget or its time limit is never finished.
`
}

// The words a builder or a prover is told when it reaches its budget.
const (
	buildWindDown = "Graft stopped you: this run has reached its budget. Build no further. Commit what is done, with messages that keep the repository's commit rules, then say what is left, one line each, with `graft report left \"<what is left, and why>\"`, and stop. The work is kept for the next attempt, never landed. Never push."
	proveWindDown = "Graft stopped you: this run has reached its budget. Prove no further. Record every proof you already have with `graft report proof`, then stop."
)

// buildLeft is how a build that is not finished goes on: the work so far is
// kept on the request, and the build stalls, saying what is left.
func (d *Desk) buildLeft(ctx context.Context, r *store.Request, dir, head string, log io.Writer, part, cause, key string, left []string) error {
	ctx = context.WithoutCancel(ctx)
	if head == "" {
		if res, err := d.wsGit(ctx, r, dir, log, "rev-parse", "HEAD"); err == nil {
			head = strings.TrimSpace(res.Output)
		}
	}
	u := &store.Unfinished{Part: part, Round: r.PlanRound}
	if was := r.Unfinished; was != nil && was.Round == r.PlanRound {
		u.Proofs, u.Shots = was.Proofs, was.Shots
	}
	if lv := d.running(r.ID); lv != nil {
		lv.mu.Lock()
		u.Proofs, u.Shots = append([]store.Proof(nil), lv.run.Proofs...), append([]store.Shot(nil), lv.run.Shots...)
		lv.mu.Unlock()
	}
	if dirty := d.uncommitted(ctx, r, dir, log); len(dirty) > 0 {
		left = append(left, "Changes in the clone were never committed, so they are not kept: "+clip(strings.Join(dirty, ", "), 300))
	}
	kept := false
	if head != "" {
		count, err := d.wsGit(ctx, r, dir, log, "rev-list", "--count", "refs/remotes/origin/main.."+head)
		if err == nil && strings.TrimSpace(count.Output) != "0" {
			if err := d.takeCommits(ctx, r, log, head); err != nil {
				return fmt.Errorf("keep the unfinished work: %w", err)
			}
			d.commitSteps(ctx, r, log, "refs/remotes/origin/main..main", false)
			u.Head, kept = head, true
		}
	}
	if !kept {
		left = append(left, "Nothing was committed, so the next attempt starts from main.")
	}
	u.Left = dedupe(left)
	r.Unfinished = u
	fmt.Fprintf(log, "the build is not finished (%s): %s\n", part, cause)
	for _, l := range u.Left {
		fmt.Fprintf(log, "  left: %s\n", l)
	}
	d.log.Info("build not finished; its work is kept on the request", "request", r.ID, "part", part, "left", len(u.Left), "kept", kept)
	d.record(ctx, r, fmt.Sprintf("the build is not finished: %s; %d thing(s) left", cause, len(u.Left)),
		annals.Step{Verb: "request.unfinished", Outcome: annals.Failed, Reason: "the build is not finished and proven, so it does not land", Labels: map[string]string{"part": part}})
	return &stopped{cause: "the build is not finished: " + cause, key: key, more: leftText(u),
		err: fmt.Errorf("the build is not finished: %s", cause)}
}

// uncommitted are the paths the agent's clone holds that no commit has,
// once build output is swept away.
func (d *Desk) uncommitted(ctx context.Context, r *store.Request, dir string, log io.Writer) []string {
	res, err := d.wsGit(ctx, r, dir, log, "status", "--porcelain")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		if l = strings.TrimSpace(l); len(l) > 3 {
			out = append(out, strings.TrimSpace(l[2:]))
		}
	}
	return out
}

// leftText is what is left of an unfinished build, for its run's comment.
func leftText(u *store.Unfinished) string {
	var b strings.Builder
	b.WriteString("#### What is left\n\n")
	for _, l := range u.Left {
		fmt.Fprintf(&b, "- %s\n", plain(l))
	}
	b.WriteString("\n")
	if u.Head != "" {
		fmt.Fprintf(&b, "The work so far is kept on this request, up to `%s`, and was not landed. A retry goes on from it, and its builder is given this list.\n\n", shortSHA(u.Head))
	}
	return b.String()
}

// continueBrief tells the builder of a retry what the attempt before left.
func continueBrief(u *store.Unfinished) string {
	if u == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Go on from the attempt before\n\nAn earlier attempt at this build stopped before it was finished.")
	if u.Head != "" {
		fmt.Fprintf(&b, " Its commits are in this clone, on top of main, up to %s: keep them, and go on from them.", shortSHA(u.Head))
	}
	b.WriteString(" What was left, as it said and as Graft found it:\n\n<<<\n")
	for _, l := range u.Left {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	b.WriteString(">>>\n\nFinish what is left and what the plan still needs, then say it is finished.\n")
	return b.String()
}

// waived is the approver's waiver of a criterion's proof for the request's
// round, or of its screenshots with store.WaiveShots, or nil.
func waived(r *store.Request, criterion int) *store.Waiver {
	for i := range r.Waived {
		if w := &r.Waived[i]; w.Round == r.PlanRound && w.Criterion == criterion {
			return w
		}
	}
	return nil
}

// evidence is what proves a build: each criterion's proof, and the
// screenshots that passed Graft's checks.
type evidence struct {
	proofs []store.Proof
	shots  []store.Shot
	none   string
	data   string
}

// gaps are what a build lacks before it may land: a criterion with no proof
// and no waiver; for a change people see, no screenshot after it and no
// waiver; and screenshots that do not say what data they show.
func gaps(r *store.Request, ev evidence, seen []string) []string {
	var out []string
	proved := map[int]bool{}
	for _, p := range ev.proofs {
		if len(p.Shots) > 0 || strings.TrimSpace(p.Test) != "" {
			proved[p.Criterion] = true
		}
	}
	for i, c := range r.Plan.Build.Acceptance {
		if !proved[i+1] && waived(r, i+1) == nil {
			out = append(out, fmt.Sprintf("Criterion %d (%s) has no proof: the test that proves it, or a screenshot that shows it.", i+1, clip(line(c), 120)))
		}
	}
	if len(seen) > 0 && waived(r, store.WaiveShots) == nil {
		after := 0
		for _, s := range ev.shots {
			if s.State == "after" {
				after++
			}
		}
		if after == 0 {
			gap := "The change touches what people see (" + clip(strings.Join(seen, ", "), 200) + "), and no screenshot after it passed Graft's checks."
			if strings.TrimSpace(ev.none) != "" {
				gap += " The agent said why: " + clip(line(ev.none), 200) + "."
			}
			out = append(out, gap)
		}
	}
	if len(ev.shots) > 0 && strings.TrimSpace(ev.data) == "" {
		out = append(out, "The screenshots do not say what data they show (`graft report data`): the repository's fixtures or demo data, never a live system's.")
	}
	return out
}

// uiExt are the files whose change people see.
var uiExt = map[string]bool{".tsx": true, ".jsx": true, ".vue": true, ".svelte": true, ".astro": true, ".html": true, ".htm": true,
	".css": true, ".scss": true, ".sass": true, ".less": true, ".razor": true, ".cshtml": true, ".xaml": true}

// seenChange is what the build changes that people see: the screens its plan
// names, or else the files it changed of the kinds that draw a screen, tests
// left out. Empty when people see nothing change, or when the program takes
// no screenshots.
func (d *Desk) seenChange(ctx context.Context, r *store.Request, dir, head string, log io.Writer) []string {
	if !d.prog.Ticket.ShotsOn() {
		return nil
	}
	if ui := r.Plan.Build.UI; ui != nil && len(ui.Screens) > 0 {
		return ui.Screens
	}
	base, err := d.gateBase(ctx, r, dir, log)
	if err != nil {
		return nil
	}
	res, err := d.wsGit(ctx, r, dir, log, "diff", "--name-only", "--no-renames", base, head)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		if p = strings.TrimSpace(p); p != "" && uiExt[strings.ToLower(path.Ext(p))] && !isTest(p) {
			out = append(out, p)
		}
	}
	return out
}

// prove sends the prover: one agent run, in a session of its own, with the
// prove budget, that proves each criterion still unproven and takes the
// screenshots people need, and changes nothing that lands. It says whether
// it ran to its end or was stopped at its budget or its time.
func (d *Desk) prove(ctx context.Context, r *store.Request, dir, head string, open []string, seen []string, log io.Writer) (stopped bool, err error) {
	budget := d.prog.Limits.Budget.Prove
	d.step(r, store.StepDid, fmt.Sprintf("The prover started: %s to prove.", count(len(open), "thing")), nil)
	s := runtime.Session{Dir: dir, Key: d.key(r, "prove"), Model: d.cfg.BuildModel, MaxTurns: d.cfg.BuildTurns, Timeout: d.cfg.BuildTimeout, Budget: budget,
		Prompt: d.proveBrief(r, open, seen) + d.reportBrief(r) + spends(budget)}
	fmt.Fprintln(log, "\n---- prover ----")
	wound, err := d.runAgent(ctx, r, agentPart{stage: store.SpendProve, round: r.PlanRound, try: 1}, s, proveWindDown, log)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	capped, timedOut := wound || errors.Is(err, runtime.ErrBudget), errors.Is(err, runtime.ErrTimeout)
	if err != nil && !capped && !timedOut {
		return false, fmt.Errorf("prover: %w", err)
	}
	if res, rerr := d.wsGit(ctx, r, dir, log, "rev-parse", "HEAD"); rerr == nil && strings.TrimSpace(res.Output) != head {
		// What lands is the builder's work as the gate passed it.
		fmt.Fprintf(log, "the prover moved HEAD; its commits are left out\n")
		d.step(r, store.StepDid, "The prover committed, which it may not: its commits are left out.", nil)
	}
	return capped || timedOut, nil
}

// proveBrief tells the prover what to prove, and how.
func (d *Desk) proveBrief(r *store.Request, open []string, seen []string) string {
	var b strings.Builder
	b.WriteString(`The build of the plan below is finished: its builder's commits are in this clone, and they passed Graft's gate. You prove it. Read AGENTS.md at the root (or CLAUDE.md where there is none) for how to run the application and its tests.

Change no tracked file and commit nothing: nothing you write lands. Never push, deploy, or reach a live system. If a criterion does not hold, do not fix it: say so, and stop.

    graft report left "criterion 2: the total is still blank after saving"

What is not proven yet:
<<<
`)
	for _, o := range open {
		fmt.Fprintf(&b, "- %s\n", o)
	}
	b.WriteString(">>>\n\n## Done when\n\n")
	for i, c := range r.Plan.Build.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	fmt.Fprintf(&b, "\nThe tests the plan names: %s\n\nFor each criterion not proven yet, run the test that proves it, or take a screenshot that shows it, and record which:\n\n    graft report proof 1 --test \"<the test's name>\"\n    graft report proof 2 --shot form-after.png\n", r.Plan.Build.Tests)
	if len(seen) > 0 {
		fmt.Fprintf(&b, "\n## Screenshots\n\nPeople see this change: %s.", strings.Join(seen, "; "))
		if ui := r.Plan.Build.UI; ui != nil && strings.TrimSpace(ui.Shots) != "" {
			fmt.Fprintf(&b, " How: %s", strings.TrimSpace(ui.Shots))
		}
		fmt.Fprintf(&b, `

Take a screenshot of each screen after the change, with the repository's own end-to-end tooling (its Playwright setup or its dev server) and its test fixtures or demo data, in this sandbox. Never point the app at production or any live environment, and never sign in to one. A before screenshot is welcome where it is cheap: run the base commit %s in a worktree beside this clone (`+"`git worktree add ../before %s`"+`), never in it. Keep each a PNG under 5 MB, at most 12 in all. Then say what data they show:

    graft report shot form-after.png --screen "Request form" --after "A unit picked beside the quantity"
    graft report data "the e2e fixture requests from e2e/fixtures/requests.json"

A shot is a file you save in %s; `+"`graft report shot`"+` copies it there from anywhere else. A change people see lands only with a screenshot after it.
`, shortSHA(r.Base), shortSHA(r.Base), filepath.Join(d.reportDir(r), ShotsDir))
	}
	b.WriteString(browsersBrief)
	return b.String()
}

// browsersBrief is what an agent is told of the browsers in its sandbox.
const browsersBrief = "\nGraft installed the browser the repository's own Playwright version wants, if it uses Playwright, under `$PLAYWRIGHT_BROWSERS_PATH`. Never link, copy, or rename a browser build to stand in for another; if a version is missing, install it with that Playwright's own `npx playwright install chromium`.\n"

// The build output a gate clears away, by the name of a folder it sits in,
// before it asks for a clean tree: what tests and tools leave untracked.
var litter = map[string]bool{"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, "TestResults": true,
	"test-results": true, "playwright-report": true, "blob-report": true, ".nyc_output": true, "coverage": true}

// litterOf says a path the clone does not track is build output, and the
// part of it to remove: the litter folder, or the file.
func litterOf(p string) (string, bool) {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	for i, part := range parts {
		if litter[part] {
			return strings.Join(parts[:i+1], "/") + "/", true
		}
	}
	if strings.HasSuffix(p, ".pyc") {
		return p, true
	}
	return "", false
}

// sweepLitter removes the build output the agent's tests left untracked in
// its clone, and says what it removed. It touches nothing tracked and
// nothing else untracked: an untracked file that is not output is the
// agent's to commit or delete.
func (d *Desk) sweepLitter(ctx context.Context, r *store.Request, dir string, log io.Writer) []string {
	res, err := d.wsGit(ctx, r, dir, log, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var gone []string
	for _, e := range strings.Split(res.Output, "\x00") {
		if !strings.HasPrefix(e, "?? ") {
			continue
		}
		if p, ok := litterOf(e[3:]); ok && !seen[p] {
			seen[p] = true
			gone = append(gone, p)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if _, err := d.wsGit(ctx, r, dir, log, append([]string{"clean", "-fdq", "--"}, gone...)...); err != nil {
		fmt.Fprintf(log, "build output not removed: %v\n", err)
		return nil
	}
	return gone
}

// checkCmd is the repository's own verify command: what its graft.yaml
// names, else `make check` where the Makefile has it, else none.
func (d *Desk) checkCmd(ctx context.Context, r *store.Request, dir string) []string {
	if repo := d.prog.Repository(r.Repo); repo != nil {
		if f := d.repoFile(ctx, repo); f != nil && len(f.Check) > 0 {
			return f.Check
		}
	}
	if d.hasMakeTarget(ctx, r, dir, "check") {
		return []string{"make", "check"}
	}
	return nil
}

// shown is a command as a person types it.
func shown(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"$") {
			a = strconv.Quote(a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// rulesConfig is the part of a release-damnit repository's
// release-please-config.json that says what a commit message must be.
type rulesConfig struct {
	Types      []string                   `json:"types"`
	SubjectMax int                        `json:"subject-max-length"`
	Packages   map[string]json.RawMessage `json:"packages"`
}

// checksBrief tells the builder the repository's commit rules and its own
// check, and to run both before it says the work is finished, so neither
// costs a round of the gate.
func (d *Desk) checksBrief(r *store.Request, check []string) string {
	var b strings.Builder
	b.WriteString("\n\n## Before you say it is finished\n\n")
	if landsWithReleaseDamnit(d.repoDir(r)) {
		rules := rulesConfig{Types: []string{"feat", "fix", "chore", "docs"}, SubjectMax: 50}
		if raw, err := os.ReadFile(filepath.Join(d.repoDir(r), "release-please-config.json")); err == nil && len(raw) < 1<<20 {
			var c rulesConfig
			if json.Unmarshal(raw, &c) == nil {
				if len(c.Types) > 0 {
					rules.Types = c.Types
				}
				if c.SubjectMax > 0 {
					rules.SubjectMax = c.SubjectMax
				}
				rules.Packages = c.Packages
			}
		}
		fmt.Fprintf(&b, "Commit messages here are checked by release-damnit. A message's first line is `type(scope): description`: the type is one of %s; the description starts with a lowercase letter, has no closing period, and is at most %d characters.", strings.Join(rules.Types, ", "), rules.SubjectMax)
		if len(rules.Packages) > 1 {
			b.WriteString(" The scope names the one package a commit changes, so a change to several packages is one commit per package.")
		}
		b.WriteString(" Its commit-msg hook checks each commit as you make it: never skip it. Run this, and reword every commit it names:\n\n    release-damnit check-commits origin/main..HEAD\n\n")
	} else {
		b.WriteString("Commit messages keep the rules this repository's AGENTS.md (or CLAUDE.md) gives.\n\n")
	}
	if len(check) > 0 {
		fmt.Fprintf(&b, "Run the repository's own check, and fix what it says:\n\n    %s\n\nGraft's gate runs it again on what you committed.\n\n", shown(check))
	} else {
		b.WriteString("Run the check this repository's AGENTS.md or CLAUDE.md says to run before a change is done, and fix what it says.\n\n")
	}
	b.WriteString("Leave the tree clean: commit what belongs, and delete what your tests and tools left behind that does not (`__pycache__`, `TestResults`, and the like). Graft clears that kind of output itself before its gate.\n")
	return b.String()
}

// playwrightVersion is a Playwright version Graft installs a browser for.
var playwrightVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// pinnedPlaywright is a node script that prints, as JSON, the Playwright
// versions the package.json files given on its command line pin: the
// lockfile's resolved version where there is one, else the exact version
// the manifest names. It only reads files.
const pinnedPlaywright = `const fs=require("fs"),p=require("path"),out=new Set();
for(const f of process.argv.slice(1)){try{const d=p.dirname(f),m=JSON.parse(fs.readFileSync(f,"utf8"));
const spec=Object.assign({},m.dependencies,m.devDependencies)["@playwright/test"]||Object.assign({},m.dependencies,m.devDependencies)["playwright"];
if(!spec)continue;let v="";try{const l=JSON.parse(fs.readFileSync(p.join(d,"package-lock.json"),"utf8")).packages||{};
v=((l["node_modules/@playwright/test"]||l["node_modules/playwright"]||{}).version)||""}catch(e){}
if(!v)v=String(spec).replace(/^[\^~=v]+/,"");out.add(v)}catch(e){}}
process.stdout.write(JSON.stringify([...out]))`

// installTime bounds the browser install of one Playwright version.
var installTime = 10 * time.Minute

// prepare readies the sandbox for the repository's own end-to-end tooling:
// for each Playwright version it pins, the browser that version wants, in
// the browsers folder the estate may keep across sandboxes, so a later
// sandbox finds it there. It never fails the build: what it could not do,
// the agent is told and may do itself.
func (d *Desk) prepare(ctx context.Context, r *store.Request, dir string, log io.Writer) {
	res, err := d.wsGit(ctx, r, dir, io.Discard, "ls-files", "-z", "--", "package.json", "**/package.json")
	if err != nil || strings.TrimSpace(res.Output) == "" {
		return
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(res.Output, "\x00"), "\x00") {
		if f != "" && !strings.Contains(f, "node_modules/") {
			files = append(files, f)
		}
	}
	if len(files) == 0 || len(files) > 200 {
		return
	}
	out, err := d.box(r).WS.Run(ctx, io.Discard, shell.Cmd{Dir: dir, Args: append([]string{"node", "-e", pinnedPlaywright}, files...)})
	if err != nil {
		fmt.Fprintf(log, "the Playwright versions were not read: %v\n", err)
		return
	}
	var versions []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Output)), &versions); err != nil {
		fmt.Fprintf(log, "the Playwright versions were not read: %v\n", err)
		return
	}
	for i, v := range versions {
		if i >= 3 {
			break
		}
		if !playwrightVersion.MatchString(v) {
			fmt.Fprintf(log, "Playwright %q is no version Graft installs a browser for\n", v)
			continue
		}
		run, cancel := context.WithTimeout(ctx, installTime)
		_, err := d.box(r).WS.Run(run, log, shell.Cmd{Dir: dir, Args: []string{"npx", "--yes", "playwright@" + v, "install", "chromium"}})
		cancel()
		if err != nil {
			d.log.Warn("the browser for the repository's Playwright was not installed", "request", r.ID, "playwright", v, "error", err)
			d.step(r, store.StepDid, "The browser Playwright "+v+" wants could not be installed; the agent is told to.", nil)
			continue
		}
		d.step(r, store.StepDid, "Installed the browser Playwright "+v+" wants.", nil)
	}
}

// Waiving: an approver lets a build land without a criterion's proof, or
// without its screenshots, by a comment on the ticket.

// WaiveCommand starts a comment that waives a proof: the criterion's number,
// or "screenshots", then why.
const WaiveCommand = "/graft waive"

var waiveForm = regexp.MustCompile(`(?is)^/graft\s+waive\s+(\d+|screenshots)\b\s*(.*)$`)

// readWaiver reads a waive command: the criterion it waives, or
// store.WaiveShots, and why. ok is false for any other comment.
func readWaiver(body string) (criterion int, why string, ok bool) {
	m := waiveForm.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return 0, "", false
	}
	why = clip(line(m[2]), 300)
	if strings.EqualFold(m[1], "screenshots") {
		return store.WaiveShots, why, true
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, why, true
}

// waive keeps an approver's waiver for the build of the request's round,
// and says on the ticket what it changed. It answers what was wrong with
// the waiver, for the ticket, or empty when it stands.
func (d *Desk) waive(r *store.Request, by, at string, criterion int, why string) string {
	if r.Plan == nil || r.Unfinished == nil {
		return "nothing on this request waits for a proof"
	}
	if criterion != store.WaiveShots && criterion > len(r.Plan.Build.Acceptance) {
		return fmt.Sprintf("the plan has %d criteria; there is no criterion %d", len(r.Plan.Build.Acceptance), criterion)
	}
	if strings.TrimSpace(why) == "" {
		return "a waiver says why: `" + WaiveCommand + " <number or screenshots> <why>`"
	}
	if waived(r, criterion) == nil {
		r.Waived = append(r.Waived, store.Waiver{Round: r.PlanRound, Criterion: criterion, By: by, Why: why, At: at})
	}
	return ""
}

// waiverWords names what a waiver waives.
func waiverWords(criterion int) string {
	if criterion == store.WaiveShots {
		return "the screenshots"
	}
	return fmt.Sprintf("criterion %d's proof", criterion)
}

// dedupe drops repeated and blank lines, keeping the first of each.
func dedupe(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

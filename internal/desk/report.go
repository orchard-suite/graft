package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
	"github.com/orchard-suite/graft/internal/sweep"
)

// An agent reports what it does in files Graft names, in a folder beside
// its clone and never inside it: progress as JSON lines, and screenshots
// with a manifest. It holds no forge credential: Graft reads the files
// through the workspace, as data, checks them, and writes the ticket.

// ReportDir is the agent's report folder, in the request's home beside the
// clone and the handle file.
const ReportDir = "report"

// The files in the report folder.
const (
	ProgressFile = "progress.jsonl"
	ShotsDir     = "shots"
	ManifestFile = "manifest.json"
)

// The most Graft reads of a report, and takes of its screenshots.
const (
	progressMax = 256 << 10
	// MaxShots, MaxShot, and MaxShotsTotal bound one run's screenshots.
	MaxShots      = 12
	MaxShot       = 5 << 20
	MaxShotsTotal = 25 << 20
)

// Entry is one line of an agent's progress.
type Entry struct {
	// Kind is step, found, test, or stuck.
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Detail string `json:"detail,omitempty"`
	// OK is a test's result.
	OK *bool `json:"ok,omitempty"`
}

// Manifest is what an agent says of its screenshots.
type Manifest struct {
	Shots []ManifestShot `json:"shots"`
	// Proofs say what proves each criterion of the plan, by its number.
	Proofs []store.Proof `json:"proofs"`
	// None says why there are no screenshots, where the plan changes what
	// people see and none could be taken.
	None string `json:"none,omitempty"`
	// Data is what data the screens show: the repository's fixtures or demo
	// data, by name.
	Data string `json:"data,omitempty"`
}

// ManifestShot is one screenshot as the agent names it.
type ManifestShot struct {
	File    string `json:"file"`
	Screen  string `json:"screen"`
	State   string `json:"state"`
	Caption string `json:"caption,omitempty"`
}

// agentReport is what Graft has read of an agent's progress so far.
type agentReport struct {
	dir  string
	ws   func() Workspace
	seen int
	bad  int
}

// reportDir is the request's report folder in its box.
func (d *Desk) reportDir(r *store.Request) string { return filepath.Join(d.home(r), ReportDir) }

// watchReport starts a new report folder for an agent run of the request's
// open run, and reads it while the agent runs.
func (d *Desk) watchReport(ctx context.Context, r *store.Request) error {
	lv := d.running(r.ID)
	dir := d.reportDir(r)
	ws := d.box(r).WS
	if err := ws.Remove(ctx, dir); err != nil {
		return err
	}
	if _, err := ws.Run(ctx, io.Discard, cmdMkdir(d.home(r), filepath.Join(dir, ShotsDir))); err != nil {
		return err
	}
	if lv == nil {
		return nil
	}
	lv.mu.Lock()
	lv.report = &agentReport{dir: dir, ws: func() Workspace { return d.box(r).WS }}
	lv.mu.Unlock()
	return nil
}

// unwatchReport reads the report one last time once its agent has stopped.
func (d *Desk) unwatchReport(ctx context.Context, r *store.Request) {
	lv := d.running(r.ID)
	if lv == nil {
		return
	}
	d.readReport(ctx, lv)
	lv.mu.Lock()
	lv.report = nil
	lv.mu.Unlock()
}

// readReport takes the agent's new progress lines onto its run. A line that
// is not an entry is counted, never shown; an entry that looks like it holds
// a secret is shown without it.
func (d *Desk) readReport(ctx context.Context, lv *live) {
	lv.mu.Lock()
	rep := lv.report
	lv.mu.Unlock()
	if rep == nil {
		return
	}
	w := &capWriter{max: progressMax}
	if err := rep.ws().Get(ctx, filepath.Join(rep.dir, ProgressFile), w); err != nil {
		return
	}
	lines := strings.Split(w.buf.String(), "\n")
	if !strings.HasSuffix(w.buf.String(), "\n") {
		// The last line may be half written.
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= rep.seen {
		return
	}
	var steps []store.Step
	bad := 0
	for _, l := range lines[rep.seen:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		e, err := readEntry(l)
		if err != nil {
			bad++
			continue
		}
		steps = append(steps, store.Step{At: d.now(), By: store.ByAgent, Kind: e.Kind, Text: redacted(e.Text), Detail: redacted(e.Detail), OK: e.OK})
	}
	lv.mu.Lock()
	defer lv.mu.Unlock()
	rep.seen, rep.bad = len(lines), rep.bad+bad
	lv.run.Steps = append(lv.run.Steps, steps...)
	if bad > 0 {
		d.log.Info("agent progress lines not read", "request", lv.request, "lines", bad)
	}
	if len(steps) > 0 {
		lv.dirty = true
	}
}

// readEntry reads one progress line as strict JSON.
func readEntry(l string) (Entry, error) {
	var e Entry
	dec := json.NewDecoder(strings.NewReader(l))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, err
	}
	switch e.Kind {
	case store.StepDid, store.StepFound, store.StepTest, store.StepStuck:
	default:
		return e, fmt.Errorf("kind %q is not step, found, test, or stuck", e.Kind)
	}
	if strings.TrimSpace(e.Text) == "" {
		return e, errors.New("text is empty")
	}
	return e, nil
}

// readManifest reads the agent's manifest: missing says there is none.
func (d *Desk) readManifest(ctx context.Context, r *store.Request) (m Manifest, missing bool, err error) {
	p := filepath.Join(d.reportDir(r), ShotsDir, ManifestFile)
	if _, err := d.box(r).WS.Run(ctx, io.Discard, shell.Cmd{Dir: d.home(r), Args: []string{"test", "-f", p}}); err != nil {
		return m, true, nil
	}
	w := &capWriter{max: maxOut}
	if err := d.box(r).WS.Get(ctx, p, w); err != nil {
		return m, false, err
	}
	if w.over {
		return m, false, fmt.Errorf("%s is larger than %d bytes", ManifestFile, maxOut)
	}
	dec := json.NewDecoder(bytes.NewReader(w.buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, false, fmt.Errorf("%s is not the JSON asked for: %v", ManifestFile, err)
	}
	return m, false, nil
}

// gather reads what proves the build: the agent's manifest, each
// screenshot checked and, where the program shows them and the forge takes
// them, put on the forge for its run's comment by Graft with its own
// token. A screenshot passes when its bytes are a picture within the limits
// and OCR in the box finds no secret in it; one that does not is left out,
// and the comment says why. What an earlier attempt of this round already
// proved and showed is kept. Nothing here stops the run.
func (d *Desk) gather(ctx context.Context, r *store.Request, log io.Writer) evidence {
	var ev evidence
	if u := r.Unfinished; u != nil && u.Round == r.PlanRound && r.State == store.StateBuilding {
		ev.proofs, ev.shots = append(ev.proofs, u.Proofs...), append(ev.shots, u.Shots...)
	}
	lv := d.running(r.ID)
	m, missing, err := d.readManifest(ctx, r)
	var left []string
	switch {
	case missing:
	case err != nil:
		left = append(left, "the screenshots and proofs: "+err.Error())
	default:
		ev.none, ev.data = clip(line(redacted(m.None)), stepMax), clip(line(redacted(m.Data)), stepMax)
		var taken []store.Shot
		if d.prog.Ticket.ShotsOn() {
			taken, left = d.checkShots(ctx, r, m, log)
		}
		names := map[string]bool{}
		for _, s := range append(ev.shots, taken...) {
			names[s.Name] = true
		}
		ev.shots = append(ev.shots, taken...)
		criteria := 0
		if r.Plan != nil {
			criteria = len(r.Plan.Build.Acceptance)
		}
		for _, p := range m.Proofs {
			if p.Criterion < 1 || p.Criterion > criteria {
				continue
			}
			kept := store.Proof{Criterion: p.Criterion, Test: clip(line(redacted(p.Test)), 200)}
			for _, n := range p.Shots {
				if names[n] {
					kept.Shots = append(kept.Shots, n)
				}
			}
			if kept.Test != "" || len(kept.Shots) > 0 {
				ev.proofs = append(ev.proofs, kept)
			}
		}
	}
	if lv != nil {
		lv.change(func(run *store.Run) {
			run.Shots, run.Proofs = ev.shots, ev.proofs
			if ev.none != "" {
				run.NoShots = ev.none
			}
			if ev.data != "" {
				run.Data = ev.data
			}
			run.Left = append(run.Left, left...)
		})
	}
	return ev
}

// checkShots checks each screenshot the manifest names and puts those that
// pass on the forge. A forge that takes no file from Graft still has each
// checked: it passes, with no address to show it by.
func (d *Desk) checkShots(ctx context.Context, r *store.Request, m Manifest, log io.Writer) (taken []store.Shot, left []string) {
	total := 0
	dir := filepath.Join(d.reportDir(r), ShotsDir)
	check := filepath.Join(d.home(r), ".graft-shots")
	named := map[string]bool{}
	uploads := true
	for i, s := range m.Shots {
		name := s.File
		twice := named[name]
		named[name] = true
		switch {
		case twice:
			left = append(left, name+": it is named twice")
			continue
		case i >= MaxShots:
			left = append(left, fmt.Sprintf("%s: a run shows at most %d screenshots", name, MaxShots))
			continue
		case !shotName(name):
			left = append(left, fmt.Sprintf("%q is not a file name in the screenshots folder", name))
			continue
		case s.State != "before" && s.State != "after":
			left = append(left, fmt.Sprintf("%s: its state is before or after, not %q", name, s.State))
			continue
		case strings.TrimSpace(s.Screen) == "":
			left = append(left, name+": it names no screen")
			continue
		}
		w := &capWriter{max: MaxShot}
		if err := d.box(r).WS.Get(ctx, filepath.Join(dir, name), w); err != nil {
			left = append(left, name+": it is not in the screenshots folder")
			continue
		}
		data := w.buf.Bytes()
		kind, ok := attach.Kind(data)
		switch {
		case w.over:
			left = append(left, fmt.Sprintf("%s: it is over %d MB", name, MaxShot>>20))
			continue
		case !ok || kind == attach.PDF:
			left = append(left, name+": its bytes are not a PNG, JPEG, GIF, or WebP picture")
			continue
		case total+len(data) > MaxShotsTotal:
			left = append(left, fmt.Sprintf("%s: a run's screenshots come to at most %d MB together", name, MaxShotsTotal>>20))
			continue
		}
		if why, err := d.sweepShot(ctx, r, check, i, kind, data); err != nil {
			fmt.Fprintf(log, "a screenshot could not be swept: %v\n", err)
			left = append(left, name+": it could not be read for secrets")
			continue
		} else if why != "" {
			d.log.Warn("a screenshot was left out", "request", r.ID, "why", why)
			left = append(left, name+": "+why)
			continue
		}
		total += len(data)
		shot := store.Shot{Name: name, Screen: clip(line(redacted(s.Screen)), 120), State: s.State, Caption: clip(line(redacted(s.Caption)), stepMax)}
		if uploads {
			u, err := d.forge.Attach(ctx, r.Repo, fmt.Sprintf("%s-%s", r.ID, name), kind, data)
			switch {
			case errors.Is(err, forge.ErrNoUploads):
				uploads = false
				left = append(left, "the screenshots: this forge takes no file from Graft, so they are checked and not shown")
			case err != nil:
				d.log.Warn("a screenshot was not attached", "request", r.ID, "error", err)
				left = append(left, name+": the forge did not take it")
				continue
			default:
				shot.URL = u
			}
		}
		taken = append(taken, shot)
	}
	_ = d.box(r).WS.Remove(context.WithoutCancel(ctx), check) // silent-ok: the box and its files go when the run's sandbox shuts
	fmt.Fprintf(log, "screenshots: %d taken, %d left out\n", len(taken), len(left))
	d.log.Info("screenshots taken", "request", r.ID, "taken", len(taken), "left", len(left))
	return taken, left
}

// shotName says name is one file in the screenshots folder.
func shotName(name string) bool {
	return name != "" && name != ManifestFile && path.Base(name) == name && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// sweepShot reads a screenshot for text in the box, from the bytes Graft
// will attach, and says why it may not go on the ticket: it holds what looks
// like a secret, or it cannot be read, so nothing can say it holds none.
func (d *Desk) sweepShot(ctx context.Context, r *store.Request, dir string, i int, kind string, data []byte) (string, error) {
	p := filepath.Join(dir, fmt.Sprintf("%d%s", i+1, attach.Ext(kind)))
	if err := d.box(r).WS.Put(ctx, p, bytes.NewReader(data)); err != nil {
		return "", err
	}
	text, ok, err := d.readText(ctx, r, dir, "tesseract", p, "stdout")
	if err != nil {
		return "", err
	}
	if !ok {
		return "it could not be read for text, so nothing can say it shows no secret", nil
	}
	if kinds, _ := sweep.Scan(text + "\n" + squeezed(text)); len(kinds) > 0 {
		return fmt.Sprintf("it looked like it showed a secret (%s)", strings.Join(kinds, ", ")), nil
	}
	return "", nil
}

// reportBrief tells an agent how to report as it goes, and where.
func (d *Desk) reportBrief(r *store.Request) string {
	if !d.prog.Ticket.RunsOn() {
		return ""
	}
	dir := d.reportDir(r)
	return fmt.Sprintf(`
## Report as you go

Graft shows the people who decide and review this work what you do, as you do it, on the request's ticket. Report each step as you start it, each thing you find, each test you run and its result, and anything that stops you with what you tried, one line each:

    graft report step "Reading how the form saves a quantity"
    graft report found "The unit is dropped where the form is saved"
    graft report test "npm test -- request-form" --passed "12 tests"
    graft report stuck "The e2e server will not start: port 4200 is taken; tried --port 4300"

Each writes one line to %s, a JSON object {"kind": "step|found|test|stuck", "text": "...", "detail": "...", "ok": true|false}; you may write the lines yourself. Write plain words for a person: no secret, no address of a live system, no instruction to anyone.
`, filepath.Join(dir, ProgressFile))
}

// shotsBrief tells the builder of a plan that changes what people see to
// show each screen before it changes anything, and that a prover proves the
// finished work after it. A builder may record a proof as it goes.
func (d *Desk) shotsBrief(r *store.Request) string {
	if r.Plan == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Proof\n\nA prover follows you, with a budget of its own, and proves each criterion under **Done when** that you have not: by its test, or by a screenshot. Where you already ran the test that proves one, say so as you go, and it is not proved again:\n\n    graft report proof 1 --test \"<the test's name>\"\n")
	ui := r.Plan.Build.UI
	if ui == nil || !d.prog.Ticket.ShotsOn() {
		return b.String()
	}
	fmt.Fprintf(&b, "\nThe plan changes what people see: %s. Before you change anything, start the app with the repository's own tooling (%s) and its test fixtures or demo data, in this sandbox, never a live environment, and take a screenshot of each of those screens as it is now. The prover takes them after.\n\n", strings.Join(ui.Screens, "; "), strings.TrimSpace(ui.Shots))
	fmt.Fprintf(&b, "    graft report shot form-before.png --screen \"Request form\" --before \"The quantity field, with no unit\"\n    graft report data \"the e2e fixture requests from e2e/fixtures/requests.json\"\n\nA shot is a file you save in %s; `graft report shot` copies it there from anywhere else.\n", filepath.Join(d.reportDir(r), ShotsDir))
	b.WriteString(browsersBrief)
	return b.String()
}

// investigateShots tells the investigator it may show the problem.
func (d *Desk) investigateShots(r *store.Request) string {
	if !d.prog.Ticket.ShotsOn() {
		return ""
	}
	return fmt.Sprintf("\nWhere the request is about what a screen shows and the repository can run the app in this sandbox with its own end-to-end tooling and test or demo data, you may show the problem as it is now: save a PNG in %s and add it with `graft report shot <file> --screen \"<screen>\" --before \"<what it shows>\"`, then `graft report data \"<the fixtures it shows>\"`. Never point the app at a live environment.\n", filepath.Join(d.reportDir(r), ShotsDir))
}

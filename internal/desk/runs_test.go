package desk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/forge/gitlab"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
	"github.com/orchard-suite/graft/internal/workspace"
)

// uiPlan is a fix that changes what people see, shown by the repository's
// own end-to-end setup.
func uiPlan(paths ...string) *store.Plan {
	p := fixPlan(paths...)
	p.Build.UI = &store.UI{Screens: []string{"The orders page"}, Shots: "Run the e2e server with its fixture orders and screenshot the page."}
	return p
}

// report writes an agent's report beside its clone, as `graft report` does.
func report(t *testing.T, dir string, entries ...Entry) {
	t.Helper()
	p := filepath.Join(filepath.Dir(dir), ReportDir, ProgressFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		if _, err := f.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// screenshots writes pictures and their manifest beside the clone.
func screenshots(t *testing.T, dir string, m Manifest, files map[string][]byte) {
	t.Helper()
	shots := filepath.Join(filepath.Dir(dir), ReportDir, ShotsDir)
	for name, data := range files {
		write(t, shots, name, string(data))
	}
	raw, _ := json.Marshal(m)
	write(t, shots, ManifestFile, string(raw))
}

// buildsWithShots makes the builder take before and after screenshots of
// the orders page, prove the plan's criterion, and commit the fix.
func (fx *fixture) buildsWithShots() {
	fx.rt.build = func(dir, _ string) error {
		report(fx.t, dir, Entry{Kind: store.StepDid, Text: "Started the e2e server with the fixture orders"})
		write(fx.t, dir, "a.txt", "one\ntwo\n")
		write(fx.t, dir, "tests/two.sh", "grep -q two a.txt\n")
		run(fx.t, dir, "git", "add", "-A")
		run(fx.t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
		ok := true
		report(fx.t, dir, Entry{Kind: store.StepTest, Text: "tests/two.sh", OK: &ok})
		screenshots(fx.t, dir, Manifest{
			Shots: []ManifestShot{{File: "orders-before.png", Screen: "The orders page", State: "before", Caption: "Blank"},
				{File: "orders-after.png", Screen: "The orders page", State: "after", Caption: "Every order shows"}},
			Proofs: []store.Proof{{Criterion: 1, Shots: []string{"orders-after.png"}}},
			Data:   "the e2e fixture orders",
		}, map[string][]byte{"orders-before.png": shot, "orders-after.png": append([]byte("\x89PNG\r\n\x1a\n"), []byte(" every order")...)})
		return nil
	}
}

func TestEachRunKeepsOneCommentWrittenOnceThenEdited(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	fx.findings(defect(), uiPlan("a.txt", "tests/two.sh"))
	fx.buildsWithShots()
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased {
		t.Fatalf("state %s: %s", r.State, r.Error)
	}
	runs, err := fx.st.Runs(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, run := range runs {
		kinds = append(kinds, run.Kind)
		if run.Note == nil || run.Outcome != store.RunDone || run.Open() {
			t.Errorf("run %d (%s): note %+v outcome %q", run.N, run.Kind, run.Note, run.Outcome)
		}
	}
	if got := strings.Join(kinds, " "); got != "investigate build land release" {
		t.Fatalf("runs: %s", got)
	}
	// One comment a run, each written once and edited after, and no other.
	if len(fx.forge.comments) != len(runs) || fx.forge.edits == 0 {
		t.Fatalf("%d comments and %d edits for %d runs:\n%s", len(fx.forge.comments), fx.forge.edits, len(runs), strings.Join(fx.forge.comments, "\n----\n"))
	}
	investigation, build, landing, release := fx.forge.comments[0], fx.forge.comments[1], fx.forge.comments[2], fx.forge.comments[3]
	for comment, want := range map[string][]string{
		investigation: {"### Round 1 of the plan: the investigation", "**Done**", "Cloned main at", "Verdict: defect."},
		build: {"### The build, before anyone is asked", "#### Done when", "- [x] The file holds two lines. (screenshot: The orders page, after)",
			"#### Screenshots", "| Before | After |", "![orders-before.png](/uploads/0123456789abcdef0123456789abcdef/" + r.ID + "-orders-before.png)",
			"They show the e2e fixture orders, in the sandbox; never production.", "agent: Started the e2e server", "agent: passed: tests/two.sh",
			"passed: The gate, after builder round 1", "fix: add the second line"},
		landing: {"### Landing", "Landing on main with `git push origin HEAD:main`.", "fix: add the second line ([link](https://gitlab.example.com/acme/api/-/commit/"},
		release: {"### The release", "Watching every pipeline of", "passed: pipeline: success", "Released: https://gitlab.example.com/acme/api/-/commit/"},
	} {
		for _, w := range want {
			if !strings.Contains(comment, w) {
				t.Errorf("the comment lacks %q:\n%s", w, comment)
			}
		}
	}
	if len(fx.forge.attached) != 2 {
		t.Errorf("attached %d screenshots", len(fx.forge.attached))
	}
	if len(r.Runs) != 4 || r.Runs[3].URL == "" || r.Runs[3].Outcome != store.RunDone {
		t.Errorf("the request lists its runs: %+v", r.Runs)
	}
	if rec := fx.hub.records[r.ID]; !strings.Contains(rec.Runs, "- [The release](https://gitlab.example.com/acme/api/-/issues/1#note_") || strings.Count(rec.Runs, "\n") != 4 {
		t.Errorf("the catalog lists the runs:\n%s", rec.Runs)
	}
}

func TestARunsCommentIsEditedAtMostOnceAWindowAndAtOnceAtItsEdges(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	fx.d.now = func() time.Time { return clock }
	fx.forge.CreateIssue(ctx, "acme/api", "t", "b")
	lv := &live{request: "r1", repo: "acme/api", ticket: 1, run: store.Run{N: 1, Kind: store.RunBuild, Round: 1, Started: clock}, stop: make(chan struct{})}
	at := func(d time.Duration, force bool) bool {
		clock = clock.Add(d)
		return fx.d.flushLive(ctx, lv, force)
	}
	step := func(text string) { lv.add(store.Step{At: clock, By: store.ByAgent, Kind: store.StepDid, Text: text}) }

	// The ticker never makes a comment: only the worker does, forced.
	step("one")
	if at(0, false) || len(fx.forge.comments) != 0 {
		t.Fatalf("the ticker wrote a comment: %v", fx.forge.comments)
	}
	if !at(0, true) || len(fx.forge.comments) != 1 || fx.forge.edits != 0 {
		t.Fatalf("the run's start: %d comments, %d edits", len(fx.forge.comments), fx.forge.edits)
	}
	// Steps inside the window wait for it.
	for i := 0; i < 5; i++ {
		step("more")
		at(5*time.Second, false)
	}
	if fx.forge.edits != 0 {
		t.Fatalf("%d edits inside the window", fx.forge.edits)
	}
	if !at(6*time.Second, false) || fx.forge.edits != 1 || !strings.Contains(fx.forge.comments[0], "Steps (6)") {
		t.Fatalf("the window passed: %d edits:\n%s", fx.forge.edits, fx.forge.comments[0])
	}
	// Nothing waits: no edit, however long.
	if at(time.Hour, false); fx.forge.edits != 1 {
		t.Errorf("an edit with nothing to say: %d", fx.forge.edits)
	}
	// A run's end, or its failure, is written at once.
	step("done")
	if !at(time.Second, true) || fx.forge.edits != 2 {
		t.Errorf("a forced write waited: %d edits", fx.forge.edits)
	}
	if runs, _ := fx.st.Runs(ctx, "r1"); len(runs) != 1 || len(runs[0].Steps) != 7 || runs[0].Note == nil {
		t.Errorf("the run is kept as written: %+v", runs)
	}
}

// approvedFeature is a feature whose round 1 its approver approved, and that
// now builds.
func approvedFeature(t *testing.T) (*fixture, *store.Request) {
	t.Helper()
	fx, r := waitingFeature(t)
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	if err := fx.d.Poll(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return fx, fx.reread(r)
}

func TestAFailedBuildIsWrittenIntoItsRunsCommentAndItsRetryIsToldWhatItHit(t *testing.T) {
	fx, r := approvedFeature(t)
	fx.rt.build = func(dir, _ string) error {
		failed := false
		report(t, dir, Entry{Kind: store.StepDid, Text: "Starting the e2e server"}, Entry{Kind: store.StepTest, Text: "npm run e2e", OK: &failed},
			Entry{Kind: store.StepStuck, Text: "The e2e server will not start: port 4200 is taken; tried --port 4300"})
		return nil
	}
	before := len(fx.forge.comments)
	r = fx.work(r)
	if r.State != store.StateStalled {
		t.Fatalf("state %s", r.State)
	}
	// The failure is the build run's own comment, written once and edited:
	// no comment of its own.
	if len(fx.forge.comments) != before+1 {
		t.Fatalf("%d comments for one run:\n%s", len(fx.forge.comments)-before, strings.Join(fx.forge.comments[before:], "\n----\n"))
	}
	said := fx.lastComment()
	for _, want := range []string{"### The build of round 1", "**Stopped**", "#### Why it stopped", "The build is not finished: the build did not pass the gate after 2 rounds.",
		"#### What is left", "Nothing was committed, so the next attempt starts from main.",
		"- **Tried:** npm run e2e (failed)", "- **Its agent said:** The e2e server will not start: port 4200 is taken; tried --port 4300",
		"failed: The gate, after builder round 2", "**Graft stopped while building.**", "`/graft retry`"} {
		if !strings.Contains(said, want) {
			t.Errorf("the run's comment lacks %q:\n%s", want, said)
		}
	}
	runs, _ := fx.st.Runs(context.Background(), r.ID)
	failed := runs[len(runs)-1]
	if failed.Outcome != store.RunFailed || failed.Failure == nil || len(failed.Failure.Stuck) != 1 {
		t.Fatalf("the failed run: %+v", failed)
	}

	// The retry is told what the attempt before hit, gets its rounds back,
	// and its comment links the attempt it retries.
	var prompts []string
	tries := 0
	fx.rt.build = func(dir, prompt string) error {
		prompts = append(prompts, prompt)
		if tries++; tries == 1 {
			return nil
		}
		write(t, dir, "a.txt", "one\ntwo\n")
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "feat: add the second line")
		return nil
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateReleased {
		t.Fatalf("the retry: state %s %s", r.State, r.Error)
	}
	if tries != 2 {
		t.Errorf("the retry had %d builder rounds; it gets them back", tries)
	}
	for _, want := range []string{"## What earlier attempts of this stage hit", "stopped: The build is not finished: the build did not pass the gate after 2 rounds", "Its agent said: The e2e server will not start: port 4200 is taken",
		"## Go on from the attempt before", "The gate: No commit since"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("the retry's builder is not told %q", want)
		}
	}
	var retried string
	for _, c := range fx.forge.comments {
		if strings.HasPrefix(c, "### The build of round 1 · attempt 2") {
			retried = c
		}
	}
	for _, want := range []string{"Tries again [the attempt before](" + failed.Note.URL + "), as lead@example.com asked on the page.", "It stopped: The build is not finished: the build did not pass the gate after 2 rounds", "<summary>What the attempt before hit</summary>"} {
		if !strings.Contains(retried, want) {
			t.Errorf("the retry's comment lacks %q:\n%s", want, retried)
		}
	}
}

func TestAScreenshotThatDoesNotPassIsLeftOffTheTicket(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	fx.findings(defect(), uiPlan("a.txt", "tests/two.sh"))
	png := func(s string) []byte { return append([]byte("\x89PNG\r\n\x1a\n"), []byte(s)...) }
	big := append(png("big"), make([]byte, MaxShot)...)
	fx.rt.build = func(dir, _ string) error {
		write(t, dir, "a.txt", "one\ntwo\n")
		write(t, dir, "tests/two.sh", "grep -q two a.txt\n")
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
		shots := []ManifestShot{{File: "good.png", Screen: "The orders page", State: "after"},
			{File: "page.svg", Screen: "The orders page", State: "after"},
			{File: "big.png", Screen: "The orders page", State: "after"},
			{File: "token.png", Screen: "The orders page", State: "after"},
			{File: "blur.png", Screen: "The orders page", State: "after"},
			{File: "../../escape.png", Screen: "The orders page", State: "after"},
			{File: "missing.png", Screen: "The orders page", State: "after"},
			{File: "odd.png", Screen: "The orders page", State: "during"}}
		shots = append(shots, ManifestShot{File: "good.png", Screen: "Again", State: "after"})
		files := map[string][]byte{"good.png": png("every order"), "page.svg": []byte("<svg/>"), "big.png": big, "token.png": png("glpat-" + strings.Repeat("e", 24)),
			"blur.png": png("unreadable"), "odd.png": png("odd")}
		for i := 0; i < MaxShots; i++ {
			name := fmt.Sprintf("more-%d.png", i)
			shots = append(shots, ManifestShot{File: name, Screen: "More", State: "after"})
			files[name] = png("more")
		}
		screenshots(t, dir, Manifest{Shots: shots, Proofs: []store.Proof{{Criterion: 1, Shots: []string{"good.png", "token.png"}}, {Criterion: 9, Test: "no-such-criterion-test"}}, Data: "the fixture orders"}, files)
		write(t, filepath.Dir(dir), "escape.png", string(png("outside")))
		return nil
	}
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased {
		t.Fatalf("state %s: %s", r.State, r.Error)
	}
	if len(fx.forge.attached) != 4 || fx.forge.attached[r.ID+"-good.png"] == nil || fx.forge.attached[r.ID+"-more-2.png"] == nil {
		var names []string
		for n := range fx.forge.attached {
			names = append(names, n)
		}
		t.Fatalf("attached %v", names)
	}
	build := fx.forge.comments[1]
	for _, want := range []string{"page.svg: its bytes are not a PNG, JPEG, GIF, or WebP picture", "big.png: it is over 5 MB",
		"token.png: it looked like it showed a secret (gitlab-token)", "blur.png: it could not be read for text", "&quot;../../escape.png&quot; is not a file name",
		"missing.png: it is not in the screenshots folder", "odd.png: its state is before or after", "good.png: it is named twice", "more-3.png: a run shows at most 12 screenshots",
		"- [x] The file holds two lines. (screenshot: The orders page, after)"} {
		if !strings.Contains(strings.ReplaceAll(build, `"`, "&quot;"), want) {
			t.Errorf("the comment lacks %q:\n%s", want, build)
		}
	}
	if strings.Contains(build, "glpat-") || strings.Contains(build, "no-such-criterion-test") {
		t.Errorf("the comment shows what it left out:\n%s", build)
	}
}

// A change people see lands only with a screenshot after it, or with an
// approver's waiver; what was already proven is kept, and a retry after a
// waiver lands the kept work without building it again.
func TestAChangePeopleSeeLandsOnlyWithAScreenshotAfterItOrAWaiver(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	fx.forge.emails = map[string]string{"lead": "lead@example.com"}
	fx.findings(defect(), uiPlan("a.txt", "tests/two.sh"))
	var builds, proves []string
	fx.rt.build = func(dir, prompt string) error {
		builds = append(builds, prompt)
		write(t, dir, "a.txt", "one\ntwo\n")
		write(t, dir, "tests/two.sh", "grep -q two a.txt\n")
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
		screenshots(t, dir, Manifest{Proofs: []store.Proof{{Criterion: 1, Test: "tests/two.sh"}}}, nil)
		return nil
	}
	fx.rt.prove = func(dir, prompt string) error {
		proves = append(proves, prompt)
		screenshots(t, dir, Manifest{None: "the page needs a live sign-in to show", Proofs: []store.Proof{{Criterion: 1, Test: "tests/two.sh"}}}, nil)
		return nil
	}
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateStalled || r.Unfinished == nil || r.Unfinished.Part != store.PartProve || len(builds) != 1 || len(proves) != 1 {
		t.Fatalf("state %s, unfinished %+v, %d builds, %d provers: %s", r.State, r.Unfinished, len(builds), len(proves), r.Error)
	}
	if got := fx.remoteFile("a.txt"); got != "one\n" {
		t.Fatalf("an unproven change landed: %q", got)
	}
	for _, want := range []string{"## Proof", "The plan changes what people see: The orders page.", "Before you change anything", "Never link, copy, or rename a browser build"} {
		if !strings.Contains(builds[0], want) {
			t.Errorf("the builder is not told %q", want)
		}
	}
	for _, want := range []string{"You prove it.", "Change no tracked file and commit nothing", "People see this change: The orders page.", "How: Run the e2e server", "Never point the app at production", "## Report as you go"} {
		if !strings.Contains(proves[0], want) {
			t.Errorf("the prover is not told %q", want)
		}
	}
	said := fx.lastComment()
	for _, want := range []string{"#### What is left", "no screenshot after it passed Graft's checks", "The agent said why: the page needs a live sign-in to show", "(test: `tests/two.sh`)", "`/graft waive"} {
		if !strings.Contains(said, want) {
			t.Errorf("the comment lacks %q:\n%s", want, said)
		}
	}
	// A waiver from the person who asked counts for nothing; an approver's
	// stands, and the retry lands the kept work, proven, building nothing.
	fx.forge.emails["asker"] = "a@example.com"
	fx.forge.say(r.Ticket.Number, "asker", "/graft waive screenshots I trust it")
	if r = fx.listen(r); len(r.Waived) != 0 {
		t.Fatalf("the asker waived: %+v", r.Waived)
	}
	fx.forge.say(r.Ticket.Number, "lead", "/graft waive screenshots the page needs a live sign-in; the test shows it")
	fx.forge.say(r.Ticket.Number, "lead", "/graft retry")
	r = fx.listen(r)
	if len(r.Waived) != 1 || r.Waived[0].Criterion != store.WaiveShots || r.Waived[0].By != "lead@example.com" {
		t.Fatalf("waived: %+v", r.Waived)
	}
	r = fx.work(r)
	if r.State != store.StateReleased || fx.remoteFile("a.txt") != "one\ntwo\n" {
		t.Fatalf("after the waiver: state %s: %s", r.State, r.Error)
	}
	if len(builds) != 1 || len(proves) != 1 {
		t.Errorf("the waived retry built %d and proved %d times; the kept work needs neither again", len(builds), len(proves))
	}
	var build string
	for _, c := range fx.forge.comments {
		if strings.HasPrefix(c, "### The build") && strings.Contains(firstLine(c), "attempt 2") {
			build = c
		}
	}
	if !strings.Contains(build, "None: waived by lead@example.com") || !strings.Contains(build, "(test: `tests/two.sh`)") {
		t.Errorf("the retry's comment:\n%s", build)
	}
}

func TestAProgramThatTurnsRunsOffWritesTheCommentsItDidBefore(t *testing.T) {
	fx := newFixtureOf(t, roundsProgram+"ticket: { runs: false }\n")
	fx.rt.investigate = func(string, string) error { return io.ErrUnexpectedEOF }
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateStalled || len(fx.forge.comments) != 1 || fx.forge.edits != 0 || len(r.Runs) != 0 {
		t.Fatalf("state %s, %d comments, %d edits, runs %+v", r.State, len(fx.forge.comments), fx.forge.edits, r.Runs)
	}
	if !strings.HasPrefix(fx.lastComment(), "**Graft stopped while investigating.**") {
		t.Errorf("the stall's own comment: %s", fx.lastComment())
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	if fx.lastComment() != "Trying again, as lead@example.com asked: investigating." {
		t.Errorf("the retry's own comment: %s", fx.lastComment())
	}
}

// gitlabSeen is a GitLab that answers what Graft asks of a request's
// ticket, and keeps every call with the token it came with.
type gitlabSeen struct {
	mu    sync.Mutex
	calls []string
	token string
}

func (g *gitlabSeen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path+" "+map[bool]string{true: "with the token", false: "WITHOUT THE TOKEN"}[r.Header.Get("PRIVATE-TOKEN") == g.token])
	g.mu.Unlock()
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/acme%2Fapi")
	switch {
	case r.Method == http.MethodPost && p == "/issues":
		_, _ = w.Write([]byte(`{"iid": 1, "web_url": "https://gitlab.example.com/acme/api/-/issues/1"}`))
	case r.Method == http.MethodPost && p == "/issues/1/notes":
		_, _ = w.Write([]byte(`{"id": 77}`))
	case r.Method == http.MethodPost && p == "/uploads":
		_, _ = w.Write([]byte(`{"url": "/uploads/0123456789abcdef0123456789abcdef/shot.png"}`))
	case r.Method == http.MethodGet && p == "/pipelines":
		_, _ = w.Write([]byte(`[]`))
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

// tokenless is the GitLab adapter, cloning from the test's own remote.
type tokenless struct {
	*gitlab.Forge
	remote string
}

func (f tokenless) CloneURL(string) string { return f.remote }

func TestTheAgentNeverSeesTheForgeToken(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	token := "glpat-" + strings.Repeat("k", 24)
	// The token lives on the runner's own disk, as GRAFT_FORGE_TOKEN_FILE.
	tokenFile := filepath.Join(fx.d.cfg.DataDir, "forge-token")
	write(t, fx.d.cfg.DataDir, "forge-token", token)
	gl := &gitlabSeen{token: token}
	srv := httptest.NewServer(gl)
	t.Cleanup(srv.Close)
	fg := tokenless{Forge: gitlab.New(srv.URL, func(context.Context, string) (string, error) {
		raw, err := os.ReadFile(tokenFile)
		return strings.TrimSpace(string(raw)), err
	}), remote: fx.remote}
	cfg := fx.d.cfg
	ws := httptest.NewServer((&workspace.Server{Root: cfg.WorkDir}).Handler())
	t.Cleanup(ws.Close)
	fx.d = New(cfg, Deps{Store: fx.st, Forge: fg, Sandboxes: &Shared{Workspace: workspace.NewClient(ws.URL), Runtime: fx.rt}, Hub: fx.hub, Incidents: fx.inc,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	// Whatever an agent can read: its prompt, every file it can reach, and
	// its environment.
	var seen []string
	look := func(dir, prompt string) {
		if strings.Contains(prompt, token) {
			seen = append(seen, "its prompt")
		}
		for _, kv := range os.Environ() {
			if strings.Contains(kv, token) {
				seen = append(seen, "its environment")
			}
		}
		_ = filepath.Walk(cfg.WorkDir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if raw, _ := os.ReadFile(p); strings.Contains(string(raw), token) {
					seen = append(seen, p)
				}
			}
			return nil
		})
	}
	fx.findings(defect(), uiPlan("a.txt", "tests/two.sh"))
	investigate := fx.rt.investigate
	fx.rt.investigate = func(dir, prompt string) error { look(dir, prompt); return investigate(dir, prompt) }
	fx.buildsWithShots()
	build := fx.rt.build
	fx.rt.build = func(dir, prompt string) error {
		look(dir, prompt)
		err := build(dir, prompt)
		look(dir, prompt)
		return err
	}
	r, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: "acme-web", Repository: "acme/api", Environment: program.Production, Lane: "developer", Text: "the orders page is blank"})
	if err != nil {
		t.Fatal(err)
	}
	if r = fx.work(r); r.State != store.StateReleased {
		t.Fatalf("state %s: %s", r.State, r.Error)
	}
	if len(seen) > 0 {
		t.Errorf("the agent could read the forge token in %v", seen)
	}
	// Graft made every write the ticket got, with the token.
	var notes, edits, uploads int
	for _, c := range gl.calls {
		if !strings.HasSuffix(c, "with the token") {
			t.Errorf("a call went to the forge without Graft's token: %s", c)
		}
		switch {
		case strings.HasPrefix(c, "POST ") && strings.Contains(c, "/issues/1/notes "):
			notes++
		case strings.HasPrefix(c, "PUT ") && strings.Contains(c, "/issues/1/notes/77 "):
			edits++
		case strings.Contains(c, "/uploads "):
			uploads++
		}
	}
	if notes != 4 || edits == 0 || uploads != 2 {
		t.Errorf("%d comments, %d edits, %d uploads:\n%s", notes, edits, uploads, strings.Join(gl.calls, "\n"))
	}
}

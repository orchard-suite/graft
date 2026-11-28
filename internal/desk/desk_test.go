package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/dispatch"
	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/config"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/store"
	"github.com/orchard-suite/graft/internal/sweep"
	"github.com/orchard-suite/graft/internal/workspace"
)

const testProgram = `
program: acme
forge: { kind: gitlab, url: https://gitlab.example.com }
committer: { email: graft@example.com }
owner: { group: platform }
approvers:
- { path: teams, values: [platform] }
repositories:
- path: acme/api
  entity: repository:acme-api
  protected: [infra/]
lanes:
- id: user
  outside: true
  fallback: true
- id: developer
  small_fix: true
  members: [{ path: teams, values: [builders] }]
- id: architect
  protected: true
  review: true
  approval: false
  members: [{ path: teams, values: [platform] }]
limits:
  small_fix_lines: 20
  plan_rounds: 2
runners:
  reader: { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
intake:
- app: acme-web
  repository: acme/api
  environment: production
- app: acme-portal
  repository: acme/api
  environment: production
  paths: [portal/]
  tag: portal-v{version}
`

// fakeForge keeps issues and pull requests in memory and clones from a
// bare repository on disk.
type fakeForge struct {
	mu       sync.Mutex
	remote   string
	issues   []string
	comments []string
	// bodies are each issue's description, as last written.
	bodies map[int]string
	// notes are the comments people left on the tickets, by issue number.
	notes    map[int][]forge.Comment
	emails   map[string]string
	closed   []int
	prs      []string
	pr       forge.PRState
	merged   []string
	enforced bool
	runs     func(sha string) []forge.Run
	// files are what the forge keeps for the repository, by ref; fetched
	// are the refs Graft read.
	files   map[string][]byte
	fetched []string
	// down says the forge takes no issue and no comment.
	down bool
	// edits counts Graft's edits of its comments; attached are the files
	// it gave the forge, by name.
	edits    int
	attached map[string][]byte
}

func (f *fakeForge) Kind() string           { return "gitlab" }
func (f *fakeForge) CloneURL(string) string { return f.remote }
func (f *fakeForge) CommitURL(repo, sha string) string {
	return "https://gitlab.example.com/" + repo + "/-/commit/" + sha
}
func (f *fakeForge) Credential(context.Context, io.Reader, io.Writer) error { return nil }
func (f *fakeForge) DeleteBranch(context.Context, string, string) error     { return nil }
func (f *fakeForge) ClosePR(context.Context, string, int, string) error     { return nil }
func (f *fakeForge) ReviewEnforced(context.Context, string) (bool, error)   { return f.enforced, nil }
func (f *fakeForge) FailedJobs(_ context.Context, _ string, run forge.Run) ([]forge.Job, error) {
	if run.Conclusion != "failure" {
		return nil, nil
	}
	return []forge.Job{{Name: "test", Stage: "test", URL: run.URL + "/jobs/test", Log: "ok 1\nFAIL TestOrders"}}, nil
}

func (f *fakeForge) CreateIssue(_ context.Context, _, title, body string) (forge.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return forge.Issue{}, errors.New("the forge is down")
	}
	f.issues = append(f.issues, title+"\n"+body)
	if f.bodies == nil {
		f.bodies = map[int]string{}
	}
	f.bodies[len(f.issues)] = body
	return forge.Issue{Number: len(f.issues), URL: fmt.Sprintf("https://gitlab.example.com/acme/api/-/issues/%d", len(f.issues))}, nil
}

func (f *fakeForge) UpdateIssue(_ context.Context, _ string, n int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies[n] = body
	return nil
}

// CommentIssue keeps Graft's comment, and puts it among the ticket's
// comments as its own.
func (f *fakeForge) CommentIssue(_ context.Context, _ string, n int, body string) (forge.Posted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return forge.Posted{}, errors.New("the forge is down")
	}
	f.comments = append(f.comments, body)
	if f.notes == nil {
		f.notes = map[int][]forge.Comment{}
	}
	id := int64(1000 + len(f.comments))
	f.notes[n] = append(f.notes[n], forge.Comment{ID: id, Body: body, Author: forge.User{ID: 1, Username: "project_1_bot", Mine: true}})
	return forge.Posted{ID: id, URL: fmt.Sprintf("https://gitlab.example.com/acme/api/-/issues/%d#note_%d", n, id)}, nil
}

// EditComment writes one of Graft's comments again, where it was.
func (f *fakeForge) EditComment(_ context.Context, _ string, n int, id int64, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("the forge is down")
	}
	f.edits++
	f.comments[id-1001] = body
	for i := range f.notes[n] {
		if f.notes[n][i].ID == id {
			f.notes[n][i].Body = body
		}
	}
	return nil
}

// Attach keeps a file Graft gives the forge, by its name.
func (f *fakeForge) Attach(_ context.Context, _, name, _ string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached == nil {
		f.attached = map[string][]byte{}
	}
	f.attached[name] = data
	return "/uploads/0123456789abcdef0123456789abcdef/" + name, nil
}

// say leaves a comment on a ticket, as a forge user.
func (f *fakeForge) say(n int, username, body string) {
	f.sayWith(n, username, body)
}

// sayWith leaves a comment that links to files the forge keeps.
func (f *fakeForge) sayWith(n int, username, body string, uploads ...forge.Upload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notes == nil {
		f.notes = map[int][]forge.Comment{}
	}
	people := 0
	for _, c := range f.notes[n] {
		if !c.Author.Mine {
			people++
		}
	}
	id := int64(2000 + people)
	f.notes[n] = append(f.notes[n], forge.Comment{ID: id, Body: body, At: "2026-01-02T03:04:05Z", URL: fmt.Sprintf("https://gitlab.example.com/acme/api/-/issues/%d#note_%d", n, id), Author: forge.User{ID: id, Username: username}, Uploads: uploads})
}

// Upload reads a file the forge keeps, at most max bytes of it. A GitHub
// attachment cannot be read.
func (f *fakeForge) Upload(_ context.Context, _, ref string, max int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, ref)
	if strings.HasPrefix(ref, "https://github.com/") {
		return nil, forge.ErrNoUploads
	}
	data, ok := f.files[ref]
	if !ok {
		return nil, &forge.Error{Method: "GET", Path: "/uploads/" + ref, Status: 404, Body: "not found"}
	}
	if int64(len(data)) > max {
		data = data[:max]
	}
	return data, nil
}

func (f *fakeForge) Comments(_ context.Context, _ string, n int, after int64) ([]forge.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []forge.Comment
	for _, c := range f.notes[n] {
		if c.ID > after {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeForge) Email(_ context.Context, _ string, u forge.User) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emails[u.Username], nil
}

func (f *fakeForge) CloseIssue(_ context.Context, _ string, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, n)
	return nil
}

func (f *fakeForge) OpenPR(_ context.Context, _, head, _, title, _ string) (forge.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = append(f.prs, head+" "+title)
	return forge.PR{Number: len(f.prs), URL: fmt.Sprintf("https://gitlab.example.com/acme/api/-/merge_requests/%d", len(f.prs))}, nil
}

func (f *fakeForge) PR(context.Context, string, int) (forge.PRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pr, nil
}

func (f *fakeForge) MergePR(_ context.Context, _ string, _ int, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merged = append(f.merged, sha)
	return nil
}

func (f *fakeForge) Runs(_ context.Context, _, sha string) ([]forge.Run, error) {
	if f.runs != nil {
		return f.runs(sha), nil
	}
	return []forge.Run{{ID: "1", Name: "pipeline", Status: "completed", Conclusion: "success"}}, nil
}

// fakeRuntime runs a scripted function in place of an agent.
type fakeRuntime struct {
	mu          sync.Mutex
	screen      string
	investigate func(dir, prompt string) error
	build       func(dir, prompt string) error
	// prove is the prover; nil proves every criterion by the plan's tests.
	prove func(dir, prompt string) error
	// unsaid keeps the builder from saying its work is finished; otherwise
	// a builder round that ends well says so, unless it said what is left.
	unsaid  bool
	prompts []string
	budgets map[string]float64
	// usage is what the runtime reports a prompt consumed, given how it
	// ended; nil reports nothing.
	usage func(s runtime.Session, err error) runtime.Usage
	// stop is what ending a session ends, given its key; nil ends nothing.
	stop func(key string)
}

func (f *fakeRuntime) Name() string { return "ion" }
func (f *fakeRuntime) Stop(_ context.Context, key string) error {
	if f.stop != nil {
		f.stop(key)
	}
	return nil
}
func (f *fakeRuntime) Ask(_ context.Context, s runtime.Session) (string, runtime.Usage, error) {
	return f.screen, f.used(s, nil), nil
}

func (f *fakeRuntime) Run(_ context.Context, s runtime.Session, _ io.Writer) (runtime.Usage, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, s.Key+"\n"+s.Prompt)
	if f.budgets == nil {
		f.budgets = map[string]float64{}
	}
	f.budgets[s.Key] = s.Budget
	f.mu.Unlock()
	var err error
	switch {
	case strings.HasSuffix(s.Key, "-investigate"):
		err = f.investigate(s.Dir, s.Prompt)
	case strings.HasSuffix(s.Key, "-prove") && f.prove != nil:
		err = f.prove(s.Dir, s.Prompt)
	case strings.HasSuffix(s.Key, "-prove"):
		err = proveAll(s.Dir)
	default:
		err = f.build(s.Dir, s.Prompt)
		finish := filepath.Join(filepath.Dir(s.Dir), ReportDir, FinishFile)
		if _, statErr := os.Stat(finish); err == nil && !f.unsaid && os.IsNotExist(statErr) {
			err = sayFinish(s.Dir, Finish{Done: true, Summary: "built the plan"})
		}
	}
	return f.used(s, err), err
}

// sayFinish writes the agent's finish file, as `graft report done` and `left`
// do.
func sayFinish(dir string, f Finish) error {
	p := filepath.Join(filepath.Dir(dir), ReportDir, FinishFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(f)
	return os.WriteFile(p, raw, 0o644)
}

// proveAll is a prover that proves every criterion by the plan's tests,
// keeping what the manifest already holds.
func proveAll(dir string) error {
	p := filepath.Join(filepath.Dir(dir), ReportDir, ShotsDir, ManifestFile)
	var m Manifest
	if raw, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
	}
	for n := 1; n <= 10; n++ {
		m.Proofs = append(m.Proofs, store.Proof{Criterion: n, Test: "the plan's tests"})
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	return os.WriteFile(p, raw, 0o644)
}

func (f *fakeRuntime) used(s runtime.Session, err error) runtime.Usage {
	if f.usage == nil {
		return runtime.Usage{}
	}
	return f.usage(s, err)
}

// fakeHub holds lanes and the plan runs asked of it.
type fakeHub struct {
	mu     sync.Mutex
	lanes  []string
	asks   []hub.PlanAsk
	runs   map[string]*hub.PlanRun
	answer string
	// members are who the approvers' grants let in, as the hub judges them.
	members map[string]bool
	// drops says the hub holds no run to the approvers asked for, as one
	// that does not know them would.
	drops   bool
	reports []orrery.ActionReport
	lends   []hub.Lend
	tool    func(action string) (*orrery.ToolAnswer, error)
	shown   map[string]string
	records map[string]hub.Request
	// askErr is what the hub answers a plan's ask with, while it is set.
	askErr error
}

func (h *fakeHub) Lanes(context.Context, string) ([]string, error) { return h.lanes, nil }
func (h *fakeHub) Withdraw(context.Context, string, string) error  { return nil }
func (h *fakeHub) RunURL(run string) string                        { return "https://hub.example.com/actions/runs/" + run }
func (h *fakeHub) Needs(context.Context, string, []string) ([]string, error) {
	return nil, nil
}

func (h *fakeHub) AskPlan(_ context.Context, ask hub.PlanAsk) (*hub.PlanRun, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.askErr != nil {
		return nil, h.askErr
	}
	// As the hub does: a run is revised once it was sent back or while it
	// still waits, and one that still waits is superseded by its next round.
	if ask.Revises != "" {
		old := h.runs[ask.Revises]
		if old == nil || (old.State != orrery.RunReturned && old.State != orrery.RunAwaiting) {
			return nil, fmt.Errorf("run %q was neither sent back nor still waits, so a new run cannot revise it", ask.Revises)
		}
		if old.State == orrery.RunAwaiting {
			old.State = orrery.RunCancelled
		}
	}
	h.asks = append(h.asks, ask)
	run := &hub.PlanRun{ActionRun: orrery.ActionRun{ID: fmt.Sprintf("run%d", len(h.asks)), State: h.answer}, RequestedFor: ask.Person}
	// As the hub does: a run asked for a person that names approvers waits
	// for one of them. One that does not know approvers holds it to none.
	if !h.drops {
		run.Approvers = ask.Approvers
		if len(ask.Approvers) > 0 {
			run.State = orrery.RunAwaiting
		}
	}
	if h.runs == nil {
		h.runs = map[string]*hub.PlanRun{}
	}
	h.runs[run.ID] = run
	cp := *run
	return &cp, nil
}

func (h *fakeHub) LetsIn(_ context.Context, address string, _ []program.Grant) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.members[strings.ToLower(address)], nil
}

func (h *fakeHub) Admits(ctx context.Context, address string, grants []program.Grant) (bool, error) {
	return h.LetsIn(ctx, address, grants)
}

func (h *fakeHub) Run(_ context.Context, id string) (*hub.PlanRun, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := *h.runs[id]
	return &cp, nil
}

// waiting counts the runs that wait for a decision.
func (h *fakeHub) waiting() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, run := range h.runs {
		if run.State == orrery.RunAwaiting {
			n++
		}
	}
	return n
}

// decide sets what a run came to, as a person in the hub would.
func (h *fakeHub) decide(id, state, by, note string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs[id].State, h.runs[id].DecidedBy, h.runs[id].Note = state, by, note
	h.runs[id].DecidedAt = "2026-01-02T03:04:05Z"
}

func (h *fakeHub) Report(_ context.Context, id string, report orrery.ActionReport) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports = append(h.reports, report)
	if run := h.runs[id]; run != nil && report.State != "" {
		run.State = report.State
	}
	return nil
}

func (h *fakeHub) CallTool(_ context.Context, action string, _ map[string]any, l hub.Lend) (*orrery.ToolAnswer, error) {
	h.mu.Lock()
	h.lends = append(h.lends, l)
	h.mu.Unlock()
	return h.tool(action)
}

func (h *fakeHub) PutRequest(_ context.Context, r hub.Request) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shown == nil {
		h.shown = map[string]string{}
	}
	h.shown[r.ID] = r.State
	if h.records == nil {
		h.records = map[string]hub.Request{}
	}
	h.records[r.ID] = r
	return nil
}

func (h *fakeHub) PutRequests(ctx context.Context, rs []hub.Request) (int, error) {
	for _, r := range rs {
		if err := h.PutRequest(ctx, r); err != nil {
			return 0, err
		}
	}
	return len(rs), nil
}

func (h *fakeHub) SyncTools(_ context.Context, repo *program.Repository, file *program.RepoFile, _ []string) ([]string, error) {
	var out []string
	for _, t := range file.Tools {
		out = append(out, t.ID)
	}
	return out, nil
}

// fakeIncidents is an incident desk with a list of open incidents.
type fakeIncidents struct {
	mu      sync.Mutex
	open    []dispatch.Incident
	opened  []dispatch.NewIncident
	sent    []dispatch.Event
	changes []dispatch.Change
	oncall  []dispatch.OnCall
}

func (f *fakeIncidents) OpenIncidents(_ context.Context, service string) ([]dispatch.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dispatch.Incident
	for _, i := range f.open {
		if i.Service == service {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeIncidents) OpenIncident(_ context.Context, in dispatch.NewIncident) (*dispatch.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, in)
	inc := dispatch.Incident{ID: int64(100 + len(f.opened)), Title: in.Title, Severity: in.Severity, Service: in.Service, State: "open", OpenedAt: time.Now()}
	f.open = append(f.open, inc)
	return &inc, nil
}

func (f *fakeIncidents) Send(_ context.Context, ev dispatch.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, ev)
	return nil
}

func (f *fakeIncidents) RecordChange(_ context.Context, ch dispatch.Change) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, ch)
	return nil
}

func (f *fakeIncidents) OnCall(context.Context) ([]dispatch.OnCall, error) { return f.oncall, nil }

// kinds are the kinds of notice sent to a person or a group.
func (f *fakeIncidents) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ev := range f.sent {
		to := ev.To[0].Person + ev.To[0].Group
		out = append(out, ev.Kind+">"+to)
	}
	return out
}

type fixture struct {
	t      *testing.T
	d      *Desk
	st     *store.Store
	forge  *fakeForge
	rt     *fakeRuntime
	hub    *fakeHub
	inc    *fakeIncidents
	remote string
	seed   string
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixture is a remote with one commit on main and a desk over fakes. The
// repository's check runs every script under tests/.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOf(t, testProgram)
}

// newFixtureOf is newFixture over another program file.
func newFixtureOf(t *testing.T, programFile string) *fixture {
	t.Helper()
	saved, start := watchEvery, pipelineStart
	watchEvery, pipelineStart = 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { watchEvery, pipelineStart = saved, start })

	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, "", "git", "clone", "-q", remote, seed)
	write(t, seed, "a.txt", "one\n")
	write(t, seed, "infra/main.tf", "# infra\n")
	write(t, seed, "tests/one.sh", "grep -q one a.txt\n")
	write(t, seed, "Makefile", "check:\n\t@for t in tests/*.sh; do sh $$t || exit 1; done\n")
	write(t, seed, "graft.yaml", "runbooks: docs/runbooks/\ntools:\n- { id: queue-depth, tier: read, runs_as: reader, run: [echo, depth] }\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "chore: seed")
	run(t, seed, "git", "tag", "v1.2.3")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main", "v1.2.3")

	prog, err := program.Parse([]byte(programFile))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{Program: prog, WorkDir: t.TempDir(), RepoDir: t.TempDir(), DataDir: t.TempDir(), BuildRounds: 2, PipelineWait: time.Minute,
		HandleURL: "http://127.0.0.1:1", ScreenModel: "s", InvestigateModel: "i", BuildModel: "b", PublicURL: "https://graft.example.com"}
	srv := httptest.NewServer((&workspace.Server{Root: cfg.WorkDir}).Handler())
	t.Cleanup(srv.Close)
	fx := &fixture{t: t, st: st, remote: remote, seed: seed,
		forge: &fakeForge{remote: remote},
		rt:    &fakeRuntime{screen: `{"sort": "bug", "title": "Orders page is blank"}`},
		hub:   &fakeHub{lanes: []string{"user", "developer"}, answer: orrery.RunAwaiting, members: map[string]bool{"lead@example.com": true}},
		inc:   &fakeIncidents{},
	}
	fx.d = New(cfg, Deps{Store: st, Forge: fx.forge, Sandboxes: &Shared{Workspace: workspace.NewClient(srv.URL), Runtime: fx.rt}, Hub: fx.hub, Incidents: fx.inc,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return fx
}

// submit takes a request in as a person in a lane.
func (fx *fixture) submit(lane, text string) *store.Request {
	fx.t.Helper()
	return fx.submitFrom("acme-web", lane, text, "1.2.3")
}

// submitFrom sends a request through one intake app, from one version.
func (fx *fixture) submitFrom(app, lane, text, version string) *store.Request {
	fx.t.Helper()
	r, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: app, Repository: "acme/api", Environment: program.Production, Lane: lane, Text: text,
		Context: &store.Context{URL: "https://app.example.com/orders?token=abc#frag", Version: version}})
	if err != nil {
		fx.t.Fatal(err)
	}
	return r
}

// work runs the request until it waits or ends, and reads it back.
func (fx *fixture) work(r *store.Request) *store.Request {
	fx.t.Helper()
	fx.d.Run(context.Background(), r.ID)
	got, err := fx.st.Get(context.Background(), r.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	if got.State == store.StateFailed {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		fx.t.Fatalf("the request failed: %s\n%s", got.Error, log)
	}
	return got
}

// poll reads a waiting request again, then works it if that moved it.
func (fx *fixture) poll(r *store.Request) *store.Request {
	fx.t.Helper()
	if err := fx.d.Poll(context.Background(), r); err != nil {
		fx.t.Fatal(err)
	}
	return fx.work(r)
}

// findings makes the investigator write a verdict, with one piece of
// evidence added through its handle, and a plan when one is given. A round
// that answers its approvers says what changed.
func (fx *fixture) findings(verdict store.Verdict, plan *store.Plan) {
	fx.rt.investigate = func(dir, prompt string) error {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), HandleFile))
		if err != nil {
			return fmt.Errorf("the investigator has no handle: %w", err)
		}
		var h Handle
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		r, err := fx.d.ByHandle(context.Background(), h.Secret)
		if err != nil {
			return err
		}
		if _, err := fx.d.AddEvidence(context.Background(), r, "a.txt:1", "the file holds one line"); err != nil {
			return err
		}
		v, _ := json.Marshal(verdict)
		write(fx.t, dir, outDir+"/verdict.json", string(v))
		if plan != nil {
			round := *plan
			if strings.Contains(prompt, "which this round answers") {
				round.Brief.Changed = []string{"Answers the approvers' note."}
			}
			p, _ := json.Marshal(round)
			write(fx.t, dir, outDir+"/plan.json", string(p))
		}
		return nil
	}
}

func defect() store.Verdict {
	return store.Verdict{Verdict: store.VerdictDefect, Summary: "The file lacks a line.", Facts: []store.Fact{{Text: "a.txt has one line", Evidence: []int{1}}},
		Answer: "You found a real problem in the orders page.", Fixable: true}
}

func fixPlan(paths ...string) *store.Plan {
	p := &store.Plan{
		Brief: store.Brief{Asked: "The orders page is blank; they need to see their orders.", WillDo: "The orders page shows every order again.",
			Fit: "It restores what the page was built to do.", Risk: "Low: one line, and a test shows it.", Recommendation: store.Recommendation{Decision: store.Approve, Why: "A plain defect with a plain fix."}},
		Build: store.Build{Kind: store.KindFix, Understood: "The file needs a second line.", Steps: []string{"Add the second line."}, Acceptance: []string{"The file holds two lines."},
			Tests: "tests/two.sh fails without the fix", Size: store.Size{Lines: 2, Files: 1}, Tiers: []string{}},
	}
	for _, path := range paths {
		p.Build.Changes = append(p.Build.Changes, store.PlanChange{Path: path, What: "add the line"})
	}
	return p
}

// featurePlan is fixPlan for something new.
func featurePlan(paths ...string) *store.Plan {
	p := fixPlan(paths...)
	p.Build.Kind = store.KindFeature
	return p
}

// commits makes the builder write files and commit them. Graft names the
// ticket.
func (fx *fixture) commits(files map[string]string) {
	fx.rt.build = func(dir, _ string) error {
		for name, content := range files {
			write(fx.t, dir, name, content)
		}
		run(fx.t, dir, "git", "add", "-A")
		run(fx.t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
		return nil
	}
}

func (fx *fixture) remoteFile(name string) string {
	fx.t.Helper()
	out, err := exec.Command("git", "-C", fx.remote, "show", "main:"+name).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func TestJunkIsDeclinedBeforeAnyAgentReadsIt(t *testing.T) {
	fx := newFixture(t)
	fx.rt.screen = `Here you go: {"sort": "junk", "title": "asdf"}`
	r := fx.work(fx.submit("", "asdf asdf asdf asdf"))
	if r.State != store.StateDeclined || r.Answer != fixedJunk {
		t.Fatalf("state %s, answer %q", r.State, r.Answer)
	}
	if len(fx.rt.prompts) != 0 {
		t.Error("an agent ran for junk")
	}
	if got := strings.Join(fx.inc.kinds(), " "); got != KindAnswered+">a@example.com" {
		t.Errorf("told: %s", got)
	}
}

func TestAProgramThatHoldsNoticesSendsNoneAndKeepsEachOnItsRequest(t *testing.T) {
	fx := newFixtureOf(t, strings.Replace(testProgram, "program: acme", "program: acme\nnotices: hold", 1))
	secret := "ghp_" + strings.Repeat("a", 36)
	r := fx.work(fx.submit("", "it fails when I use my token "+secret+" on the orders page"))
	if r.State != store.StateQuarantined {
		t.Fatalf("state %s", r.State)
	}
	if len(fx.inc.sent) != 0 {
		t.Fatalf("a held notice went out: %+v", fx.inc.sent)
	}
	events, _ := fx.st.Events(context.Background(), r.ID)
	var held []string
	for _, e := range events {
		if strings.HasPrefix(e.Message, "notice held: ") {
			held = append(held, e.Message)
		}
	}
	if len(held) != 2 || !strings.HasPrefix(held[1], "notice held: "+KindAnswered+" to person a@example.com\nAbout your request: ") || !strings.Contains(held[1], fixedQuarantined) ||
		!strings.HasPrefix(held[0], "notice held: "+KindFiled+" to group platform\nNeeds a person: ") {
		t.Errorf("held notices: %q", held)
	}
}

func TestSubmitPicksTheHighestLaneAndStripsContext(t *testing.T) {
	fx := newFixture(t)
	fx.hub.lanes = []string{"developer", "architect"}
	r := fx.submit("", "the orders page is blank when I open it")
	if r.Lane != "architect" {
		t.Errorf("lane %s, want the highest held", r.Lane)
	}
	if r.Context.URL != "https://app.example.com/orders" || r.Version != "1.2.3" {
		t.Errorf("context: %+v", r.Context)
	}
	if r := fx.submit("developer", "please add an export button to orders"); r.Lane != "developer" {
		t.Errorf("a lower lane named and held: %s", r.Lane)
	}
	// An app is never refused for want of a lane: one it names that the person does not hold gives way.
	fx.hub.lanes = []string{"user"}
	if r := fx.submit("architect", "please add an export button"); r.Lane != "user" {
		t.Errorf("an app's request in a lane not held: %s", r.Lane)
	}
	// On Graft's own page a person may pick only a lane they hold.
	if _, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: program.PageApp, Repository: "acme/api", Lane: "architect", Text: "please add an export button"}); !errors.Is(err, ErrNoLane) {
		t.Errorf("the page took a lane the person does not hold: %v", err)
	}
	fx.hub.lanes = nil
	if _, err := fx.d.Submit(context.Background(), Submission{Person: "b@example.com", App: "acme-web", Repository: "acme/api", Text: "please add an export button"}); err != ErrNoLane {
		t.Errorf("a person with no lane: %v", err)
	}
}

func TestASecretIsQuarantined(t *testing.T) {
	fx := newFixture(t)
	secret := "ghp_" + strings.Repeat("a", 36)
	r := fx.work(fx.submit("", "it fails when I use my token "+secret+" on the orders page"))
	if r.State != store.StateQuarantined || strings.Contains(r.Text, secret) || r.Context != nil || len(r.Secrets) != 1 {
		t.Fatalf("request: %+v", r.Doc)
	}
	if len(fx.rt.prompts) != 0 {
		t.Error("an agent read a request that held a secret")
	}
	if got := strings.Join(fx.inc.kinds(), " "); got != KindFiled+">platform "+KindAnswered+">a@example.com" {
		t.Errorf("told: %s", got)
	}
}

func TestAnAnswerNeedsEvidence(t *testing.T) {
	fx := newFixture(t)
	v := store.Verdict{Verdict: store.VerdictUserError, Summary: "It works.", Facts: []store.Fact{{Text: "the page loads", Evidence: []int{7}}}, Answer: "Use the other button."}
	fx.findings(v, nil)
	fx.d.Run(context.Background(), fx.submit("", "the orders page is blank when I open it").ID)
	all, _ := fx.st.List(context.Background(), store.Filter{})
	if all[0].State != store.StateStalled || !strings.Contains(all[0].Error, "cites evidence 7") {
		t.Fatalf("a fact citing evidence that does not exist passed: %s %q", all[0].State, all[0].Error)
	}
	if n := len(fx.rt.prompts); n != 2 || !strings.Contains(fx.rt.prompts[1], "does not pass") {
		t.Errorf("the investigator gets one more try with the reason, got %d prompts", n)
	}
}

func TestUserErrorIsAnsweredInFixedWordsForAnOutsideReporterWhoReadProduction(t *testing.T) {
	fx := newFixture(t)
	v := store.Verdict{Verdict: store.VerdictUserError, Summary: "It works.", Facts: []store.Fact{{Text: "the page loads", Evidence: []int{1}}}, Answer: "The queue held 7 orders for customer 42."}
	fx.findings(v, nil)
	r := fx.submit("user", "the orders page is blank when I open it")
	if err := fx.st.MarkReadProduction(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateAnswered || r.Answer != fixedVerdict[store.VerdictUserError] {
		t.Fatalf("state %s, answer %q", r.State, r.Answer)
	}
	if !strings.Contains(fx.rt.prompts[0], "page: https://app.example.com/orders\n") || strings.Contains(fx.rt.prompts[0], "token=abc") {
		t.Errorf("the brief: %s", fx.rt.prompts[0])
	}
	// Two dollars, a fifth of it held back for writing what it found.
	if fx.rt.budgets["graft-"+r.ID+"-investigate"] != 1.6 {
		t.Errorf("investigate budget: %v", fx.rt.budgets)
	}
}

func TestASmallFixShipsWithNoPlanApproval(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt", "tests/two.sh"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n", "tests/two.sh": "grep -q two a.txt\n"})
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased || !r.Small {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s small %v why %q\n%s", r.State, r.Small, r.SmallWhy, log)
	}
	if len(fx.hub.asks) != 0 {
		t.Errorf("a small fix asked the hub: %+v", fx.hub.asks)
	}
	if got := fx.remoteFile("a.txt"); got != "one\ntwo\n" {
		t.Errorf("main holds %q", got)
	}
	if len(fx.forge.issues) != 1 || len(fx.forge.closed) != 1 {
		t.Errorf("a ticket exists before any change and is closed after: %d issues, %d closed", len(fx.forge.issues), len(fx.forge.closed))
	}
	if len(fx.inc.changes) != 1 || fx.inc.changes[0].Service != "acme-api" {
		t.Errorf("the release is recorded as a change: %+v", fx.inc.changes)
	}
	if got := strings.Join(fx.inc.kinds(), " "); got != KindShipped+">a@example.com" {
		t.Errorf("told: %s", got)
	}
	if at, by, _ := fx.st.LastRelease(context.Background(), "acme/api"); at.IsZero() || by != r.ID {
		t.Error("the release was not kept for the loop guard")
	}
	if _, err := os.Stat(fx.d.repoDir(r)); !os.IsNotExist(err) {
		t.Error("the runner's clone outlived the request")
	}
}

// TestACommandTheBuilderLeftRunningIsEndedBeforeTheGate pins that a
// builder's run ends with its session. A command it started in the
// background and left running, here a check still writing in its clone,
// is stopped before the gate reads the clone, and the builder is told so.
func TestACommandTheBuilderLeftRunningIsEndedBeforeTheGate(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt", "tests/two.sh", "tests/still.sh"))
	files := map[string]string{"a.txt": "one\ntwo\n", "tests/two.sh": "grep -q two a.txt\n",
		// The repository's check fails while the builder's command still runs.
		"tests/still.sh": "test ! -e .git/still-running\n"}
	var running string
	fx.rt.build = func(dir, prompt string) error {
		if running == "" {
			for name, content := range files {
				write(t, dir, name, content)
			}
			run(t, dir, "git", "add", "-A")
			run(t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
		}
		// Every round starts the check again and stops while it runs.
		if !strings.Contains(prompt, "never stop while a command you started is still running") {
			t.Error("the builder was not told its session ends when it stops")
		}
		running = filepath.Join(dir, ".git", "still-running")
		write(t, filepath.Dir(running), filepath.Base(running), "")
		return nil
	}
	fx.rt.stop = func(key string) {
		if strings.HasSuffix(key, "-build") && running != "" {
			os.Remove(running)
		}
	}
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s, want released: the gate ran beside the builder's command\n%s", r.State, log)
	}
}

func TestAFixWithNoNewTestAsksAboutThePlanThenLandsWhatWasBuilt(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n"})
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateAwaitingPlan || r.Small || r.SmallWhy != "it adds no test" || !r.Built {
		t.Fatalf("state %s small %v why %q built %v", r.State, r.Small, r.SmallWhy, r.Built)
	}
	if got := fx.remoteFile("a.txt"); got != "one\n" {
		t.Fatalf("something landed before the plan was approved: %q", got)
	}
	ask := fx.hub.asks[0]
	if ask.Person != "a@example.com" || ask.Lane != "developer" || ask.Entity != "repository:acme-api" || ask.Request != r.ID {
		t.Errorf("ask: %+v", ask)
	}
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateReleased || fx.remoteFile("a.txt") != "one\ntwo\n" {
		t.Fatalf("after approval: %s", r.State)
	}
	builds := 0
	for _, p := range fx.rt.prompts {
		if strings.HasSuffix(firstLine(p), "-build") {
			builds++
		}
	}
	if builds != 1 {
		t.Errorf("the built change lands as built, with no second build; the builder ran %d times", builds)
	}
	last := fx.hub.reports[len(fx.hub.reports)-1]
	if last.State != orrery.RunSucceeded {
		t.Errorf("the plan's run ends with the request: %+v", last)
	}
}

func TestAFeatureAsksFirstAndASentBackPlanIsPlannedAgain(t *testing.T) {
	fx := newFixture(t)
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, featurePlan("a.txt"))
	built := 0
	fx.rt.build = func(string, string) error { built++; return nil }
	r := fx.work(fx.submit("developer", "please add a second line to the orders file"))
	if r.State != store.StateAwaitingPlan || built != 0 || r.PlanRound != 1 {
		t.Fatalf("state %s, built %d, round %d", r.State, built, r.PlanRound)
	}
	first := r.Run
	fx.hub.decide(first, orrery.RunReturned, "lead@example.com", "also keep the old line")
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || fx.hub.asks[1].Revises != first {
		t.Fatalf("after a send back: %s round %d asks %+v", r.State, r.PlanRound, fx.hub.asks)
	}
	if !strings.Contains(fx.rt.prompts[1], "also keep the old line") {
		t.Error("the note did not reach the investigator")
	}
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "no, still wrong")
	r = fx.poll(r)
	if r.State != store.StateWaiting || r.Answer == "" || built != 0 {
		t.Fatalf("after the last round: %s", r.State)
	}
}

func TestADeclinedPlanEndsTheRequest(t *testing.T) {
	fx := newFixture(t)
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	r := fx.work(fx.submit("developer", "please add a second line to the orders file"))
	fx.hub.decide(r.Run, orrery.RunDeclined, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateDeclined || r.Answer == "" {
		t.Fatalf("state %s", r.State)
	}
}

func TestAnOpenIncidentHoldsTheRequestUntilItCloses(t *testing.T) {
	fx := newFixture(t)
	fx.inc.open = []dispatch.Incident{{ID: 42, Service: "acme-api", Severity: dispatch.Critical, State: "open", OpenedAt: time.Now()}}
	fx.findings(store.Verdict{Verdict: store.VerdictDesigned, Summary: "By design.", Facts: []store.Fact{{Text: "x", Evidence: []int{1}}}, Answer: "It works this way."}, nil)
	r := fx.work(fx.submit("", "the orders page is blank when I open it"))
	if r.State != store.StateHolding || r.Incident == nil || r.Incident.ID != "42" {
		t.Fatalf("state %s incident %+v", r.State, r.Incident)
	}
	if len(fx.rt.prompts) != 0 {
		t.Error("an agent investigated a known outage")
	}
	if sw, _ := fx.st.Switch(context.Background(), "acme/api"); !sw.Stopped || sw.Reason != "incident:42" {
		t.Errorf("a critical incident stops the repository: %+v", sw)
	}
	if got := strings.Join(fx.inc.kinds(), " "); got != KindAnswered+">a@example.com" {
		t.Errorf("told: %s", got)
	}
	r = fx.poll(r)
	if r.State != store.StateHolding {
		t.Fatalf("still open, yet state %s", r.State)
	}
	fx.inc.open = nil
	r = fx.poll(r)
	if r.State != store.StateAnswered {
		t.Fatalf("after it closed: %s", r.State)
	}
	if sw, _ := fx.st.Switch(context.Background(), "acme/api"); sw.Stopped {
		t.Error("the stop outlived its incident")
	}
}

func TestAnIncidentSoonAfterGraftsOwnReleaseGoesToAPerson(t *testing.T) {
	fx := newFixture(t)
	if err := fx.st.AddRelease(context.Background(), "acme/api", "earlier", "abc"); err != nil {
		t.Fatal(err)
	}
	fx.inc.open = []dispatch.Incident{{ID: 43, Service: "acme-api", Severity: dispatch.Warning, State: "open", OpenedAt: time.Now().Add(10 * time.Minute)}}
	r := fx.work(fx.submit("", "the orders page is blank when I open it"))
	if r.State != store.StateWaiting {
		t.Fatalf("state %s", r.State)
	}
	if sw, _ := fx.st.Switch(context.Background(), "acme/api"); !sw.Stopped || sw.Reason != "loop:43" {
		t.Errorf("switch: %+v", sw)
	}
}

func TestAnOutageOpensAnIncidentAndABlockedDefectOpensOneOnTheApp(t *testing.T) {
	fx := newFixture(t)
	fx.findings(store.Verdict{Verdict: store.VerdictOutage, Summary: "The queue is down.", Facts: []store.Fact{{Text: "x", Evidence: []int{1}}}, Answer: "We know.", System: "queue:orders"}, nil)
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateHolding || len(fx.inc.opened) != 1 || fx.inc.opened[0].Service != "orders" {
		t.Fatalf("state %s opened %+v", r.State, fx.inc.opened)
	}
	if len(fx.forge.issues) != 0 {
		t.Error("an outage is not a ticket on the repository")
	}

	fx2 := newFixture(t)
	v := defect()
	v.Fixable, v.Blocked = false, true
	fx2.findings(v, nil)
	r = fx2.work(fx2.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateHolding || len(fx2.inc.opened) != 1 || fx2.inc.opened[0].Service != "acme-api" || fx2.inc.opened[0].Severity != dispatch.Critical {
		t.Fatalf("state %s opened %+v", r.State, fx2.inc.opened)
	}

	fx3 := newFixture(t)
	v.Blocked = false
	fx3.findings(v, nil)
	r = fx3.work(fx3.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateFiled || len(fx3.inc.opened) != 0 || len(fx3.forge.issues) != 1 {
		t.Fatalf("a defect it cannot fix with nobody blocked is a ticket: %s", r.State)
	}
	if got := strings.Join(fx3.inc.kinds(), " "); got != KindFiled+">platform "+KindAnswered+">a@example.com" {
		t.Errorf("told: %s", got)
	}
}

func TestAProtectedPathNeedsALaneThatMayPassIt(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("infra/main.tf"))
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateFiled || len(fx.hub.asks) != 0 {
		t.Fatalf("state %s", r.State)
	}
	if len(fx.rt.prompts) != 1 {
		t.Error("a build ran for a plan the lane may not pass")
	}
}

func TestAnAppsRequestMayChangeCodeBeyondTheAppsPaths(t *testing.T) {
	fx := newFixture(t)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	v := defect()
	v.Verdict = store.VerdictFeature
	// The portal's request needs the API it calls too: a.txt lies outside portal/.
	fx.findings(v, fixPlan("portal/orders.ts", "a.txt"))
	fx.commits(map[string]string{"portal/orders.ts": "two\n", "a.txt": "one\ntwo\n"})
	r := fx.work(fx.submitFrom("acme-portal", "developer", "the orders page is blank when I open it", "1.2.3"))
	if r.State != store.StateAwaitingPlan || len(fx.hub.asks) != 1 {
		t.Fatalf("a plan past the app's paths is asked about, not filed: state %s answer %q", r.State, r.Answer)
	}
	if !strings.Contains(fx.rt.prompts[0], "built from `portal/`") || !strings.Contains(fx.rt.prompts[0], "may change anything else in this repository") {
		t.Errorf("the brief names the app's paths as where to start: %s", fx.rt.prompts[0])
	}
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	r = fx.poll(r)
	if fx.remoteFile("portal/orders.ts") != "two\n" || fx.remoteFile("a.txt") != "one\ntwo\n" {
		t.Fatalf("the change beyond the app's paths did not land: state %s", r.State)
	}
}

func TestAnAppsRequestIsStillHeldToTheProtectedPaths(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("portal/orders.ts", "infra/main.tf"))
	r := fx.work(fx.submitFrom("acme-portal", "developer", "the orders page is blank when I open it", "1.2.3"))
	if r.State != store.StateFiled || len(fx.hub.asks) != 0 {
		t.Fatalf("state %s", r.State)
	}
}

func TestTheInvestigatorReadsTheAppsOwnTagForItsVersion(t *testing.T) {
	fx := newFixture(t)
	run(t, fx.seed, "git", "tag", "portal-v2.0.0")
	tagged := strings.TrimSpace(run(t, fx.seed, "git", "rev-parse", "HEAD"))
	write(t, fx.seed, "a.txt", "one\nlater\n")
	run(t, fx.seed, "git", "commit", "-q", "-am", "chore: later")
	run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main", "portal-v2.0.0")
	fx.findings(store.Verdict{Verdict: store.VerdictUserError, Summary: "It works.", Facts: []store.Fact{{Text: "the page loads", Evidence: []int{1}}}, Answer: "It works."}, nil)
	var read string
	investigate := fx.rt.investigate
	fx.rt.investigate = func(dir, prompt string) error {
		read = run(t, dir, "git", "rev-parse", "HEAD")
		return investigate(dir, prompt)
	}
	fx.work(fx.submitFrom("acme-portal", "developer", "the orders page is blank when I open it", "2.0.0"))
	if read != tagged {
		t.Fatalf("read %s, want the tagged %s", read, tagged)
	}
}

func TestABuildThatTouchesAProtectedPathThePlanDidNotNameAsksAgain(t *testing.T) {
	fx := newFixture(t)
	fx.hub.lanes = []string{"user", "developer", "architect"}
	fx.hub.answer = orrery.RunQueued
	fx.forge.enforced = true
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n", "infra/main.tf": "# infra changed\n"})
	r := fx.work(fx.submit("architect", "please add a second line to the orders file"))
	// The first run was approved, not sent back: the hub revises only a run sent back, so the new ask
	// stands alone and the first run ends.
	if len(fx.hub.asks) != 2 || fx.hub.asks[1].Revises != "" || fx.hub.runs["run1"].State != orrery.RunFailed {
		t.Fatalf("asks: %+v (state %s)", fx.hub.asks, r.State)
	}
	if !strings.Contains(fmt.Sprint(fx.hub.asks[1].Inputs["plan"]), "infra/main.tf") {
		t.Error("the second ask does not show the protected path")
	}
	// The hub let the second ask through, and the lane lands through review.
	if r.State != store.StateAwaitingReview || len(fx.forge.prs) != 1 || fx.remoteFile("a.txt") != "one\n" {
		t.Fatalf("state %s prs %v", r.State, fx.forge.prs)
	}
	head := run(t, fx.remote, "git", "rev-parse", "refs/heads/graft/"+r.ID)
	fx.forge.pr = forge.PRState{State: forge.PROpen, Approved: true, Head: head}
	r = fx.poll(r)
	if len(fx.forge.merged) != 1 || fx.forge.merged[0] != head || r.State != store.StateAwaitingReview {
		t.Fatalf("an approved pull request is merged by the forge: %v %s", fx.forge.merged, r.State)
	}
	// The forge merges it: main gets the branch.
	run(t, fx.remote, "git", "update-ref", "refs/heads/main", head)
	fx.forge.pr = forge.PRState{State: forge.PRMerged, Head: head, MergeSHA: head}
	r = fx.poll(r)
	if r.State != store.StateReleased || r.Commit != head {
		t.Fatalf("after the merge: %s %s", r.State, r.Commit)
	}
}

func TestAReviewLaneRefusesToLandWhereTheForgeDoesNotProtectMain(t *testing.T) {
	fx := newFixture(t)
	fx.hub.lanes = []string{"architect"}
	fx.hub.answer = orrery.RunQueued
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n"})
	r := fx.work(fx.submit("architect", "please add a second line to the orders file"))
	if r.State != store.StateWaiting || len(fx.forge.prs) != 0 || fx.remoteFile("a.txt") != "one\n" {
		t.Fatalf("state %s", r.State)
	}
}

func TestNothingShipsWhileNobodyIsOnCallOrTheRepositoryIsStopped(t *testing.T) {
	fx := newFixture(t)
	fx.d.prog.Hours.Schedule = "platform"
	fx.inc.oncall = []dispatch.OnCall{{Slug: "platform"}}
	fx.findings(defect(), fixPlan("a.txt", "tests/two.sh"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n", "tests/two.sh": "grep -q two a.txt\n"})
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateBuilding || r.Held == "" || r.Rounds != 0 {
		t.Fatalf("state %s held %q rounds %d", r.State, r.Held, r.Rounds)
	}
	fx.inc.oncall = []dispatch.OnCall{{Slug: "platform", Person: "oncall@example.com"}}
	if err := fx.d.Stop(context.Background(), fx.d.prog.Repository("acme/api"), "lead@example.com", ""); err != nil {
		t.Fatal(err)
	}
	r.NotBefore = time.Time{}
	if err := fx.st.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateBuilding || !strings.Contains(r.Held, "stopped") {
		t.Fatalf("stopped, yet state %s held %q", r.State, r.Held)
	}
	if err := fx.d.Start(context.Background(), fx.d.prog.Repository("acme/api"), "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r.NotBefore = time.Time{}
	if err := fx.st.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r = fx.work(r); r.State != store.StateReleased {
		t.Fatalf("started and on call, yet state %s", r.State)
	}
}

func TestAToolCallIsLentToThePersonAndKeptAsEvidence(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	r := fx.submit("developer", "the orders page is blank when I open it")
	r.State = store.StateInvestigating
	if err := fx.st.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	fx.hub.tool = func(action string) (*orrery.ToolAnswer, error) {
		return &orrery.ToolAnswer{Run: "t1", State: "succeeded", Text: "depth 7"}, nil
	}
	list, err := fx.d.Tools(ctx, r)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].ID != "queue-depth" || list.Default != program.Production {
		t.Fatalf("tools: %+v %v", list, err)
	}
	res, err := fx.d.CallTool(ctx, r, "queue-depth", "", nil)
	if err != nil || res.Evidence != 1 || res.Text != "depth 7" {
		t.Fatalf("call: %+v %v", res, err)
	}
	lend := fx.hub.lends[0]
	if lend.Person != "a@example.com" || lend.Lane != "developer" || lend.Entity != "repository:acme-api" || lend.Run != "" {
		t.Errorf("lend: %+v", lend)
	}
	got, _ := fx.st.Get(ctx, r.ID)
	if !got.ReadProduction {
		t.Error("a production read was not kept on the request")
	}
	if _, err := fx.d.CallTool(ctx, r, "drop-tables", "", nil); err == nil {
		t.Error("a tool the repository does not declare was called")
	}
	fx.hub.tool = func(string) (*orrery.ToolAnswer, error) { return nil, &orrery.APIError{Status: 403, Message: "tier"} }
	res, err = fx.d.CallTool(ctx, r, "queue-depth", program.NonProduction, nil)
	if err != nil || res.State != "refused" || res.Evidence != 2 {
		t.Fatalf("a refused call is an answer the agent reads: %+v %v", res, err)
	}
	if _, err := fx.d.ByHandle(ctx, "not-a-secret"); err != ErrNoHandle {
		t.Errorf("a wrong handle: %v", err)
	}
}

func TestADuplicateIsAnsweredUnlessItComesFromAHigherLane(t *testing.T) {
	fx := newFixture(t)
	first := fx.submit("user", "the orders page is blank when I open it on monday")
	first.State = store.StateInvestigating
	if err := fx.st.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	same := fx.work(fx.submit("user", "The orders page is blank when I open it on Monday!"))
	if same.State != store.StateAnswered || len(same.Links) != 1 || same.Links[0].ID != first.ID {
		t.Fatalf("state %s links %+v", same.State, same.Links)
	}
	fx.findings(store.Verdict{Verdict: store.VerdictDesigned, Summary: "By design.", Facts: []store.Fact{{Text: "x", Evidence: []int{1}}}, Answer: "It works this way."}, nil)
	higher := fx.work(fx.submit("developer", "the orders page is blank when I open it on monday"))
	if len(higher.Links) != 1 || higher.Links[0].ID != first.ID || len(fx.rt.prompts) != 1 {
		t.Fatalf("a developer's request goes on and links: state %s links %+v prompts %d", higher.State, higher.Links, len(fx.rt.prompts))
	}
}

// readers puts stand-ins for the tools a sandbox reads files with on the
// path: pdftoppm draws two pages, pdftotext prints the PDF's bytes, and
// tesseract prints a picture's bytes, or fails on one that says unreadable.
func readers(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "pdftoppm", "#!/bin/sh\nfor last; do :; done\nprintf 'page one' > \"$last-1.png\"\nprintf 'page two' > \"$last-2.png\"\n")
	write(t, dir, "pdftotext", "#!/bin/sh\ncat \"$5\"\n")
	write(t, dir, "tesseract", "#!/bin/sh\nif grep -q unreadable \"$1\"; then echo 'cannot read' >&2; exit 1; fi\ncat \"$1\"\n")
	for _, name := range []string{"pdftoppm", "pdftotext", "tesseract"} {
		if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

var (
	shot   = append([]byte("\x89PNG\r\n\x1a\n"), []byte(" the save button, circled")...)
	markup = []byte("%PDF-1.7\nthe Delta Portal form, marked up\n%%EOF")
)

func (fx *fixture) submitFiles(files ...attach.Upload) *store.Request {
	fx.t.Helper()
	r, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: "acme-web", Repository: "acme/api", Environment: program.Production, Lane: "user", Text: "the save button on the Delta Portal form does nothing", Attachments: files})
	if err != nil {
		fx.t.Fatal(err)
	}
	return r
}

func TestFilesAreSweptThenShownToTheInvestigatorAsPictures(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt"))
	var seen []string
	inner := fx.rt.investigate
	fx.rt.investigate = func(dir, prompt string) error {
		for _, name := range []string{"1-portal.png", "2-markup.pdf", "2-markup-page-1.png", "2-markup-page-2.png"} {
			if _, err := os.Stat(filepath.Join(dir, inDir, name)); err == nil {
				seen = append(seen, name)
			}
		}
		return inner(dir, prompt)
	}
	r := fx.submitFiles(attach.Upload{Name: "C:\\Users\\a\\portal.png", Data: shot}, attach.Upload{Name: "markup.pdf", Data: markup})
	if len(r.Attachments) != 2 || r.Attachments[0].Name != "portal.png" {
		t.Fatalf("attachments %+v", r.Attachments)
	}
	if _, data, err := fx.d.Attachment(r, r.Attachments[1].ID); err != nil || string(data) != string(markup) {
		t.Fatalf("the PDF is kept on disk: %v %q", err, data)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan {
		t.Fatalf("state %s", r.State)
	}
	if strings.Join(seen, ",") != "1-portal.png,2-markup.pdf,2-markup-page-1.png,2-markup-page-2.png" {
		t.Errorf("the investigator's clone held %v", seen)
	}
	prompt := fx.rt.prompts[0]
	for _, line := range []string{
		"- `.graft-in/1-portal.png`: \"portal.png\", a picture the person sent; their own picture of the page.",
		"- `.graft-in/2-markup-page-2.png`: page 2 of \"markup.pdf\"; the person's own picture of the page.",
	} {
		if !strings.Contains(prompt, line) {
			t.Errorf("the brief lacks %q:\n%s", line, prompt)
		}
	}
	log, _ := os.ReadFile(fx.d.LogPath(r.ID))
	if strings.Contains(string(log), "the save button, circled") {
		t.Error("what a picture says reached the request's log")
	}
	files := fx.hub.asks[0].Inputs["files"].(string)
	if !strings.Contains(files, "[portal.png](https://graft.example.com/api/requests/"+r.ID+"/attachments/"+r.Attachments[0].ID+")") {
		t.Errorf("the plan's ask links the files: %q", files)
	}
}

func TestASecretInAPictureQuarantinesTheRequestAndDeletesItsFiles(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	leaked := append(append([]byte{}, shot...), []byte(" token=ghp_"+strings.Repeat("b", 36))...)
	r := fx.work(fx.submitFiles(attach.Upload{Name: "markup.pdf", Data: markup}, attach.Upload{Name: "leak.png", Data: leaked}))
	if r.State != store.StateQuarantined || r.Answer != fixedQuarantinedFile || len(r.Attachments) != 0 || len(r.Secrets) == 0 {
		t.Fatalf("state %s answer %q files %d secrets %v", r.State, r.Answer, len(r.Attachments), r.Secrets)
	}
	if _, err := os.Stat(filepath.Join(fx.d.cfg.DataDir, "attachments", r.ID)); !os.IsNotExist(err) {
		t.Errorf("the files are still on disk: %v", err)
	}
	if len(fx.rt.prompts) != 0 {
		t.Error("an agent read a request whose file held a secret")
	}
}

func TestAFileThatCannotBeReadIsDroppedAndTheRestGoOn(t *testing.T) {
	readers(t)
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt"))
	odd := append(append([]byte{}, shot...), []byte(" unreadable")...)
	r := fx.work(fx.submitFiles(attach.Upload{Name: "odd.png", Data: odd}, attach.Upload{Name: "portal.png", Data: shot}))
	if len(r.Attachments) != 1 || r.Attachments[0].Name != "portal.png" {
		t.Fatalf("attachments %+v", r.Attachments)
	}
	if _, _, err := fx.d.Attachment(&store.Request{ID: r.ID, Doc: store.Doc{Attachments: []attach.File{{ID: "x"}}}}, "x"); err == nil {
		t.Error("read a file that is not there")
	}
	events, _ := fx.st.Events(context.Background(), r.ID)
	var dropped bool
	for _, e := range events {
		dropped = dropped || strings.Contains(e.Message, "odd.png could not be read")
	}
	if !dropped {
		t.Error("the page does not say a file was dropped")
	}
}

func TestSubmitRefusesAFileByItsBytesNotItsName(t *testing.T) {
	fx := newFixture(t)
	_, err := fx.d.Submit(context.Background(), Submission{Person: "a@example.com", App: "acme-web", Repository: "acme/api", Text: "the save button on the Delta Portal form does nothing",
		Attachments: []attach.Upload{{Name: "page.png", Data: []byte("<html><script>alert(1)</script>")}}})
	var invalid Invalid
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "page.png is not a picture") {
		t.Fatalf("err %v", err)
	}
	if all, _ := fx.st.List(context.Background(), store.Filter{}); len(all) != 0 {
		t.Error("a refused request was kept")
	}
}

func TestOldFilesAreDroppedWithTheContext(t *testing.T) {
	fx := newFixture(t)
	r := fx.submitFiles(attach.Upload{Name: "portal.png", Data: shot})
	fx.d.now = func() time.Time { return time.Now().AddDate(0, 0, 31) }
	fx.d.expire(context.Background())
	got, _ := fx.st.Get(context.Background(), r.ID)
	if len(got.Attachments) != 0 || got.Context != nil {
		t.Errorf("kept after context_days: %+v", got.Attachments)
	}
	if _, err := os.Stat(filepath.Join(fx.d.cfg.DataDir, "attachments", r.ID)); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
}

// approvedProgram is the test program with the Platform team deciding every plan.
var approvedProgram = testProgram

func TestAPlanIsDecidedOnlyByItsApproversAndNeverByWhoAsked(t *testing.T) {
	fx := newFixtureOf(t, approvedProgram)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, featurePlan("a.txt"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n", "tests/two.sh": "grep -q two a.txt\n"})
	commit := fx.rt.build
	built := 0
	fx.rt.build = func(dir, prompt string) error { built++; return commit(dir, prompt) }
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateAwaitingPlan || r.Run != "run1" {
		t.Fatalf("state %s run %s", r.State, r.Run)
	}
	if a := fx.hub.asks[0].Approvers; len(a) != 1 || a[0].Path != "teams" || a[0].Values[0] != "platform" {
		t.Fatalf("the ask names its approvers: %+v", a)
	}
	// The person who asked approves it, and someone off the team declines the next: Graft takes neither.
	for i, c := range []struct{ state, by, why string }{
		{orrery.RunQueued, "A@Example.com", "A@Example.com asked for this change, and the person who asked never decides their own plan"},
		{orrery.RunDeclined, "stakeholder@example.com", "stakeholder@example.com is not one of the plan's approvers"},
	} {
		run := r.Run
		fx.hub.decide(run, c.state, c.by, "")
		r = fx.poll(r)
		if r.State != store.StateAwaitingPlan || r.Run == run || r.PlanRound != 1 || built != 0 || r.Decision != "" {
			t.Fatalf("after a %s by %s: state %s run %s round %d built %d decision %q", c.state, c.by, r.State, r.Run, r.PlanRound, built, r.Decision)
		}
		if c.state == orrery.RunQueued && fx.hub.runs[run].State != orrery.RunFailed {
			t.Errorf("the refused run is still %s", fx.hub.runs[run].State)
		}
		if len(fx.hub.asks) != i+2 || fx.hub.asks[i+1].Revises != "" {
			t.Fatalf("asks %d", len(fx.hub.asks))
		}
		events, _ := fx.st.Events(context.Background(), r.ID)
		found := false
		for _, e := range events {
			found = found || e.Message == "decision refused: "+c.why
		}
		if !found {
			t.Errorf("the page does not say %q", c.why)
		}
	}
	// Someone on the approvers' team approves it: it goes ahead, and says who and when.
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "Yes, the second line.")
	r = fx.poll(r)
	if r.State == store.StateAwaitingPlan || built != 1 {
		t.Fatalf("after an approver approved: %s built %d", r.State, built)
	}
	if r.Decision != store.DecisionApproved || r.DecidedBy != "lead@example.com" || r.DecidedAt == "" || r.DecisionNote != "Yes, the second line." {
		t.Errorf("decision kept: %q %q %q %q", r.Decision, r.DecidedBy, r.DecidedAt, r.DecisionNote)
	}
	shown := fx.hub.records[r.ID]
	if shown.Decision != store.DecisionApproved || shown.DecidedBy != "lead@example.com" || shown.Plan != "https://hub.example.com/actions/runs/"+r.Run {
		t.Errorf("the catalog record: %+v", shown)
	}
}

func TestADecisionFromAHubThatHeldThePlanToNoApproversDoesNotCount(t *testing.T) {
	fx := newFixtureOf(t, approvedProgram)
	fx.hub.members = map[string]bool{"lead@example.com": true}
	fx.hub.drops = true
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	fx.hub.decide(r.Run, orrery.RunDeclined, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateAwaitingPlan || r.Decision != "" || len(fx.hub.asks) != 2 {
		t.Fatalf("state %s decision %q asks %d", r.State, r.Decision, len(fx.hub.asks))
	}
}

func TestInALaneThatShipsUnaskedTheHubsDeciderCounts(t *testing.T) {
	fx := newFixture(t)
	fx.hub.lanes = []string{"architect"}
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	// The lane names no approvers, but the hub held the run by its own rule.
	r := fx.work(fx.submit("architect", "please add a second line to the orders file"))
	if len(fx.hub.asks) != 1 || len(fx.hub.asks[0].Approvers) != 0 {
		t.Fatalf("asks: %+v", fx.hub.asks)
	}
	fx.hub.decide(r.Run, orrery.RunDeclined, "lead@example.com", "Not now.")
	r = fx.poll(r)
	if r.State != store.StateDeclined || r.Decision != store.DecisionDeclined || r.DecidedBy != "lead@example.com" || r.DecisionNote != "Not now." {
		t.Fatalf("state %s decision %q by %q note %q", r.State, r.Decision, r.DecidedBy, r.DecisionNote)
	}
}

func TestATokenOCRSplitsIsStillFound(t *testing.T) {
	// What tesseract read from a page that held token ghp_A1b2...: it split the token where letters crowd.
	read := "Saved settings: ghp_A1b2C 3d4E 5f6G 7h819j0K 112M 3n405p6Q 7r8x9"
	if kinds, _ := sweep.Scan(read); len(kinds) != 0 {
		t.Fatalf("the split text matched %v; this test needs one that does not", kinds)
	}
	if kinds, _ := sweep.Scan(squeezed(read)); len(kinds) == 0 {
		t.Error("joined again, the token is not found")
	}
}

func TestThePlatformLaneShipsWithNoGate(t *testing.T) {
	// The platform lane: members by team, protected paths allowed, no plan approval, no review.
	fx := newFixtureOf(t, strings.Replace(testProgram, "  review: true\n", "", 1))
	fx.hub.lanes = []string{"architect"}
	// The hub runs a plan that names no approvers at once.
	fx.hub.answer = orrery.RunQueued
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt", "infra/main.tf"))
	fx.commits(map[string]string{"a.txt": "one\ntwo\n", "infra/main.tf": "# infra changed\n"})
	r := fx.work(fx.submit("", "please add a second line to the orders file"))
	if r.Lane != "architect" || len(fx.hub.asks) != 1 || len(fx.hub.asks[0].Approvers) != 0 || fx.hub.asks[0].Person != "a@example.com" {
		t.Fatalf("lane %s asks %+v", r.Lane, fx.hub.asks)
	}
	if r.Decision != "" || fx.remoteFile("a.txt") != "one\ntwo\n" || fx.remoteFile("infra/main.tf") != "# infra changed\n" {
		t.Fatalf("state %s decision %q: the plan did not ship unasked", r.State, r.Decision)
	}
}

func TestAPlanThatWaitsIsNeverBuiltWhenTheHubLetsItGoUnasked(t *testing.T) {
	fx := newFixture(t)
	// A hub that knows no approvers runs the plan at once.
	fx.hub.drops = true
	fx.hub.answer = orrery.RunQueued
	v := defect()
	v.Verdict = store.VerdictFeature
	fx.findings(v, fixPlan("a.txt"))
	built := 0
	fx.rt.build = func(string, string) error { built++; return nil }
	r := fx.work(fx.submit("user", "please add a second line to the orders file"))
	if r.State != store.StateWaiting || built != 0 || fx.hub.runs["run1"].State != orrery.RunFailed || len(fx.hub.asks) != 1 {
		t.Fatalf("state %s built %d run %s asks %d", r.State, built, fx.hub.runs["run1"].State, len(fx.hub.asks))
	}
}

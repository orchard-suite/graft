// Package desk works a request from the words a person sent to the answer
// they get. Each stage is a state of the request; a stage that stops midway
// is run again from its start, so every stage can be run twice.
//
// The stages that decide anything are plain code. A model screens, an agent
// investigates and writes a verdict and a plan, and an agent builds; none
// of them holds a credential, and what they write is checked by code before
// anything acts on it.
package desk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/platformkit/v3/dispatch"
	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/config"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// Hub is what the desk asks the catalog hub.
type Hub interface {
	Lanes(ctx context.Context, email string) ([]string, error)
	AskPlan(ctx context.Context, ask hub.PlanAsk) (*hub.PlanRun, error)
	Run(ctx context.Context, id string) (*hub.PlanRun, error)
	LetsIn(ctx context.Context, address string, grants []program.Grant) (bool, error)
	Admits(ctx context.Context, address string, grants []program.Grant) (bool, error)
	Report(ctx context.Context, id string, report orrery.ActionReport) error
	Withdraw(ctx context.Context, id, reason string) error
	CallTool(ctx context.Context, action string, inputs map[string]any, l hub.Lend) (*orrery.ToolAnswer, error)
	Needs(ctx context.Context, entity string, relations []string) ([]string, error)
	PutRequest(ctx context.Context, r hub.Request) error
	PutRequests(ctx context.Context, rs []hub.Request) (int, error)
	SyncTools(ctx context.Context, repo *program.Repository, file *program.RepoFile, was []string) ([]string, error)
	RunURL(run string) string
}

// Incidents is what the desk asks the incident desk. A nil
// *dispatch.Client answers every call with nothing.
type Incidents interface {
	OpenIncidents(ctx context.Context, service string) ([]dispatch.Incident, error)
	OpenIncident(ctx context.Context, in dispatch.NewIncident) (*dispatch.Incident, error)
	Send(ctx context.Context, ev dispatch.Event) error
	RecordChange(ctx context.Context, ch dispatch.Change) error
	OnCall(ctx context.Context) ([]dispatch.OnCall, error)
}

// Workspace is where an agent's clone lives and where every command that
// reads it runs. It holds no credential.
type Workspace interface {
	Run(ctx context.Context, log io.Writer, cmd shell.Cmd) (shell.Result, error)
	Put(ctx context.Context, path string, r io.Reader) error
	Get(ctx context.Context, path string, w io.Writer) error
	Remove(ctx context.Context, path string) error
}

// Deps are the things a desk works with.
type Deps struct {
	Store     *store.Store
	Forge     forge.Forge
	Sandboxes Sandboxes
	Hub       Hub
	Incidents Incidents
	Annals    *annals.Client
	Log       *slog.Logger
}

type Desk struct {
	cfg    config.Config
	prog   *program.Program
	store  *store.Store
	forge  forge.Forge
	boxes  Sandboxes
	hub    Hub
	inc    Incidents
	annals *annals.Client
	log    *slog.Logger
	now    func() time.Time

	mu sync.Mutex
	// files are the repository files last read from each main branch.
	files map[string]*program.RepoFile
	// active are the requests a worker is on now.
	active map[string]bool
	// open are the sandboxes open now, by request.
	open map[string]*Box
	// listened is when each waiting request's ticket was last read for
	// comments.
	listened map[string]time.Time
	// locks keeps one hand on a waiting request at a time.
	locks locks
	// runs are each request's open run, by request.
	runs map[string]*live
}

func New(cfg config.Config, deps Deps) *Desk {
	return &Desk{cfg: cfg, prog: cfg.Program, store: deps.Store, forge: deps.Forge, boxes: deps.Sandboxes, hub: deps.Hub,
		inc: deps.Incidents, annals: deps.Annals, log: deps.Log, now: time.Now, files: map[string]*program.RepoFile{}, active: map[string]bool{}, open: map[string]*Box{}, listened: map[string]time.Time{}, runs: map[string]*live{}}
}

// LogPath is the request's log file.
func (d *Desk) LogPath(id string) string {
	return filepath.Join(d.cfg.DataDir, "logs", id+".log")
}

func (d *Desk) openLog(id string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(d.LogPath(id)), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(d.LogPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

type stepFn func(ctx context.Context, r *store.Request, log io.Writer) error

func (d *Desk) steps() map[string]stepFn {
	return map[string]stepFn{
		store.StateReceived:      d.intake,
		store.StateScreening:     d.screen,
		store.StateChecking:      d.check,
		store.StateInvestigating: d.investigate,
		store.StatePlanning:      d.plan,
		store.StateBuilding:      d.build,
		store.StateLanding:       d.land,
		store.StateWatching:      d.watch,
	}
}

// backToAgent is a stage's way of handing the request back to the build
// agent: the next round starts from prompt in the same session.
type backToAgent struct {
	why    string
	prompt string
}

func (e *backToAgent) Error() string { return e.why }

// Run works the request through every stage from its current one. It
// returns when the request has ended, waits on a person or a timer, or the
// context ends; where it stands is in the store.
func (d *Desk) Run(ctx context.Context, id string) {
	r, err := d.store.Get(ctx, id)
	if err != nil {
		d.log.Error("request not read", "request", id, "error", err)
		return
	}
	f, err := d.openLog(id)
	if err != nil {
		d.fail(ctx, r, err)
		return
	}
	defer f.Close()
	// No sandbox outlives the run that opened it.
	defer d.shut(ctx, r)
	steps := d.steps()
	for store.Working(r.State) {
		if r.NotBefore.After(d.now()) {
			return
		}
		if r.Stall != nil && r.Stall.Stage != r.State {
			// The stage that failed is behind the request now.
			r.Stall = nil
		}
		stage := r.State
		fmt.Fprintf(f, "\n==== %s %s ====\n", d.now().UTC().Format(time.RFC3339), stage)
		d.begin(ctx, r, stage)
		err := steps[stage](ctx, r, f)
		if ctx.Err() != nil {
			// Shutting down: the stage runs again from its start.
			return
		}
		var back *backToAgent
		if errors.As(err, &back) {
			fmt.Fprintf(f, "\n%s; back to the agent:\n%s\n", back.why, back.prompt)
			if r.Rounds >= d.cfg.BuildRounds {
				err = d.giveUp(ctx, r, f, back)
			} else {
				r.State, r.Feedback, err = store.StateBuilding, back.prompt, nil
				d.note(ctx, r, stage, back.why+"; back to the builder")
			}
		}
		if err != nil {
			fmt.Fprintf(f, "\nFAILED: %v\n", err)
			d.stop(ctx, r, stage, err, f)
			return
		}
		if r.Stall != nil && r.State != r.Stall.Stage {
			r.Stall = nil
		}
		d.after(ctx, r)
		if err := d.save(ctx, r); err != nil {
			d.log.Error("request not saved", "request", r.ID, "error", err)
			return
		}
		if r.State == stage && r.NotBefore.After(d.now()) {
			return
		}
	}
}

// save keeps the request and shows it to the catalog.
func (d *Desk) save(ctx context.Context, r *store.Request) error {
	if err := d.store.Save(ctx, r); err != nil {
		return err
	}
	d.show(ctx, r)
	return nil
}

// show writes the request as a catalog record. The record is a view: a
// hub that refuses it is logged and nothing more.
func (d *Desk) show(ctx context.Context, r *store.Request) {
	spend, err := d.store.Spend(context.WithoutCancel(ctx), r.ID)
	if err != nil {
		// A record without its spend would show less than it did.
		d.log.Warn("request not shown in the catalog: its spend was not read", "request", r.ID, "error", err)
		return
	}
	hr, ok := d.catalogRecord(r, spend)
	if !ok {
		return
	}
	if err := d.hub.PutRequest(context.WithoutCancel(ctx), hr); err != nil {
		d.log.Debug("request not shown in the catalog", "request", r.ID, "error", err)
	}
}

// catalogRecord is the request as its catalog record, with what its agents
// spent, or false for one whose repository the program no longer lists. An
// ended request changes no more, so its last change is when it ended.
func (d *Desk) catalogRecord(r *store.Request, spend store.Spend) (hub.Request, bool) {
	repo := d.prog.Repository(r.Repo)
	if repo == nil {
		return hub.Request{}, false
	}
	hr := hub.Request{ID: r.ID, Person: r.Person, Title: d.title(r), State: r.State, Lane: r.Lane, Sort: r.Sort, Entity: repo.Entity, URL: d.requestURL(r),
		Plan: r.RunURL, Decision: r.Decision, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt, RequestedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Round: r.PlanRound}
	if store.Ended(r.State) {
		hr.EndedAt = r.UpdatedAt
	}
	if r.Verdict != nil {
		hr.Verdict = r.Verdict.Verdict
	}
	if r.Ticket != nil {
		hr.Ticket = r.Ticket.URL
	}
	hr.Runs = runList(r.Runs)
	by := spend.ByStage()
	hr.Spend = hub.Spend{TotalUSD: spend.TotalUSD, ScreenUSD: by[store.SpendScreen], PlanUSD: by[store.SpendInvestigate] + by[store.SpendRevise], BuildUSD: by[store.SpendBuild] + by[store.SpendProve], AgentRuns: spend.Runs}
	return hr, true
}

// ShowAll writes every request Graft holds, worked, waiting, or ended, as
// its catalog record, in one run of the hub, so each record holds what the
// request holds now: a record a Graft before wrote gains what it lacked.
// Writing a record as it already is costs the hub nothing, so it runs at
// every start.
func (d *Desk) ShowAll(ctx context.Context) error {
	start := d.now()
	all, err := d.store.List(ctx, store.Filter{})
	if err != nil {
		return fmt.Errorf("read the requests: %w", err)
	}
	spend, err := d.store.AllSpend(ctx)
	if err != nil {
		return fmt.Errorf("read what the requests spent: %w", err)
	}
	var rs []hub.Request
	for _, r := range all {
		if hr, ok := d.catalogRecord(r, spend[r.ID]); ok {
			rs = append(rs, hr)
		}
	}
	took, err := d.hub.PutRequests(ctx, rs)
	if err != nil {
		return fmt.Errorf("write the requests to the catalog: %w", err)
	}
	d.log.Info("requests written to the catalog", "requests", len(rs), "taken", took, "ms", d.now().Sub(start).Milliseconds())
	return nil
}

// title is the request in one line.
func (d *Desk) title(r *store.Request) string {
	if r.Title != "" {
		return r.Title
	}
	if len(r.Secrets) > 0 {
		return "A request that held a secret"
	}
	return clip(firstLine(r.Text), 100)
}

func (d *Desk) requestURL(r *store.Request) string {
	if d.cfg.PublicURL == "" {
		return ""
	}
	return d.cfg.PublicURL + "/#" + r.ID
}

// note records something that happened inside a stage, for the page, and
// on the open run's comment as one of Graft's steps.
func (d *Desk) note(ctx context.Context, r *store.Request, stage, msg string) {
	if err := d.store.AddEvent(context.WithoutCancel(ctx), r.ID, stage, msg); err != nil {
		d.log.Error("event not kept", "request", r.ID, "error", err)
	}
	d.noteStep(r, msg)
}

// record notes a step on the page and posts it to Annals. Annals
// gets the step's own words only, never an agent's or a reporter's.
func (d *Desk) record(ctx context.Context, r *store.Request, msg string, s annals.Step) {
	d.note(ctx, r, r.State, msg)
	r.Seq++
	r.AnnalsKey = d.annals.Post(context.WithoutCancel(ctx), r, fmt.Sprint(r.Seq), s)
}

// key names one of the request's agent sessions. The cost reader names the
// session's spend by it.
func (d *Desk) key(r *store.Request, stage string) string {
	return "graft-" + r.ID + "-" + stage
}

// repo is the request's repository in the program file.
func (d *Desk) repo(r *store.Request) (*program.Repository, error) {
	repo := d.prog.Repository(r.Repo)
	if repo == nil {
		return nil, fmt.Errorf("%s is no longer a repository of %s", r.Repo, d.prog.Name)
	}
	return repo, nil
}

// intakeApp is the request's intake app in the program file, or nil for a
// request made on Graft's own page.
func (d *Desk) intakeApp(r *store.Request) (*program.Intake, error) {
	if r.App == program.PageApp {
		return nil, nil
	}
	return d.prog.IntakeFor(r.App, r.Repo)
}

// lane is the request's lane in the program file.
func (d *Desk) lane(r *store.Request) (*program.Lane, error) {
	lane := d.prog.Lane(r.Lane)
	if lane == nil {
		return nil, fmt.Errorf("%s is no longer a lane of %s", r.Lane, d.prog.Name)
	}
	return lane, nil
}

// hold keeps a worker off the request for a while, and says why.
func (d *Desk) hold(ctx context.Context, r *store.Request, why string, wait time.Duration) {
	if r.Held != why {
		d.note(ctx, r, r.State, "holding: "+why)
	}
	r.Held, r.NotBefore = why, d.now().Add(wait)
}

// unhold clears a hold once its reason has passed.
func (d *Desk) unhold(r *store.Request) {
	r.Held, r.NotBefore = "", time.Time{}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// clip cuts s to at most n bytes at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "..."
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

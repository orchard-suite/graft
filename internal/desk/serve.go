package desk

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

// ErrNoLane is a person on Graft's page who named a lane they do not
// hold, or a person in a program with no fallback lane who holds none.
var ErrNoLane = errors.New("you hold no lane on this repository")

// ErrTooMany is a person past their hourly limit.
var ErrTooMany = errors.New("too many requests this hour")

// Invalid is a submission that cannot be taken as sent; its text is for
// the person who sent it.
type Invalid string

func (e Invalid) Error() string { return string(e) }

// Submission is one request as it comes in. Person is who is signed in, as
// the app's own server or the front door says, never as a browser says.
type Submission struct {
	Person     string
	App        string
	Repository string
	// Environment is the tier the app runs in; empty is nonproduction.
	Environment string
	// Lane is the lane the person asked for; empty picks the highest held.
	Lane    string
	Text    string
	Context *store.Context
	// Attachments are the files sent with the words: pictures of the page
	// and PDFs, checked by their bytes.
	Attachments []attach.Upload
}

// Lanes lists the lanes a person holds in the program, lowest first. A lane
// is held on every repository of the program alike.
func (d *Desk) Lanes(ctx context.Context, person string) ([]string, error) {
	return d.hub.Lanes(ctx, person)
}

// Approves says the person decides plans in the repository: its approvers,
// or the program's, let them in, as the hub judged it within the last
// minute. Nobody does where none are named.
func (d *Desk) Approves(ctx context.Context, person string, repo *program.Repository) (bool, error) {
	grants := d.prog.ApproversOf(repo)
	if len(grants) == 0 {
		return false, nil
	}
	return d.hub.Admits(ctx, person, grants)
}

// Submit takes a new request: it checks the person's hourly limit, puts
// the request in the highest lane they hold (the fallback lane when they
// hold no other) unless they asked for another they hold, cuts the captured
// context down to its fixed fields, and keeps it for a worker. A person an
// intake app vouches for is never refused for want of a lane: a lane the
// app names that they do not hold gives way to the one chosen for them. No
// approval moves a request to another lane later.
func (d *Desk) Submit(ctx context.Context, s Submission) (*store.Request, error) {
	repo := d.prog.Repository(s.Repository)
	if repo == nil {
		return nil, Invalid(fmt.Sprintf("%s is not a repository of %s", s.Repository, d.prog.Name))
	}
	text := strings.TrimSpace(s.Text)
	if len(text) < 10 {
		return nil, Invalid("say a little more about what you need")
	}
	if len(text) > 16000 {
		return nil, Invalid("that is too long: keep it under 16,000 characters")
	}
	n, err := d.store.CountSince(ctx, s.Person, d.now().Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if n >= d.prog.Limits.RequestsPerHour {
		return nil, ErrTooMany
	}
	held, err := d.hub.Lanes(ctx, s.Person)
	if err != nil {
		return nil, fmt.Errorf("lanes: %w", err)
	}
	if len(held) == 0 {
		return nil, ErrNoLane
	}
	lane := d.prog.Choose(held)
	if s.Lane != "" && s.Lane != lane {
		named := slices.Contains(held, s.Lane)
		switch {
		case named:
			lane = s.Lane
		case s.App == program.PageApp:
			return nil, fmt.Errorf("%w: %s", ErrNoLane, s.Lane)
		default:
			d.log.Info("lane named by the app is not held; the chosen lane stands", "app", s.App, "named", s.Lane, "lane", lane)
		}
	}
	files, err := attach.Check(s.Attachments)
	var refused attach.Refused
	if errors.As(err, &refused) {
		return nil, Invalid(refused.Error())
	}
	if err != nil {
		return nil, err
	}
	r := &store.Request{ID: store.NewID(), Person: s.Person, Repo: repo.Path, App: s.App, Doc: store.Doc{Environment: s.Environment, Lane: lane, Text: text, Context: trimContext(s.Context), Attachments: files}}
	// The files are on disk before the request exists, so no worker meets a
	// request whose files are not there yet.
	for i, f := range files {
		if err := d.stored().Put(r.ID, f.ID, s.Attachments[i].Data); err != nil {
			_ = d.stored().Drop(r.ID)
			return nil, fmt.Errorf("keep file: %w", err)
		}
	}
	if r.Environment == "" {
		r.Environment = program.NonProduction
	}
	if r.Context != nil {
		r.Version = r.Context.Version
	}
	if err := d.store.Create(ctx, r); err != nil {
		_ = d.stored().Drop(r.ID)
		return nil, err
	}
	d.log.Info("request received", "request", r.ID, "repo", r.Repo, "app", r.App, "lane", r.Lane, "environment", r.Environment, "files", len(files))
	return r, nil
}

// trimContext keeps only the fixed fields of what an app captured, each cut
// to size, and a page address without its query or fragment: a form's
// values have nowhere to ride.
func trimContext(c *store.Context) *store.Context {
	if c == nil {
		return nil
	}
	out := &store.Context{URL: clipPlain(pageOnly(c.URL), 500), Version: clipPlain(c.Version, 100), UserAgent: clipPlain(c.UserAgent, 300)}
	for i, line := range c.Console {
		if i == 50 {
			break
		}
		out.Console = append(out.Console, clipPlain(line, 500))
	}
	if out.URL == "" && out.Version == "" && out.UserAgent == "" && len(out.Console) == 0 {
		return nil
	}
	return out
}

func pageOnly(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimSpace(u)
}

func clipPlain(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		for n > 0 && s[n]&0xC0 == 0x80 {
			n--
		}
		s = s[:n]
	}
	return s
}

// Stop flips a repository's stop switch: nothing ships there until it is
// started again. Start lifts it.
func (d *Desk) Stop(ctx context.Context, repo *program.Repository, by, reason string) error {
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "stopped by a person"
	}
	d.log.Warn("repository stopped", "repo", repo.Path)
	return d.store.SetSwitch(ctx, store.Switch{Repo: repo.Path, Stopped: true, Reason: clip(reason, 300), By: by})
}

func (d *Desk) Start(ctx context.Context, repo *program.Repository, by string) error {
	d.log.Info("repository started", "repo", repo.Path)
	return d.store.SetSwitch(ctx, store.Switch{Repo: repo.Path, Reason: "started by a person", By: by})
}

// Poll reads a waiting request again and moves it when what it waits on
// has changed.
func (d *Desk) Poll(ctx context.Context, r *store.Request) error {
	switch r.State {
	case store.StateHolding:
		return d.pollHolding(ctx, r)
	case store.StateAwaitingPlan:
		return d.pollPlan(ctx, r)
	case store.StateAwaitingReview:
		return d.pollReview(ctx, r)
	case store.StateStalled:
		return d.pollStalled(ctx, r)
	}
	return nil
}

// pollStalled reads the ticket of a request that waits for a retry, when
// the sweep is due: a webhook may have missed the retry command.
func (d *Desk) pollStalled(ctx context.Context, r *store.Request) error {
	m := d.locks.of(r.ID)
	m.Lock()
	defer m.Unlock()
	d.mu.Lock()
	due := d.now().Sub(d.listened[r.ID]) >= listenEvery
	d.mu.Unlock()
	if !due {
		return nil
	}
	r, err := d.store.Get(ctx, r.ID)
	if err != nil || r.State != store.StateStalled {
		return err
	}
	return d.listen(ctx, r)
}

// Tick does one round of the desk's own housekeeping: it starts a worker on
// each request that is ready for one, up to the program's limit.
func (d *Desk) Tick(ctx context.Context) {
	ready, err := d.store.List(ctx, store.Filter{States: store.WorkStates})
	if err != nil {
		d.log.Error("requests not read", "error", err)
		return
	}
	// Oldest first.
	for i := len(ready) - 1; i >= 0; i-- {
		r := ready[i]
		if r.NotBefore.After(d.now()) {
			continue
		}
		d.mu.Lock()
		if d.active[r.ID] || len(d.active) >= d.prog.Limits.Working {
			d.mu.Unlock()
			continue
		}
		d.active[r.ID] = true
		d.mu.Unlock()
		go func(id string) {
			defer func() {
				d.mu.Lock()
				delete(d.active, id)
				d.mu.Unlock()
			}()
			d.Run(ctx, id)
		}(r.ID)
	}
}

// PollAll reads every waiting request again.
func (d *Desk) PollAll(ctx context.Context) {
	waiting, err := d.store.List(ctx, store.Filter{States: store.WaitStates})
	if err != nil {
		d.log.Error("requests not read", "error", err)
		return
	}
	for _, r := range waiting {
		if err := d.Poll(ctx, r); err != nil && ctx.Err() == nil {
			d.log.Warn("waiting request not read again", "request", r.ID, "state", r.State, "error", err)
		}
	}
}

// Serve runs the desk until the context ends: workers on ready requests,
// waiting requests read again, each repository's tools kept in step with
// its main branch, and captured context dropped when it is old.
func (d *Desk) Serve(ctx context.Context) {
	work, wait, tools, tidy := time.NewTicker(3*time.Second), time.NewTicker(30*time.Second), time.NewTicker(5*time.Minute), time.NewTicker(time.Hour)
	defer work.Stop()
	defer wait.Stop()
	defer tools.Stop()
	defer tidy.Stop()
	d.SyncAllTools(ctx)
	d.expire(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-work.C:
			d.Tick(ctx)
		case <-wait.C:
			d.PollAll(ctx)
		case <-tools.C:
			d.SyncAllTools(ctx)
		case <-tidy.C:
			d.expire(ctx)
		}
	}
}

// expire drops the captured context and the files of requests older than
// the program keeps them. A file is deleted from disk before the request
// stops naming it, so one that could not be deleted is tried again.
func (d *Desk) expire(ctx context.Context) {
	before := d.now().AddDate(0, 0, -d.prog.Limits.ContextDays)
	n, err := d.store.ExpireContext(ctx, before)
	if err != nil {
		d.log.Error("old context not dropped", "error", err)
		return
	}
	if n > 0 {
		d.log.Info("old captured context dropped", "requests", n)
	}
	ids, err := d.store.Attached(ctx, before)
	if err != nil {
		d.log.Error("old files not read", "error", err)
		return
	}
	for _, id := range ids {
		if err := d.stored().Drop(id); err != nil {
			d.log.Error("old files not dropped", "request", id, "error", err)
			continue
		}
		if err := d.store.ForgetAttachments(ctx, id); err != nil {
			d.log.Error("old files not forgotten", "request", id, "error", err)
		}
	}
	if len(ids) > 0 {
		d.log.Info("old files dropped", "requests", len(ids))
	}
}

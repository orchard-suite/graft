// Package web serves Graft's four ports. The page's port trusts the
// front door's headers. The token port trusts an intake app's token alone.
// The handle port, on loopback, trusts only the secret of a request an
// agent is working on. The forge's port trusts only the secret its webhook
// carries. No port reads another's credential.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/platformkit/v3/door"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/auth"
	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

//go:embed static
var static embed.FS

// Desk is what the ports ask of the desk.
type Desk interface {
	Submit(ctx context.Context, s desk.Submission) (*store.Request, error)
	Lanes(ctx context.Context, person string) ([]string, error)
	Approves(ctx context.Context, person string, repo *program.Repository) (bool, error)
	Stop(ctx context.Context, repo *program.Repository, by, reason string) error
	Start(ctx context.Context, repo *program.Repository, by string) error
	ByHandle(ctx context.Context, secret string) (*store.Request, error)
	Tools(ctx context.Context, r *store.Request) (desk.ToolList, error)
	CallTool(ctx context.Context, r *store.Request, id, env string, inputs map[string]any) (*desk.ToolResult, error)
	AddEvidence(ctx context.Context, r *store.Request, ref, note string) (int, error)
	LogPath(id string) string
	Attachment(r *store.Request, id string) (attach.File, []byte, error)
	Heard(ctx context.Context, repo string, number int) bool
	Retry(ctx context.Context, id, by string) (*store.Request, error)
	MayRetry(ctx context.Context, r *store.Request, person string) (bool, error)
}

type Server struct {
	store   *store.Store
	desk    Desk
	prog    *program.Program
	log     *slog.Logger
	version string
	// origin is the page's own origin; a change on the page's port must
	// come from it. Empty checks nothing, as in local development.
	origin string
	// hookSecret is the file that holds the secret the forge's webhook
	// carries.
	hookSecret string
}

func New(st *store.Store, d Desk, prog *program.Program, log *slog.Logger, version, publicURL string) *Server {
	return &Server{store: st, desk: d, prog: prog, log: log, version: version, origin: strings.TrimRight(publicURL, "/")}
}

// route is one API operation: a "METHOD /path" pattern and its handler.
// openapi.yaml at the repository root describes every one of them.
type route struct {
	pattern string
	handler http.HandlerFunc
}

// sessionRoutes are the page's routes, for a person the front door signed in.
func (s *Server) sessionRoutes() []route {
	return []route{
		{"GET /api/me", s.me},
		{"GET /api/requests", s.listRequests},
		{"POST /api/requests", s.createRequest},
		{"GET /api/requests/{id}", s.getRequest},
		{"GET /api/requests/{id}/attachments/{attachmentId}", s.getAttachment},
		{"GET /api/requests/{id}/events", s.requestEvents},
		{"GET /api/requests/{id}/evidence", s.requestEvidence},
		{"GET /api/requests/{id}/log", s.requestLog},
		{"POST /api/requests/{id}/retry", s.retryRequest},
		{"POST /api/repositories/stop", s.stopRepository},
		{"POST /api/repositories/start", s.startRepository},
	}
}

// intakeRoutes are the token port's routes, for an intake app.
func (s *Server) intakeRoutes() []route {
	return []route{
		{"POST /v1/requests", s.intake},
		{"GET /v1/requests", s.intakeList},
		{"GET /v1/requests/{id}", s.intakeGet},
		{"GET /v1/requests/{id}/attachments/{attachmentId}", s.intakeAttachment},
	}
}

// Session builds the page's port. protect must leave a door.Person on every
// request it lets through.
func (s *Server) Session(protect func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.sessionRoutes() {
		var h http.Handler = rt.handler
		if s.origin != "" && !strings.HasPrefix(rt.pattern, "GET ") {
			h = door.SameOrigin(s.origin, s.log, h)
		}
		mux.Handle(rt.pattern, protect(h))
	}
	// Any other API path still meets the gate before it is not found.
	mux.Handle("/api/", protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such API route")
	})))
	mux.HandleFunc("GET /healthz", healthz)
	files, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServerFS(files))
	return s.logged(mux)
}

// Intake builds the token port. protect must leave the app's name on every
// request it lets through.
func (s *Server) Intake(protect func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.intakeRoutes() {
		mux.Handle(rt.pattern, protect(rt.handler))
	}
	mux.Handle("/", protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such API route")
	})))
	mux.HandleFunc("GET /healthz", healthz)
	return s.logged(mux)
}

func healthz(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }

// logged logs one line per API call: its route, its status, its time, and,
// for a call that read one request, who the reader was to it (reader).
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}
		var as reader
		r = r.WithContext(context.WithValue(r.Context(), readerKey{}, &as))
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" && (strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || rec.code >= 400) {
			attrs := []any{"method", r.Method, "route", r.Pattern, "status", rec.code, "ms", time.Since(start).Milliseconds()}
			if as != "" {
				attrs = append(attrs, "reader", string(as))
			}
			s.log.InfoContext(r.Context(), "request", attrs...)
		}
	})
}

type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

// View is a request as the person who made it may see it: what they sent,
// where it stands, and the answer. It leaves out what the agents read and
// wrote.
type View struct {
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	State     string          `json:"state"`
	Ended     bool            `json:"ended"`
	Repo      string          `json:"repo"`
	App       string          `json:"app"`
	Lane      string          `json:"lane"`
	Title     string          `json:"title,omitempty"`
	Text      string          `json:"text"`
	Answer    string          `json:"answer,omitempty"`
	Ticket    *store.Ticket   `json:"ticket,omitempty"`
	Incident  *store.Incident `json:"incident,omitempty"`
	Links     []store.Link    `json:"links,omitempty"`
	// Attachments are the files sent with the request, while it keeps them.
	Attachments []attach.File `json:"attachments"`
	// Plan is where the request's plan is decided, and how it was.
	Plan *PlanView `json:"plan,omitempty"`
}

// PlanView is a request's plan as the person who asked sees it: where it
// is read and decided in the hub, and the last decision, by whom, when,
// and with what note.
type PlanView struct {
	Round     int    `json:"round"`
	URL       string `json:"url,omitempty"`
	Decision  string `json:"decision,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
	DecidedAt string `json:"decidedAt,omitempty"`
	Note      string `json:"note,omitempty"`
}

func planView(r *store.Request) *PlanView {
	if r.PlanRound == 0 {
		return nil
	}
	return &PlanView{Round: r.PlanRound, URL: r.RunURL, Decision: r.Decision, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt, Note: r.DecisionNote}
}

func view(r *store.Request) View {
	files := r.Attachments
	if files == nil {
		files = []attach.File{}
	}
	return View{ID: r.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, State: r.State, Ended: store.Ended(r.State), Repo: r.Repo, App: r.App, Lane: r.Lane,
		Title: r.Title, Text: r.Text, Answer: r.Answer, Ticket: r.Ticket, Incident: r.Incident, Links: r.Links, Attachments: files, Plan: planView(r)}
}

func views(rs []*store.Request) []View {
	out := make([]View, 0, len(rs))
	for _, r := range rs {
		out = append(out, view(r))
	}
	return out
}

// Full is a request as the people who read all of it see it: the view,
// and everything the desk kept.
type Full struct {
	View
	// Detail is everything the desk kept, for its whole readers alone.
	Detail *store.Doc `json:"detail,omitempty"`
	// Staff says the reader sees the whole request: they run its
	// repository, or decide its plans.
	Staff bool `json:"staff"`
	// Retry is the stage that failed and waits to be tried again, and why,
	// for whole readers.
	Retry *store.Stall `json:"retry,omitempty"`
	// MayRetry says the reader may try it again: one of its approvers, and
	// not the person who asked.
	MayRetry bool `json:"mayRetry"`
	// Spend is what the request's agents spent, for whole readers alone:
	// never the person who asked (ADR-6004).
	Spend *store.Spend `json:"spend,omitempty"`
}

// reach is what a person may do in one repository beyond their own
// requests.
type reach struct {
	// runs: they hold a lane above the lowest, so they may stop and start
	// the repository, and read every request in it.
	runs bool
	// approves: the repository's approvers let them in, so they decide its
	// plans, and read every request in it.
	approves bool
}

// reads says the person reads every request in the repository, whole.
func (r reach) reads() bool { return r.runs || r.approves }

// reaches works out, for each repository, what the person may do there
// beyond their own requests. Who runs a repository is by lane, which is the
// program's: every repository or none. Who approves is by the repository's
// approvers, or the program's; repositories that share them cost one ask.
func (s *Server) reaches(ctx context.Context, person string) (map[string]reach, error) {
	lanes, err := s.desk.Lanes(ctx, person)
	if err != nil {
		return nil, err
	}
	runs := s.isStaff(lanes)
	out := map[string]reach{}
	for i := range s.prog.Repositories {
		repo := &s.prog.Repositories[i]
		approves, err := s.desk.Approves(ctx, person, repo)
		if err != nil {
			return nil, err
		}
		out[repo.Path] = reach{runs: runs, approves: approves}
	}
	return out, nil
}

// isStaff says one of the lanes is above the lowest.
func (s *Server) isStaff(lanes []string) bool {
	for _, l := range lanes {
		if s.prog.LaneRank(l) > 0 {
			return true
		}
	}
	return false
}

type repoInfo struct {
	Path    string   `json:"path"`
	Lanes   []string `json:"lanes"`
	Lane    string   `json:"lane"`
	Staff   bool     `json:"staff"`
	Stopped bool     `json:"stopped"`
	Reason  string   `json:"reason,omitempty"`
}

// me says who the page is signed in as, which Graft it is talking to, the
// lanes they hold on each repository, and the repositories whose every
// request they read.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	person := door.From(r.Context()).Email
	repos := []repoInfo{}
	lanes, err := s.desk.Lanes(r.Context(), person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reaches, err := s.reaches(r.Context(), person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reads := []string{}
	for _, repo := range s.prog.Repositories {
		if reaches[repo.Path].reads() {
			reads = append(reads, repo.Path)
		}
	}
	for i := range s.prog.Repositories {
		repo := &s.prog.Repositories[i]
		if len(lanes) == 0 {
			break
		}
		info := repoInfo{Path: repo.Path, Lanes: lanes, Lane: s.prog.Choose(lanes), Staff: s.isStaff(lanes)}
		if sw, err := s.store.Switch(r.Context(), repo.Path); err == nil {
			info.Stopped, info.Reason = sw.Stopped, sw.Reason
		}
		repos = append(repos, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": person, "version": s.version, "program": s.prog.Name, "repositories": repos, "reads": reads})
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	person := door.From(r.Context()).Email
	reaches, err := s.reaches(r.Context(), person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := store.Filter{Person: person, Limit: 200}
	for repo, can := range reaches {
		if can.reads() {
			f.Repos = append(f.Repos, repo)
		}
	}
	rs, err := s.store.List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, views(rs))
}

func (s *Server) createRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repository string `json:"repository"`
		Lane       string `json:"lane"`
		Text       string `json:"text"`
	}
	files, ok := readRequest(w, r, &in)
	if !ok {
		return
	}
	s.submit(w, r, desk.Submission{Person: door.From(r.Context()).Email, App: program.PageApp, Repository: in.Repository, Lane: in.Lane, Text: in.Text, Attachments: files})
}

// submit takes a request in and answers with its view, or with why not.
func (s *Server) submit(w http.ResponseWriter, r *http.Request, sub desk.Submission) {
	req, err := s.desk.Submit(r.Context(), sub)
	var invalid desk.Invalid
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, desk.ErrNoLane):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, desk.ErrTooMany):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case err != nil:
		s.fail(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, view(req))
	}
}

// reader is who a person is to one request they read, from the most they
// may do: they run its repository, they decide its plans, they asked for
// it, or none of these.
type reader string

const (
	readerStaff     reader = "staff"
	readerApprover  reader = "approver"
	readerRequester reader = "requester"
	readerNone      reader = "none"
)

// whole says the reader reads the whole request: what the agents found and
// planned, the steps, the evidence, and the log.
func (r reader) whole() bool { return r == readerStaff || r == readerApprover }

type readerKey struct{}

// noteReader puts the reader on the call's log line.
func noteReader(ctx context.Context, as reader) {
	if slot, ok := ctx.Value(readerKey{}).(*reader); ok {
		*slot = as
	}
}

// readerOf is who the person is to the request.
func readerOf(person string, req *store.Request, can reach) reader {
	switch {
	case can.runs:
		return readerStaff
	case can.approves:
		return readerApprover
	case strings.EqualFold(req.Person, person):
		return readerRequester
	}
	return readerNone
}

// readable reads the request named in the path for the signed-in person:
// their own, any in a repository they help run, and any in a repository
// whose plans they decide. Anyone else is told it does not exist. as says
// who they are to it.
func (s *Server) readable(w http.ResponseWriter, r *http.Request) (req *store.Request, as reader, ok bool) {
	person := door.From(r.Context()).Email
	req, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such request")
		return nil, "", false
	}
	if err != nil {
		s.fail(w, r, err)
		return nil, "", false
	}
	reaches, err := s.reaches(r.Context(), person)
	if err != nil {
		s.fail(w, r, err)
		return nil, "", false
	}
	as = readerOf(person, req, reaches[req.Repo])
	noteReader(r.Context(), as)
	countRead(r.Context(), as)
	if as == readerNone {
		// Not theirs to know exists.
		writeError(w, http.StatusNotFound, "no such request")
		return nil, as, false
	}
	return req, as, true
}

// getAttachment answers one file of a request the signed-in person may read.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	req, _, ok := s.readable(w, r)
	if !ok {
		return
	}
	s.sendFile(w, r, req)
}

// fileCSP is the only policy a file is shown under: nothing runs, nothing
// loads but the file itself, no form posts, and no other site frames it.
const fileCSP = "default-src 'none'; img-src 'self'; object-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// sendFile answers one file of a request as the kind its own bytes say it
// is: a picture or a PDF is shown in place, under a policy that runs
// nothing and loads nothing else. Bytes that are neither are only ever
// downloaded, as bytes.
func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, req *store.Request) {
	f, data, err := s.desk.Attachment(req, r.PathValue("attachmentId"))
	if errors.Is(err, attach.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	kind, shown := attach.Kind(data)
	disposition := "inline"
	if !shown {
		s.log.WarnContext(r.Context(), "a kept file is not a picture or a PDF; sent as bytes to download", "request", req.ID, "file", f.ID)
		kind, disposition = "application/octet-stream", "attachment"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", fileCSP)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	req, as, ok := s.readable(w, r)
	if !ok {
		return
	}
	full, err := s.full(r.Context(), req, as)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

// full is the request as the reader may see it: whole for its whole
// readers, with what a retry would run again and whether they may ask it.
func (s *Server) full(ctx context.Context, req *store.Request, as reader) (Full, error) {
	if !as.whole() {
		return Full{View: view(req)}, nil
	}
	may, err := s.desk.MayRetry(ctx, req, door.From(ctx).Email)
	if err != nil {
		return Full{}, err
	}
	spend, err := s.store.Spend(ctx, req.ID)
	if err != nil {
		return Full{}, err
	}
	return Full{View: view(req), Detail: &req.Doc, Staff: true, Retry: desk.Retryable(req), MayRetry: may, Spend: &spend}, nil
}

// retryRequest tries the stage of a request that failed again, for one of
// its approvers. The desk judges them afresh, as it judges any decision.
func (s *Server) retryRequest(w http.ResponseWriter, r *http.Request) {
	req, as, ok := s.readable(w, r)
	if !ok {
		return
	}
	got, err := s.desk.Retry(r.Context(), req.ID, door.From(r.Context()).Email)
	switch {
	case errors.Is(err, desk.ErrMayNotRetry):
		writeError(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, desk.ErrNothingToRetry), errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "nothing on this request waits to be tried again")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	full, err := s.full(r.Context(), got, as)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

// wholeOnly reads the request named in the path for someone who reads all
// of it: who helps run its repository, or decides its plans. What the
// agents read and wrote is theirs alone to see.
func (s *Server) wholeOnly(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	req, as, ok := s.readable(w, r)
	if !ok {
		return nil, false
	}
	if !as.whole() {
		writeError(w, http.StatusForbidden, "only the people who run this repository or decide its plans see this")
		return nil, false
	}
	return req, true
}

func (s *Server) requestEvents(w http.ResponseWriter, r *http.Request) {
	req, ok := s.wholeOnly(w, r)
	if !ok {
		return
	}
	ev, err := s.store.Events(r.Context(), req.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ev == nil {
		ev = []store.Event{}
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) requestEvidence(w http.ResponseWriter, r *http.Request) {
	req, ok := s.wholeOnly(w, r)
	if !ok {
		return
	}
	ev, err := s.store.Evidence(r.Context(), req.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ev == nil {
		ev = []store.Evidence{}
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) requestLog(w http.ResponseWriter, r *http.Request) {
	req, ok := s.wholeOnly(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, err := os.Open(s.desk.LogPath(req.ID))
	if err != nil {
		_, _ = w.Write([]byte("no log yet\n"))
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

// switchable reads the repository a stop or a start names, for someone who
// helps run it.
func (s *Server) switchable(w http.ResponseWriter, r *http.Request) (*program.Repository, string, string, bool) {
	var in struct {
		Repository string `json:"repository"`
		Reason     string `json:"reason"`
	}
	if !readJSON(w, r, &in) {
		return nil, "", "", false
	}
	person := door.From(r.Context()).Email
	repo := s.prog.Repository(in.Repository)
	reaches, err := s.reaches(r.Context(), person)
	if err != nil {
		s.fail(w, r, err)
		return nil, "", "", false
	}
	if repo == nil || !reaches[repo.Path].runs {
		writeError(w, http.StatusForbidden, "only the people who run a repository stop or start it")
		return nil, "", "", false
	}
	return repo, person, in.Reason, true
}

// stopRepository flips a repository's stop switch: nothing ships there
// until it is started again.
func (s *Server) stopRepository(w http.ResponseWriter, r *http.Request) {
	repo, person, reason, ok := s.switchable(w, r)
	if !ok {
		return
	}
	if err := s.desk.Stop(r.Context(), repo, person, reason); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo.Path, "stopped": true})
}

func (s *Server) startRepository(w http.ResponseWriter, r *http.Request) {
	repo, person, _, ok := s.switchable(w, r)
	if !ok {
		return
	}
	if err := s.desk.Start(r.Context(), repo, person); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo.Path, "stopped": false})
}

// intake takes a request from an intake app. The app's own server says who
// is signed in; the token says which app it is, and the program file says
// which repository and environment that app reports against.
func (s *Server) intake(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Person     string         `json:"person"`
		Text       string         `json:"text"`
		Repository string         `json:"repository"`
		Lane       string         `json:"lane"`
		Context    *store.Context `json:"context"`
	}
	files, ok := readRequest(w, r, &in)
	if !ok {
		return
	}
	app := auth.App(r.Context())
	entry, err := s.prog.IntakeFor(app, in.Repository)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	person := strings.ToLower(strings.TrimSpace(in.Person))
	if !strings.Contains(person, "@") || strings.ContainsAny(person, " \t\n") {
		writeError(w, http.StatusBadRequest, "person is the sign-in address of whoever is signed in to the app")
		return
	}
	s.submit(w, r, desk.Submission{Person: person, App: app, Repository: entry.Repository, Environment: entry.Environment, Lane: in.Lane, Text: in.Text, Context: in.Context, Attachments: files})
}

// asked reads the person an intake app asks about. The app is trusted to
// say who is signed in to it, and sees only what it sent itself.
func (s *Server) asked(w http.ResponseWriter, r *http.Request) (string, bool) {
	person := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("person")))
	if person == "" {
		writeError(w, http.StatusBadRequest, "name the person: ?person=<their sign-in address>")
		return "", false
	}
	return person, true
}

func (s *Server) intakeList(w http.ResponseWriter, r *http.Request) {
	person, ok := s.asked(w, r)
	if !ok {
		return
	}
	rs, err := s.store.List(r.Context(), store.Filter{Person: person, Limit: 100})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	app := auth.App(r.Context())
	mine := rs[:0]
	for _, req := range rs {
		if req.App == app {
			mine = append(mine, req)
		}
	}
	writeJSON(w, http.StatusOK, views(mine))
}

func (s *Server) intakeGet(w http.ResponseWriter, r *http.Request) {
	if req, ok := s.sent(w, r); ok {
		writeJSON(w, http.StatusOK, view(req))
	}
}

// intakeAttachment answers one file of a request the app sent for the
// person it names.
func (s *Server) intakeAttachment(w http.ResponseWriter, r *http.Request) {
	if req, ok := s.sent(w, r); ok {
		s.sendFile(w, r, req)
	}
}

// sent reads the request named in the path, when the calling app sent it
// for the person it names; anything else is not there, to this app.
func (s *Server) sent(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	person, ok := s.asked(w, r)
	if !ok {
		return nil, false
	}
	req, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return nil, false
	}
	if err != nil || req.Person != person || req.App != auth.App(r.Context()) {
		writeError(w, http.StatusNotFound, "no such request")
		return nil, false
	}
	return req, true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "request failed", "route", r.Pattern, "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// jsonMax is the most a request's JSON may hold.
const jsonMax = 256 << 10

// readRequest reads a new request: a JSON body, or a multipart form whose
// part `request` is that same JSON and whose parts `attachments` are its
// files. It reads no file past its limit, and answers 400 itself, in words
// a person can read, when it cannot take what was sent.
func readRequest(w http.ResponseWriter, r *http.Request, v any) ([]attach.Upload, bool) {
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind != "multipart/form-data" {
		return nil, readJSON(w, r, v)
	}
	r.Body = http.MaxBytesReader(w, r.Body, attach.MaxTotal+jsonMax+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "the form could not be read")
		return nil, false
	}
	var files []attach.Upload
	total, gotRequest := 0, false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusBadRequest, "the files together are larger than 25 MB; send fewer or smaller ones")
			return nil, false
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "the form could not be read")
			return nil, false
		}
		switch part.FormName() {
		case "request":
			if gotRequest {
				writeError(w, http.StatusBadRequest, "send the request part once")
				return nil, false
			}
			gotRequest = true
			if err := json.NewDecoder(io.LimitReader(part, jsonMax)).Decode(v); err != nil {
				writeError(w, http.StatusBadRequest, "the request part must be JSON")
				return nil, false
			}
		case "attachments":
			if len(files) == attach.MaxFiles {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("send at most %d files with one request", attach.MaxFiles))
				return nil, false
			}
			name := attach.Name(part.FileName(), len(files)+1)
			data, err := io.ReadAll(io.LimitReader(part, attach.MaxFile+1))
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusBadRequest, "the files together are larger than 25 MB; send fewer or smaller ones")
				return nil, false
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, "the form could not be read")
				return nil, false
			}
			if len(data) > attach.MaxFile {
				writeError(w, http.StatusBadRequest, name+" is larger than 10 MB; send a smaller picture or PDF")
				return nil, false
			}
			if total += len(data); total > attach.MaxTotal {
				writeError(w, http.StatusBadRequest, "the files together are larger than 25 MB; send fewer or smaller ones")
				return nil, false
			}
			files = append(files, attach.Upload{Name: part.FileName(), Data: data})
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("the form has a part %q; it takes request and attachments", part.FormName()))
			return nil, false
		}
	}
	if !gotRequest {
		writeError(w, http.StatusBadRequest, "the form needs a request part: the request's JSON")
		return nil, false
	}
	return files, true
}

// readJSON decodes a small JSON body, answering 400 itself when it cannot.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, jsonMax)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be JSON")
		return false
	}
	return true
}

// writeError answers with the API's one error shape, openapi.yaml's Error.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

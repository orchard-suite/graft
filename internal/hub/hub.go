// Package hub is Graft's side of the catalog hub. The program file says
// who holds each lane and who decides a plan, as grants; the hub judges
// those grants on each person's own record. The hub holds the rest of
// access: which tool tiers a lane carries, and the door every tool call goes
// through. Graft holds no cloud key and decides no tier; it asks the hub.
// A plan's run is Graft's own, asked for the person who asked, who is kept
// on it as data.
//
// It uses two credentials. The source token declares Graft's own actions
// and writes its entities. The machine access token asks for runs, lends
// tool calls, reports, and reads where a person may ask.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/program"
)

// RequestBlueprint is the catalog blueprint a request is written as.
const RequestBlueprint = "change_request"

// laneTTL is how long an answer about a person's lanes is kept.
const laneTTL = time.Minute

type Hub struct {
	prog *program.Program
	// machine asks, lends, reports, and reads; source declares and writes.
	machine *orrery.Client
	source  *orrery.Client
	public  string
	log     *slog.Logger

	mu    sync.Mutex
	lanes map[string]held
	// admits keeps Admits' answers, by address and grants.
	admits map[string]admitted
	// offered says the request blueprint was offered to the hub.
	offered bool
}

type held struct {
	at    time.Time
	lanes []string
}

type admitted struct {
	at time.Time
	in bool
}

// New returns the hub at url. public is where a person opens the hub.
func New(prog *program.Program, url, machineToken, sourceToken, public string, log *slog.Logger) (*Hub, error) {
	if url == "" || machineToken == "" || sourceToken == "" {
		return nil, errors.New("hub: the address and both tokens are required")
	}
	machine := orrery.New(url, machineToken, orrery.WithLogger(log))
	source := orrery.New(url, sourceToken, orrery.WithLogger(log))
	return &Hub{prog: prog, machine: machine, source: source, public: strings.TrimRight(public, "/"), log: log, lanes: map[string]held{}, admits: map[string]admitted{}}, nil
}

// RunURL is the hub page of a run, or empty when Graft was not told
// where people open the hub.
func (h *Hub) RunURL(run string) string {
	if h.public == "" || run == "" {
		return ""
	}
	return h.public + "/actions/runs/" + run
}

// Alive tells the hub that Graft's source is up. The source writes a
// request only when one moves, so without this a quiet day reads as a
// source that stopped.
func (h *Hub) Alive(ctx context.Context) error {
	return h.source.Heartbeat(ctx)
}

// Lanes lists the lanes a person holds in the program, lowest first: each
// lane whose members the hub says let them in, or the fallback lane when
// none does. What the person may see in the hub plays no part. The answer
// is kept for a minute.
func (h *Hub) Lanes(ctx context.Context, email string) ([]string, error) {
	h.mu.Lock()
	got, ok := h.lanes[email]
	h.mu.Unlock()
	if ok && time.Since(got.at) <= laneTTL {
		return got.lanes, nil
	}
	lanes, err := h.prog.Held(func(l *program.Lane) (bool, error) { return h.LetsIn(ctx, email, l.Members) })
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.lanes[email] = held{at: time.Now(), lanes: lanes}
	h.mu.Unlock()
	return lanes, nil
}

// blueprints are the blueprints the program's repositories are records of.
func (h *Hub) blueprints() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range h.prog.Repositories {
		bp, _, _ := strings.Cut(r.Entity, ":")
		if !seen[bp] {
			seen[bp] = true
			out = append(out, bp)
		}
	}
	sort.Strings(out)
	return out
}

// DeclareLanes puts one action per lane on the hub. A plan approval is a
// run of a lane's action; who may ask it, and whether it waits, is the
// access rule an admin sets there, which a source's save never touches.
func (h *Hub) DeclareLanes(ctx context.Context) error {
	for _, lane := range h.prog.Lanes {
		id := h.prog.LaneAction(lane.ID)
		action := orrery.Action{
			"title":       fmt.Sprintf("Graft %s: %s lane", h.prog.Name, lane.ID),
			"description": fmt.Sprintf("A request in the %s lane of %s. Graft asks each run as itself, for the person whose request it is, who is named on the run and needs no access here; Graft's program file says who holds the lane. A run of it is one round of a plan: read its brief, and the build plan and the files sent with the request and on its ticket where you need them, then approve it, decline it, or send it back with a note of what to change. A comment of yours on the request's ticket is planned into the next round too, which revises this run, with any picture or PDF you attach to it. A note on a decline goes to the person who asked, as the reason. Graft names the approvers who alone decide each plan that waits; the person who asked never decides it.", lane.ID, h.prog.Name),
			"group":       "Graft",
			"blueprint":   h.blueprints(),
			"form":        false,
			"serial":      []string{"request"},
			// The hub lays a run's inputs out in this order: the brief leads
			// the run, open; the files follow it; the build plan is folded.
			"inputs": orderedInputs{
				{"brief", map[string]any{"type": "markdown", "title": "The brief"}},
				{"files", map[string]any{"type": "markdown", "title": "Files"}},
				{"plan", map[string]any{"type": "attachment", "title": "The build plan"}},
				{"request", map[string]any{"type": "string", "title": "Request", "required": true}},
				{"title", map[string]any{"type": "string", "title": "What was asked"}},
				{"round", map[string]any{"type": "number", "title": "Plan round"}},
				{"kind", map[string]any{"type": "string", "title": "Kind of change"}},
				{"size", map[string]any{"type": "string", "title": "Size"}},
				{"link", map[string]any{"type": "url", "title": "The request in Graft"}},
			},
			"backend": map[string]any{"type": "caller", "timeout": "24h"},
		}
		if err := h.source.PutAction(ctx, id, action); err != nil {
			return fmt.Errorf("lane action %s: %w", id, err)
		}
	}
	h.log.Info("lane actions declared", "lanes", len(h.prog.Lanes))
	return nil
}

// orderedInputs are an action's inputs as a JSON object in the order given,
// which is the order the hub lays them out in.
type orderedInputs []struct {
	name string
	def  map[string]any
}

func (o orderedInputs) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, in := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(in.name)
		if err != nil {
			return nil, err
		}
		def, err := json.Marshal(in.def)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(def)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// SyncTools makes the hub's tool actions for one repository match its
// repository file: one action per run tool and environment tier the program
// names a runner for. was lists the actions declared before; one no longer
// declared is deleted. It returns the actions declared now.
func (h *Hub) SyncTools(ctx context.Context, repo *program.Repository, file *program.RepoFile, was []string) ([]string, error) {
	bp, _, _ := strings.Cut(repo.Entity, ":")
	var now []string
	for _, t := range file.Tools {
		if len(t.Run) == 0 {
			continue
		}
		for _, env := range []string{program.Production, program.NonProduction} {
			runner := h.prog.Runners[t.RunsAs].For(env)
			if runner == "" {
				continue
			}
			id := h.prog.ToolAction(repo, t.ID, env)
			title := t.Title
			if title == "" {
				title = t.ID
			}
			backend := map[string]any{"type": "runner", "runner": runner, "command": t.Run}
			if t.Timeout != "" {
				backend["timeout"] = t.Timeout
			}
			inputs := map[string]any{}
			for name, def := range t.Inputs {
				inputs[name] = def
			}
			action := orrery.Action{
				"title":       fmt.Sprintf("%s (%s, %s)", title, repo.Path, env),
				"description": t.Description,
				"group":       "Graft tools",
				"blueprint":   bp,
				"inputs":      inputs,
				"backend":     backend,
				"tool":        map[string]any{"tier": t.Tier, "from": repo.Entity},
			}
			if err := h.source.PutAction(ctx, id, action); err != nil {
				return nil, fmt.Errorf("tool action %s: %w", id, err)
			}
			now = append(now, id)
		}
	}
	keep := map[string]bool{}
	for _, id := range now {
		keep[id] = true
	}
	for _, id := range was {
		if keep[id] {
			continue
		}
		if err := h.source.Call(ctx, http.MethodDelete, "/v1/actions/"+id, nil, nil); err != nil && status(err) != http.StatusNotFound {
			return nil, fmt.Errorf("remove tool action %s: %w", id, err)
		}
		h.log.Info("tool action removed", "action", id, "repo", repo.Path)
	}
	sort.Strings(now)
	return now, nil
}

func status(err error) int {
	var api *orrery.APIError
	if errors.As(err, &api) {
		return api.Status
	}
	return 0
}

// PlanAsk is one plan to ask about.
type PlanAsk struct {
	Lane   string
	Entity string
	// Person is whose request it is: kept on the run as data, never its
	// asker.
	Person  string
	Request string
	Inputs  map[string]any
	// Tiers are the tool tiers the build needs beyond the lane's.
	Tiers []string
	// Revises is the run this plan replaces.
	Revises string
	// Approvers decide the plan, as the hub's grants on a person. Named,
	// the run waits for one of them; empty, it goes ahead unless the hub
	// holds it for a tier.
	Approvers []program.Grant
}

// PlanRun is a plan's run as the hub answers it, with the approvers it is
// held to: the people who alone decide it, empty from a hub that holds it
// to none; and the person it was asked for.
type PlanRun struct {
	orrery.ActionRun
	Approvers    []program.Grant `json:"approvers"`
	RequestedFor string          `json:"requestedFor"`
}

// planAsk is a run Graft asks as itself for a person, with the approvers
// who decide it.
type planAsk struct {
	orrery.RunRequest
	RequestedFor string          `json:"requestedFor"`
	Approvers    []program.Grant `json:"approvers,omitempty"`
}

// AskPlan asks the lane's action on the repository's entity, as Graft's
// own machine identity, for the person whose request it is. The hub's
// access rule on the action lets Graft in; the person needs none, sees
// nothing, never decides the run, and hears nothing of it from the hub.
// With approvers named the run waits for one of them.
func (h *Hub) AskPlan(ctx context.Context, ask PlanAsk) (*PlanRun, error) {
	ref, err := orrery.ParseRef(ask.Entity)
	if err != nil {
		return nil, err
	}
	inputs := map[string]any{"request": ask.Request}
	for k, v := range ask.Inputs {
		inputs[k] = v
	}
	var run PlanRun
	req := planAsk{RunRequest: orrery.RunRequest{Entity: &ref, Inputs: inputs, Tiers: ask.Tiers, Revises: ask.Revises}, RequestedFor: ask.Person, Approvers: ask.Approvers}
	if err := h.machine.Call(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(h.prog.LaneAction(ask.Lane))+"/run", req, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// LetsIn asks the hub whether grants let one person in, as it judges them
// now: whether a person is a lane's member, and Graft's own check of a
// plan's decider.
func (h *Hub) LetsIn(ctx context.Context, address string, grants []program.Grant) (bool, error) {
	var out struct {
		LetsIn bool `json:"letsIn"`
	}
	if err := h.machine.Call(ctx, http.MethodPost, "/v1/access/lets-in", map[string]any{"address": address, "grants": grants}, &out); err != nil {
		return false, err
	}
	return out.LetsIn, nil
}

// Admits is LetsIn as the hub judged it within the last minute. It is for
// reading, such as whether a person may read a request because they decide
// its plan; a decision is checked afresh with LetsIn.
func (h *Hub) Admits(ctx context.Context, address string, grants []program.Grant) (bool, error) {
	shape, err := json.Marshal(grants)
	if err != nil {
		return false, err
	}
	key := address + "\x00" + string(shape)
	h.mu.Lock()
	got, ok := h.admits[key]
	h.mu.Unlock()
	if ok && time.Since(got.at) <= laneTTL {
		return got.in, nil
	}
	in, err := h.LetsIn(ctx, address, grants)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	h.admits[key] = admitted{at: time.Now(), in: in}
	h.mu.Unlock()
	return in, nil
}

// Run reads a plan's run again.
func (h *Hub) Run(ctx context.Context, id string) (*PlanRun, error) {
	var run PlanRun
	if err := h.machine.Call(ctx, http.MethodGet, "/v1/action-runs/"+url.PathEscape(id), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (h *Hub) Report(ctx context.Context, id string, report orrery.ActionReport) error {
	return h.machine.ReportActionRun(ctx, id, report)
}

func (h *Hub) Withdraw(ctx context.Context, id, reason string) error {
	return h.machine.WithdrawRun(ctx, id, reason)
}

// Lend says whose request a tool call serves.
type Lend struct {
	Person string
	Lane   string
	Entity string
	// Run is the approved plan's run, which may carry more tiers.
	Run string
}

func (h *Hub) lend(l Lend) (orrery.EntityRef, *orrery.Lend, error) {
	ref, err := orrery.ParseRef(l.Entity)
	if err != nil {
		return ref, nil, err
	}
	return ref, &orrery.Lend{Person: l.Person, Action: h.prog.LaneAction(l.Lane), Entity: ref, Run: l.Run}, nil
}

// Tools lists the hub actions a lent call may reach on the entity.
func (h *Hub) Tools(ctx context.Context, l Lend) ([]orrery.Tool, error) {
	ref, lend, err := h.lend(l)
	if err != nil {
		return nil, err
	}
	return h.machine.Tools(ctx, ref, lend)
}

// CallTool calls one hub action through the tool door, lent to the
// person's request. The hub checks the tier on every call.
func (h *Hub) CallTool(ctx context.Context, action string, inputs map[string]any, l Lend) (*orrery.ToolAnswer, error) {
	ref, lend, err := h.lend(l)
	if err != nil {
		return nil, err
	}
	return h.machine.CallTool(ctx, action, orrery.ToolCall{Entity: &ref, Inputs: inputs, As: lend})
}

// Refused says the hub refused a tool call, and why in plain words: the
// tier is not held, or no runner serves the tool there.
func Refused(err error) string {
	switch status(err) {
	case http.StatusForbidden:
		return "this request does not hold the tier this tool needs"
	case http.StatusConflict:
		return "no runner serves this tool here"
	case http.StatusNotFound:
		return "the hub does not know this tool or this person"
	}
	return ""
}

// Needs lists the catalog keys an entity reaches by the named relations.
func (h *Hub) Needs(ctx context.Context, entity string, relations []string) ([]string, error) {
	if len(relations) == 0 {
		return nil, nil
	}
	ref, err := orrery.ParseRef(entity)
	if err != nil {
		return nil, err
	}
	var rec orrery.Record
	if err := h.machine.Call(ctx, http.MethodGet, "/v1/entities/"+ref.Blueprint+"/"+ref.Key, nil, &rec); err != nil {
		return nil, err
	}
	var out []string
	for _, rel := range relations {
		for _, target := range rec.Relations[rel] {
			out = append(out, target.Key)
		}
	}
	return out, nil
}

// Request is a request as the catalog shows it. Plan is the page of its
// plan's run in the hub; Decision, DecidedBy, and DecidedAt say how the
// last plan was decided, and by whom.
type Request struct {
	ID, Title, State, Lane, Sort, Verdict string
	// Person is who asked for the change.
	Person                         string
	Entity, URL, Ticket, Plan      string
	Decision, DecidedBy, DecidedAt string
	// RequestedAt is when the person asked, UpdatedAt when the request last
	// changed, and EndedAt when it ended, zero while it is worked or waits.
	RequestedAt, UpdatedAt, EndedAt time.Time
	// Round is the plan's round, 0 before a plan is asked.
	Round int
	// Runs lists the request's runs as Markdown, each linking to its
	// comment on the ticket.
	Runs string
	// Spend is what the request's agents spent, as the runtime reported.
	Spend Spend
}

// Spend is a request's spend as its record shows it: the total, each stage
// apart, and how many agent runs it took. AgentRuns 0 shows none of it.
type Spend struct {
	TotalUSD, ScreenUSD, PlanUSD, BuildUSD float64
	AgentRuns                              int
}

// requestBlueprint is the change_request blueprint Graft offers. The hub
// creates it, or adds to the one it holds the fields it lacks.
func (h *Hub) requestBlueprint() orrery.Blueprint {
	bp := orrery.Blueprint{
		"id": RequestBlueprint, "title": "Change request", "icon": "git-pull-request",
		"fields": map[string]any{
			"state":        map[string]any{"type": "string", "title": "State"},
			"lane":         map[string]any{"type": "string", "title": "Lane"},
			"requested_by": map[string]any{"type": "user", "title": "Requested by", "description": "Who asked for the change, as the app or Graft's page signed them in. They decide nothing about it."},
			"sort":         map[string]any{"type": "string", "title": "Sort"},
			"verdict":      map[string]any{"type": "string", "title": "Verdict"},
			"url":          map[string]any{"type": "url", "title": "In Graft"},
			"ticket":       map[string]any{"type": "url", "title": "Ticket"},
			"plan":         map[string]any{"type": "url", "title": "Plan", "description": "The page in this hub where the request's plan is read and decided: its run of the lane's action. An approval record links here by its own address."},
			"decision":     map[string]any{"type": "string", "title": "Decision", "description": "How the last plan was decided: approved, declined, or returned (sent back with a note)."},
			"decided_by":   map[string]any{"type": "user", "title": "Decided by", "description": "Who decided the last plan, as the hub signed them in."},
			"decided_at":   map[string]any{"type": "datetime", "title": "Decided at"},
			"requested_at": map[string]any{"type": "datetime", "title": "Requested at", "description": "When the person asked for the change."},
			"updated_at":   map[string]any{"type": "datetime", "title": "Updated at", "description": "When the request last changed in Graft: a step, a decision, or a round. This record's own last write is the hub's Last update."},
			"ended_at":     map[string]any{"type": "datetime", "title": "Ended at", "description": "When the request ended: answered, filed, released, declined, or stopped for a person. Empty while it is worked or waits."},
			"round":        map[string]any{"type": "number", "title": "Plan round", "description": "Which round of its plan the request is on: each answer the approvers give is planned as the next round. Empty before a plan is asked."},
			"runs":         map[string]any{"type": "markdown", "title": "Runs", "description": "Every run of the request, oldest first: each investigation, round, build, landing, and release, with how it ended and a link to its comment on the ticket, where it shows what it did, its screenshots, and why it stopped if it did."},
			"cost_usd": costField("Cost", "What the request's agent runs have spent so far, in US dollars, as the agent runtime reported each one: the screen, every round of the plan, and every builder round. "+
				"It grows as each run ends. A run the runtime reported nothing of adds nothing; Graft's page counts those."),
			"cost_screen_usd": costField("Cost: screen", "What the one call that sorted the request spent, in US dollars, as the agent runtime reported."),
			"cost_plan_usd":   costField("Cost: plan", "What every round of the plan spent, in US dollars, as the agent runtime reported: the investigation, each revision that answered the approvers, and each retry of either."),
			"cost_build_usd":  costField("Cost: build", "What every builder and prover run spent, in US dollars, as the agent runtime reported: each round of the build, each prover that proved it, each round that repaired what the gate or the pipeline found, and each retry."),
			"agent_runs": map[string]any{"type": "number", "title": "Agent runs", "description": "How many agent runs the request took: one a prompt Graft sent an agent, from the screen to each builder round, " +
				"whether it finished, failed, timed out, or was stopped at its budget."},
		},
	}
	if bps := h.blueprints(); len(bps) == 1 {
		bp["relations"] = map[string]any{"repository": map[string]any{"target": bps[0]}}
	}
	return bp
}

// costField is a number field of US dollars. Every cost field's name starts
// cost_, so one field class in the hub's access policy holds them all.
func costField(title, description string) map[string]any {
	return map[string]any{"type": "number", "title": title, "description": description, "format": map[string]any{"kind": "currency", "currency": "USD"}}
}

// offer brings the change_request blueprint to the hub, once.
func (h *Hub) offer(ctx context.Context) error {
	h.mu.Lock()
	offered := h.offered
	h.mu.Unlock()
	if offered {
		return nil
	}
	if _, err := h.source.OfferBlueprints(ctx, []orrery.Blueprint{h.requestBlueprint()}); err != nil {
		return fmt.Errorf("offer %s: %w", RequestBlueprint, err)
	}
	h.mu.Lock()
	h.offered = true
	h.mu.Unlock()
	return nil
}

// stamp is a time as the catalog keeps a datetime, or empty for none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// entity is a request as a change_request record.
func (h *Hub) entity(r Request) orrery.Entity {
	fields := map[string]any{"state": r.State, "lane": r.Lane}
	for k, v := range map[string]string{"requested_by": r.Person, "sort": r.Sort, "verdict": r.Verdict, "url": r.URL, "ticket": r.Ticket, "plan": r.Plan, "decision": r.Decision, "decided_by": r.DecidedBy, "decided_at": r.DecidedAt,
		"requested_at": stamp(r.RequestedAt), "updated_at": stamp(r.UpdatedAt), "ended_at": stamp(r.EndedAt), "runs": r.Runs} {
		if v != "" {
			fields[k] = v
		}
	}
	if r.Round > 0 {
		fields["round"] = r.Round
	}
	if sp := r.Spend; sp.AgentRuns > 0 {
		fields["cost_usd"], fields["cost_screen_usd"], fields["cost_plan_usd"], fields["cost_build_usd"] = cents(sp.TotalUSD), cents(sp.ScreenUSD), cents(sp.PlanUSD), cents(sp.BuildUSD)
		fields["agent_runs"] = sp.AgentRuns
	}
	e := orrery.Entity{Blueprint: RequestBlueprint, Key: h.prog.Name + "-" + r.ID, Title: r.Title, Fields: fields}
	if len(h.blueprints()) == 1 {
		e.Relations = map[string]any{"repository": r.Entity}
	}
	return e
}

// cents is dollars to the cent, as the record shows them. Graft's store
// keeps the runtime's own figure, to its fractions of a cent.
func cents(usd float64) float64 { return math.Round(usd*100) / 100 }

// PutRequest writes a request as a catalog record. The record is a view:
// a hub that refuses it loses nothing Graft needs.
func (h *Hub) PutRequest(ctx context.Context, r Request) error {
	if err := h.offer(ctx); err != nil {
		return err
	}
	return h.source.Put(ctx, h.entity(r))
}

// requestsScope is the scope of the runs that write many requests at once.
// Graft never commits a snapshot, so no run of it marks a record missing.
const requestsScope = "change-requests"

// PutRequests writes many requests as catalog records in one incremental
// run, two hundred a call: what it leaves out stays as it was, and a record
// that holds what it is sent already costs the hub nothing. It says how
// many records the hub took.
func (h *Hub) PutRequests(ctx context.Context, rs []Request) (int, error) {
	if len(rs) == 0 {
		return 0, nil
	}
	if err := h.offer(ctx); err != nil {
		return 0, err
	}
	res, err := h.source.Incremental(ctx, requestsScope, func(add func(orrery.Entity) error) error {
		for _, r := range rs {
			if err := add(h.entity(r)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if res.Counts.Rejected > 0 {
		h.log.Warn("change requests refused by the hub", "rejected", res.Counts.Rejected, "sent", len(rs))
	}
	return len(rs) - res.Counts.Rejected, nil
}

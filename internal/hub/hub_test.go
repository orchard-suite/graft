package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/graft/internal/program"
)

const file = `
program: acme
forge: { kind: gitlab, url: https://gitlab.example.com }
committer: { email: graft@example.com }
repositories:
- { path: acme/api, entity: "repository:acme-api" }
approvers:
- { path: teams, values: [platform] }
lanes:
- id: user
  fallback: true
- id: developer
  members: [{ path: teams, values: [builders] }]
runners:
  reader: { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
  operator: { nonproduction: acme-operator-nonprod }
`

// fakeHub records every call and judges a grant by the teams it holds for
// each person.
type fakeHub struct {
	mu      sync.Mutex
	calls   []string
	bodies  map[string]map[string]any
	raw     map[string]string
	auth    map[string]string
	teams   map[string][]string
	answers map[string]string
	// store, when set, keeps each entity's fields as the hub would, by
	// blueprint/key.
	store map[string]map[string]any
}

func setup(t *testing.T, teams map[string][]string) (*Hub, *fakeHub, *program.Program) {
	t.Helper()
	prog, err := program.Parse([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	fk := &fakeHub{bodies: map[string]map[string]any{}, raw: map[string]string{}, auth: map[string]string{}, teams: teams}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fk.mu.Lock()
		defer fk.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		fk.calls = append(fk.calls, key)
		fk.auth[key] = r.Header.Get("Authorization")
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			fk.bodies[key] = body
			fk.raw[key] = string(raw)
		}
		if fk.store != nil {
			fk.keep(r.Method, r.URL.Path, fk.raw[key])
		}
		if strings.HasSuffix(r.URL.Path, "/targets") {
			t.Errorf("a lane is never read from where the hub lets a person ask: %s", r.URL)
		}
		if a, ok := fk.answers[key]; ok {
			_, _ = w.Write([]byte(a))
			return
		}
		if key == "POST /v1/access/lets-in" && fk.teams != nil {
			body := fk.bodies[key]
			in := false
			for _, g := range body["grants"].([]any) {
				for _, v := range g.(map[string]any)["values"].([]any) {
					for _, team := range fk.teams[body["address"].(string)] {
						in = in || team == v
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"letsIn": in})
			return
		}
		if a, ok := fk.answers[key]; ok {
			_, _ = w.Write([]byte(a))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	h, err := New(prog, srv.URL, "machine", "source", "https://hub.example.com/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h, fk, prog
}

// keep stores what a write of entities sends, as the hub keeps it.
func (fk *fakeHub) keep(method, path, raw string) {
	type entity struct {
		Blueprint string         `json:"blueprint"`
		Key       string         `json:"key"`
		Fields    map[string]any `json:"fields"`
	}
	switch {
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/ingest/entities/"):
		var e entity
		_ = json.Unmarshal([]byte(raw), &e)
		fk.store[strings.TrimPrefix(path, "/v1/ingest/entities/")] = e.Fields
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/ingest/runs/") && strings.HasSuffix(path, "/entities"):
		var batch struct {
			Entities []entity `json:"entities"`
		}
		_ = json.Unmarshal([]byte(raw), &batch)
		for _, e := range batch.Entities {
			fk.store[e.Blueprint+"/"+e.Key] = e.Fields
		}
	}
}

func TestALaneIsHeldByItsMembersNeverByWhatThePersonSees(t *testing.T) {
	h, fk, _ := setup(t, map[string][]string{"ada@example.com": {"builders"}})
	ctx := context.Background()
	got, err := h.Lanes(ctx, "ada@example.com")
	if err != nil || strings.Join(got, ",") != "developer" {
		t.Fatalf("a member: %v %v", got, err)
	}
	if body := fk.bodies["POST /v1/access/lets-in"]; body["address"] != "ada@example.com" || fk.auth["POST /v1/access/lets-in"] != "Bearer machine" {
		t.Errorf("membership is asked of the hub with the machine token: %v %v", body, fk.auth)
	}
	// Someone the hub lets see nothing, in no team, holds the fallback lane.
	if got, err := h.Lanes(ctx, "amanda@example.com"); err != nil || strings.Join(got, ",") != "user" {
		t.Fatalf("anyone else: %v %v", got, err)
	}
	before := len(fk.calls)
	if _, err := h.Lanes(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(fk.calls) != before {
		t.Errorf("the second ask within a minute read the hub again")
	}
}

func TestDeclareLanesLeavesAccessToTheAdmin(t *testing.T) {
	h, fk, _ := setup(t, nil)
	if err := h.DeclareLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := fk.bodies["PUT /v1/actions/graft_acme_developer"]
	if body == nil {
		t.Fatalf("no lane action put: %v", fk.calls)
	}
	if _, has := body["access"]; has {
		t.Error("a lane action must not carry access: who holds a lane is the admin's to set in the hub")
	}
	if body["form"] != false || body["backend"].(map[string]any)["type"] != "caller" {
		t.Errorf("lane action: %v", body)
	}
	if fk.auth["PUT /v1/actions/graft_acme_developer"] != "Bearer source" {
		t.Error("actions are declared with the source token")
	}
}

func TestSyncToolsDeclaresPerTierAndRemovesWhatIsGone(t *testing.T) {
	h, fk, prog := setup(t, nil)
	repo := prog.Repository("acme/api")
	f, err := prog.ParseRepoFile([]byte(`
tools:
- { id: queue-depth, tier: read, runs_as: reader, run: [az, servicebus, queue, show] }
- { id: flush, tier: act, runs_as: operator, run: [make, flush] }
- { id: restart, tier: act, action: restart_service }
`))
	if err != nil {
		t.Fatal(err)
	}
	now, err := h.SyncTools(context.Background(), repo, f, []string{"graft_acme_acme_api_old_prod", "graft_acme_acme_api_flush_nonprod"})
	if err != nil {
		t.Fatal(err)
	}
	want := "graft_acme_acme_api_flush_nonprod,graft_acme_acme_api_queue_depth_nonprod,graft_acme_acme_api_queue_depth_prod"
	if strings.Join(now, ",") != want {
		t.Errorf("declared %v", now)
	}
	prod := fk.bodies["PUT /v1/actions/graft_acme_acme_api_queue_depth_prod"]
	if prod["backend"].(map[string]any)["runner"] != "acme-reader-prod" || prod["tool"].(map[string]any)["from"] != "repository:acme-api" || prod["tool"].(map[string]any)["tier"] != "read" {
		t.Errorf("production tool: %v", prod)
	}
	joined := strings.Join(fk.calls, "\n")
	if !strings.Contains(joined, "DELETE /v1/actions/graft_acme_acme_api_old_prod") {
		t.Error("a tool no longer declared was not removed")
	}
	if strings.Contains(joined, "DELETE /v1/actions/graft_acme_acme_api_flush_nonprod") || strings.Contains(joined, "flush_prod") {
		t.Errorf("flush has a nonproduction runner only: %s", joined)
	}
}

func TestToolCallIsLentToThePerson(t *testing.T) {
	h, fk, _ := setup(t, nil)
	_, err := h.CallTool(context.Background(), "graft_acme_acme_api_queue_depth_prod", map[string]any{"queue": "orders"},
		Lend{Person: "a@example.com", Lane: "developer", Entity: "repository:acme-api", Run: "run1"})
	if err != nil {
		t.Fatal(err)
	}
	body := fk.bodies["POST /v1/tools/graft_acme_acme_api_queue_depth_prod/call"]
	as, _ := body["as"].(map[string]any)
	if as["person"] != "a@example.com" || as["action"] != "graft_acme_developer" || as["run"] != "run1" {
		t.Errorf("lend: %v", body)
	}
	if h.RunURL("run1") != "https://hub.example.com/actions/runs/run1" {
		t.Errorf("run url: %s", h.RunURL("run1"))
	}
}

func TestAliveSaysTheSourceIsUpWithTheSourceToken(t *testing.T) {
	h, fk, _ := setup(t, nil)
	if err := h.Alive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fk.auth["POST /v1/ingest/heartbeat"] != "Bearer source" {
		t.Errorf("the heartbeat goes as the source: %v %v", fk.calls, fk.auth)
	}
}

func TestAPlanIsAskedByGraftForThePersonWithItsApprovers(t *testing.T) {
	h, fk, _ := setup(t, nil)
	fk.answers = map[string]string{
		"POST /v1/actions/graft_acme_user/run": `{"id":"run1","action":"graft_acme_user","state":"awaiting","by":"a@example.com","decidedBy":null,"note":null,"approvers":[{"path":"teams","values":["platform"]}]}`,
		"GET /v1/action-runs/run1":             `{"id":"run1","state":"queued","by":"a@example.com","decidedBy":"lead@example.com","decidedAt":"2026-11-18T15:00:00.000Z","note":"Go ahead.","approvers":[{"path":"teams","values":["platform"]}]}`,
		"POST /v1/access/lets-in":              `{"letsIn":true}`,
	}
	team := []program.Grant{{Path: "teams", Values: []string{"platform"}}}
	asked, err := h.AskPlan(context.Background(), PlanAsk{Lane: "user", Entity: "repository:acme-api", Person: "a@example.com", Request: "r1", Inputs: map[string]any{"plan": "the plan"}, Approvers: team})
	if err != nil || asked.ID != "run1" || len(asked.Approvers) != 1 || asked.Approvers[0].Values[0] != "platform" {
		t.Fatalf("asked: %+v %v", asked, err)
	}
	body := fk.bodies["POST /v1/actions/graft_acme_user/run"]
	sent, _ := body["approvers"].([]any)
	if _, asks := body["for"]; asks || body["requestedFor"] != "a@example.com" || len(sent) != 1 || sent[0].(map[string]any)["path"] != "teams" {
		t.Errorf("the run is Graft's, for the person, never asked as them: %v", body)
	}
	if fk.auth["POST /v1/actions/graft_acme_user/run"] != "Bearer machine" {
		t.Error("a plan is asked with Graft's machine token")
	}
	got, err := h.Run(context.Background(), "run1")
	if err != nil || got.DecidedBy != "lead@example.com" || got.Note != "Go ahead." || len(got.Approvers) != 1 {
		t.Fatalf("run: %+v %v", got, err)
	}
	ok, err := h.LetsIn(context.Background(), "lead@example.com", team)
	check := fk.bodies["POST /v1/access/lets-in"]
	if err != nil || !ok || check["address"] != "lead@example.com" || fk.auth["POST /v1/access/lets-in"] != "Bearer machine" {
		t.Errorf("lets in: %v %v %v", ok, err, check)
	}
}

func TestAPlanThatGoesAheadUnaskedNamesNoApprovers(t *testing.T) {
	h, fk, _ := setup(t, nil)
	if _, err := h.AskPlan(context.Background(), PlanAsk{Lane: "developer", Entity: "repository:acme-api", Person: "ada@example.com", Request: "r2"}); err != nil {
		t.Fatal(err)
	}
	body := fk.bodies["POST /v1/actions/graft_acme_developer/run"]
	if _, named := body["approvers"]; named || body["requestedFor"] != "ada@example.com" {
		t.Errorf("ask: %v", body)
	}
}

func TestARequestRecordNamesWhoAskedForIt(t *testing.T) {
	h, fk, _ := setup(t, nil)
	if err := h.PutRequest(context.Background(), Request{ID: "r1", Person: "amanda@example.com", Title: "the form", State: "received", Lane: "user", Entity: "repository:acme-api"}); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	for key, body := range fk.bodies {
		if strings.HasPrefix(key, "PUT /v1/ingest/entities/change_request/") {
			put = body
		}
	}
	if fields, _ := put["fields"].(map[string]any); fields["requested_by"] != "amanda@example.com" {
		t.Errorf("record: %v (calls %v)", put, fk.calls)
	}
}

func TestALanesRunLeadsWithTheBriefAndFoldsTheBuildPlan(t *testing.T) {
	h, fk, _ := setup(t, nil)
	if err := h.DeclareLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := "PUT /v1/actions/graft_acme_user"
	inputs := fk.bodies[key]["inputs"].(map[string]any)
	if brief := inputs["brief"].(map[string]any); brief["type"] != "markdown" {
		t.Errorf("the brief is shown open on the run: %v", brief)
	}
	if plan := inputs["plan"].(map[string]any); plan["type"] != "attachment" || plan["title"] != "The build plan" {
		t.Errorf("the build plan is folded on the run: %v", plan)
	}
	// The hub lays a run out in the order its action names its inputs.
	raw := fk.raw[key]
	at := func(name string) int { return strings.Index(raw, `"`+name+`":{`) }
	if !(at("brief") >= 0 && at("brief") < at("files") && at("files") < at("plan") && at("plan") < at("request")) {
		t.Errorf("inputs out of order: %s", raw)
	}
	if !strings.Contains(fk.bodies[key]["description"].(string), "A comment of yours on the request's ticket is planned into the next round") {
		t.Errorf("the lane does not tell deciders a comment counts: %v", fk.bodies[key]["description"])
	}
}

func TestANextRoundRevisesTheRunOfTheRoundBefore(t *testing.T) {
	h, fk, _ := setup(t, nil)
	fk.answers = map[string]string{"POST /v1/actions/graft_acme_user/run": `{"id":"run2","state":"awaiting"}`}
	if _, err := h.AskPlan(context.Background(), PlanAsk{Lane: "user", Entity: "repository:acme-api", Person: "a@example.com", Request: "r1", Revises: "run1"}); err != nil {
		t.Fatal(err)
	}
	if got := fk.bodies["POST /v1/actions/graft_acme_user/run"]["revises"]; got != "run1" {
		t.Errorf("revises %v", got)
	}
}

func TestARequestRecordCarriesItsTimesAndItsRound(t *testing.T) {
	h, fk, _ := setup(t, nil)
	asked := time.Date(2026, 11, 18, 15, 4, 5, 999, time.UTC)
	r := Request{ID: "r1", Person: "a@example.com", Title: "the form", State: "released", Lane: "user", Entity: "repository:acme-api",
		RequestedAt: asked, UpdatedAt: asked.Add(time.Hour), EndedAt: asked.Add(time.Hour), Round: 2}
	if err := h.PutRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fields := fk.bodies["PUT /v1/ingest/entities/change_request/acme-r1"]["fields"].(map[string]any)
	want := map[string]any{"requested_at": "2026-11-18T15:04:05Z", "updated_at": "2026-11-18T16:04:05Z", "ended_at": "2026-11-18T16:04:05Z", "round": float64(2)}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %v, want %v", k, fields[k], v)
		}
	}
	offered := fk.bodies["PUT /v1/ingest/blueprints"]["blueprints"].([]any)[0].(map[string]any)["fields"].(map[string]any)
	for name, title := range map[string]string{"requested_at": "Requested at", "updated_at": "Updated at", "ended_at": "Ended at", "round": "Plan round"} {
		f, _ := offered[name].(map[string]any)
		if f["title"] != title || (name != "round" && f["type"] != "datetime") || (name == "round" && f["type"] != "number") {
			t.Errorf("offered %s: %v", name, f)
		}
	}
	// A request that is still worked has no end, and before its plan no round.
	r.ID, r.EndedAt, r.Round = "r2", time.Time{}, 0
	if err := h.PutRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fields = fk.bodies["PUT /v1/ingest/entities/change_request/acme-r2"]["fields"].(map[string]any)
	if _, has := fields["ended_at"]; has {
		t.Errorf("a request still worked has an end: %v", fields)
	}
	if _, has := fields["round"]; has {
		t.Errorf("a request with no plan has a round: %v", fields)
	}
}

func TestWritingEveryRequestFillsARecordAnOlderGraftPushed(t *testing.T) {
	h, fk, _ := setup(t, nil)
	// The hub keeps one record per key: a put replaces its fields, and a run's entities replace theirs.
	held := map[string]map[string]any{}
	fk.store = held
	fk.answers = map[string]string{
		"POST /v1/ingest/runs":              `{"id": "run9"}`,
		"POST /v1/ingest/runs/run9/commit":  `{"id": "run9", "state": "committed", "counts": {"received": 2, "updated": 2}}`,
		"PUT /v1/ingest/runs/run9/entities": `{"accepted": 2}`,
	}
	// What Graft 6.0.1 wrote: no time, no round.
	old := `{"title": "the form", "fields": {"state": "awaiting_plan", "lane": "user", "requested_by": "a@example.com", "plan": "https://hub.example.com/actions/runs/run1"}, "relations": {"repository": "repository:acme-api"}}`
	if err := h.source.Call(context.Background(), http.MethodPut, "/v1/ingest/entities/change_request/acme-r1", json.RawMessage(old), nil); err != nil {
		t.Fatal(err)
	}
	asked := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	took, err := h.PutRequests(context.Background(), []Request{
		{ID: "r1", Person: "a@example.com", Title: "the form", State: "awaiting_plan", Lane: "user", Entity: "repository:acme-api", Plan: "https://hub.example.com/actions/runs/run1", RequestedAt: asked, UpdatedAt: asked.Add(time.Hour), Round: 1},
		{ID: "r2", Person: "b@example.com", Title: "the page", State: "answered", Lane: "user", Entity: "repository:acme-api", RequestedAt: asked, UpdatedAt: asked.Add(2 * time.Hour), EndedAt: asked.Add(2 * time.Hour)},
	})
	if err != nil || took != 2 {
		t.Fatalf("took %d: %v", took, err)
	}
	run := fk.bodies["POST /v1/ingest/runs"]
	if run["mode"] != "incremental" || run["scope"] != "change-requests" {
		t.Errorf("a run that may never mark a record missing: %v", run)
	}
	r1 := held["change_request/acme-r1"]
	if r1["requested_at"] != "2026-11-10T09:00:00Z" || r1["updated_at"] != "2026-11-10T10:00:00Z" || r1["round"] != float64(1) || r1["plan"] != "https://hub.example.com/actions/runs/run1" {
		t.Errorf("the older record: %v", r1)
	}
	if r2 := held["change_request/acme-r2"]; r2["ended_at"] != "2026-11-10T11:00:00Z" {
		t.Errorf("an ended request: %v", r2)
	}
	if _, offered := fk.bodies["PUT /v1/ingest/blueprints"]; !offered {
		t.Error("the fields were not offered before they were written")
	}
}

func TestARequestRecordCarriesWhatItsAgentsSpent(t *testing.T) {
	h, fk, _ := setup(t, nil)
	r := Request{ID: "r1", Person: "a@example.com", Title: "the form", State: "building", Lane: "user", Entity: "repository:acme-api",
		Spend: Spend{TotalUSD: 4.6612, ScreenUSD: 0.0112, PlanUSD: 2.35, BuildUSD: 2.30, AgentRuns: 7}}
	if err := h.PutRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fields := fk.bodies["PUT /v1/ingest/entities/change_request/acme-r1"]["fields"].(map[string]any)
	for k, v := range map[string]float64{"cost_usd": 4.66, "cost_screen_usd": 0.01, "cost_plan_usd": 2.35, "cost_build_usd": 2.30, "agent_runs": 7} {
		if fields[k] != v {
			t.Errorf("%s = %v, want %v", k, fields[k], v)
		}
	}
	offered := fk.bodies["PUT /v1/ingest/blueprints"]["blueprints"].([]any)[0].(map[string]any)["fields"].(map[string]any)
	for _, name := range []string{"cost_usd", "cost_screen_usd", "cost_plan_usd", "cost_build_usd"} {
		f, _ := offered[name].(map[string]any)
		format, _ := f["format"].(map[string]any)
		if f["type"] != "number" || f["title"] == "" || f["description"] == "" || format["kind"] != "currency" || format["currency"] != "USD" {
			t.Errorf("offered %s: %v", name, f)
		}
		if _, carries := f["access"]; carries {
			t.Errorf("offered %s carries access, which is the hub admin's to set: %v", name, f)
		}
	}
	if f, _ := offered["agent_runs"].(map[string]any); f["type"] != "number" || f["title"] != "Agent runs" || f["description"] == "" {
		t.Errorf("offered agent_runs: %v", f)
	}

	// The record moves as more runs end.
	r.Spend = Spend{TotalUSD: 5.10, ScreenUSD: 0.01, PlanUSD: 2.35, BuildUSD: 2.74, AgentRuns: 8}
	if err := h.PutRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fields = fk.bodies["PUT /v1/ingest/entities/change_request/acme-r1"]["fields"].(map[string]any)
	if fields["cost_usd"] != 5.10 || fields["cost_build_usd"] != 2.74 || fields["agent_runs"] != float64(8) {
		t.Errorf("the record after another run: %v", fields)
	}

	// A request no agent has run for yet shows no cost at all.
	r.ID, r.Spend = "r2", Spend{}
	if err := h.PutRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fields = fk.bodies["PUT /v1/ingest/entities/change_request/acme-r2"]["fields"].(map[string]any)
	for _, k := range []string{"cost_usd", "agent_runs"} {
		if _, has := fields[k]; has {
			t.Errorf("a request no agent ran for has %s: %v", k, fields)
		}
	}
}

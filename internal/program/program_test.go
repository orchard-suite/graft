package program

import (
	"strings"
	"testing"
)

const sample = `
program: acme
trusted: false
forge:
  kind: gitlab
  url: https://gitlab.example.com/
committer:
  email: graft@example.com
owner:
  group: platform
approvers:
- { path: teams, values: [platform] }
repositories:
- path: acme/api
  entity: repository:acme-api
  protected: [infrastructure/, graft.yaml, deploy/prod.yaml]
- path: acme/web
  entity: repository:acme-web
  service: web
  owner: { person: owner@example.com }
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
  members: [{ path: teams, values: [platform] }, { values: [lead@example.com] }]
limits:
  spend_usd: 8
runners:
  reader:   { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
  operator: { production: acme-operator-prod }
intake:
- app: acme-web
  repository: acme/web
  environment: production
  paths: [web/portal/, ./shared/portal/, README.md]
  tag: web-portal-v{version}
- app: acme-admin
  repository: acme/web
  environment: nonproduction
- app: acme-admin
  repository: acme/api
  environment: nonproduction
hours:
  schedule: platform
incidents:
  needs: [depends_on]
`

func parse(t *testing.T, doc string) *Program {
	t.Helper()
	p, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFillsDefaults(t *testing.T) {
	p := parse(t, sample)
	if p.Forge.URL != "https://gitlab.example.com" {
		t.Errorf("forge url = %q", p.Forge.URL)
	}
	if p.Committer.Name != "Graft" {
		t.Errorf("committer name = %q", p.Committer.Name)
	}
	l := p.Limits
	if l.SmallFixLines != 150 || l.SpendUSD != 8 || l.Working != 2 || l.PlanRounds != 3 || l.RequestsPerHour != 10 || l.ContextDays != 30 {
		t.Errorf("limits = %+v", l)
	}
	if p.Notices != NoticesSend || p.Holding() {
		t.Errorf("notices = %q: a program sends its notices unless it says hold", p.Notices)
	}
	if held := parse(t, strings.Replace(sample, "trusted: false", "trusted: false\nnotices: hold", 1)); !held.Holding() {
		t.Error("notices: hold does not hold")
	}
	api := p.Repository("ACME/api")
	if api == nil || api.Service != "acme-api" {
		t.Fatalf("api = %+v", api)
	}
	if got := strings.Join(api.Protected, ","); got != "graft.yaml,infrastructure/,deploy/prod.yaml" {
		t.Errorf("protected = %s", got)
	}
	web := p.Repository("acme/web")
	if got := strings.Join(web.Protected, ","); got != "graft.yaml" {
		t.Errorf("the repository file is always protected, got %s", got)
	}
	approvers := parse(t, strings.Replace(sample, "  service: web\n", "  service: web\n  approvers: [{ values: [lead@example.com] }]\n", 1))
	if got := approvers.ApproversOf(approvers.Repository("acme/api")); len(got) != 1 || got[0].Path != "teams" || got[0].Values[0] != "platform" {
		t.Errorf("the program's approvers: %+v", got)
	}
	if got := approvers.ApproversOf(approvers.Repository("acme/web")); len(got) != 1 || got[0].Path != "" || got[0].Values[0] != "lead@example.com" {
		t.Errorf("a repository's own approvers: %+v", got)
	}
	if p.OwnerOf(web).Person != "owner@example.com" || p.OwnerOf(api).Group != "platform" {
		t.Errorf("owners: web %+v api %+v", p.OwnerOf(web), p.OwnerOf(api))
	}
}

func TestProtectedIn(t *testing.T) {
	api := parse(t, sample).Repository("acme/api")
	got := api.ProtectedIn([]string{"src/a.go", "infrastructure/main.tf", "infrastructure", "deploy/prod.yaml", "deploy/prod.yaml.bak", "graft.yaml"})
	if strings.Join(got, ",") != "infrastructure/main.tf,deploy/prod.yaml,graft.yaml" {
		t.Errorf("protected in = %v", got)
	}
}

func TestLanesAndActions(t *testing.T) {
	p := parse(t, sample)
	if p.LaneRank("user") != 0 || p.LaneRank("architect") != 2 || p.LaneRank("nobody") != -1 {
		t.Error("lane ranks are wrong")
	}
	if got := p.LaneAction("developer"); got != "graft_acme_developer" {
		t.Errorf("lane action = %s", got)
	}
	if got := p.ToolAction(p.Repository("acme/api"), "queue-depth", Production); got != "graft_acme_acme_api_queue_depth_prod" {
		t.Errorf("tool action = %s", got)
	}
	if got := p.ToolAction(p.Repository("acme/api"), "queue-depth", NonProduction); got != "graft_acme_acme_api_queue_depth_nonprod" {
		t.Errorf("tool action = %s", got)
	}
}

func TestChoosePicksTheHighestLaneHeld(t *testing.T) {
	p := parse(t, sample)
	for held, want := range map[string]string{"user": "user", "developer": "developer", "developer,architect": "architect", "architect": "architect", "": ""} {
		var lanes []string
		if held != "" {
			lanes = strings.Split(held, ",")
		}
		if got := p.Choose(lanes); got != want {
			t.Errorf("holding %q: chose %q, want %q", held, got, want)
		}
	}
	if _, err := Parse([]byte(strings.Replace(sample, "- id: developer\n  small_fix: true", "- id: developer\n  fallback: true", 1))); err == nil {
		t.Error("a lane above the lowest parsed as the fallback")
	}
}

func TestALaneIsHeldByItsMembersAndTheFallbackByEveryoneElse(t *testing.T) {
	p := parse(t, sample)
	teams := map[string]map[string]bool{
		"lead@example.com":  {"platform": true, "builders": true},
		"ada@example.com":   {"builders": true},
		"staff@example.com": {},
	}
	held := func(who string) string {
		lanes, err := p.Held(func(l *Lane) (bool, error) {
			for _, g := range l.Members {
				for _, v := range g.Values {
					if (g.Path == "teams" && teams[who][v]) || (g.Path == "" && v == who) {
						return true, nil
					}
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lanes, ",")
	}
	for who, want := range map[string]string{"lead@example.com": "developer,architect", "ada@example.com": "developer", "staff@example.com": "user", "stranger@example.com": "user"} {
		if got := held(who); got != want {
			t.Errorf("%s holds %q, want %q", who, got, want)
		}
	}
	if got := p.Choose(strings.Split(held("lead@example.com"), ",")); got != "architect" {
		t.Errorf("the platform owner works in %q", got)
	}
	if !p.Lane("developer").Waits() || p.Lane("architect").Waits() || !p.Lane("user").Waits() {
		t.Error("a lane waits for its approvers unless it says approval: false")
	}
	// With no fallback, someone no lane names holds none.
	bare := parse(t, strings.Replace(sample, "  fallback: true\n", "  members: [{ path: teams, values: [support] }]\n", 1))
	if lanes, _ := bare.Held(func(*Lane) (bool, error) { return false, nil }); len(lanes) != 0 {
		t.Errorf("no fallback: holds %v", lanes)
	}
}

func TestIntakeFor(t *testing.T) {
	p := parse(t, sample)
	in, err := p.IntakeFor("acme-web", "")
	if err != nil || in.Repository != "acme/web" || in.Environment != Production {
		t.Fatalf("one entry: %+v %v", in, err)
	}
	if _, err := p.IntakeFor("acme-admin", ""); err == nil {
		t.Error("an app with two entries must name its repository")
	}
	if in, err := p.IntakeFor("acme-admin", "acme/api"); err != nil || in.Repository != "acme/api" {
		t.Errorf("named entry: %+v %v", in, err)
	}
	if _, err := p.IntakeFor("acme-web", "acme/api"); err == nil {
		t.Error("an app may not report against a repository it is not listed for")
	}
	if _, err := p.IntakeFor("stranger", ""); err == nil {
		t.Error("an unlisted app may not report")
	}
}

func TestAnIntakeAppKeepsItsPathsAndTagAsContext(t *testing.T) {
	p := parse(t, sample)
	web, err := p.IntakeFor("acme-web", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(web.Paths, " "); got != "web/portal/ shared/portal/ README.md" {
		t.Errorf("paths = %s", got)
	}
	if got := web.Tags("v1.4.0"); len(got) != 1 || got[0] != "web-portal-v1.4.0" {
		t.Errorf("tags = %v", got)
	}
	admin, err := p.IntakeFor("acme-admin", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if got := admin.Tags("1.4.0"); strings.Join(got, " ") != "v1.4.0 1.4.0" {
		t.Errorf("default tags = %v", got)
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string][2]string{
		"an unknown key":              {"trusted: false", "trusted: false\nplan_approval: false"},
		"a notices word it lacks":     {"trusted: false", "trusted: false\nnotices: mute"},
		"an approver with no values":  {"{ path: teams, values: [platform] }", "{ path: teams }"},
		"a lane with no members":      {"  members: [{ path: teams, values: [builders] }]\n", ""},
		"a member with no values":     {"members: [{ path: teams, values: [builders] }]", "members: [{ path: teams }]"},
		"a fallback with members":     {"  fallback: true", "  fallback: true\n  members: [{ values: [a@example.com] }]"},
		"a fallback that never waits": {"  fallback: true", "  fallback: true\n  approval: false"},
		"small_fix where none waits":  {"  approval: false", "  approval: false\n  small_fix: true"},
		"waiting with no approvers":   {"approvers:\n- { path: teams, values: [platform] }\n", ""},
		"a forge it does not speak":   {"kind: gitlab", "kind: bitbucket"},
		"no committer address":        {"  email: graft@example.com", "  name: Graft"},
		"a path outside":              {"infrastructure/,", "../other/,"},
		"a lane twice":                {"- id: developer", "- id: user"},
		"an unlisted intake repo":     {"  repository: acme/api\n  environment: nonproduction", "  repository: acme/other\n  environment: nonproduction"},
		"an unknown environment":      {"environment: production", "environment: staging"},
		"two owners":                  {"  group: platform", "  group: platform\n  person: a@example.com"},
		"a bad entity":                {"entity: repository:acme-api", "entity: acme-api"},
		"a program no action fits":    {"program: acme", "program: 9lives"},
		"an app path outside":         {"web/portal/,", "../web/portal/,"},
		"a tag with no version":       {"tag: web-portal-v{version}", "tag: web-portal"},
		"a tag git refuses":           {"tag: web-portal-v{version}", "tag: web portal v{version}"},
		"an app named for the page":   {"- app: acme-admin\n  repository: acme/web", "- app: graft\n  repository: acme/web"},
	}
	for name, c := range cases {
		doc := strings.Replace(sample, c[0], c[1], 1)
		if doc == sample {
			t.Fatalf("%s: the sample has no %q", name, c[0])
		}
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestRepoFile(t *testing.T) {
	p := parse(t, sample)
	f, err := p.ParseRepoFile([]byte(`
runbooks: docs/runbooks/
tools:
- id: queue-depth
  tier: read
  runs_as: reader
  run: [az, servicebus, queue, show]
- id: restart-api
  tier: act
  action: restart_service
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tools) != 2 || f.Tools[1].Action != "restart_service" || f.Runbooks != "docs/runbooks/" {
		t.Errorf("file = %+v", f)
	}
	if f, err := p.ParseRepoFile(nil); err != nil || len(f.Tools) != 0 {
		t.Errorf("a missing file is no tools: %+v %v", f, err)
	}
	bad := map[string]string{
		"a runner the program does not name": "tools:\n- {id: a, tier: read, runs_as: root, run: [id]}",
		"run and action together":            "tools:\n- {id: a, tier: read, runs_as: reader, run: [id], action: x}",
		"neither run nor action":             "tools:\n- {id: a, tier: read}",
		"the look tier":                      "tools:\n- {id: a, tier: look, runs_as: reader, run: [id]}",
		"a tool twice":                       "tools:\n- {id: a, tier: read, action: x}\n- {id: a, tier: read, action: y}",
		"runbooks outside":                   "runbooks: ../elsewhere",
		"an unknown key":                     "tools:\n- {id: a, tier: read, action: x, as_root: true}",
	}
	for name, doc := range bad {
		if _, err := p.ParseRepoFile([]byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestAForgeUsersSignInAddressComesFromTheProgramFile(t *testing.T) {
	with := func(person string) string {
		return strings.Replace(sample, "  url: https://gitlab.example.com/\n", "  url: https://gitlab.example.com/\n  person: "+person+"\n", 1)
	}
	p, err := Parse([]byte(with(`"{username}@example.com"`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Forge.PersonOf("Lead.Person"); got != "lead.person@example.com" {
		t.Errorf("person %q", got)
	}
	if got := p.Forge.PersonOf("a@b"); got != "" {
		t.Errorf("a username that is no forge's: %q", got)
	}
	none, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := none.Forge.PersonOf("lead"); got != "" {
		t.Errorf("no forge.person names nobody: %q", got)
	}
	for _, bad := range []string{`"{username}"`, `"lead@example.com"`, `"{username}{username}@example.com"`} {
		if _, err := Parse([]byte(with(bad))); err == nil || !strings.Contains(err.Error(), "forge.person") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestEachAgentStageHasABudget(t *testing.T) {
	// spend_usd 8: the first round's investigator gets two fifths, a round
	// that answers the approvers as much, the builder three fifths, and the
	// prover, apart from the builder, one fifth.
	if b := parse(t, sample).Limits.Budget; b.Investigate != 3.2 || b.Revise != 3.2 || b.Build != 4.8 || b.Prove != 1.6 {
		t.Errorf("budget from spend_usd 8 = %+v", b)
	}
	if b := parse(t, strings.Replace(sample, "  spend_usd: 8\n", "", 1)).Limits.Budget; b.Investigate != 2 || b.Revise != 2 || b.Build != 3 || b.Prove != 1 {
		t.Errorf("default budget = %+v", b)
	}
	set := parse(t, strings.Replace(sample, "  spend_usd: 8\n", "  budget: { investigate: 2.5, revise: 4, build: 6, prove: 0.75 }\n", 1)).Limits.Budget
	if set.Investigate != 2.5 || set.Revise != 4 || set.Build != 6 || set.Prove != 0.75 {
		t.Errorf("budget as set = %+v", set)
	}
	if b := parse(t, strings.Replace(sample, "  spend_usd: 8\n", "  budget: { investigate: 3 }\n", 1)).Limits.Budget; b.Revise != 3 || b.Build != 3 {
		t.Errorf("a revision gets the first round's budget: %+v", b)
	}
	for doc, want := range map[string]string{
		"  budget: { investigate: 3, revise: 2 }\n": "limits.budget.revise 2.00 is less than limits.budget.investigate 3.00",
		"  budget: { build: -1 }\n":                 "no limit may be negative",
		"  budget: { prove: -1 }\n":                 "no limit may be negative",
		"  budget: { plan: 2 }\n":                   "field plan not found",
	} {
		_, err := Parse([]byte(strings.Replace(sample, "  spend_usd: 8\n", doc, 1)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
}

func TestEachRunWritesOnTheTicketUnlessTheProgramSaysNot(t *testing.T) {
	if tk := parse(t, sample).Ticket; !tk.RunsOn() || !tk.ShotsOn() {
		t.Errorf("by default each run keeps a comment, with screenshots: %+v", tk)
	}
	if tk := parse(t, sample+"ticket: { screenshots: false }\n").Ticket; !tk.RunsOn() || tk.ShotsOn() {
		t.Errorf("screenshots off: %+v", tk)
	}
	if tk := parse(t, sample+"ticket: { runs: false }\n").Ticket; tk.RunsOn() || tk.ShotsOn() {
		t.Errorf("runs off takes the screenshots with them: %+v", tk)
	}
	if _, err := Parse([]byte(sample + "ticket: { runs: false, screenshots: true }\n")); err == nil || !strings.Contains(err.Error(), "ticket.screenshots needs ticket.runs") {
		t.Errorf("screenshots with no run comment: %v", err)
	}
}

func TestTheTicketShowsNoCostUnlessTheProgramSaysSo(t *testing.T) {
	if parse(t, sample).Ticket.Cost {
		t.Error("by default the ticket shows a cost")
	}
	if !parse(t, sample+"ticket: { cost: true }\n").Ticket.Cost {
		t.Error("ticket.cost: true shows no cost")
	}
}

func TestARepositoryNamesItsOwnCheck(t *testing.T) {
	p := parse(t, sample)
	f, err := p.ParseRepoFile([]byte("check: [dotnet, test, --no-restore]\n"))
	if err != nil || strings.Join(f.Check, " ") != "dotnet test --no-restore" {
		t.Fatalf("check: %+v %v", f, err)
	}
	if f, err := p.ParseRepoFile([]byte("runbooks: docs/\n")); err != nil || len(f.Check) != 0 {
		t.Errorf("no check: %+v %v", f, err)
	}
	if _, err := p.ParseRepoFile([]byte("check: [make, \"\"]\n")); err == nil {
		t.Error("a check with an empty word was taken")
	}
}

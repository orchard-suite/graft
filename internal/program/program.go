// Package program reads the two files that say how Graft works: the
// program file, which the platform admin owns and Graft reads at start,
// and graft.yaml at a repository's root, which holds that repository's
// runbooks and tool declarations and is read only from its main branch.
package program

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment tiers a request, a tool call, and a runner belong to.
const (
	Production    = "production"
	NonProduction = "nonproduction"
)

// RepoFileName is the repository file. It is always a protected path.
const RepoFileName = "graft.yaml"

// PageApp is the app a request made on Graft's own page is from. No
// intake app may take the name.
const PageApp = "graft"

// VersionMark is where the version goes in an intake app's tag.
const VersionMark = "{version}"

// Program is one program: its forge, its repositories, its lanes, and the
// limits every request in it works under. One Graft serves one program.
type Program struct {
	// Name is the program's slug. It is part of every hub action id.
	Name string `yaml:"program"`
	// Trusted says the deployment may mount one read-only cloud key in the
	// sandbox. Every run in a trusted program counts as having read
	// production.
	Trusted   bool      `yaml:"trusted"`
	Forge     Forge     `yaml:"forge"`
	Committer Committer `yaml:"committer"`
	Owner     *Owner    `yaml:"owner"`
	// Approvers are who decide a plan, as the hub's grants on a person.
	// A repository may name its own.
	Approvers    []Grant        `yaml:"approvers"`
	Repositories []Repository   `yaml:"repositories"`
	Lanes        []Lane         `yaml:"lanes"`
	Limits       Limits         `yaml:"limits"`
	Runners      map[string]Env `yaml:"runners"`
	Intake       []Intake       `yaml:"intake"`
	Hours        Hours          `yaml:"hours"`
	Incidents    Incidents      `yaml:"incidents"`
	// Notices says whether Graft's notices go out: NoticesSend, the
	// default, or NoticesHold, which keeps each one on its request instead.
	Notices string `yaml:"notices"`
	// Ticket says what each run writes on the request's ticket.
	Ticket Ticket `yaml:"ticket"`
}

// Ticket is what each run of a request writes on its ticket. Runs and
// Screenshots are on, and Cost off, unless the program file says otherwise.
type Ticket struct {
	// Runs gives each run one comment on the ticket, written as it goes and
	// edited in place: its steps, its outcome, and its failure.
	Runs *bool `yaml:"runs"`
	// Screenshots ends a run that changes what people see with screenshots
	// of the finished work on its comment. It needs Runs.
	Screenshots *bool `yaml:"screenshots"`
	// Cost puts dollars on the ticket: each run's budget and what its agents
	// spent. Everyone who reads the ticket reads them, so it is off unless
	// the program file turns it on (ADR-6004).
	Cost bool `yaml:"cost"`
}

// RunsOn says each run keeps one comment on the ticket.
func (t Ticket) RunsOn() bool { return t.Runs == nil || *t.Runs }

// ShotsOn says a run that changes what people see ends with screenshots.
func (t Ticket) ShotsOn() bool { return t.RunsOn() && (t.Screenshots == nil || *t.Screenshots) }

// What the program file's notices key takes.
const (
	NoticesSend = "send"
	NoticesHold = "hold"
)

// Holding says the program holds its notices: none goes out, and each is
// kept on its request as an event instead.
func (p *Program) Holding() bool { return p.Notices == NoticesHold }

// Forge is where the program's repositories live.
type Forge struct {
	// Kind is github or gitlab.
	Kind string `yaml:"kind"`
	// URL is the forge's web address. GitHub's is the default for github.
	URL string `yaml:"url"`
	// Person is a forge user's sign-in address, made from their username
	// at PersonMark, such as {username}@example.com. Empty takes the address
	// a forge user shows on their profile.
	Person string `yaml:"person"`
}

// PersonMark is where the username goes in the forge's person.
const PersonMark = "{username}"

// PersonOf is the sign-in address of the forge user named username, by the
// program file's forge.person, or empty when it names none.
func (f Forge) PersonOf(username string) string {
	if f.Person == "" || !forgeName.MatchString(username) {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(f.Person, PersonMark, username))
}

// Committer is the identity on every commit Graft and its agent write.
type Committer struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

// Owner is who the incident desk tells about a repository's requests:
// exactly one of a group, a person, or an escalation chain.
type Owner struct {
	Group  string `yaml:"group"`
	Person string `yaml:"person"`
	Chain  string `yaml:"chain"`
}

// Repository is one repository Graft may work in.
type Repository struct {
	// Path is the repository as the forge names it, such as acme/api.
	Path string `yaml:"path"`
	// Entity is its catalog record, as blueprint:key.
	Entity string `yaml:"entity"`
	// Service is its name at the incident desk; the entity's key when empty.
	Service string `yaml:"service"`
	// Protected lists paths only a lane marked protected may change. One
	// ending in / covers everything under it; any other is one file.
	Protected []string `yaml:"protected"`
	Owner     *Owner   `yaml:"owner"`
	// Approvers override the program's for this repository.
	Approvers []Grant `yaml:"approvers"`
}

// Grant names people as the catalog hub's access rules do, judged on each
// person's own catalog record: Values alone are sign-in addresses; with
// Path, the keys of what the person reaches by that relation path; with
// Field, the values of that field of the person.
type Grant struct {
	Path   string   `yaml:"path" json:"path,omitempty"`
	Field  string   `yaml:"field" json:"field,omitempty"`
	Values []string `yaml:"values" json:"values"`
}

func checkGrants(what string, grants []Grant) error {
	for i, g := range grants {
		if len(g.Values) == 0 {
			return fmt.Errorf("%s %d: values names who it lets in", what, i+1)
		}
		if g.Path != "" && g.Field != "" {
			return fmt.Errorf("%s %d: name a path or a field, not both", what, i+1)
		}
	}
	return nil
}

// Lane is one way into the program. Lanes are listed lowest first. A lane
// belongs to the program: a person holds it on every repository of the
// program or on none, by its members alone, never by what they may see.
type Lane struct {
	ID string `yaml:"id"`
	// Members are who hold the lane, as the hub's grants on a person: by
	// team, department, or address. Every lane but the fallback names them.
	Members []Grant `yaml:"members"`
	// Approval says whether the lane's plans wait for the approvers before
	// anything is built. Unset is true: only a lane that says false ships a
	// plan unasked.
	Approval *bool `yaml:"approval"`
	// Protected lets the lane's plan name a protected path.
	Protected bool `yaml:"protected"`
	// Review lands the lane's changes through a pull request.
	Review bool `yaml:"review"`
	// SmallFix lets a small fix in this lane ship with no plan approval.
	SmallFix bool `yaml:"small_fix"`
	// Outside marks reporters outside the program: a request of theirs
	// that read production is answered in fixed words only.
	Outside bool `yaml:"outside"`
	// Fallback marks the lane for everyone else: anyone a port vouches for
	// who holds no other lane holds it. Only the lowest lane may be one, and
	// it names no members.
	Fallback bool `yaml:"fallback"`
}

// Waits says the lane's plans wait for the approvers' decision.
func (l *Lane) Waits() bool { return l.Approval == nil || *l.Approval }

// Limits bound one request and the program as a whole.
type Limits struct {
	SmallFixLines int `yaml:"small_fix_lines"`
	// SpendUSD is the base an agent stage's budget is a share of, where
	// Budget does not name it.
	SpendUSD        float64 `yaml:"spend_usd"`
	Budget          Budget  `yaml:"budget"`
	Working         int     `yaml:"working"`
	PlanRounds      int     `yaml:"plan_rounds"`
	RequestsPerHour int     `yaml:"requests_per_hour"`
	ContextDays     int     `yaml:"context_days"`
}

// Budget is the most one agent run may spend, in US dollars, by stage. A
// run is one prompt: a gate report sent back to the builder, or findings
// sent back to the investigator, is a run of its own.
type Budget struct {
	// Investigate is the investigator of a request's first round. It
	// defaults to two fifths of spend_usd.
	Investigate float64 `yaml:"investigate"`
	// Revise is the investigator of a round that answers the approvers. It
	// defaults to Investigate, and is never less.
	Revise float64 `yaml:"revise"`
	// Build is the builder. It defaults to three fifths of spend_usd.
	Build float64 `yaml:"build"`
	// Prove is the prover, which proves each criterion of a build the
	// builder said is finished, apart from the builder's budget so running
	// out while building never spends it. It defaults to a fifth of
	// spend_usd.
	Prove float64 `yaml:"prove"`
}

// Env names one thing per environment tier.
type Env struct {
	Production    string `yaml:"production"`
	NonProduction string `yaml:"nonproduction"`
}

// For is the name for an environment tier.
func (e Env) For(env string) string {
	if env == Production {
		return e.Production
	}
	return e.NonProduction
}

// Intake lets one app report against one repository.
type Intake struct {
	App         string `yaml:"app"`
	Repository  string `yaml:"repository"`
	Environment string `yaml:"environment"`
	// Paths are the part of the repository the app is built from: where the
	// investigation starts. They never fence what a request may change; a
	// request may change whatever in the repository the fix needs, under the
	// protected paths.
	Paths []string `yaml:"paths"`
	// Tag is the app's release tag, with VersionMark where the version
	// goes. Empty, v{version} or {version}.
	Tag string `yaml:"tag"`
}

// Tags are the tags a version the app reports may have, most likely first.
func (in *Intake) Tags(version string) []string {
	v := strings.TrimPrefix(version, "v")
	if in.Tag != "" {
		return []string{strings.ReplaceAll(in.Tag, VersionMark, v)}
	}
	return []string{"v" + v, v}
}

// Hours names the on-call schedule Graft ships under: it lands a change
// only while that schedule has a person on. Empty ships at any hour.
type Hours struct {
	Schedule string `yaml:"schedule"`
}

// Incidents says what else is checked for an open incident.
type Incidents struct {
	// Needs lists catalog relations of a repository's entity; an open
	// incident on any service they reach counts as one on the repository.
	Needs []string `yaml:"needs"`
}

var (
	slug     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	actionID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	email    = regexp.MustCompile(`^[^\s@<>"']+@[^\s@<>"']+$`)
	tagChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	// forgeName is a username GitLab or GitHub gives a person.
	forgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Load reads and checks the program file at file.
func Load(file string) (*Program, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("program file: %w", err)
	}
	p, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("program file %s: %w", file, err)
	}
	return p, nil
}

// Parse reads and checks a program file. A key it does not know is an
// error: a typo must not silently switch a fence off.
func Parse(raw []byte) (*Program, error) {
	var p Program
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Program) check() error {
	if !slug.MatchString(p.Name) || !actionID.MatchString(under(p.Name)) {
		return fmt.Errorf("program %q: want a slug that starts with a letter", p.Name)
	}
	switch p.Forge.Kind {
	case "github":
		if p.Forge.URL == "" {
			p.Forge.URL = "https://github.com"
		}
	case "gitlab":
		if p.Forge.URL == "" {
			return fmt.Errorf("forge.url is required for gitlab")
		}
	default:
		return fmt.Errorf("forge.kind %q: want github or gitlab", p.Forge.Kind)
	}
	switch p.Notices {
	case "":
		p.Notices = NoticesSend
	case NoticesSend, NoticesHold:
	default:
		return fmt.Errorf("notices %q: want send or hold", p.Notices)
	}
	if !p.Ticket.RunsOn() && p.Ticket.Screenshots != nil && *p.Ticket.Screenshots {
		return fmt.Errorf("ticket.screenshots needs ticket.runs: screenshots go on a run's comment")
	}
	if p.Forge.Person != "" && (strings.Count(p.Forge.Person, PersonMark) != 1 || !email.MatchString(strings.ReplaceAll(p.Forge.Person, PersonMark, "a"))) {
		return fmt.Errorf("forge.person %q: want an address with one %s, such as %s@example.com", p.Forge.Person, PersonMark, PersonMark)
	}
	p.Forge.URL = strings.TrimRight(p.Forge.URL, "/")
	if !strings.HasPrefix(p.Forge.URL, "https://") && !strings.HasPrefix(p.Forge.URL, "http://") {
		return fmt.Errorf("forge.url %q: want an http address", p.Forge.URL)
	}
	if p.Committer.Name == "" {
		p.Committer.Name = "Graft"
	}
	if !email.MatchString(p.Committer.Email) {
		return fmt.Errorf("committer.email is required: the address on Graft's commits")
	}
	if p.Owner != nil {
		if err := p.Owner.check("owner"); err != nil {
			return err
		}
	}
	if err := checkGrants("approvers", p.Approvers); err != nil {
		return err
	}
	if len(p.Repositories) == 0 {
		return fmt.Errorf("repositories: list at least one")
	}
	seen := map[string]bool{}
	for i := range p.Repositories {
		r := &p.Repositories[i]
		r.Path = strings.Trim(r.Path, "/")
		if r.Path == "" || !strings.Contains(r.Path, "/") {
			return fmt.Errorf("repository %q: want the path the forge names it by, such as acme/api", r.Path)
		}
		if seen[strings.ToLower(r.Path)] {
			return fmt.Errorf("repository %s is listed twice", r.Path)
		}
		seen[strings.ToLower(r.Path)] = true
		bp, key, ok := strings.Cut(r.Entity, ":")
		if !ok || bp == "" || key == "" {
			return fmt.Errorf("repository %s: entity %q is not blueprint:key", r.Path, r.Entity)
		}
		if r.Service == "" {
			r.Service = key
		}
		if r.Owner != nil {
			if err := r.Owner.check("repository " + r.Path + ": owner"); err != nil {
				return err
			}
		}
		if err := checkGrants("repository "+r.Path+": approvers", r.Approvers); err != nil {
			return err
		}
		protected := []string{RepoFileName}
		for _, pp := range r.Protected {
			clean, ok := inside(pp)
			if !ok {
				return fmt.Errorf("repository %s: protected path %q is not inside the repository", r.Path, pp)
			}
			if clean != RepoFileName {
				protected = append(protected, clean)
			}
		}
		r.Protected = protected
	}
	if len(p.Lanes) == 0 {
		return fmt.Errorf("lanes: list at least one, lowest first")
	}
	lanes := map[string]bool{}
	for i, l := range p.Lanes {
		if l.Fallback && i != 0 {
			return fmt.Errorf("lane %s: only the lowest lane may be the fallback", l.ID)
		}
		if !slug.MatchString(l.ID) {
			return fmt.Errorf("lane %q: want a slug", l.ID)
		}
		if lanes[l.ID] {
			return fmt.Errorf("lane %s is listed twice", l.ID)
		}
		lanes[l.ID] = true
		switch {
		case l.Fallback && len(l.Members) > 0:
			return fmt.Errorf("lane %s: the fallback lane is everyone else, so it names no members", l.ID)
		case l.Fallback && !l.Waits():
			return fmt.Errorf("lane %s: the fallback lane is everyone, so its plans always wait: drop approval: false", l.ID)
		case !l.Fallback && len(l.Members) == 0:
			return fmt.Errorf("lane %s: name its members, or mark it the fallback", l.ID)
		case l.SmallFix && !l.Waits():
			return fmt.Errorf("lane %s: small_fix lets a small fix skip a wait this lane never has: drop it", l.ID)
		}
		if err := checkGrants("lane "+l.ID+": members", l.Members); err != nil {
			return err
		}
		if l.Waits() && len(p.Approvers) == 0 {
			for _, r := range p.Repositories {
				if len(r.Approvers) == 0 {
					return fmt.Errorf("lane %s: its plans wait for approvers, and neither the program nor repository %s names any", l.ID, r.Path)
				}
			}
		}
	}
	l := &p.Limits
	b := &l.Budget
	if l.SmallFixLines < 0 || l.SpendUSD < 0 || l.Working < 0 || l.PlanRounds < 0 || l.RequestsPerHour < 0 || l.ContextDays < 0 || b.Investigate < 0 || b.Revise < 0 || b.Build < 0 || b.Prove < 0 {
		return fmt.Errorf("limits: no limit may be negative")
	}
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&l.SmallFixLines, 150)
	def(&l.Working, 2)
	def(&l.PlanRounds, 3)
	def(&l.RequestsPerHour, 10)
	def(&l.ContextDays, 30)
	if l.SpendUSD == 0 {
		l.SpendUSD = 5
	}
	if b.Investigate == 0 {
		b.Investigate = l.SpendUSD * 2 / 5
	}
	if b.Build == 0 {
		b.Build = l.SpendUSD * 3 / 5
	}
	if b.Prove == 0 {
		b.Prove = l.SpendUSD / 5
	}
	switch {
	case b.Revise == 0:
		b.Revise = b.Investigate
	case b.Revise < b.Investigate:
		return fmt.Errorf("limits.budget.revise %.2f is less than limits.budget.investigate %.2f: a round that answers the approvers gets at least the first round's budget", b.Revise, b.Investigate)
	}
	for name, r := range p.Runners {
		if !slug.MatchString(name) {
			return fmt.Errorf("runner %q: want a slug", name)
		}
		if r.Production == "" && r.NonProduction == "" {
			return fmt.Errorf("runner %s: name a runner for production, nonproduction, or both", name)
		}
	}
	apps := map[string]bool{}
	for i := range p.Intake {
		in := &p.Intake[i]
		if !slug.MatchString(in.App) {
			return fmt.Errorf("intake app %q: want a slug", in.App)
		}
		if in.App == PageApp {
			return fmt.Errorf("intake app %s: that is the page's own name", in.App)
		}
		for j, pp := range in.Paths {
			clean, ok := inside(pp)
			if !ok {
				return fmt.Errorf("intake app %s: path %q is not inside the repository", in.App, pp)
			}
			in.Paths[j] = clean
		}
		if in.Tag != "" && (strings.Count(in.Tag, VersionMark) != 1 || !tagChars.MatchString(strings.ReplaceAll(in.Tag, VersionMark, "1"))) {
			return fmt.Errorf("intake app %s: tag %q: want one %s in a plain tag name", in.App, in.Tag, VersionMark)
		}
		if p.Repository(in.Repository) == nil {
			return fmt.Errorf("intake app %s: repository %q is not listed", in.App, in.Repository)
		}
		if in.Environment != Production && in.Environment != NonProduction {
			return fmt.Errorf("intake app %s: environment %q: want %s or %s", in.App, in.Environment, Production, NonProduction)
		}
		k := in.App + " " + strings.ToLower(in.Repository)
		if apps[k] {
			return fmt.Errorf("intake app %s lists %s twice", in.App, in.Repository)
		}
		apps[k] = true
	}
	return nil
}

func (o *Owner) check(what string) error {
	n := 0
	for _, v := range []string{o.Group, o.Person, o.Chain} {
		if v != "" {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%s: name exactly one of group, person, or chain", what)
	}
	return nil
}

// Repository is the listed repository named path, whatever its case, or nil.
func (p *Program) Repository(repoPath string) *Repository {
	for i := range p.Repositories {
		if strings.EqualFold(p.Repositories[i].Path, strings.Trim(repoPath, "/")) {
			return &p.Repositories[i]
		}
	}
	return nil
}

// Lane is the lane with this id, or nil.
func (p *Program) Lane(id string) *Lane {
	for i := range p.Lanes {
		if p.Lanes[i].ID == id {
			return &p.Lanes[i]
		}
	}
	return nil
}

// Held lists the lanes a person holds, lowest first: each lane whose
// members let them in, as member says, or, when none does, the fallback
// lane. It is empty only in a program with no fallback lane.
func (p *Program) Held(member func(l *Lane) (bool, error)) ([]string, error) {
	var out []string
	fallback := ""
	for i := range p.Lanes {
		l := &p.Lanes[i]
		if l.Fallback {
			fallback = l.ID
			continue
		}
		in, err := member(l)
		if err != nil {
			return nil, fmt.Errorf("lane %s: %w", l.ID, err)
		}
		if in {
			out = append(out, l.ID)
		}
	}
	if len(out) == 0 && fallback != "" {
		out = []string{fallback}
	}
	return out, nil
}

// Choose is the lane a request enters when the person named none: the
// highest lane they hold, since a lane above another names its members for
// more. held is lowest first. It is empty when they hold none.
func (p *Program) Choose(held []string) string {
	if len(held) == 0 {
		return ""
	}
	return held[len(held)-1]
}

// LaneRank is the lane's place in the list, lowest first, or -1.
func (p *Program) LaneRank(id string) int {
	for i := range p.Lanes {
		if p.Lanes[i].ID == id {
			return i
		}
	}
	return -1
}

// LaneAction is the hub action a lane is: a person holds the lane on a
// repository when the hub lets them run this action on its entity.
func (p *Program) LaneAction(lane string) string {
	return "graft_" + under(p.Name) + "_" + under(lane)
}

// ToolAction is the hub action Graft declares for one tool of a
// repository in one environment tier.
func (p *Program) ToolAction(r *Repository, tool, env string) string {
	_, key, _ := strings.Cut(r.Entity, ":")
	tier := "nonprod"
	if env == Production {
		tier = "prod"
	}
	return "graft_" + under(p.Name) + "_" + under(key) + "_" + under(tool) + "_" + tier
}

// IntakeFor is what lets app report against repository. With repository
// empty, the app must have exactly one entry.
func (p *Program) IntakeFor(app, repository string) (*Intake, error) {
	var found []*Intake
	for i := range p.Intake {
		if p.Intake[i].App == app {
			found = append(found, &p.Intake[i])
		}
	}
	switch {
	case len(found) == 0:
		return nil, fmt.Errorf("%s may not report here", app)
	case repository == "" && len(found) == 1:
		return found[0], nil
	case repository == "":
		return nil, fmt.Errorf("%s reports against more than one repository: name one", app)
	}
	for _, in := range found {
		if strings.EqualFold(in.Repository, strings.Trim(repository, "/")) {
			return in, nil
		}
	}
	return nil, fmt.Errorf("%s may not report against %s", app, repository)
}

// OwnerOf is who hears about the repository's requests: its own owner, or
// the program's, or nil when neither is set.
func (p *Program) OwnerOf(r *Repository) *Owner {
	if r.Owner != nil {
		return r.Owner
	}
	return p.Owner
}

// ApproversOf is who decides a plan in a repository: its own approvers,
// else the program's. Empty leaves it to the hub's lane rules.
func (p *Program) ApproversOf(r *Repository) []Grant {
	if len(r.Approvers) > 0 {
		return r.Approvers
	}
	return p.Approvers
}

// ProtectedIn picks the protected paths out of files.
func (r *Repository) ProtectedIn(files []string) []string {
	var out []string
	for _, f := range files {
		if within(f, r.Protected) {
			out = append(out, f)
		}
	}
	return out
}

// inside cleans a path a file names in a repository, keeping the slash
// that marks a folder. It is false for a path that leaves the repository.
func inside(p string) (string, bool) {
	clean := path.Clean(p)
	if p == "" || path.IsAbs(p) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if strings.HasSuffix(p, "/") {
		clean += "/"
	}
	return clean, true
}

// within is whether file is one of paths, or inside one that is a folder.
func within(file string, paths []string) bool {
	for _, p := range paths {
		if file == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(file, p)) {
			return true
		}
	}
	return false
}

// under turns a slug or a catalog key into the part of a hub action id:
// lower case, with every other character an underscore.
func under(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Tool tiers a repository's tool may ask for. Look needs no tool: it is
// the sandbox itself.
const (
	TierRead = "read"
	TierData = "data"
	TierAct  = "act"
)

// RepoFile is graft.yaml at a repository's root.
type RepoFile struct {
	// Runbooks is the directory of runbooks the investigator reads first.
	Runbooks string `yaml:"runbooks"`
	// Check is the repository's own verify command, as argv: what its
	// builder runs before it says the work is finished, and what the gate
	// runs on what it built. Empty means `make check` where the Makefile
	// has that target, and nothing otherwise.
	Check []string `yaml:"check"`
	Tools []Tool   `yaml:"tools"`
}

// Tool is one tool a repository declares: a command a hub runner runs
// under the key the program names, or a hub action that already exists.
type Tool struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Tier        string `yaml:"tier"`
	// RunsAs names an entry of the program's runners.
	RunsAs string   `yaml:"runs_as"`
	Run    []string `yaml:"run"`
	// Action is a hub action's id.
	Action string `yaml:"action"`
	// Timeout is how long one call may run, as a duration such as 2m.
	Timeout string `yaml:"timeout"`
	// Inputs are the tool's inputs, in the hub's own words for an action's
	// inputs. The command reads one as {{ inputs.<name> }}.
	Inputs map[string]map[string]any `yaml:"inputs"`
}

// ParseRepoFile reads and checks a repository file against the program. A
// repository without the file has no tools and no runbooks.
func (p *Program) ParseRepoFile(raw []byte) (*RepoFile, error) {
	var f RepoFile
	if len(bytes.TrimSpace(raw)) == 0 {
		return &f, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if f.Runbooks != "" {
		clean := path.Clean(f.Runbooks)
		if path.IsAbs(f.Runbooks) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("runbooks %q is not inside the repository", f.Runbooks)
		}
	}
	for _, a := range f.Check {
		if strings.TrimSpace(a) == "" {
			return nil, fmt.Errorf("check: every word of the command is filled in")
		}
	}
	seen := map[string]bool{}
	for _, t := range f.Tools {
		if !slug.MatchString(t.ID) {
			return nil, fmt.Errorf("tool %q: want a slug", t.ID)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("tool %s is declared twice", t.ID)
		}
		seen[t.ID] = true
		switch t.Tier {
		case TierRead, TierData, TierAct:
		default:
			return nil, fmt.Errorf("tool %s: tier %q: want read, data, or act", t.ID, t.Tier)
		}
		switch {
		case len(t.Run) > 0 && t.Action != "":
			return nil, fmt.Errorf("tool %s: give run or action, not both", t.ID)
		case len(t.Run) > 0:
			for name := range t.Inputs {
				if !actionID.MatchString(name) {
					return nil, fmt.Errorf("tool %s: input %q: want lowercase letters, digits, and _", t.ID, name)
				}
			}
			if _, ok := p.Runners[t.RunsAs]; !ok {
				return nil, fmt.Errorf("tool %s: runs_as %q is not a runner the program names", t.ID, t.RunsAs)
			}
		case t.Action != "":
			if !actionID.MatchString(t.Action) {
				return nil, fmt.Errorf("tool %s: action %q is not a hub action id", t.ID, t.Action)
			}
			if t.RunsAs != "" || len(t.Inputs) > 0 {
				return nil, fmt.Errorf("tool %s: runs_as and inputs belong with run, not action", t.ID)
			}
		default:
			return nil, fmt.Errorf("tool %s: give run or action", t.ID)
		}
	}
	return &f, nil
}

package store

import (
	"time"

	"github.com/orchard-suite/graft/internal/attach"
)

// States a request moves through. A work state has a worker on it; a
// waiting state is read again on a timer and holds no worker; an end state
// is where a request stops.
const (
	// Work states.
	StateReceived      = "received"
	StateScreening     = "screening"
	StateChecking      = "checking"
	StateInvestigating = "investigating"
	StatePlanning      = "planning"
	StateBuilding      = "building"
	StateLanding       = "landing"
	StateWatching      = "watching"

	// Waiting states.
	// StateHolding waits for an incident to close.
	StateHolding = "holding"
	// StateAwaitingPlan waits for a person to decide the plan, in the hub.
	StateAwaitingPlan = "awaiting_plan"
	// StateAwaitingReview waits for a pull request to be reviewed and merged.
	StateAwaitingReview = "awaiting_review"
	// StateStalled waits for one of its approvers to retry a stage that
	// failed. Its Stall says which, and why.
	StateStalled = "stalled"

	// End states.
	StateAnswered    = "answered"
	StateFiled       = "filed"
	StateWaiting     = "waiting"
	StateReleased    = "released"
	StateDeclined    = "declined"
	StateQuarantined = "quarantined"
	StateFailed      = "failed"
)

// WorkStates are the states a worker runs, in the order a request meets them.
var WorkStates = []string{StateReceived, StateScreening, StateChecking, StateInvestigating, StatePlanning, StateBuilding, StateLanding, StateWatching}

// WaitStates are the states read again on a timer.
var WaitStates = []string{StateHolding, StateAwaitingPlan, StateAwaitingReview, StateStalled}

// EndStates are where a request stops.
var EndStates = []string{StateAnswered, StateFiled, StateWaiting, StateReleased, StateDeclined, StateQuarantined, StateFailed}

func in(states []string, s string) bool {
	for _, x := range states {
		if x == s {
			return true
		}
	}
	return false
}

// Working says whether a worker runs a request in this state.
func Working(state string) bool { return in(WorkStates, state) }

// Ended says whether a request in this state has stopped.
func Ended(state string) bool { return in(EndStates, state) }

// How a plan was decided.
const (
	DecisionApproved = "approved"
	DecisionDeclined = "declined"
	DecisionReturned = "returned"
)

// Sorts the screen puts a request in.
const (
	SortBug      = "bug"
	SortFeature  = "feature"
	SortQuestion = "question"
	SortJunk     = "junk"
	SortAbuse    = "abuse"
)

// Verdicts the investigator may reach.
const (
	VerdictDefect    = "defect"
	VerdictUserError = "user_error"
	VerdictDesigned  = "as_designed"
	VerdictFeature   = "feature"
	VerdictDuplicate = "duplicate"
	VerdictNotEnough = "not_enough"
	VerdictOutage    = "outage"
)

// Request is one thing a person asked for, from the words they sent to the
// answer they got.
type Request struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	State     string    `json:"state"`
	// Person is the sign-in address of whoever asked, as the app's own
	// server stamped it.
	Person string `json:"person"`
	// Repo is the repository's path on the forge.
	Repo string `json:"repo"`
	// App is the intake app that sent it.
	App string `json:"app"`

	Doc
}

// Doc is the part of a request kept as one JSON document.
type Doc struct {
	Environment string   `json:"environment"`
	Lane        string   `json:"lane"`
	Text        string   `json:"text"`
	Title       string   `json:"title,omitempty"`
	Context     *Context `json:"context,omitempty"`
	// Attachments are the files the person sent with the words, and those
	// the plan's approvers sent on its ticket once the sweep read them. The
	// files are on the data disk, and go with the context.
	Attachments []attach.File `json:"attachments,omitempty"`
	// Incoming are the files the plan's approvers sent on its ticket that
	// the sweep has not read yet. The next round sweeps each into
	// Attachments or deletes it; nothing else reads them.
	Incoming []attach.File `json:"incoming,omitempty"`
	Error    string        `json:"error,omitempty"`
	// NotBefore keeps a worker off the request until then: outside working
	// hours, or while its repository is stopped.
	NotBefore time.Time `json:"notBefore,omitzero"`
	// Held says why NotBefore is set.
	Held string `json:"held,omitempty"`

	Sort string `json:"sort,omitempty"`
	// Secrets are the kinds of secret the sweep found; the text is then
	// kept redacted and no agent sees the request.
	Secrets []string `json:"secrets,omitempty"`

	Verdict *Verdict `json:"verdict,omitempty"`
	Plan    *Plan    `json:"plan,omitempty"`
	// PlanRound counts the plans asked about: the first is 1.
	PlanRound int `json:"planRound,omitempty"`
	// Thread is what the plan's approvers said of it, oldest first: a note
	// a plan was sent back with in the hub, and a comment on the ticket.
	Thread []Note `json:"thread,omitempty"`
	// Earlier are the plans of the rounds before this one, oldest first.
	Earlier []Round `json:"earlier,omitempty"`
	// Stall is the stage that failed last and waits to be tried again, while
	// the request waits on a person; nil when nothing failed.
	Stall *Stall `json:"stall,omitempty"`
	// Runs list the request's runs, each with its comment on the ticket.
	// The runs themselves are kept apart, one record each.
	Runs []RunLink `json:"runs,omitempty"`
	// Seen is the last comment on the ticket Graft has read.
	Seen int64 `json:"seen,omitempty"`
	// Run is the hub run that asks about the plan; RunURL is its page.
	Run    string `json:"run,omitempty"`
	RunURL string `json:"runUrl,omitempty"`
	// Decision is how the last plan was decided in the hub: DecisionApproved,
	// DecisionDeclined, or DecisionReturned. DecidedBy decided it, at
	// DecidedAt, with DecisionNote.
	Decision     string `json:"decision,omitempty"`
	DecidedBy    string `json:"decidedBy,omitempty"`
	DecidedAt    string `json:"decidedAt,omitempty"`
	DecisionNote string `json:"decisionNote,omitempty"`

	Ticket   *Ticket   `json:"ticket,omitempty"`
	Incident *Incident `json:"incident,omitempty"`
	Links    []Link    `json:"links,omitempty"`

	// Version is what the person was on; Base is the commit the agent's
	// work starts after; Commit is the last commit landed on the remote;
	// First is the commit main was at before the request's first landing.
	Version string `json:"version,omitempty"`
	Base    string `json:"base,omitempty"`
	Commit  string `json:"commit,omitempty"`
	First   string `json:"first,omitempty"`
	PR      *PR    `json:"pr,omitempty"`
	// Rounds counts the build agent's rounds. Feedback is the report the
	// next round starts from.
	Rounds   int    `json:"rounds,omitempty"`
	Feedback string `json:"-"`
	// Built says the change is built and through the fences. Small says
	// the fences judged it a small fix, or SmallWhy says why not.
	Built    bool   `json:"built,omitempty"`
	Small    bool   `json:"small,omitempty"`
	SmallWhy string `json:"smallWhy,omitempty"`
	// Size is what the built change came to.
	Size *Size `json:"size,omitempty"`
	// Reverting says the worker is taking its own commits back out.
	Reverting bool `json:"reverting,omitempty"`
	// Unfinished is a build that stopped before it was finished and proven:
	// what is left, and the work kept on the request for a retry to go on
	// from. Nil once a build is finished.
	Unfinished *Unfinished `json:"unfinished,omitempty"`
	// Waived are what the plan's approvers let a build land without: a
	// criterion's proof, or its screenshots.
	Waived []Waiver `json:"waived,omitempty"`
	// Release is the commit a repository's own release job wrote on main
	// after the landed commit, whose pipeline is the release; empty where
	// the landed commit's own pipeline is.
	Release string `json:"release,omitempty"`

	// Answer is the one message the person gets.
	Answer string `json:"answer,omitempty"`
	// ReadProduction says a tool read production for this request.
	ReadProduction bool `json:"readProduction,omitempty"`

	// HandleHash is the SHA-256 of the secret the agent's CLI presents.
	HandleHash string `json:"-"`
	// AnnalsKey is the key of the request's last Annals event, and Seq
	// counts the steps recorded so far.
	AnnalsKey string `json:"-"`
	Seq       int    `json:"-"`
}

// stored is the document as the database keeps it: the fields the API
// leaves out are kept here.
type stored struct {
	Doc
	Feedback   string `json:"feedback,omitempty"`
	HandleHash string `json:"handleHash,omitempty"`
	AnnalsKey  string `json:"annalsKey,omitempty"`
	Seq        int    `json:"seq,omitempty"`
}

// Context is what the app captured about where the person was. It holds
// fixed fields only, so no form value can ride along.
type Context struct {
	URL       string   `json:"url,omitempty"`
	Version   string   `json:"version,omitempty"`
	UserAgent string   `json:"user_agent,omitempty"`
	Console   []string `json:"console,omitempty"`
}

// Fact is one thing the investigator found, with the numbers of the
// evidence that shows it.
type Fact struct {
	Text     string `json:"text"`
	Evidence []int  `json:"evidence"`
}

// Verdict is the investigator's finding.
type Verdict struct {
	Verdict string   `json:"verdict"`
	Summary string   `json:"summary"`
	Facts   []Fact   `json:"facts"`
	Guesses []string `json:"guesses,omitempty"`
	// Answer is what to tell the person.
	Answer string `json:"answer"`
	// DuplicateOf is the request this one repeats.
	DuplicateOf string `json:"duplicate_of,omitempty"`
	// System is the catalog entity an outage is on.
	System string `json:"system,omitempty"`
	// Blocked says people cannot work until this is fixed.
	Blocked bool `json:"blocked"`
	// Fixable says a change to this repository's code fixes it.
	Fixable bool `json:"fixable"`
}

// Where a note on a plan came from.
const (
	// FromHub is a note a plan was sent back with in the hub.
	FromHub = "hub"
	// FromTicket is a comment on the request's ticket.
	FromTicket = "ticket"
)

// Note is one thing an approver said of a plan: what it says, who said it,
// where, and when. A round that answers it says so.
type Note struct {
	// Round is the round the note was said of.
	Round int    `json:"round"`
	From  string `json:"from"`
	By    string `json:"by"`
	At    string `json:"at,omitempty"`
	Text  string `json:"text"`
	// URL is where it was said: the comment on the ticket, or the run.
	URL string `json:"url,omitempty"`
	// Ref names what it was said on: the comment's id, or the run's.
	Ref string `json:"ref,omitempty"`
	// Files are the names of the files the note sent for the plan.
	Files []string `json:"files,omitempty"`
	// Answered is the round that answered it; Replied says it was a
	// question, answered on the ticket, and the plan stood.
	Answered int  `json:"answered,omitempty"`
	Replied  bool `json:"replied,omitempty"`
}

// Round is the plan of a round a later round replaced, the verdict it
// stood on, and where it was decided.
type Round struct {
	N       int      `json:"n"`
	Plan    Plan     `json:"plan"`
	Verdict *Verdict `json:"verdict,omitempty"`
	RunURL  string   `json:"runUrl,omitempty"`
}

// Stall is a stage that failed, kept so one of the request's approvers can
// run it again. Nothing the request held is lost with it.
type Stall struct {
	// Stage is the work state a retry runs again.
	Stage string `json:"stage"`
	// Round is the plan round the request stood at.
	Round int `json:"round"`
	// Cause is why, in plain words, for the ticket and the page.
	Cause string    `json:"cause"`
	At    time.Time `json:"at"`
	// Tries counts the times the stage has failed.
	Tries int `json:"tries"`
	// Draft is the next round's findings, written but never asked about,
	// kept while the round before stands. A retry asks about it.
	Draft *Round `json:"draft,omitempty"`
	// RetriedBy asked to try the stage again, on Via (the ticket or the
	// page); empty until someone did.
	RetriedBy string `json:"retriedBy,omitempty"`
	Via       string `json:"via,omitempty"`
}

// The parts of a build an Unfinished build stopped in.
const (
	// PartBuild is the builder's work: the change itself.
	PartBuild = "build"
	// PartProve is the proof of a finished change: a screenshot or a test
	// for each criterion of the plan.
	PartProve = "prove"
)

// Unfinished is a build that stopped before its builder said it was
// finished, or before every criterion of its plan was proven. Its commits
// are kept in the runner's clone, never on the forge's main, and a retry
// goes on from them with Left.
type Unfinished struct {
	// Part is PartBuild, or PartProve when only the proof is left.
	Part string `json:"part"`
	// Round is the plan round it builds.
	Round int `json:"round"`
	// Left is what is left, one line each: what the builder said, and what
	// Graft found.
	Left []string `json:"left"`
	// Head is the last commit of the work kept; empty when it made none.
	Head string `json:"head,omitempty"`
	// Proofs and Shots are what was already proven, and the screenshots
	// already on the ticket, which the next attempt keeps.
	Proofs []Proof `json:"proofs,omitempty"`
	Shots  []Shot  `json:"shots,omitempty"`
}

// WaiveShots is the Criterion of a waiver that lets a change people see
// land without screenshots.
const WaiveShots = 0

// Waiver is an approver letting a build of one plan round land without a
// criterion's proof, or, with Criterion WaiveShots, without screenshots.
type Waiver struct {
	Round     int    `json:"round"`
	Criterion int    `json:"criterion"`
	By        string `json:"by"`
	Why       string `json:"why"`
	At        string `json:"at,omitempty"`
}

// PlanChange is one file the plan changes, and what changes in it.
type PlanChange struct {
	Path string `json:"path"`
	What string `json:"what"`
}

// Size is how big a change is.
type Size struct {
	Lines int `json:"lines"`
	Files int `json:"files"`
}

// Plan is two documents: the brief, which the approvers decide on, and the
// build plan, which the builder builds from and is held to.
type Plan struct {
	Brief Brief `json:"brief"`
	Build Build `json:"build"`
}

// Kinds of change a plan makes.
const (
	// KindFix makes the system do what it was already meant to do.
	KindFix = "fix"
	// KindFeature adds to or changes what the system does.
	KindFeature = "feature"
)

// Brief is the plan as the people who decide it read it: what was asked,
// what will change for the people who use the system, what will not and
// why, what only they can settle, what was checked, how it fits, and what
// the planner recommends. It names behaviour, never files.
type Brief struct {
	// Asked is what the person asked for and the problem under it, in a few
	// sentences, with their own words quoted where they matter.
	Asked string `json:"asked"`
	// WillDo is the change as the people who use the system will see it,
	// and the shape of the solution.
	WillDo string `json:"will_do"`
	// WillNot are the parts of the request declined, deferred, or narrowed.
	WillNot []Declined `json:"will_not"`
	// Calls are the questions only the approvers can settle.
	Calls []Call `json:"judgment_calls"`
	// Checked are the claims in the request the planner checked.
	Checked []Check `json:"checked"`
	// Fit is how the change fits the system and what it adds to it.
	Fit string `json:"fit"`
	// Risk is what could break, and how the change will be proven.
	Risk string `json:"risk"`
	// Recommendation is the planner's advice, in one line.
	Recommendation Recommendation `json:"recommendation"`
	// Changed says, on a round after the first, what changed since the
	// round before and which note each change answers.
	Changed []string `json:"changed,omitempty"`
}

// Declined is a part of the request the plan does not do, and why, in
// words the person who asked would accept.
type Declined struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// Call is a question for the approvers, with the planner's answer to it.
type Call struct {
	Question       string `json:"question"`
	Recommendation string `json:"recommendation"`
}

// What a check of a claim found.
const (
	CheckTrue    = "true"
	CheckFalse   = "false"
	CheckPartly  = "partly"
	CheckUnknown = "unknown"
)

// Check is one claim in the request, checked against the repository, its
// history, or the running system, with the evidence that shows what was
// found.
type Check struct {
	Claim    string `json:"claim"`
	Found    string `json:"found"`
	Detail   string `json:"detail"`
	Evidence []int  `json:"evidence"`
}

// What the planner recommends the approvers do.
const (
	Approve            = "approve"
	ApproveWithChanges = "approve_with_changes"
	Decline            = "decline"
)

// Recommendation is the planner's advice on the plan.
type Recommendation struct {
	Decision string `json:"decision"`
	Why      string `json:"why"`
}

// Build is the plan as the builder reads it: precise steps, what must hold
// when it is done, the tests that fail first, the rules it keeps, and the
// files it changes. It is a contract: the build is held to it.
type Build struct {
	// Kind is KindFix or KindFeature.
	Kind string `json:"kind"`
	// Understood is the problem the build solves, for the builder.
	Understood string `json:"understood"`
	// Steps say what to build, in order.
	Steps []string `json:"steps"`
	// Acceptance are what must be true when the build is done, each one a
	// person or a test can see.
	Acceptance []string `json:"acceptance"`
	// Tests names each test that fails before the change and passes after.
	Tests string `json:"tests"`
	// Constraints are the rules the build keeps, such as a feature flag.
	Constraints []string `json:"constraints"`
	// Changes are every file the build changes or adds.
	Changes   []PlanChange `json:"changes"`
	Untouched []string     `json:"untouched"`
	Size      Size         `json:"size"`
	// Tiers are the tool tiers the build needs beyond the lane's.
	Tiers []string `json:"tiers"`
	// Protected are the protected paths among Changes, set by Graft.
	Protected []string `json:"protected,omitempty"`
	// UI says what people will see change, when anything will: the screens
	// the build changes, and how its builder shows them.
	UI *UI `json:"ui,omitempty"`
}

// UI is what a build changes that people see.
type UI struct {
	// Screens are the screens the build changes, as a person reaches each.
	Screens []string `json:"screens"`
	// Shots says how the builder takes the screenshots of them: the
	// repository's own tooling, and the test or demo data it shows.
	Shots string `json:"shots"`
}

type Ticket struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type Incident struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

// Link ties a request to another thing: a request it repeats, an incident.
type Link struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	URL  string `json:"url,omitempty"`
}

type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Head   string `json:"head"`
}

// Evidence is one numbered thing an investigation read.
type Evidence struct {
	N     int       `json:"n"`
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	Body  string    `json:"body"`
	At    time.Time `json:"at"`
}

type Event struct {
	At      time.Time `json:"at"`
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
}

// Switch is a repository's stop switch.
type Switch struct {
	Repo    string    `json:"repo"`
	Stopped bool      `json:"stopped"`
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

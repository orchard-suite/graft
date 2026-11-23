package store

import (
	"context"
	"encoding/json"
	"time"
)

// Kinds of run. A run is one pass of one stage from its start, and its
// comment on the ticket is the record of it.
const (
	// RunInvestigate writes a request's first verdict and plan.
	RunInvestigate = "investigate"
	// RunRevise writes a round of the plan that answers its approvers.
	RunRevise = "revise"
	// RunBuild builds the approved plan, and the gate judges each round.
	RunBuild = "build"
	// RunLand lands what was built, or opens its review.
	RunLand = "land"
	// RunRelease watches the landed commit's pipeline to its release.
	RunRelease = "release"
)

// How a run ended.
const (
	RunDone   = "done"
	RunFailed = "failed"
)

// Who wrote a step.
const (
	ByGraft = "graft"
	ByAgent = "agent"
)

// Kinds of step.
const (
	// StepDid is something done, by Graft or the agent.
	StepDid = "step"
	// StepFound is something the agent found.
	StepFound = "found"
	// StepTest is a test run, with OK saying whether it passed.
	StepTest = "test"
	// StepStuck is what the agent says stops it, and what it tried.
	StepStuck = "stuck"
	// StepCommit is a commit the run made, with its SHA.
	StepCommit = "commit"
	// StepPipeline is a pipeline run of the landed commit, with its state.
	StepPipeline = "pipeline"
)

// Run is one pass of one stage of a request: what it did, as it did it, and
// how it ended. Its comment on the ticket shows it.
type Run struct {
	N    int    `json:"n"`
	Kind string `json:"kind"`
	// Round is the plan round it writes, for a round, or builds on.
	Round int `json:"round"`
	// Attempt counts the runs of this kind and round, from 1.
	Attempt int `json:"attempt"`
	// Retries names the run that failed, which this one tries again, and
	// RetriedBy who asked for it, Via where.
	Retries   int    `json:"retries,omitempty"`
	RetriedBy string `json:"retriedBy,omitempty"`
	Via       string `json:"via,omitempty"`
	// Revises names the run that wrote the round before, for a revision,
	// and Answers says what it answers.
	Revises int      `json:"revises,omitempty"`
	Answers []string `json:"answers,omitempty"`
	// Budget is what each agent prompt of the run may spend; Session names
	// its agent session.
	Budget  float64 `json:"budget,omitempty"`
	Session string  `json:"session,omitempty"`
	// Agents is what the run's agent prompts consumed, as the runtime said.
	Agents *Tally `json:"agents,omitempty"`

	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Outcome is RunDone or RunFailed once it ended; Summary says how, in a
	// line.
	Outcome string `json:"outcome,omitempty"`
	Summary string `json:"summary,omitempty"`

	// Note is the run's comment on the ticket.
	Note *RunNote `json:"note,omitempty"`

	Steps []Step `json:"steps,omitempty"`
	// Criteria are what the plan says must hold when the build is done, and
	// Proofs what shows each.
	Criteria []string `json:"criteria,omitempty"`
	Proofs   []Proof  `json:"proofs,omitempty"`
	// Waived are the proofs the plan's approvers let the build land
	// without.
	Waived []Waiver `json:"waived,omitempty"`
	// Shots are the screenshots taken on the ticket; NoShots why there are
	// none where the plan changes what people see; Data what data they show.
	Shots   []Shot `json:"shots,omitempty"`
	NoShots string `json:"noShots,omitempty"`
	Data    string `json:"data,omitempty"`
	// Left are what Graft left off the comment, and why, one line each.
	Left []string `json:"left,omitempty"`
	// Closing is what the run says last, such as where its round is decided.
	Closing string `json:"closing,omitempty"`

	Failure *Failure `json:"failure,omitempty"`
}

// Tally is what a run's agent prompts consumed: how many, their turns, and
// what the runtime said they spent. Unknown counts the prompts it reported
// nothing of, which add no dollars.
type Tally struct {
	Prompts int     `json:"prompts"`
	Turns   int     `json:"turns"`
	USD     float64 `json:"usd"`
	Unknown int     `json:"unknown,omitempty"`
}

// Add counts one agent prompt.
func (t *Tally) Add(a AgentRun) {
	t.Prompts++
	t.Turns += a.Turns
	t.USD += a.CostUSD
	if a.Cost == CostUnknown {
		t.Unknown++
	}
}

// RunNote is a run's comment on the ticket.
type RunNote struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

// Step is one thing a run did or found.
type Step struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	// Detail is more of it, folded.
	Detail string `json:"detail,omitempty"`
	// OK is a test's or a pipeline's result: nil while unknown.
	OK *bool `json:"ok,omitempty"`
	// SHA is a commit's; URL where a step's thing is.
	SHA string `json:"sha,omitempty"`
	URL string `json:"url,omitempty"`
}

// Shot is one screenshot of the finished work, on the ticket.
type Shot struct {
	Name   string `json:"name"`
	Screen string `json:"screen"`
	// State is "before" or "after".
	State   string `json:"state"`
	Caption string `json:"caption,omitempty"`
	// URL is where the forge keeps it.
	URL string `json:"url"`
}

// Proof is what shows one criterion of the plan holds: screenshots by name,
// or the named test that proves it where nothing is seen.
type Proof struct {
	// Criterion is its place in the plan's list, from 1.
	Criterion int      `json:"criterion"`
	Shots     []string `json:"shots,omitempty"`
	Test      string   `json:"test,omitempty"`
}

// Failure is how a run that failed stopped: why, where, what it tried, and
// what its agent said stopped it. The next attempt of its stage reads it.
type Failure struct {
	Cause string `json:"cause"`
	Where string `json:"where,omitempty"`
	// Tried are the last things the run did, and Stuck what its agent said
	// stops it.
	Tried []string `json:"tried,omitempty"`
	Stuck []string `json:"stuck,omitempty"`
	// Detail is the end of what the failing step reported.
	Detail string `json:"detail,omitempty"`
	// Key is the program setting that gives it more room, if any.
	Key string `json:"key,omitempty"`
}

// RunLink is a run as the request lists it: enough to link to its comment.
type RunLink struct {
	N       int    `json:"n"`
	Kind    string `json:"kind"`
	Round   int    `json:"round"`
	Attempt int    `json:"attempt"`
	Outcome string `json:"outcome,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Link is the run as the request lists it.
func (r *Run) Link() RunLink {
	l := RunLink{N: r.N, Kind: r.Kind, Round: r.Round, Attempt: r.Attempt, Outcome: r.Outcome}
	if r.Note != nil {
		l.URL = r.Note.URL
	}
	return l
}

// Open says the run has not ended.
func (r *Run) Open() bool { return r.Ended.IsZero() }

// PutRun keeps a run as it stands now.
func (s *Store) PutRun(ctx context.Context, request string, r *Run) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (request, n, doc) VALUES (?,?,?) ON CONFLICT (request, n) DO UPDATE SET doc = excluded.doc`, request, r.N, string(raw))
	return err
}

// Runs are a request's runs, oldest first.
func (s *Store) Runs(ctx context.Context, request string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM runs WHERE request=? ORDER BY n`, request)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

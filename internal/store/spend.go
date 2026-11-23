package store

import (
	"context"
	"encoding/json"
	"time"
)

// Stages an agent run serves, as spend is told by.
const (
	// SpendScreen is the one cheap call that sorts a request.
	SpendScreen = "screen"
	// SpendInvestigate is the investigator of a request's first round.
	SpendInvestigate = "investigate"
	// SpendRevise is the investigator of a round that answers the approvers.
	SpendRevise = "revise"
	// SpendBuild is a builder run: a round of the build, or of its repair.
	SpendBuild = "build"
	// SpendProve is a prover run: the proof of each criterion of a build.
	SpendProve = "prove"
)

// Where an agent run's spend came from.
const (
	// CostReported is what the runtime reported as it closed the run.
	CostReported = "reported"
	// CostStopped is what the runtime said the run had spent when it
	// stopped it at its budget: the dollars, and nothing else.
	CostStopped = "stopped"
	// CostUnknown is a run the runtime reported nothing of: its spend is
	// not known, and counts as nothing.
	CostUnknown = "unknown"
)

// How an agent run ended.
const (
	AgentDone    = "done"
	AgentCapped  = "capped"
	AgentTimeout = "timeout"
	AgentFailed  = "failed"
)

// AgentRun is one prompt Graft sent an agent runtime, and what the runtime
// said it consumed: one model call, from the screen to each builder round
// and each wind-down. Its spend is the runtime's figure, never Graft's.
type AgentRun struct {
	ID      int64  `json:"id"`
	Request string `json:"-"`
	// Stage is what it served (SpendScreen, ...), Round the plan round, and
	// Run the stage's run on the ticket it belongs to, 0 for none.
	Stage string `json:"stage"`
	Round int    `json:"round"`
	Run   int    `json:"run,omitempty"`
	// Attempt is its stage run's attempt; Try counts its prompts in that
	// run: the investigator's tries, the builder's rounds.
	Attempt int `json:"attempt,omitempty"`
	Try     int `json:"try,omitempty"`
	// WindDown says it was the prompt that asks a run at its budget to write
	// what it has.
	WindDown bool   `json:"windDown,omitempty"`
	Session  string `json:"session,omitempty"`

	Model string `json:"model,omitempty"`
	// Cost says where CostUSD came from: CostReported, CostStopped, or
	// CostUnknown.
	Cost                string    `json:"cost"`
	CostUSD             float64   `json:"costUsd"`
	InputTokens         int       `json:"inputTokens,omitempty"`
	OutputTokens        int       `json:"outputTokens,omitempty"`
	CacheReadTokens     int       `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int       `json:"cacheCreationTokens,omitempty"`
	Turns               int       `json:"turns,omitempty"`
	DurationMs          int64     `json:"durationMs"`
	Outcome             string    `json:"outcome"`
	At                  time.Time `json:"at"`
}

// AddAgentRun keeps one agent run, and gives it its id.
func (s *Store) AddAgentRun(ctx context.Context, a *AgentRun) error {
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, `INSERT INTO agent_runs (request, at, doc) VALUES (?,?,?) RETURNING id`, a.Request, a.At.UTC().Format(stamp), string(raw)).Scan(&a.ID)
}

// AgentRuns are a request's agent runs, oldest first.
func (s *Store) AgentRuns(ctx context.Context, request string) ([]AgentRun, error) {
	all, err := s.agentRuns(ctx, `WHERE request = ?`, request)
	return all[request], err
}

// AllSpend is every request's spend, by request; a request with no agent run
// is not in it.
func (s *Store) AllSpend(ctx context.Context) (map[string]Spend, error) {
	all, err := s.agentRuns(ctx, ``)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Spend, len(all))
	for id, runs := range all {
		out[id] = SpendOf(runs)
	}
	return out, nil
}

// Spend is a request's spend, read from its agent runs.
func (s *Store) Spend(ctx context.Context, request string) (Spend, error) {
	runs, err := s.AgentRuns(ctx, request)
	return SpendOf(runs), err
}

func (s *Store) agentRuns(ctx context.Context, where string, args ...any) (map[string][]AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, request, doc FROM agent_runs `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]AgentRun{}
	for rows.Next() {
		var a AgentRun
		var id int64
		var request, raw string
		if err := rows.Scan(&id, &request, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		a.ID, a.Request = id, request
		out[request] = append(out[request], a)
	}
	return out, rows.Err()
}

// Spend is what a request's agents spent: the total, and each stage and
// round apart. Only the runtime's figures are in it; a run it reported
// nothing of is counted in Runs and Unknown, and adds no dollars.
type Spend struct {
	TotalUSD float64 `json:"totalUsd"`
	// Runs counts every agent run; Unknown those whose spend is not known.
	Runs    int `json:"runs"`
	Unknown int `json:"unknown,omitempty"`
	// Parts are the stages and rounds, in the order they first ran.
	Parts []SpendPart `json:"parts,omitempty"`
	// AgentRuns are every run, oldest first.
	AgentRuns []AgentRun `json:"agentRuns,omitempty"`
}

// SpendPart is one stage of one round: the screen, round 1's investigation,
// round 2's revision, the build of round 2.
type SpendPart struct {
	Stage               string  `json:"stage"`
	Round               int     `json:"round"`
	USD                 float64 `json:"usd"`
	Runs                int     `json:"runs"`
	Turns               int     `json:"turns"`
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	DurationMs          int64   `json:"durationMs"`
}

// SpendOf sums agent runs into a request's spend.
func SpendOf(runs []AgentRun) Spend {
	var s Spend
	at := map[[2]any]int{}
	for _, a := range runs {
		s.Runs++
		s.TotalUSD += a.CostUSD
		if a.Cost == CostUnknown {
			s.Unknown++
		}
		k := [2]any{a.Stage, a.Round}
		i, ok := at[k]
		if !ok {
			i = len(s.Parts)
			at[k] = i
			s.Parts = append(s.Parts, SpendPart{Stage: a.Stage, Round: a.Round})
		}
		p := &s.Parts[i]
		p.USD += a.CostUSD
		p.Runs++
		p.Turns += a.Turns
		p.InputTokens += a.InputTokens
		p.OutputTokens += a.OutputTokens
		p.CacheReadTokens += a.CacheReadTokens
		p.CacheCreationTokens += a.CacheCreationTokens
		p.DurationMs += a.DurationMs
	}
	s.AgentRuns = runs
	return s
}

// ByStage is the spend of each stage, all rounds together.
func (s Spend) ByStage() map[string]float64 {
	out := map[string]float64{}
	for _, p := range s.Parts {
		out[p.Stage] += p.USD
	}
	return out
}

// Package runtime is the crossing to whatever runs an agent. Graft asks a
// runtime for three things: run a prompt in a directory under a model, a
// turn limit, a time limit, and a spend limit; ask a model one question;
// and stop a session. Each run answers what the runtime says it consumed.
package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrTimeout is a run that hit its time limit. What it left is still there.
var ErrTimeout = errors.New("runtime: the run hit its time limit")

// ErrBudget is a run the runtime stopped at its spend limit. The runtime
// stops a run between turns, so what it wrote is still there, and a later
// prompt with the same key continues the same conversation.
var ErrBudget = errors.New("runtime: the run reached its spend limit")

// Session is one prompt to a keyed session. A later prompt with the same
// key continues the same conversation while the runtime still holds it.
type Session struct {
	Dir      string
	Key      string
	Model    string
	MaxTurns int
	Timeout  time.Duration
	// Budget is the most this one prompt's run may spend, in US dollars; 0
	// sets none. A later prompt to the same session has a budget of its own.
	Budget float64
	Prompt string
}

// Where a run's spend came from.
const (
	// FromRun is the runtime's own report of the run, as it closed it.
	FromRun = "run"
	// FromStop is the runtime's words when it stopped the run at its
	// budget, which name what it had spent; they carry nothing else.
	FromStop = "stop"
)

// Usage is what the runtime says one prompt's run consumed. Graft never
// estimates it: a run whose end the runtime did not report has no From and
// no spend.
type Usage struct {
	// From says where the figures came from: FromRun, FromStop, or empty
	// when the runtime reported nothing.
	From string
	// Model is the model the runtime ran, as it names it.
	Model   string
	CostUSD float64
	// The provider's token counts, summed across the run's turns.
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	Turns               int
	// Duration is the runtime's own, or the wall clock where it gave none.
	Duration time.Duration
	// Reason is how the runtime says the run completed, when it says.
	Reason string
}

// Reported says the runtime said what the run spent.
func (u Usage) Reported() bool { return u.From != "" }

// Runtime runs agents.
type Runtime interface {
	// Name is the runtime's name as Annals records it.
	Name() string
	// Run sends the prompt and streams the agent's output to out until the
	// run is idle, and answers what the run consumed, however it ended.
	Run(ctx context.Context, s Session, out io.Writer) (Usage, error)
	// Ask runs one prompt with no session to keep and returns what the
	// model wrote, and what the run consumed.
	Ask(ctx context.Context, s Session) (string, Usage, error)
	// Stop ends a keyed session. It is safe for a session that is gone.
	Stop(ctx context.Context, key string) error
}

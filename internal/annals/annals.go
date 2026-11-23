// Package annals posts each step of a request to Annals, the audit log, one
// event each, under Graft on behalf of the person who asked. Each event
// names the one before it as its cause, so a request reads as one chain.
// A post never blocks a request: a failure is logged and dropped, and a
// refused event is never retried, because it would be refused again.
package annals

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	kit "github.com/orchard-suite/platformkit/v3/annals"

	"github.com/orchard-suite/graft/internal/store"
)

// source is the Annals source Graft's token is issued for.
const source = "graft"

// Outcomes of a step.
const (
	OK      = "ok"
	Refused = "refused"
	Failed  = "failed"
)

// Client posts to one Annals. A nil Client posts nothing, so a Graft
// without Annals calls it like any other.
type Client struct {
	annals  *kit.Client
	runtime string
	log     *slog.Logger
	now     func() time.Time
	post    func(ctx context.Context, ev kit.Event) error
}

// New returns a client for the Annals at url, or nil when url or token is
// empty. runtime is the agent runtime's name, for steps an agent took.
func New(url, token, runtime string, log *slog.Logger) *Client {
	c := kit.New(url, token, source)
	if c == nil {
		return nil
	}
	return &Client{annals: c, runtime: runtime, log: log, now: time.Now, post: c.Post}
}

// Step is one thing that happened to a request.
type Step struct {
	// Verb is what happened, such as request.planned.
	Verb string
	// Outcome is OK when empty.
	Outcome string
	// Reason says why a step that did not go well ended that way. It is
	// Graft's own words, never an agent's or a reporter's.
	Reason string
	// Model and Session name the agent session when an agent took the step.
	Model, Session string
	// Approval says whether a person's yes was needed, and who gave it.
	Approval *kit.Approval
	Change   *kit.Change
	// Items are things the step touched besides the request: a tool it
	// called, by name and never by what it answered.
	Items  []kit.Item
	Labels map[string]string
}

// Post records one step of the request and returns the event's key, which
// the next step names as its cause. n tells the request's steps apart. A nil client returns the request's last
// key unchanged.
func (c *Client) Post(ctx context.Context, r *store.Request, n string, s Step) string {
	if c == nil {
		return r.AnnalsKey
	}
	outcome := s.Outcome
	if outcome == "" {
		outcome = OK
	}
	key := fmt.Sprintf("request:%s:%s:%s", r.ID, n, s.Verb)
	labels := map[string]string{"lane": r.Lane, "app": r.App}
	if r.Environment != "" {
		labels["environment"] = r.Environment
	}
	for k, v := range s.Labels {
		labels[k] = v
	}
	ev := kit.Event{
		At:       c.now().UTC().Truncate(time.Second),
		EventKey: key,
		Actor:    kit.Actor{Kind: "system", ID: "graft", Name: "Graft", OnBehalfOf: r.Person},
		Action: kit.Action{
			Verb:    s.Verb,
			Target:  kit.Target{System: "graft", Kind: "request", ID: r.ID, Title: r.Repo},
			Outcome: outcome,
		},
		Change: s.Change,
		Items:  s.Items,
		Labels: labels,
	}
	if r.AnnalsKey != "" && r.AnnalsKey != key {
		ev.Cause = r.AnnalsKey
	}
	if outcome != OK {
		ev.Action.Reason = kit.Clip(s.Reason)
	}
	if s.Session != "" {
		ev.Agent = &kit.Agent{Runtime: c.runtime, Model: s.Model, SessionID: s.Session, Harness: "graft"}
	}
	if s.Approval != nil {
		ev.Authority = &kit.Authority{Approval: s.Approval}
	}
	err := c.post(ctx, ev)
	switch {
	case err == nil:
	case kit.Reason(err) != "":
		c.log.Warn("annals refused event", "event_key", key, "reason", kit.Reason(err), "error", err)
	default:
		c.log.Warn("annals post dropped", "event_key", key, "error", err)
	}
	return key
}

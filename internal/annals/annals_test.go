package annals

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	kit "github.com/orchard-suite/platformkit/v3/annals"

	"github.com/orchard-suite/graft/internal/store"
)

func TestStepsChainUnderThePersonWhoAsked(t *testing.T) {
	var sent []kit.Event
	c := &Client{runtime: "ion", log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now,
		post: func(_ context.Context, ev kit.Event) error { sent = append(sent, ev); return nil }}
	r := &store.Request{ID: "abc", Person: "a@example.com", Repo: "acme/api", App: "acme-web", Doc: store.Doc{Lane: "user", Environment: "production"}}
	r.AnnalsKey = c.Post(context.Background(), r, "1", Step{Verb: "request.received"})
	r.AnnalsKey = c.Post(context.Background(), r, "2", Step{Verb: "request.investigated", Model: "m", Session: "graft-abc-investigate"})
	r.AnnalsKey = c.Post(context.Background(), r, "3", Step{Verb: "request.declined", Outcome: Refused, Reason: "junk"})
	if len(sent) != 3 {
		t.Fatalf("sent %d", len(sent))
	}
	first, second, third := sent[0], sent[1], sent[2]
	if first.Actor.Kind != "system" || first.Actor.OnBehalfOf != "a@example.com" || first.Cause != "" {
		t.Errorf("first: %+v", first)
	}
	if second.Cause != first.EventKey || second.Agent == nil || second.Agent.Runtime != "ion" || second.Agent.SessionID != "graft-abc-investigate" {
		t.Errorf("second: %+v", second)
	}
	if third.Cause != second.EventKey || third.Action.Outcome != Refused || third.Action.Reason != "junk" {
		t.Errorf("third: %+v", third)
	}
	if first.Labels["lane"] != "user" || first.Labels["environment"] != "production" {
		t.Errorf("labels: %v", first.Labels)
	}
	for _, ev := range sent {
		if err := ev.Validate(source); err != nil {
			t.Errorf("%s does not meet the Annals contract: %v", ev.EventKey, err)
		}
	}
}

func TestNilClientPostsNothing(t *testing.T) {
	var c *Client
	r := &store.Request{ID: "abc", Doc: store.Doc{AnnalsKey: "k"}}
	if got := c.Post(context.Background(), r, "1", Step{Verb: "request.received"}); got != "k" {
		t.Errorf("a nil client changed the key: %q", got)
	}
}

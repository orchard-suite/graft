// Package telemetry pushes the requests' state to the estate's metrics as
// OTLP gauges, so its alerts can show each request as it starts and ends.
// Every request touched in the last day is reported; older ones drop out.
// A request is labelled by its id, repository, lane, and state, and never by
// anything a person wrote.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/store"
)

const (
	// MetricActive is 1 while a request is being worked or waits on a
	// person or a timer, 0 once it has ended.
	MetricActive = "graft_request_active"
	// MetricFailed is 1 while a request ended because Graft itself could
	// not work it.
	MetricFailed = "graft_request_failed"
	// MetricWaiting is 1 while a request waits on a person: a plan to
	// decide, a pull request to review.
	MetricWaiting = "graft_request_waiting"

	// window is how long a request keeps being reported after it last changed.
	window = 24 * time.Hour
)

type kv struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func attr(key, value string) kv {
	a := kv{Key: key}
	a.Value.StringValue = value
	return a
}

type point struct {
	Attributes   []kv    `json:"attributes"`
	TimeUnixNano string  `json:"timeUnixNano"`
	AsDouble     float64 `json:"asDouble"`
}

type metric struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Gauge       struct {
		DataPoints []point `json:"dataPoints"`
	} `json:"gauge"`
}

func flag(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Payload is one OTLP/JSON metrics request for the requests, or nil when
// none changed within the window.
func Payload(requests []*store.Request, now time.Time) []byte {
	ts := strconv.FormatInt(now.UnixNano(), 10)
	active := metric{Name: MetricActive, Description: "1 while a Graft request is being worked or waits, 0 once it has ended."}
	failed := metric{Name: MetricFailed, Description: "1 while a stage of a Graft request has failed: it waits for a retry, or ended because Graft could not work it."}
	waiting := metric{Name: MetricWaiting, Description: "1 while a Graft request waits on a person: for a decision, a review, or a retry."}
	for _, r := range requests {
		if now.Sub(r.UpdatedAt) > window {
			continue
		}
		attrs := []kv{attr("request_id", r.ID), attr("repo", r.Repo), attr("lane", r.Lane)}
		stated := append(append([]kv{}, attrs...), attr("state", r.State))
		active.Gauge.DataPoints = append(active.Gauge.DataPoints, point{Attributes: attrs, TimeUnixNano: ts, AsDouble: flag(!store.Ended(r.State))})
		failed.Gauge.DataPoints = append(failed.Gauge.DataPoints, point{Attributes: attrs, TimeUnixNano: ts, AsDouble: flag(r.State == store.StateFailed || r.Stall != nil)})
		waiting.Gauge.DataPoints = append(waiting.Gauge.DataPoints, point{Attributes: stated, TimeUnixNano: ts,
			AsDouble: flag(r.State == store.StateAwaitingPlan || r.State == store.StateAwaitingReview || r.State == store.StateStalled)})
	}
	if len(active.Gauge.DataPoints) == 0 {
		return nil
	}
	body := map[string]any{
		"resourceMetrics": []any{map[string]any{
			"resource": map[string]any{"attributes": []kv{attr("service.name", "graft"), attr("service.instance.id", "graft")}},
			"scopeMetrics": []any{map[string]any{
				"scope":   map[string]any{"name": "graft"},
				"metrics": []metric{active, failed, waiting},
			}},
		}},
	}
	out, _ := json.Marshal(body)
	return out
}

// Pusher reports the requests every interval until the context ends.
type Pusher struct {
	Endpoint string
	Every    time.Duration
	Store    *store.Store
	Log      *slog.Logger
	HTTP     *http.Client
}

func (p *Pusher) Run(ctx context.Context) {
	for {
		p.push(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Every):
		}
	}
}

func (p *Pusher) push(ctx context.Context) {
	requests, err := p.Store.List(ctx, store.Filter{Since: time.Now().Add(-30 * 24 * time.Hour)})
	if err != nil {
		p.Log.Error("request metrics not read", "error", err)
		return
	}
	payload := Payload(requests, time.Now())
	if payload == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := Post(ctx, p.HTTP, p.Endpoint, payload); err != nil {
		p.Log.Error("request metrics not pushed", "endpoint", p.Endpoint, "error", err)
	}
}

// Post sends a payload to an OTLP/HTTP receiver's /v1/metrics.
func Post(ctx context.Context, c *http.Client, endpoint string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/metrics", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s: %s", res.Status, bytes.TrimSpace(msg))
	}
	return nil
}

package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/graft/internal/store"
)

type body struct {
	ResourceMetrics []struct {
		ScopeMetrics []struct {
			Metrics []metric `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

// values maps metric name and request id to the value reported.
func values(t *testing.T, raw []byte) map[string]float64 {
	t.Helper()
	var b body
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, m := range b.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		for _, p := range m.Gauge.DataPoints {
			labels := map[string]string{}
			for _, a := range p.Attributes {
				labels[a.Key] = a.Value.StringValue
			}
			out[m.Name+" "+labels["request_id"]] = p.AsDouble
		}
	}
	return out
}

func TestPayloadReportsEachRecentRequest(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	requests := []*store.Request{
		{ID: "working", Repo: "acme/api", State: store.StateBuilding, UpdatedAt: now, Doc: store.Doc{Lane: "developer", Text: "private words", Title: "private title"}},
		{ID: "asked", Repo: "acme/api", State: store.StateAwaitingPlan, UpdatedAt: now},
		{ID: "shipped", Repo: "acme/api", State: store.StateReleased, UpdatedAt: now.Add(-time.Hour)},
		{ID: "broke", Repo: "acme/api", State: store.StateFailed, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "old", Repo: "acme/api", State: store.StateReleased, UpdatedAt: now.Add(-25 * time.Hour)},
	}
	raw := Payload(requests, now)
	got := values(t, raw)
	want := map[string]float64{
		MetricActive + " working": 1, MetricActive + " asked": 1, MetricActive + " shipped": 0, MetricActive + " broke": 0,
		MetricFailed + " working": 0, MetricFailed + " broke": 1,
		MetricWaiting + " asked": 1, MetricWaiting + " working": 0,
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (reported %v), want %v", k, g, ok, v)
		}
	}
	if _, ok := got[MetricActive+" old"]; ok {
		t.Error("a request untouched for a day is still reported")
	}
	if strings.Contains(string(raw), "private") {
		t.Error("a metric carries what a person wrote")
	}
	if Payload(requests[4:], now) != nil {
		t.Error("nothing recent, yet a payload")
	}
}

func TestPostSendsToTheMetricsPath(t *testing.T) {
	var path, sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
	}))
	defer srv.Close()
	if err := Post(context.Background(), srv.Client(), srv.URL+"/", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/metrics" || sent != `{"a":1}` {
		t.Errorf("path %q body %q", path, sent)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "full", http.StatusServiceUnavailable) }))
	defer bad.Close()
	if err := Post(context.Background(), bad.Client(), bad.URL, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "full") {
		t.Errorf("a refusal: %v", err)
	}
}

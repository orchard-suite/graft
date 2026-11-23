package web

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The page's own counters, sent wherever the platform kit sends the
// server's telemetry. A label is a fixed set of values: never a request, a
// repository, or a person.
var (
	reads, _ = otel.Meter("graft").Int64Counter("graft.request.reads",
		metric.WithDescription("Requests read on Graft's page, by who the reader is to the request: staff, approver, or requester."))
	refusals, _ = otel.Meter("graft").Int64Counter("graft.refusals",
		metric.WithDescription("Calls Graft refused on policy, by reason."))
)

// countRead counts one read of a request on the page, or one refused.
func countRead(ctx context.Context, as reader) {
	if as == readerNone {
		refusals.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "not_a_reader")))
		return
	}
	reads.Add(ctx, 1, metric.WithAttributes(attribute.String("reader", string(as))))
}

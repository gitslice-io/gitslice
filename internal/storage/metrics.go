package storage

import (
	"errors"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/metrics"
)

const (
	SubmitReasonNone                = "none"
	SubmitReasonStalePathBase       = "stale_path_base"
	SubmitReasonRequirementsChanged = "requirements_changed"
	SubmitReasonApprovalsMissing    = "approvals_missing"
	SubmitReasonChecksMissing       = "checks_missing"
	SubmitReasonConflict            = "conflict"
	SubmitReasonError               = "error"
)

var (
	submitTotal = metrics.NewCounter(
		"gitslice_submit_total",
		"Changeset submit attempts by acceptance result and blocked reason category.",
		"result",
		"reason",
	)
	publishBatchesTotal = metrics.NewCounter(
		"gitslice_publish_batches_total",
		"PublishPending batch attempts by result.",
		"result",
	)
	publishedChangesetsTotal = metrics.NewCounter(
		"gitslice_published_changesets_total",
		"Changesets successfully published.",
	)
	refCASFailuresTotal = metrics.NewCounter(
		"gitslice_ref_cas_failures_total",
		"Target ref compare-and-swap failures while publishing.",
	)
	pendingPublishQueueDepth = metrics.NewGauge(
		"gitslice_pending_publish_queue_depth",
		"Pending publish queue depth sampled by the publisher loop.",
	)
	publishLatencySeconds = metrics.NewHistogram(
		"gitslice_publish_latency_seconds",
		"Accepted-to-published latency for changesets.",
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	)
)

func RecordSubmitResult(err error) {
	if err == nil {
		submitTotal.Inc(metrics.Labels{"result": "accepted", "reason": SubmitReasonNone})
		return
	}
	reason := SubmitReasonError
	if errors.Is(err, ErrConflict) {
		reason = SubmitBlockedReasonCategory(err.Error())
	}
	submitTotal.Inc(metrics.Labels{"result": "rejected", "reason": reason})
}

func SubmitBlockedReasonCategory(reason string) string {
	reason = strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "path base conflict"):
		return SubmitReasonStalePathBase
	case strings.Contains(reason, "requirements changed"),
		strings.Contains(reason, "outside latest slice definition"):
		return SubmitReasonRequirementsChanged
	case strings.Contains(reason, "required approvals"):
		return SubmitReasonApprovalsMissing
	case strings.Contains(reason, "required check"):
		return SubmitReasonChecksMissing
	default:
		return SubmitReasonConflict
	}
}

func RecordPublishBatch(published int, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	publishBatchesTotal.Inc(metrics.Labels{"result": result})
	if err == nil && published > 0 {
		publishedChangesetsTotal.Add(float64(published), nil)
	}
}

func RecordRefCASFailure() {
	refCASFailuresTotal.Inc(nil)
}

func SetPendingPublishQueueDepth(depth int) {
	pendingPublishQueueDepth.Set(float64(depth), nil)
}

func ObservePublishLatency(duration time.Duration) {
	publishLatencySeconds.Observe(duration.Seconds(), nil)
}

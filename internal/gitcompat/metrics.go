package gitcompat

import (
	"strconv"

	"gitslice.io/gitslice/internal/metrics"
)

var gitHTTPRequestsTotal = metrics.NewCounter(
	"gitslice_git_http_requests_total",
	"Git smart HTTP requests by operation and HTTP status.",
	"operation",
	"status",
)

func recordGitHTTPRequest(operation string, status int) {
	if operation == "" {
		operation = "unknown"
	}
	gitHTTPRequestsTotal.Inc(metrics.Labels{
		"operation": operation,
		"status":    strconv.Itoa(status),
	})
}

var gitMirrorOperationsTotal = metrics.NewCounter(
	"gitslice_git_mirror_operations_total",
	"Git projection mirror operations by operation (publish, restore) and result.",
	"operation",
	"result",
)

func recordGitMirror(operation, result string) {
	gitMirrorOperationsTotal.Inc(metrics.Labels{"operation": operation, "result": result})
}

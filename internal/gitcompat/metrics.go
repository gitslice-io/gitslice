package gitcompat

import (
	"strconv"

	"github.com/gitslice-io/gitslice/internal/metrics"
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

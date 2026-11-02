package storage

import (
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// A slice's pool of CI daemons, and claims on check runs
// (design/32_ci_dispatch_on_cloudflare.md, layer 3).

// CheckRunLease is how long a claim on a check run holds without a sign of
// life from the daemon process that has it. Daemons send a heartbeat every
// 15 s, so a live process renews it several times over.
const CheckRunLease = 90 * time.Second

// LegacyInstanceID is the process id of a daemon that does not name its
// processes (older gs): it counts as a single process.
const LegacyInstanceID = "legacy"

// MaxCheckRunAttempts is how many times a run is tried in all when the
// daemon process running it stops responding.
const MaxCheckRunAttempts = 3

// NormalizeCIDaemons trims ids, drops empty ones and repeats, keeping order.
func NormalizeCIDaemons(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// CIDaemonPool is the slice's CI daemons in order of preference: its pool,
// or its single CI daemon from a store that has no pool.
func CIDaemonPool(slice *corev1.Slice) []string {
	if slice == nil {
		return nil
	}
	if len(slice.GetCiDaemonIds()) > 0 {
		return NormalizeCIDaemons(slice.GetCiDaemonIds())
	}
	return NormalizeCIDaemons([]string{slice.GetCiDaemonId()})
}

// InstanceKey is how a check run's claim names a daemon process: the
// process id within its daemon, as daemons choose their own.
func InstanceKey(daemonID, instanceID string) string {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		instanceID = LegacyInstanceID
	}
	return strings.TrimSpace(daemonID) + "/" + instanceID
}

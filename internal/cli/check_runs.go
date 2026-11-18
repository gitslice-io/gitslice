package cli

import (
	"sort"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// LatestPatchsetCheckRuns returns the most recent attempt for each check on the
// given patchset (or across all patchsets if patchsetID is empty), sorted by check name.
func LatestPatchsetCheckRuns(runs []*corev1.CheckRun, patchsetID string) []*corev1.CheckRun {
	latest := map[string]*corev1.CheckRun{}
	for _, run := range runs {
		if run == nil {
			continue
		}
		if patchsetID != "" && run.GetPatchsetId() != patchsetID {
			continue
		}
		if prev := latest[run.GetCheckName()]; prev == nil || run.GetAttempt() > prev.GetAttempt() || (run.GetAttempt() == prev.GetAttempt() && run.GetCreatedAt() > prev.GetCreatedAt()) {
			latest[run.GetCheckName()] = run
		}
	}
	out := make([]*corev1.CheckRun, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetCheckName() < out[j].GetCheckName() })
	return out
}

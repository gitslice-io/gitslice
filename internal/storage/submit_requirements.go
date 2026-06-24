package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

const (
	CheckStatusPass = "pass"
	CheckStatusFail = "fail"
)

func NormalizeCheckStatus(status string) (string, bool) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case CheckStatusPass, CheckStatusFail:
		return status, true
	default:
		return "", false
	}
}

func EvaluateSubmitRequirements(req *corev1.SubmitRequirements, author string, approvalSubjectIDs []string, checkStatuses map[string]string) string {
	if req == nil {
		return ""
	}
	if req.RequiredApprovals > 0 {
		count := DistinctNonAuthorApprovalCount(author, approvalSubjectIDs)
		if count < req.RequiredApprovals {
			return fmt.Sprintf("required approvals not satisfied: requires %d distinct non-author approval(s), has %d", req.RequiredApprovals, count)
		}
	}
	for _, check := range req.RequiredChecks {
		check = strings.TrimSpace(check)
		if check == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(checkStatuses[check]))
		switch status {
		case CheckStatusPass:
			continue
		case "":
			return fmt.Sprintf("required check %q has no result for current patchset", check)
		case CheckStatusFail:
			return fmt.Sprintf("required check %q is failing for current patchset", check)
		default:
			return fmt.Sprintf("required check %q is not passing for current patchset", check)
		}
	}
	return ""
}

// SubmitRequirementsHash hashes only the fields that gate whether a patchset is
// still valid to submit: included paths and the required approvals/checks. It
// deliberately excludes slice visibility and definition version, so cosmetic
// definition edits (e.g. flipping visibility) do not force open changesets to
// refresh. Inputs are canonicalized (sorted) so ordering differences between
// callers do not change the hash.
func SubmitRequirementsHash(includedPaths []string, requiredApprovals int32, requiredChecks []string) string {
	included := make([]string, len(includedPaths))
	copy(included, includedPaths)
	sort.Strings(included)

	checks := make([]string, 0, len(requiredChecks))
	for _, check := range requiredChecks {
		check = strings.TrimSpace(check)
		if check == "" {
			continue
		}
		checks = append(checks, check)
	}
	sort.Strings(checks)

	payload, _ := json.Marshal(struct {
		IncludedPaths     []string `json:"included_paths"`
		RequiredApprovals int32    `json:"required_approvals"`
		RequiredChecks    []string `json:"required_checks"`
	}{
		IncludedPaths:     included,
		RequiredApprovals: requiredApprovals,
		RequiredChecks:    checks,
	})
	sum := sha256.Sum256(payload)
	return "submitreq_sha256:" + hex.EncodeToString(sum[:])
}

func DistinctNonAuthorApprovalCount(author string, subjectIDs []string) int32 {
	author = strings.TrimSpace(author)
	seen := map[string]struct{}{}
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" || subjectID == author {
			continue
		}
		seen[subjectID] = struct{}{}
	}
	return int32(len(seen))
}

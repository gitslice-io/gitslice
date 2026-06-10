package storage

import (
	"fmt"
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

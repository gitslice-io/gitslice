package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	CheckStatusPass = "pass"
	CheckStatusFail = "fail"
)

// StaleBaseReason is why a submit is refused when a path the patchset changes
// moved on since its base. A slice with merge_on_submit merges the changeset
// onto the latest base instead (design/07 section 7.4).
const StaleBaseReason = "path base conflict, refresh or rebase the changeset"

// SubmitMergeKind is the kind of a patchset the server made by merging a
// stale changeset onto the latest base at submit.
const SubmitMergeKind = "submit_merge"

func NormalizeCheckStatus(status string) (string, bool) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case CheckStatusPass, CheckStatusFail:
		return status, true
	default:
		return "", false
	}
}

// EvaluateSubmitRequirements checks approval, human approval, and required check
// policies for a changeset. excludedAuthors must contain the changeset author
// and every non-mechanical (kind != SubmitMergeKind) patchset author.
func EvaluateSubmitRequirements(req *corev1.SubmitRequirements, excludedAuthors []Subject, approvers []Subject, checkStatuses map[string]string) string {
	return EvaluateSubmitRequirementsWithGroup(req, excludedAuthors, approvers, checkStatuses, nil)
}

// EvaluateSubmitRequirementsWithGroup is EvaluateSubmitRequirements where, when
// req.Approvers is non-empty (@account/name), candidate approvers are filtered
// to members of that group (approverGroupMembers) before controller-dedup and
// human-approval counting.
func EvaluateSubmitRequirementsWithGroup(req *corev1.SubmitRequirements, excludedAuthors []Subject, approvers []Subject, checkStatuses map[string]string, approverGroupMembers map[string]bool) string {
	if req == nil {
		return ""
	}
	if req.RequiredApprovals > 0 || req.RequiredHumanApprovals > 0 {
		candidates := approvers
		approversGroup := strings.TrimSpace(req.GetApprovers())
		if approversGroup != "" {
			candidates = FilterApproversByMembership(approvers, approverGroupMembers)
		}
		eligible := FilterApprovalSubjects(candidates, excludedAuthors...)
		if req.RequiredApprovals > 0 {
			count := DistinctControllerApprovalCount(eligible)
			if count < req.RequiredApprovals {
				if approversGroup != "" {
					return fmt.Sprintf("required approvals not satisfied: requires %d distinct non-author approval(s) from %s, has %d", req.RequiredApprovals, approversGroup, count)
				}
				return fmt.Sprintf("required approvals not satisfied: requires %d distinct non-author approval(s), has %d", req.RequiredApprovals, count)
			}
		}
		if req.RequiredHumanApprovals > 0 {
			count := DistinctHumanApprovalCount(eligible)
			if count < req.RequiredHumanApprovals {
				if approversGroup != "" {
					return fmt.Sprintf("required human approvals not satisfied: requires %d human approval(s) from %s, has %d", req.RequiredHumanApprovals, approversGroup, count)
				}
				return fmt.Sprintf("required human approvals not satisfied: requires %d human approval(s), has %d", req.RequiredHumanApprovals, count)
			}
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

// FilterApproversByMembership keeps only approver subjects whose ID is present
// in allowedSubjectIDs. An empty or nil map yields no approvers (fail-closed).
func FilterApproversByMembership(approvers []Subject, allowedSubjectIDs map[string]bool) []Subject {
	if len(allowedSubjectIDs) == 0 || len(approvers) == 0 {
		return nil
	}
	out := make([]Subject, 0, len(approvers))
	for _, approver := range approvers {
		if allowedSubjectIDs[strings.TrimSpace(approver.ID)] {
			out = append(out, approver)
		}
	}
	return out
}

// IsRequirementBlock reports whether a submit-blocked reason is one
// EvaluateSubmitRequirements gives (a check or approval requirement). Such a
// reason describes the last submit attempt; a new check result or approval
// for the patchset makes it stale.
func IsRequirementBlock(reason string) bool {
	return strings.HasPrefix(reason, "required check ") ||
		strings.HasPrefix(reason, "required approvals ") ||
		strings.HasPrefix(reason, "required human approvals ") ||
		strings.HasPrefix(reason, "path owner") ||
		strings.HasPrefix(reason, ".gitslice")
}

// SubmitRequirementsHash hashes only the fields that gate whether a patchset is
// still valid to submit: included paths and the required approvals/checks. It
// deliberately excludes slice visibility and definition version, so cosmetic
// definition edits (e.g. flipping visibility) do not force open changesets to
// refresh. Inputs are canonicalized (sorted) so ordering differences between
// callers do not change the hash.
func SubmitRequirementsHash(includedPaths []string, requiredApprovals, requiredHumanApprovals int32, requiredChecks []string) string {
	return SubmitRequirementsHashWithApprovers(includedPaths, requiredApprovals, requiredHumanApprovals, requiredChecks, "")
}

// SubmitRequirementsHashWithApprovers is SubmitRequirementsHash including the
// optional slice approvers group (@account/name).
func SubmitRequirementsHashWithApprovers(includedPaths []string, requiredApprovals, requiredHumanApprovals int32, requiredChecks []string, approvers string) string {
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
		IncludedPaths          []string `json:"included_paths"`
		RequiredApprovals      int32    `json:"required_approvals"`
		RequiredHumanApprovals int32    `json:"required_human_approvals,omitempty"`
		RequiredChecks         []string `json:"required_checks"`
		Approvers              string   `json:"approvers,omitempty"`
	}{
		IncludedPaths:          included,
		RequiredApprovals:      requiredApprovals,
		RequiredHumanApprovals: requiredHumanApprovals,
		RequiredChecks:         checks,
		Approvers:              strings.ToLower(strings.TrimSpace(approvers)),
	})
	sum := sha256.Sum256(payload)
	return "submitreq_sha256:" + hex.EncodeToString(sum[:])
}

// FilterApprovalSubjects keeps only approver subjects that have a non-empty
// ControllerSubjectID (unclaimed agents never count) and whose subject ID and
// ControllerSubjectID differ from every excluded author (for example the
// changeset author and every non-mechanical patchset author).
func FilterApprovalSubjects(approvers []Subject, excludedAuthors ...Subject) []Subject {
	excludedIDs := make(map[string]struct{}, len(excludedAuthors))
	excludedControllers := make(map[string]struct{}, len(excludedAuthors))
	for _, author := range excludedAuthors {
		if id := strings.TrimSpace(author.ID); id != "" {
			excludedIDs[id] = struct{}{}
		}
		if ctrl := strings.TrimSpace(author.ControllerSubjectID); ctrl != "" {
			excludedControllers[ctrl] = struct{}{}
		}
	}
	out := make([]Subject, 0, len(approvers))
	for _, approver := range approvers {
		id := strings.TrimSpace(approver.ID)
		ctrl := strings.TrimSpace(approver.ControllerSubjectID)
		if id == "" || ctrl == "" {
			continue
		}
		if _, skip := excludedIDs[id]; skip {
			continue
		}
		if _, skip := excludedControllers[ctrl]; skip {
			continue
		}
		out = append(out, Subject{
			ID:                  id,
			Kind:                strings.TrimSpace(approver.Kind),
			ControllerSubjectID: ctrl,
			DisplayName:         approver.DisplayName,
		})
	}
	return out
}

// DistinctControllerApprovalCount counts distinct non-empty controller subject
// IDs across eligible approvers.
func DistinctControllerApprovalCount(approvers []Subject) int32 {
	seen := map[string]struct{}{}
	for _, approver := range approvers {
		ctrl := strings.TrimSpace(approver.ControllerSubjectID)
		if ctrl == "" {
			continue
		}
		seen[ctrl] = struct{}{}
	}
	return int32(len(seen))
}

// DistinctHumanApprovalCount counts distinct eligible approvers of kind "user".
func DistinctHumanApprovalCount(approvers []Subject) int32 {
	seen := map[string]struct{}{}
	for _, approver := range approvers {
		if strings.TrimSpace(approver.Kind) != "user" || strings.TrimSpace(approver.ControllerSubjectID) == "" {
			continue
		}
		id := strings.TrimSpace(approver.ID)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	return int32(len(seen))
}

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// SubmitBlockCode classifies why a draft changeset was blocked at submit time.
type SubmitBlockCode string

const (
	SubmitBlockCodeNone                SubmitBlockCode = ""
	SubmitBlockCodeStalePathBase       SubmitBlockCode = "stale_path_base"
	SubmitBlockCodeRequirementsChanged SubmitBlockCode = "requirements_changed"
	SubmitBlockCodeApprovalsMissing    SubmitBlockCode = "approvals_missing"
	SubmitBlockCodeChecksMissing       SubmitBlockCode = "checks_missing"
	SubmitBlockCodeBlockedOnParent     SubmitBlockCode = "blocked_on_parent"
)

// RequirementBlockCodes returns the submit_blocked_code values that represent
// transient check or approval blocks cleared when a new check result or
// approval arrives for the current patchset.
func RequirementBlockCodes() []string {
	return []string{
		string(SubmitBlockCodeApprovalsMissing),
		string(SubmitBlockCodeChecksMissing),
	}
}

// IsRequirementBlock reports whether c is a check or approval requirement block.
func (c SubmitBlockCode) IsRequirementBlock() bool {
	return c == SubmitBlockCodeApprovalsMissing || c == SubmitBlockCodeChecksMissing
}

// Category maps a typed SubmitBlockCode to its Prometheus submit metric reason label.
func (c SubmitBlockCode) Category() string {
	switch c {
	case SubmitBlockCodeStalePathBase:
		return SubmitReasonStalePathBase
	case SubmitBlockCodeRequirementsChanged:
		return SubmitReasonRequirementsChanged
	case SubmitBlockCodeApprovalsMissing:
		return SubmitReasonApprovalsMissing
	case SubmitBlockCodeChecksMissing:
		return SubmitReasonChecksMissing
	default:
		return SubmitReasonConflict
	}
}

// SubmitBlockedError wraps a submit conflict with a typed SubmitBlockCode while
// unwrapping to ErrConflict so errors.Is(err, ErrConflict) remains true.
type SubmitBlockedError struct {
	Code   SubmitBlockCode
	Reason string
}

func (e *SubmitBlockedError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", ErrConflict, e.Reason)
}

func (e *SubmitBlockedError) Unwrap() error {
	return ErrConflict
}

// NewSubmitBlockedError constructs a typed *SubmitBlockedError. If code is
// empty, it classifies reason into a SubmitBlockCode automatically.
func NewSubmitBlockedError(code SubmitBlockCode, reason string) error {
	if code == SubmitBlockCodeNone {
		code = ClassifySubmitBlockedReason(reason)
	}
	return &SubmitBlockedError{
		Code:   code,
		Reason: reason,
	}
}

// SubmitBlockCodeOf extracts the typed SubmitBlockCode from err if it wraps
// *SubmitBlockedError.
func SubmitBlockCodeOf(err error) (SubmitBlockCode, bool) {
	var blocked *SubmitBlockedError
	if errors.As(err, &blocked) && blocked != nil {
		return blocked.Code, true
	}
	return SubmitBlockCodeNone, false
}

// ClassifySubmitBlockedReason maps a human-readable submit block reason string
// to its canonical SubmitBlockCode.
func ClassifySubmitBlockedReason(reason string) SubmitBlockCode {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return SubmitBlockCodeNone
	}
	if trimmed == "BlockedOnBaseChangeset" {
		return SubmitBlockCodeBlockedOnParent
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "path base conflict"):
		return SubmitBlockCodeStalePathBase
	case strings.Contains(lower, "requirements changed"),
		strings.Contains(lower, "outside latest slice definition"):
		return SubmitBlockCodeRequirementsChanged
	case strings.HasPrefix(trimmed, "required approvals "),
		strings.HasPrefix(trimmed, "required human approvals "),
		strings.HasPrefix(trimmed, "path owner"),
		strings.HasPrefix(trimmed, ".gitslice"),
		strings.Contains(lower, "required approvals"),
		strings.Contains(lower, "required human approvals"),
		strings.Contains(lower, "path owner"),
		strings.Contains(lower, ".gitslice"):
		return SubmitBlockCodeApprovalsMissing
	case strings.HasPrefix(trimmed, "required check "),
		strings.Contains(lower, "required check"):
		return SubmitBlockCodeChecksMissing
	default:
		return SubmitBlockCodeNone
	}
}

const (
	CheckStatusPass = model.CheckStatusPass
	CheckStatusFail = model.CheckStatusFail
)

// StaleBaseReason is why a submit is refused when a path the patchset changes
// moved on since its base. A slice with merge_on_submit merges the changeset
// onto the latest base instead (design/07 section 7.4).
const StaleBaseReason = "path base conflict, refresh or rebase the changeset"

// SubmitMergeKind is the kind of a patchset the server made by merging a
// stale changeset onto the latest base at submit.
const SubmitMergeKind = model.SubmitMergeKind

func NormalizeCheckStatus(status string) (string, bool) {
	return model.NormalizeCheckStatus(status)
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
	_, reason := EvaluateSubmitRequirementsWithCode(req, excludedAuthors, approvers, checkStatuses, approverGroupMembers)
	return reason
}

// EvaluateSubmitRequirementsWithCode evaluates approval, human approval, and
// required check policies for a changeset and returns both a typed
// SubmitBlockCode and the human-readable failure reason.
func EvaluateSubmitRequirementsWithCode(req *corev1.SubmitRequirements, excludedAuthors []Subject, approvers []Subject, checkStatuses map[string]string, approverGroupMembers map[string]bool) (SubmitBlockCode, string) {
	if req == nil {
		return SubmitBlockCodeNone, ""
	}
	if req.RequiredApprovals > 0 || req.RequiredHumanApprovals > 0 {
		approversGroup := strings.TrimSpace(req.GetApprovers())
		eval := EvaluateControllerApprovals(ControllerApprovalInput{
			Authors:           excludedAuthors,
			Approvers:         approvers,
			AllowedSubjectIDs: approverGroupMembers,
			RestrictToAllowed: approversGroup != "",
		})
		if req.RequiredApprovals > 0 {
			if eval.ControllerCount < req.RequiredApprovals {
				if approversGroup != "" {
					return SubmitBlockCodeApprovalsMissing, fmt.Sprintf("required approvals not satisfied: requires %d distinct non-author approval(s) from %s, has %d", req.RequiredApprovals, approversGroup, eval.ControllerCount)
				}
				return SubmitBlockCodeApprovalsMissing, fmt.Sprintf("required approvals not satisfied: requires %d distinct non-author approval(s), has %d", req.RequiredApprovals, eval.ControllerCount)
			}
		}
		if req.RequiredHumanApprovals > 0 {
			if eval.HumanCount < req.RequiredHumanApprovals {
				if approversGroup != "" {
					return SubmitBlockCodeApprovalsMissing, fmt.Sprintf("required human approvals not satisfied: requires %d human approval(s) from %s, has %d", req.RequiredHumanApprovals, approversGroup, eval.HumanCount)
				}
				return SubmitBlockCodeApprovalsMissing, fmt.Sprintf("required human approvals not satisfied: requires %d human approval(s), has %d", req.RequiredHumanApprovals, eval.HumanCount)
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
			return SubmitBlockCodeChecksMissing, fmt.Sprintf("required check %q has no result for current patchset", check)
		case CheckStatusFail:
			return SubmitBlockCodeChecksMissing, fmt.Sprintf("required check %q is failing for current patchset", check)
		default:
			return SubmitBlockCodeChecksMissing, fmt.Sprintf("required check %q is not passing for current patchset", check)
		}
	}
	return SubmitBlockCodeNone, ""
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
	return ClassifySubmitBlockedReason(reason).IsRequirementBlock()
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
	return filterEligibleApprovers(approvers, excludedIDs, excludedControllers)
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

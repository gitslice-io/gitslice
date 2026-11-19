package storage

import "strings"

// ControllerApprovalInput describes the authors, candidate approvers, and
// optional ownership/group constraints for controller-deduplicated approval
// evaluation.
type ControllerApprovalInput struct {
	Authors                 []Subject
	Approvers               []Subject
	AllowedSubjects         []Subject
	AllowedSubjectIDs       map[string]bool
	RestrictToAllowed       bool
	CountSelfAuthorApproval bool
	SelfOwnBySubject        bool
}

// ControllerApprovalResult reports distinct controller approvals and distinct
// human ("user") approvals satisfied under ControllerApprovalInput.
type ControllerApprovalResult struct {
	ControllerCount int32
	HumanCount      int32
}

// EvaluateControllerApprovals evaluates controller-deduplicated approvals and
// human approvals across slice submit requirements, .gitslice rules, and path
// owner rules. Unclaimed agents (empty ControllerSubjectID) never count as
// owners or approvers.
func EvaluateControllerApprovals(in ControllerApprovalInput) ControllerApprovalResult {
	excludedIDs := make(map[string]struct{}, len(in.Authors))
	excludedControllers := make(map[string]struct{}, len(in.Authors))
	ownerControllers := make(map[string]bool, len(in.AllowedSubjects))
	if in.CountSelfAuthorApproval && !in.SelfOwnBySubject {
		for _, owner := range in.AllowedSubjects {
			ctrl := strings.TrimSpace(owner.ControllerSubjectID)
			if ctrl == "" {
				continue
			}
			ownerControllers[ctrl] = true
		}
	}

	seenAuthors := make(map[string]struct{}, len(in.Authors))
	allAuthorsClaimed := false
	allAuthorsOwn := false
	allAuthorsHuman := false
	for _, author := range in.Authors {
		id := strings.TrimSpace(author.ID)
		ctrl := strings.TrimSpace(author.ControllerSubjectID)
		if id != "" {
			excludedIDs[id] = struct{}{}
		}
		if ctrl != "" {
			excludedControllers[ctrl] = struct{}{}
		}
		if id == "" && ctrl == "" {
			continue
		}
		key := id
		if key == "" {
			key = "ctrl:" + ctrl
		}
		if _, dup := seenAuthors[key]; dup {
			continue
		}
		if len(seenAuthors) == 0 {
			allAuthorsClaimed = true
			allAuthorsOwn = true
			allAuthorsHuman = true
		}
		seenAuthors[key] = struct{}{}
		if !strings.EqualFold(strings.TrimSpace(author.Kind), "user") {
			allAuthorsHuman = false
		}
		if ctrl == "" {
			allAuthorsClaimed = false
			allAuthorsOwn = false
			allAuthorsHuman = false
			continue
		}
		if in.CountSelfAuthorApproval {
			if in.SelfOwnBySubject {
				if !in.AllowedSubjectIDs[id] {
					allAuthorsOwn = false
				}
			} else if !ownerControllers[ctrl] {
				allAuthorsOwn = false
			}
		}
	}

	candidates := in.Approvers
	if in.RestrictToAllowed {
		candidates = FilterApproversByMembership(candidates, in.AllowedSubjectIDs)
	}
	eligible := filterEligibleApprovers(candidates, excludedIDs, excludedControllers)
	res := ControllerApprovalResult{
		ControllerCount: DistinctControllerApprovalCount(eligible),
		HumanCount:      DistinctHumanApprovalCount(eligible),
	}
	if in.CountSelfAuthorApproval {
		// Under ownership rules, a human owner may explicitly approve a
		// changeset authored by their own agent (different subject ID, same
		// controller) to satisfy require_human, without double-counting either
		// ControllerCount or any human who directly authored the changeset.
		seenHumans := make(map[string]struct{}, len(eligible)+len(candidates))
		for _, a := range eligible {
			if strings.EqualFold(strings.TrimSpace(a.Kind), "user") {
				if id := strings.TrimSpace(a.ID); id != "" {
					seenHumans[id] = struct{}{}
				}
			}
		}
		for _, a := range candidates {
			id := strings.TrimSpace(a.ID)
			ctrl := strings.TrimSpace(a.ControllerSubjectID)
			if id == "" || ctrl == "" || !strings.EqualFold(strings.TrimSpace(a.Kind), "user") {
				continue
			}
			if _, isDirectAuthor := excludedIDs[id]; isDirectAuthor {
				continue
			}
			seenHumans[id] = struct{}{}
		}
		if allAuthorsClaimed && allAuthorsOwn {
			res.ControllerCount += int32(len(excludedControllers))
			if allAuthorsHuman {
				for _, author := range in.Authors {
					if id := strings.TrimSpace(author.ID); id != "" && strings.EqualFold(strings.TrimSpace(author.Kind), "user") {
						seenHumans[id] = struct{}{}
					}
				}
			}
		}
		res.HumanCount = int32(len(seenHumans))
	}
	return res
}

func filterEligibleApprovers(approvers []Subject, excludedIDs, excludedControllers map[string]struct{}) []Subject {
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

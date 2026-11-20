package service

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/owners"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// populateApprovalStatus evaluates all approval gates for an open changeset's
// current patchset using the same rules enforced at submit time:
// 1. Slice required_approvals (controller-deduped non-author approvals)
// 2. Slice required_human_approvals (distinct human non-author approvals)
// 3. .gitslice/** account owner/admin approval rule
// 4. Path owner rules (.gitslice/owners.yaml / CODEOWNERS) at target head
//
// Must be called while cs.Author, patchset.Author, and approval.SubjectId still
// carry raw subject IDs (before resolveAuthors rewrites them to usernames).
func (s *ChangesetService) populateApprovalStatus(ctx context.Context, cs *corev1.Changeset) {
	if cs == nil {
		return
	}
	status := strings.ToLower(strings.TrimSpace(cs.GetStatus()))
	if status == "submitted" || status == "abandoned" {
		return
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return
	}

	req := cs.GetSubmitRequirements()
	if s.Slices != nil && cs.GetAuthoringSlice() != nil {
		if slice, err := s.Slices.Resolve(ctx, cs.GetAuthoringSlice()); err == nil && slice != nil && slice.Definition != nil {
			req = &corev1.SubmitRequirements{
				RequiredApprovals:      slice.Definition.RequiredApprovals,
				RequiredHumanApprovals: slice.Definition.RequiredHumanApprovals,
				Approvers:              slice.Definition.Approvers,
				RequiredChecks:         slice.Definition.RequiredChecks,
			}
		}
	}

	approverIDs := approvalSubjectIDsForPatchset(cs, patchset.GetId())
	authorIDs := changesetNonMechanicalAuthors(cs)

	var requirements []*corev1.ApprovalRequirement

	// 1 & 2. Slice approval and human-approval gates.
	if req != nil && (req.GetRequiredApprovals() > 0 || req.GetRequiredHumanApprovals() > 0) {
		approversGroup := strings.TrimSpace(req.GetApprovers())
		var allowedIDs map[string]bool
		if approversGroup != "" && s.Auth != nil {
			if resolved, err := s.resolveRuleOwnerSubjects(ctx, []string{approversGroup}, map[string]string{}, map[string][]string{}); err == nil {
				allowedIDs = resolved
			}
		}
		eval := s.evaluateSliceApprovals(ctx, authorIDs, approverIDs, approversGroup, allowedIDs)
		if req.GetRequiredApprovals() > 0 {
			label := "Approvals"
			detail := "Distinct non-author approvals required"
			if approversGroup != "" {
				label = fmt.Sprintf("Approvals from %s", approversGroup)
				detail = fmt.Sprintf("Distinct non-author approvals from %s", approversGroup)
			}
			requirements = append(requirements, &corev1.ApprovalRequirement{
				Kind:      "slice_approvals",
				Satisfied: eval.ControllerCount >= req.GetRequiredApprovals(),
				Required:  req.GetRequiredApprovals(),
				Current:   eval.ControllerCount,
				Label:     label,
				Detail:    detail,
			})
		}
		if req.GetRequiredHumanApprovals() > 0 {
			label := "Human approvals"
			detail := "Distinct human non-author approvals required"
			if approversGroup != "" {
				label = fmt.Sprintf("Human approvals from %s", approversGroup)
				detail = fmt.Sprintf("Distinct human non-author approvals from %s", approversGroup)
			}
			requirements = append(requirements, &corev1.ApprovalRequirement{
				Kind:      "slice_human_approvals",
				Satisfied: eval.HumanCount >= req.GetRequiredHumanApprovals(),
				Required:  req.GetRequiredHumanApprovals(),
				Current:   eval.HumanCount,
				Label:     label,
				Detail:    detail,
			})
		}
	}

	// 3 & 4. Tree-based ownership gates (.gitslice/** and path owners).
	if s.Repository != nil && s.Auth != nil {
		if headCommitID, err := s.submitHeadCommitID(ctx, cs); err == nil && strings.TrimSpace(headCommitID) != "" {
			if reqItem := s.evaluateGitsliceConfigRequirement(ctx, cs, patchset, headCommitID, approverIDs); reqItem != nil {
				requirements = append(requirements, reqItem)
			}
			ownerReqs := s.evaluatePathOwnerRequirements(ctx, patchset, headCommitID, authorIDs, approverIDs)
			requirements = append(requirements, ownerReqs...)
		}
	}

	allSatisfied := true
	for _, item := range requirements {
		if !item.GetSatisfied() {
			allSatisfied = false
			break
		}
	}
	cs.ApprovalStatus = &corev1.ApprovalStatus{
		Satisfied:    allSatisfied,
		Requirements: requirements,
	}
}

func approvalSubjectIDsForPatchset(cs *corev1.Changeset, patchsetID string) []string {
	var ids []string
	for _, app := range cs.GetApprovals() {
		if app == nil || app.GetPatchsetId() != patchsetID {
			continue
		}
		if sid := strings.TrimSpace(app.GetSubjectId()); sid != "" {
			ids = append(ids, sid)
		}
	}
	return uniqueNonEmptyStrings(ids...)
}

func (s *ChangesetService) evaluateSliceApprovals(
	ctx context.Context,
	authorIDs []string,
	approverIDs []string,
	approversGroup string,
	allowedIDs map[string]bool,
) storage.ControllerApprovalResult {
	var subjects map[string]storage.Subject
	if s.Auth != nil {
		allIDs := uniqueNonEmptyStrings(append(append([]string{}, authorIDs...), approverIDs...)...)
		if resolved, err := s.Auth.SubjectsByIDs(ctx, allIDs); err == nil {
			subjects = resolved
		}
	}
	resolve := func(ids []string) []storage.Subject {
		out := make([]storage.Subject, 0, len(ids))
		for _, id := range uniqueNonEmptyStrings(ids...) {
			if subj, ok := subjects[id]; ok {
				out = append(out, subj)
			} else {
				out = append(out, storage.Subject{ID: id, ControllerSubjectID: id})
			}
		}
		return out
	}
	return storage.EvaluateControllerApprovals(storage.ControllerApprovalInput{
		Authors:           resolve(authorIDs),
		Approvers:         resolve(approverIDs),
		AllowedSubjectIDs: allowedIDs,
		RestrictToAllowed: approversGroup != "",
	})
}

func (s *ChangesetService) evaluateGitsliceConfigRequirement(
	ctx context.Context,
	cs *corev1.Changeset,
	patchset *corev1.Patchset,
	headCommitID string,
	approverIDs []string,
) *corev1.ApprovalRequirement {
	touched, err := s.patchsetTouchedGitslicePathForSubmit(ctx, patchset, headCommitID)
	if err != nil || touched == "" {
		return nil
	}
	account := cs.GetAuthoringSlice().GetAccount()
	ownerSubjects, err := s.accountAdminSubjects(ctx, account)
	if err != nil {
		return nil
	}
	authors, err := s.gitsliceAuthorsForChangeset(ctx, cs, patchset, headCommitID)
	if err != nil {
		return nil
	}
	res := s.evaluateOwnerRule(ctx, authors, approverIDs, ownerRuleSpec{
		ownerSubjects: ownerSubjects,
		require:       1,
		selfBySubject: true,
	})
	if res.err != nil {
		return nil
	}
	return &corev1.ApprovalRequirement{
		Kind:      "gitslice_config",
		Satisfied: res.count >= 1,
		Required:  1,
		Current:   int32(res.count),
		Label:     "Config owner approval",
		Detail:    fmt.Sprintf("%s requires an owner or admin of %s", touched, account),
	}
}

func (s *ChangesetService) evaluatePathOwnerRequirements(
	ctx context.Context,
	patchset *corev1.Patchset,
	headCommitID string,
	authorIDs []string,
	approverIDs []string,
) []*corev1.ApprovalRequirement {
	if s.ObjectStore == nil {
		return nil
	}
	allPaths, err := collectPatchsetPaths(ctx, s.Repository, patchset.GetBaseCommitId(), headCommitID, patchset)
	if err != nil || len(allPaths) == 0 {
		return nil
	}
	rootTree, err := s.Repository.RootTreeForCommit(ctx, headCommitID)
	if err != nil {
		return nil
	}
	reader := newGitsliceTreeReader(s.Repository, s.ObjectStore, maxAutomationFileBytes, "", automations.ErrNotFound)
	resolver := owners.NewResolver(reader.ReadConfig)

	type ruleKey struct {
		configPath string
		ruleIndex  int
	}
	var order []ruleKey
	grouped := map[ruleKey]*matchedOwnerGroup{}
	for _, p := range allPaths {
		if touchesGitslice(p) {
			continue
		}
		m, ok, lookupErr := owners.LookupMatchWithResolver(ctx, resolver, rootTree, "", p)
		if lookupErr != nil || !ok || (m.Rule.Require <= 0 && m.Rule.RequireHuman <= 0) {
			continue
		}
		key := ruleKey{configPath: m.ConfigPath, ruleIndex: m.RuleIndex}
		g, exists := grouped[key]
		if !exists {
			g = &matchedOwnerGroup{match: m}
			grouped[key] = g
			order = append(order, key)
		}
		g.paths = append(g.paths, p)
	}
	if len(order) == 0 {
		return nil
	}

	resolvedUserCache := map[string]string{}
	resolvedGroupCache := map[string][]string{}
	out := make([]*corev1.ApprovalRequirement, 0, len(order))
	for _, key := range order {
		g := grouped[key]
		ownerSubjects, resolveErr := s.resolveRuleOwnerSubjects(ctx, g.match.Rule.Owners, resolvedUserCache, resolvedGroupCache)
		if resolveErr != nil {
			continue
		}
		res := s.evaluateOwnerRule(ctx, authorIDs, approverIDs, ownerRuleSpec{
			ownerSubjects: ownerSubjects,
			require:       g.match.Rule.Require,
			requireHuman:  g.match.Rule.RequireHuman,
			selfBySubject: false,
		})
		if res.err != nil {
			continue
		}
		if g.match.Rule.Require > 0 {
			out = append(out, &corev1.ApprovalRequirement{
				Kind:      "path_owners",
				Satisfied: res.count >= g.match.Rule.Require,
				Required:  int32(g.match.Rule.Require),
				Current:   int32(res.count),
				Label:     fmt.Sprintf("Path owners (%s)", strings.Join(g.match.Rule.Owners, ", ")),
				Detail:    summarizeMatchedPaths(g.paths),
			})
		}
		if g.match.Rule.RequireHuman > 0 {
			out = append(out, &corev1.ApprovalRequirement{
				Kind:      "path_owners_human",
				Satisfied: res.humanCount >= g.match.Rule.RequireHuman,
				Required:  int32(g.match.Rule.RequireHuman),
				Current:   int32(res.humanCount),
				Label:     fmt.Sprintf("Human path owners (%s)", strings.Join(g.match.Rule.Owners, ", ")),
				Detail:    summarizeMatchedPaths(g.paths),
			})
		}
	}
	return out
}

func summarizeMatchedPaths(paths []string) string {
	switch len(paths) {
	case 0:
		return "owned paths"
	case 1:
		return paths[0]
	case 2:
		return paths[0] + ", " + paths[1]
	default:
		return fmt.Sprintf("%s (+%d more)", paths[0], len(paths)-1)
	}
}

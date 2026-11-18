package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// submitExtras checks what submit needs beyond the slice's own rules, and
// returns the check statuses to count besides the stored results
// (design/34_triggers.md & design/36_multi_agent_collaboration.md):
//
//   - A change under a .gitslice folder decides what code runs with which
//     secrets (checks.yaml, triggers.yaml, owners.yaml and trigger scripts), so
//     it needs an owner or admin of the account: as the author, or as an
//     approver (controller-deduped, claimed agents only).
//   - Any path covered by .gitslice/owners.yaml on the target ref's landed tree
//     needs its required owner approvals.
//   - A required check that checks.yaml skips, or that only a trigger
//     reports and no trigger would report for this patchset, counts as
//     passed.
func (s *ChangesetService) submitExtras(ctx context.Context, cs *corev1.Changeset) (map[string]string, error) {
	if cs != nil && (cs.GetStatus() == "pending_publish" || cs.GetStatus() == "submitted") {
		return nil, nil
	}
	headCommitID, err := s.submitHeadCommitID(ctx, cs)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.requireGitsliceApproval(ctx, cs, headCommitID); err != nil {
		return nil, err
	}
	if err := s.requirePathOwnersApproval(ctx, cs, headCommitID); err != nil {
		return nil, err
	}
	extra, err := s.skippedRequiredCheckStatuses(ctx, cs)
	if err != nil {
		return nil, err
	}
	skipped, err := s.skippedTriggerChecks(ctx, cs)
	if err != nil {
		return nil, err
	}
	for name := range skipped {
		if extra == nil {
			extra = map[string]string{}
		}
		if _, set := extra[name]; !set {
			extra[name] = storage.CheckStatusPass
		}
	}
	return extra, nil
}

// touchesGitslice reports whether a path is inside a .gitslice folder.
func touchesGitslice(p string) bool {
	return gitslicecfg.TouchesGitslice(p)
}

// skippedTriggerChecks returns the slice's required checks that only a
// trigger reports, and that no trigger would report for the current
// patchset.
func (s *ChangesetService) skippedTriggerChecks(ctx context.Context, cs *corev1.Changeset) (map[string]bool, error) {
	patchset := currentPatchset(cs)
	if patchset == nil || s.Repository == nil || s.ObjectStore == nil {
		return nil, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.GetAuthoringSlice())
	if err != nil {
		return nil, err
	}
	required := slice.GetDefinition().GetRequiredChecks()
	if len(required) == 0 {
		return nil, nil
	}
	ref := cs.GetTargetRef()
	if ref == "" {
		ref = triggers.DefaultRef
	}
	head, err := s.Repository.GetRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId())
	if err != nil {
		return nil, err
	}
	files := TreeFiles{Repository: s.Repository, Objects: s.ObjectStore}
	changes := triggers.ChangesFromPatchset(patchset)
	// A check that has runs on this patchset has a real result, which a
	// skip must never replace.
	ran := map[string]bool{}
	if s.Checks != nil {
		runs, err := s.Checks.ListCheckRuns(ctx, cs.GetId(), patchset.GetId())
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			ran[run.GetCheckName()] = true
		}
	}
	out := map[string]bool{}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" || ran[name] {
			continue
		}
		skipped, _, err := triggers.CheckSkipped(ctx, files, tree, cs.GetAuthoringSlice().GetAccount(), ref, name, changes)
		if err != nil {
			return nil, err
		}
		if skipped {
			out[name] = true
		}
	}
	return out, nil
}

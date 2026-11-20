package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// submitExtraResult holds folder-level and trigger-level check gates computed
// outside the slice's stored SubmitRequirements.
type submitExtraResult struct {
	CheckStatuses  map[string]string
	RequiredChecks []string
}

// submitExtras checks what submit needs beyond the slice's own rules, and
// returns folder-level required checks and extra check statuses to count
// besides the stored results (design/34_triggers.md &
// design/36_multi_agent_collaboration.md):
//
//   - A change under a .gitslice folder decides what code runs with which
//     secrets (checks.yaml, automations.yaml, owners.yaml and trigger scripts), so
//     it needs an owner or admin of the account: as the author, or as an
//     approver (controller-deduped, claimed agents only).
//   - Any path covered by .gitslice/owners.yaml on the target ref's landed tree
//     needs its required owner (and human owner) approvals.
//   - Any check marked `required: true` in .gitslice/checks.yaml on the target
//     ref's landed tree whose paths match the patchset is required to pass.
//   - A required check that checks.yaml skips, or that only a trigger
//     reports and no trigger would report for this patchset, counts as
//     passed.
func (s *ChangesetService) submitExtras(ctx context.Context, cs *corev1.Changeset) (submitExtraResult, error) {
	if cs != nil && (cs.GetStatus() == "pending_publish" || cs.GetStatus() == "submitted") {
		return submitExtraResult{}, nil
	}
	headCommitID, err := s.submitHeadCommitID(ctx, cs)
	if err != nil {
		return submitExtraResult{}, grpcError(err)
	}
	if err := s.requireGitsliceApproval(ctx, cs, headCommitID); err != nil {
		return submitExtraResult{}, err
	}
	if err := s.requirePathOwnersApproval(ctx, cs, headCommitID); err != nil {
		return submitExtraResult{}, err
	}
	folderRequired, err := s.folderRequiredChecksAtCommit(ctx, cs, currentPatchset(cs), headCommitID)
	if err != nil {
		return submitExtraResult{}, grpcError(err)
	}
	extra, err := s.skippedRequiredCheckStatuses(ctx, cs)
	if err != nil {
		return submitExtraResult{}, err
	}
	skipped, err := s.skippedAutomationChecks(ctx, cs, folderRequired)
	if err != nil {
		return submitExtraResult{}, err
	}
	for name := range skipped {
		if extra == nil {
			extra = map[string]string{}
		}
		if _, set := extra[name]; !set {
			extra[name] = storage.CheckStatusPass
		}
	}
	return submitExtraResult{
		CheckStatuses:  extra,
		RequiredChecks: folderRequired,
	}, nil
}

// folderRequiredChecksAtCommit evaluates `.gitslice/checks.yaml` files on the
// target ref's landed tree (headCommitID) so unapproved draft edits cannot
// disable folder-level `required: true` checks.
func (s *ChangesetService) folderRequiredChecksAtCommit(ctx context.Context, cs *corev1.Changeset, patchset *corev1.Patchset, headCommitID string) ([]string, error) {
	if cs == nil || patchset == nil || strings.TrimSpace(headCommitID) == "" || s.Repository == nil || s.ObjectStore == nil {
		return nil, nil
	}
	account := strings.TrimSpace(cs.GetAuthoringSlice().GetAccount())
	if account == "" {
		return nil, nil
	}
	allPaths, err := collectPatchsetPaths(ctx, s.Repository, patchset.GetBaseCommitId(), headCommitID, patchset)
	if err != nil {
		return nil, err
	}
	if len(allPaths) == 0 {
		return nil, nil
	}
	return s.folderRequiredChecksForPaths(ctx, account, headCommitID, allPaths)
}

func (s *ChangesetService) folderRequiredChecksForPaths(ctx context.Context, account, commitID string, allPaths []string) ([]string, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(commitID) == "" || len(allPaths) == 0 || s.Repository == nil || s.ObjectStore == nil {
		return nil, nil
	}
	rootTree, err := s.Repository.RootTreeForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	var resolver *gitslicecfg.Resolver[*checks.File]
	if s.dispatcher != nil {
		resolver = s.dispatcher.checkResolver(account)
	} else {
		resolver = checks.NewResolver(checkTreeReader{
			repository: s.Repository,
			objects:    s.ObjectStore,
			account:    account,
		})
	}
	return checks.RequiredForPaths(ctx, resolver, rootTree, logicalPathsForAccount(account, allPaths))
}

// touchesGitslice reports whether a path is inside a .gitslice folder.
func touchesGitslice(p string) bool {
	return gitslicecfg.TouchesGitslice(p)
}

// skippedAutomationChecks returns the slice's or folder's required checks that
// only a trigger reports, and that no trigger would report for the current
// patchset.
func (s *ChangesetService) skippedAutomationChecks(ctx context.Context, cs *corev1.Changeset, folderRequired []string) (map[string]bool, error) {
	patchset := currentPatchset(cs)
	if patchset == nil || s.Repository == nil || s.ObjectStore == nil {
		return nil, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.GetAuthoringSlice())
	if err != nil {
		return nil, err
	}
	seenReq := map[string]bool{}
	var required []string
	for _, list := range [][]string{slice.GetDefinition().GetRequiredChecks(), folderRequired} {
		for _, name := range list {
			name = strings.TrimSpace(name)
			if name == "" || seenReq[name] {
				continue
			}
			seenReq[name] = true
			required = append(required, name)
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	ref := cs.GetTargetRef()
	if ref == "" {
		ref = automations.DefaultRef
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
	changes := automations.ChangesFromPatchset(patchset)
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
		if ran[name] {
			continue
		}
		skipped, _, err := automations.CheckSkipped(ctx, files, tree, cs.GetAuthoringSlice().GetAccount(), ref, name, changes)
		if err != nil {
			return nil, err
		}
		if skipped {
			out[name] = true
		}
	}
	return out, nil
}

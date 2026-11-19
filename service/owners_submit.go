package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/owners"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ChangesetService) recordRequirementBlock(ctx context.Context, cs *corev1.Changeset, reason string) {
	if cs == nil || reason == "" || s.Changesets == nil {
		return
	}
	patchsetID := cs.GetCurrentPatchsetId()
	if patchsetID == "" {
		if ps := currentPatchset(cs); ps != nil {
			patchsetID = ps.GetId()
		}
	}
	_ = s.Changesets.RecordSubmitBlockedReason(ctx, cs.GetId(), patchsetID, reason)
}

func (s *ChangesetService) submitHeadCommitID(ctx context.Context, cs *corev1.Changeset) (string, error) {
	return targetRefHeadCommitID(ctx, s.Repository, cs)
}

type ownerRuleSpec struct {
	ownerSubjects map[string]bool
	require       int
	requireHuman  int
	selfBySubject bool
}

type ownerEvalResult struct {
	count      int
	humanCount int
	err        error
}

func (s *ChangesetService) evaluateOwnerRule(ctx context.Context, authorIDs, approverIDs []string, spec ownerRuleSpec) ownerEvalResult {
	if spec.require <= 0 && spec.requireHuman <= 0 {
		return ownerEvalResult{}
	}
	needIDs := make([]string, 0, len(spec.ownerSubjects)+len(authorIDs)+len(approverIDs))
	ownerIDs := make([]string, 0, len(spec.ownerSubjects))
	for id := range spec.ownerSubjects {
		ownerIDs = append(ownerIDs, id)
		needIDs = append(needIDs, id)
	}
	needIDs = append(needIDs, authorIDs...)
	needIDs = append(needIDs, approverIDs...)

	subjects, err := s.Auth.SubjectsByIDs(ctx, uniqueNonEmptyStrings(needIDs...))
	if err != nil {
		return ownerEvalResult{err: grpcError(err)}
	}

	resolveSubjects := func(ids []string) []storage.Subject {
		out := make([]storage.Subject, 0, len(ids))
		for _, id := range uniqueNonEmptyStrings(ids...) {
			if subj, ok := subjects[id]; ok {
				out = append(out, subj)
			} else {
				out = append(out, storage.Subject{ID: id})
			}
		}
		return out
	}

	eval := storage.EvaluateControllerApprovals(storage.ControllerApprovalInput{
		Authors:                 resolveSubjects(authorIDs),
		Approvers:               resolveSubjects(approverIDs),
		AllowedSubjects:         resolveSubjects(ownerIDs),
		AllowedSubjectIDs:       spec.ownerSubjects,
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        spec.selfBySubject,
	})
	return ownerEvalResult{
		count:      int(eval.ControllerCount),
		humanCount: int(eval.HumanCount),
	}
}

// requireGitsliceApproval enforces the implicit .gitslice/** ownership rule:
// changes touching any .gitslice folder require an account owner or admin
// (claimed/human, controller-deduped) as author or approver.
func (s *ChangesetService) requireGitsliceApproval(ctx context.Context, cs *corev1.Changeset, headCommitID string) error {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil
	}
	touched, err := s.patchsetTouchedGitslicePathForSubmit(ctx, patchset, headCommitID)
	if err != nil {
		return grpcError(err)
	}
	if touched == "" {
		return nil
	}
	account := cs.GetAuthoringSlice().GetAccount()
	ownerSubjects, err := s.accountAdminSubjects(ctx, account)
	if err != nil {
		return grpcError(err)
	}
	authors, err := s.gitsliceAuthorsForChangeset(ctx, cs, patchset, headCommitID)
	if err != nil {
		return grpcError(err)
	}
	approvers, err := s.Changesets.ApprovalSubjects(ctx, cs.GetId(), patchset.GetId())
	if err != nil {
		return grpcError(err)
	}
	res := s.evaluateOwnerRule(ctx, authors, approvers, ownerRuleSpec{
		ownerSubjects: ownerSubjects,
		require:       1,
		selfBySubject: true,
	})
	if res.err != nil {
		return res.err
	}
	if res.count >= 1 {
		return nil
	}
	reason := fmt.Sprintf(".gitslice configuration path %s requires approval: an owner or admin of %s must approve this patchset", touched, account)
	s.recordRequirementBlock(ctx, cs, reason)
	return storage.NewSubmitBlockedError(storage.SubmitBlockCodeApprovalsMissing, reason)
}

func (s *ChangesetService) patchsetTouchedGitslicePathForSubmit(ctx context.Context, patchset *corev1.Patchset, headCommitID string) (string, error) {
	return patchsetTouchedGitslicePathAt(ctx, s.Repository, headCommitID, patchset)
}

func (s *ChangesetService) accountAdminSubjects(ctx context.Context, account string) (map[string]bool, error) {
	members, err := s.Auth.ListAccountMembers(ctx, account)
	if errors.Is(err, storage.ErrNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(members))
	for _, m := range members {
		if m.Role == "owner" || m.Role == "admin" {
			out[strings.TrimSpace(m.SubjectID)] = true
		}
	}
	return out, nil
}

func (s *ChangesetService) gitsliceAuthorsForChangeset(ctx context.Context, cs *corev1.Changeset, current *corev1.Patchset, headCommitID string) ([]string, error) {
	if current.GetKind() != storage.SubmitMergeKind {
		if author := strings.TrimSpace(current.GetAuthor()); author != "" {
			return []string{author}, nil
		}
		if csAuthor := strings.TrimSpace(cs.GetAuthor()); csAuthor != "" {
			return []string{csAuthor}, nil
		}
		return nil, nil
	}
	var authors []string
	for _, ps := range cs.GetPatchsets() {
		if ps == nil || ps.GetKind() == storage.SubmitMergeKind {
			continue
		}
		touched, err := s.patchsetTouchedGitslicePathForSubmit(ctx, ps, headCommitID)
		if err != nil {
			return nil, err
		}
		if touched != "" {
			authors = append(authors, ps.GetAuthor())
		}
	}
	authors = uniqueNonEmptyStrings(authors...)
	if len(authors) > 0 {
		return authors, nil
	}
	if csAuthor := strings.TrimSpace(cs.GetAuthor()); csAuthor != "" {
		return []string{csAuthor}, nil
	}
	return nil, nil
}

func changesetNonMechanicalAuthors(cs *corev1.Changeset) []string {
	if cs == nil {
		return nil
	}
	var raw []string
	if cs.GetAuthor() != "" {
		raw = append(raw, cs.GetAuthor())
	}
	for _, ps := range cs.GetPatchsets() {
		if ps != nil && ps.GetKind() != storage.SubmitMergeKind && ps.GetAuthor() != "" {
			raw = append(raw, ps.GetAuthor())
		}
	}
	return uniqueNonEmptyStrings(raw...)
}

type matchedOwnerGroup struct {
	match owners.MatchResult
	paths []string
}

func (s *ChangesetService) requirePathOwnersApproval(ctx context.Context, cs *corev1.Changeset, headCommitID string) error {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil
	}
	allPaths, err := collectPatchsetPaths(ctx, s.Repository, patchset.GetBaseCommitId(), headCommitID, patchset)
	if err != nil {
		return grpcError(err)
	}
	if len(allPaths) == 0 {
		return nil
	}
	approvers, err := s.Changesets.ApprovalSubjects(ctx, cs.GetId(), patchset.GetId())
	if err != nil {
		return grpcError(err)
	}
	reason, err := s.checkPathOwnersAtCommit(ctx, headCommitID, allPaths, changesetNonMechanicalAuthors(cs), approvers)
	if err != nil {
		return err
	}
	if reason == "" {
		return nil
	}
	s.recordRequirementBlock(ctx, cs, reason)
	return storage.NewSubmitBlockedError(storage.SubmitBlockCodeApprovalsMissing, reason)
}

func (s *ChangesetService) requirePathOwnersForDirectOperation(ctx context.Context, baseCommitID, subjectID string, allPaths []string) error {
	if len(allPaths) == 0 {
		return nil
	}
	reason, err := s.checkPathOwnersAtCommit(ctx, baseCommitID, allPaths, []string{subjectID}, nil)
	if err != nil {
		return grpcError(err)
	}
	if reason != "" {
		return status.Error(codes.FailedPrecondition, reason+"; use a changeset")
	}
	return nil
}

func (s *ChangesetService) checkPathOwnersAtCommit(ctx context.Context, headCommitID string, allPaths, authors, approvers []string) (string, error) {
	if strings.TrimSpace(headCommitID) == "" || s.Repository == nil || s.ObjectStore == nil {
		return "", nil
	}
	rootTree, err := s.Repository.RootTreeForCommit(ctx, headCommitID)
	if err != nil {
		return "", grpcError(err)
	}
	reader := newGitsliceTreeReader(s.Repository, s.ObjectStore, maxTriggerFileBytes, "", triggers.ErrNotFound)
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
		m, ok, err := owners.LookupMatchWithResolver(ctx, resolver, rootTree, "", p)
		if err != nil {
			return "", fmt.Errorf("%w: %v", storage.ErrInvalid, err)
		}
		if !ok || (m.Rule.Require <= 0 && m.Rule.RequireHuman <= 0) {
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
		return "", nil
	}

	resolvedUserCache := map[string]string{}
	resolvedGroupCache := map[string][]string{}
	for _, key := range order {
		g := grouped[key]
		ownerSubjects, err := s.resolveRuleOwnerSubjects(ctx, g.match.Rule.Owners, resolvedUserCache, resolvedGroupCache)
		if err != nil {
			return "", err
		}
		res := s.evaluateOwnerRule(ctx, authors, approvers, ownerRuleSpec{
			ownerSubjects: ownerSubjects,
			require:       g.match.Rule.Require,
			requireHuman:  g.match.Rule.RequireHuman,
			selfBySubject: false,
		})
		if res.err != nil {
			return "", res.err
		}
		if g.match.Rule.Require > 0 && res.count < g.match.Rule.Require {
			return formatPathOwnerBlockReason(g.paths, g.match, res.count), nil
		}
		if g.match.Rule.RequireHuman > 0 && res.humanCount < g.match.Rule.RequireHuman {
			return formatPathOwnerHumanBlockReason(g.paths, g.match, res.humanCount), nil
		}
	}
	return "", nil
}

func (s *ChangesetService) resolveRuleOwnerSubjects(ctx context.Context, ownerRefs []string, userCache map[string]string, groupCache map[string][]string) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, raw := range ownerRefs {
		ref := strings.ToLower(strings.TrimSpace(raw))
		if ref == "" {
			continue
		}
		if account, name, ok := owners.ParseGroupRef(ref); ok {
			handle := storage.FormatGroupHandle(account, name)
			memberIDs, cached := groupCache[handle]
			if !cached {
				grp, err := s.Auth.GetGroup(ctx, account, name)
				if errors.Is(err, storage.ErrNotFound) {
					groupCache[handle] = nil
					continue
				}
				if err != nil {
					return nil, grpcError(err)
				}
				memberIDs = make([]string, 0, len(grp.Members))
				for _, m := range grp.Members {
					if m.SubjectID != "" {
						memberIDs = append(memberIDs, m.SubjectID)
					}
				}
				groupCache[handle] = memberIDs
			}
			for _, id := range memberIDs {
				out[id] = true
			}
			continue
		}
		if strings.HasPrefix(ref, "@") {
			continue // malformed group ref fails closed
		}
		sid, cached := userCache[ref]
		if !cached {
			resolved, err := s.Auth.SubjectIDForUsername(ctx, ref)
			if errors.Is(err, storage.ErrNotFound) {
				userCache[ref] = ""
				continue
			}
			if err != nil {
				return nil, grpcError(err)
			}
			sid = resolved
			userCache[ref] = sid
		}
		if sid != "" {
			out[sid] = true
		}
	}
	return out, nil
}

func formatPathOwnerBlockReason(matchedPaths []string, m owners.MatchResult, count int) string {
	pathDesc := formatMatchedPathList(matchedPaths)
	return fmt.Sprintf("path owner approvals not satisfied for %s (%s): requires %d approval(s) from %s, has %d",
		pathDesc, m.ConfigPath, m.Rule.Require, strings.Join(m.Rule.Owners, ", "), count)
}

func formatPathOwnerHumanBlockReason(matchedPaths []string, m owners.MatchResult, humanCount int) string {
	pathDesc := formatMatchedPathList(matchedPaths)
	return fmt.Sprintf("path owner human approvals not satisfied for %s (%s): requires %d human approval(s) from %s, has %d",
		pathDesc, m.ConfigPath, m.Rule.RequireHuman, strings.Join(m.Rule.Owners, ", "), humanCount)
}

func formatMatchedPathList(matchedPaths []string) string {
	switch len(matchedPaths) {
	case 0:
		return "owned paths"
	case 1:
		return matchedPaths[0]
	case 2:
		return matchedPaths[0] + ", " + matchedPaths[1]
	default:
		return fmt.Sprintf("%s (+%d more)", matchedPaths[0], len(matchedPaths)-1)
	}
}

// validateOwnersConfigEdits validates any uploaded or copied .gitslice/owners.yaml
// file edit on patchset creation so invalid owner configs cannot be staged.
func (s *ChangesetService) validateOwnersConfigEdits(ctx context.Context, baseTreeID string, edits []*corev1.FileEdit) error {
	if len(edits) == 0 {
		return nil
	}
	var blobIDs []string
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		p := strings.TrimSpace(edit.GetPath())
		if !strings.HasSuffix(p, "/"+owners.ConfigName) {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(edit.GetOp()))
		if op != "delete" && op != "mkdir" && op != "copy" && op != "rename" && op != "move" && edit.GetBlobId() != "" {
			blobIDs = append(blobIDs, edit.GetBlobId())
		}
	}
	var blobs map[string]*corev1.BlobRecord
	if len(blobIDs) > 0 && s.Blobs != nil {
		var err error
		blobs, err = s.Blobs.GetByIDs(ctx, blobIDs)
		if err != nil {
			return grpcError(err)
		}
	}
	files := TreeFiles{Repository: s.Repository, Objects: s.ObjectStore}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		p := strings.TrimSpace(edit.GetPath())
		if !strings.HasSuffix(p, "/"+owners.ConfigName) {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(edit.GetOp()))
		if op == "delete" || op == "mkdir" {
			continue
		}
		var data []byte
		if op == "copy" || op == "rename" || op == "move" {
			if baseTreeID == "" || s.Repository == nil || s.ObjectStore == nil {
				continue
			}
			var err error
			data, err = files.ReadFile(ctx, baseTreeID, edit.GetOldPath())
			if errors.Is(err, triggers.ErrNotFound) {
				continue
			}
			if err != nil {
				return grpcError(err)
			}
		} else {
			blob := blobs[edit.GetBlobId()]
			if blob == nil || s.ObjectStore == nil {
				continue
			}
			rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(blob.ContentHash), 0, 0)
			if err != nil {
				return grpcError(err)
			}
			data, err = io.ReadAll(io.LimitReader(rc, maxTriggerFileBytes))
			_ = rc.Close()
			if err != nil {
				return grpcError(err)
			}
		}
		dir := strings.TrimSuffix(p, "/"+owners.ConfigName)
		if _, err := owners.Parse(dir, data); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	return nil
}

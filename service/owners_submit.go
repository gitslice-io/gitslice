package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/owners"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type changesetBlockRecorder interface {
	RecordSubmitBlockedReason(ctx context.Context, changesetID, expectedCurrentPatchsetID, reason string) error
}

func (s *ChangesetService) recordRequirementBlock(ctx context.Context, cs *corev1.Changeset, reason string) {
	if cs == nil || reason == "" {
		return
	}
	recorder, ok := s.Changesets.(changesetBlockRecorder)
	if !ok {
		return
	}
	patchsetID := cs.GetCurrentPatchsetId()
	if patchsetID == "" {
		if ps := currentPatchset(cs); ps != nil {
			patchsetID = ps.GetId()
		}
	}
	_ = recorder.RecordSubmitBlockedReason(ctx, cs.GetId(), patchsetID, reason)
}

func (s *ChangesetService) submitHeadCommitID(ctx context.Context, cs *corev1.Changeset) (string, error) {
	if s.Repository == nil {
		return "", nil
	}
	refName := strings.TrimSpace(cs.GetTargetRef())
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	head, err := s.Repository.GetRef(ctx, refName)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head.GetCommitId()), nil
}

// collectPatchsetPaths returns every path touched by patchset, including
// edit.Path, non-copy edit.OldPath, and directory children under moved or
// deleted folders at baseCommitID / headCommitID plus mapped move/copy targets.
func collectPatchsetPaths(ctx context.Context, repo storage.RepositoryStore, baseCommitID, headCommitID string, patchset *corev1.Patchset) ([]string, error) {
	if patchset == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if canonical, err := paths.Canonical(p); err == nil {
			p = canonical
		}
		seen[p] = struct{}{}
	}
	for _, p := range patchset.GetChangedPaths() {
		add(p)
	}
	commits := uniqueNonEmptyStrings(baseCommitID, headCommitID)
	for _, edit := range patchset.GetFileEdits() {
		if edit == nil {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(edit.GetOp()))
		dst := strings.TrimSpace(edit.GetPath())
		src := strings.TrimSpace(edit.GetOldPath())
		add(dst)
		if op != "copy" {
			add(src)
		}
		if repo == nil {
			continue
		}
		switch op {
		case "delete":
			if dst != "" {
				for _, cid := range commits {
					children, err := directoryChildrenAtCommit(ctx, repo, cid, dst)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(child)
					}
				}
			}
		case "rename", "move":
			if src != "" {
				for _, cid := range commits {
					children, err := directoryChildrenAtCommit(ctx, repo, cid, src)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(child)
						if dst != "" {
							add(remapPrefix(src, dst, child))
						}
					}
				}
			}
		case "copy":
			if src != "" && dst != "" {
				for _, cid := range commits {
					children, err := directoryChildrenAtCommit(ctx, repo, cid, src)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(remapPrefix(src, dst, child))
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func directoryChildrenAtCommit(ctx context.Context, repo storage.RepositoryStore, commitID, dirPath string) ([]string, error) {
	if repo == nil || strings.TrimSpace(commitID) == "" || strings.TrimSpace(dirPath) == "" {
		return nil, nil
	}
	if canonical, err := paths.Canonical(dirPath); err == nil {
		dirPath = canonical
	}
	entry, err := repo.GetEntry(ctx, commitID, dirPath)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && entry.Kind == "file") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	children, err := repo.ListFiles(ctx, commitID, dirPath)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(children))
	for _, child := range children {
		if child.Path != "" {
			out = append(out, child.Path)
		}
	}
	return out, nil
}

func remapPrefix(oldPrefix, newPrefix, p string) string {
	if p == oldPrefix {
		return newPrefix
	}
	rel := strings.TrimPrefix(p, oldPrefix)
	if !strings.HasPrefix(rel, "/") {
		return newPrefix
	}
	return strings.TrimSuffix(newPrefix, "/") + rel
}

func uniqueNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

type ownerRuleSpec struct {
	ownerSubjects map[string]bool
	require       int
	selfBySubject bool
}

type ownerEvalResult struct {
	count int
	err   error
}

func (s *ChangesetService) evaluateOwnerRule(ctx context.Context, authorIDs, approverIDs []string, spec ownerRuleSpec) ownerEvalResult {
	if spec.require <= 0 {
		return ownerEvalResult{count: 0}
	}
	needIDs := make([]string, 0, len(spec.ownerSubjects)+len(authorIDs)+len(approverIDs))
	for id := range spec.ownerSubjects {
		needIDs = append(needIDs, id)
	}
	needIDs = append(needIDs, authorIDs...)
	needIDs = append(needIDs, approverIDs...)

	subjects, err := s.Auth.SubjectsByIDs(ctx, uniqueNonEmptyStrings(needIDs...))
	if err != nil {
		return ownerEvalResult{err: grpcError(err)}
	}

	ownerControllers := make(map[string]bool, len(spec.ownerSubjects))
	for ownerID := range spec.ownerSubjects {
		subj, ok := subjects[ownerID]
		if !ok || strings.TrimSpace(subj.ControllerSubjectID) == "" {
			continue // unclaimed agents never count as owners
		}
		ownerControllers[subj.ControllerSubjectID] = true
	}

	authorControllers := make(map[string]bool, len(authorIDs))
	allNonMechanicalAuthorsClaimed := len(authorIDs) > 0
	allAuthorsOwn := len(authorIDs) > 0
	for _, authorID := range uniqueNonEmptyStrings(authorIDs...) {
		subj, ok := subjects[authorID]
		if !ok || strings.TrimSpace(subj.ControllerSubjectID) == "" {
			allNonMechanicalAuthorsClaimed = false
			allAuthorsOwn = false
			continue
		}
		authorControllers[subj.ControllerSubjectID] = true
		if spec.selfBySubject {
			if !spec.ownerSubjects[authorID] {
				allAuthorsOwn = false
			}
		} else if !ownerControllers[subj.ControllerSubjectID] {
			allAuthorsOwn = false
		}
	}

	approvedOwnerControllers := make(map[string]bool, len(approverIDs))
	for _, approverID := range uniqueNonEmptyStrings(approverIDs...) {
		if !spec.ownerSubjects[approverID] {
			continue
		}
		subj, ok := subjects[approverID]
		if !ok || strings.TrimSpace(subj.ControllerSubjectID) == "" {
			continue // unclaimed agents never count as approvers
		}
		if authorControllers[subj.ControllerSubjectID] {
			continue // controller deduplication with authors
		}
		approvedOwnerControllers[subj.ControllerSubjectID] = true
	}

	count := len(approvedOwnerControllers)
	if allNonMechanicalAuthorsClaimed && allAuthorsOwn {
		count += len(authorControllers)
	}
	return ownerEvalResult{count: count}
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
	return fmt.Errorf("%w: %s", storage.ErrConflict, reason)
}

func (s *ChangesetService) patchsetTouchedGitslicePathForSubmit(ctx context.Context, patchset *corev1.Patchset, headCommitID string) (string, error) {
	if touched := patchsetTouchedGitslicePath(ctx, s.Repository, patchset); touched != "" {
		return touched, nil
	}
	allPaths, err := collectPatchsetPaths(ctx, s.Repository, patchset.GetBaseCommitId(), headCommitID, patchset)
	if err != nil {
		return "", err
	}
	for _, p := range allPaths {
		if touchesGitslice(p) {
			return p, nil
		}
	}
	return "", nil
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
	return fmt.Errorf("%w: %s", storage.ErrConflict, reason)
}

func (s *ChangesetService) requirePathOwnersForDirectOperation(ctx context.Context, baseCommitID, subjectID string, edit *corev1.FileEdit, childPaths []string) error {
	if edit == nil {
		return nil
	}
	op := strings.ToLower(strings.TrimSpace(edit.GetOp()))
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" {
			seen[p] = struct{}{}
		}
	}
	add(edit.GetPath())
	if op != "copy" {
		add(edit.GetOldPath())
	}
	for _, child := range childPaths {
		if op != "copy" {
			add(child)
		}
		if edit.GetOldPath() != "" && edit.GetPath() != "" {
			add(remapPrefix(edit.GetOldPath(), edit.GetPath(), child))
		}
	}
	allPaths := make([]string, 0, len(seen))
	for p := range seen {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

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
	files := TreeFiles{Repository: s.Repository, Objects: s.ObjectStore}
	type cachedConfig struct {
		data []byte
		ok   bool
		err  error
	}
	cfgCache := map[string]cachedConfig{}
	readCfg := func(cfgPath string) ([]byte, bool, error) {
		if cached, found := cfgCache[cfgPath]; found {
			return cached.data, cached.ok, cached.err
		}
		data, err := files.ReadFile(ctx, rootTree, cfgPath)
		if errors.Is(err, triggers.ErrNotFound) {
			cfgCache[cfgPath] = cachedConfig{ok: false}
			return nil, false, nil
		}
		if err != nil {
			cfgCache[cfgPath] = cachedConfig{err: err}
			return nil, false, err
		}
		cfgCache[cfgPath] = cachedConfig{data: data, ok: true}
		return data, true, nil
	}

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
		m, ok, err := owners.LookupMatch(p, readCfg)
		if err != nil {
			return "", fmt.Errorf("%w: %v", storage.ErrInvalid, err)
		}
		if !ok || m.Rule.Require <= 0 {
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
			selfBySubject: false,
		})
		if res.err != nil {
			return "", res.err
		}
		if res.count < g.match.Rule.Require {
			return formatPathOwnerBlockReason(g.paths, g.match, res.count), nil
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
	pathDesc := ""
	switch len(matchedPaths) {
	case 0:
		pathDesc = "owned paths"
	case 1:
		pathDesc = matchedPaths[0]
	case 2:
		pathDesc = matchedPaths[0] + ", " + matchedPaths[1]
	default:
		pathDesc = fmt.Sprintf("%s (+%d more)", matchedPaths[0], len(matchedPaths)-1)
	}
	return fmt.Sprintf("path owner approvals not satisfied for %s (%s): requires %d approval(s) from %s, has %d",
		pathDesc, m.ConfigPath, m.Rule.Require, strings.Join(m.Rule.Owners, ", "), count)
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

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/diffutil"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	Agents      storage.AgentStore
	Checks      storage.CheckStore
	ObjectStore ObjectStore
	validator   diffValidator
	dispatcher  *checkDispatcher
}

type diffValidator struct {
	Blobs      storage.BlobStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
}

const diffFileConcurrency = 16

func (s *ChangesetService) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionWrite); err != nil {
		return nil, err
	}
	createReq := &corev1.CreateChangesetRequest{
		AuthoringSlice:    req.AuthoringSlice,
		TargetRef:         targetRef,
		BaseCommitId:      req.BaseCommitId,
		Title:             req.Title,
		Description:       req.Description,
		StackId:           req.StackId,
		ParentChangesetId: req.ParentChangesetId,
		ParentPatchsetId:  req.ParentPatchsetId,
	}
	cs, err := s.Changesets.Create(ctx, subjectID, createReq)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveAuthors(ctx, cs); err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	subjectID := optionalSubject(ctx)
	return s.getAuthorizedChangeset(ctx, subjectID, req.ChangesetId)
}

func (s *ChangesetService) ListChangesets(ctx context.Context, req *corev1.ListChangesetsRequest) (*corev1.ListChangesetsResponse, error) {
	subjectID := optionalSubject(ctx)
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionRead); err != nil {
		return nil, err
	}
	changesets, err := s.Changesets.List(ctx, req)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, cs := range changesets {
		storage.PopulateChangesetHandles(cs)
	}
	if err := s.resolveAuthors(ctx, changesets...); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListChangesetsResponse{Changesets: changesets}, nil
}

func (s *ChangesetService) DiffChangeset(ctx context.Context, req *corev1.DiffChangesetRequest) (*corev1.DiffChangesetResponse, error) {
	subjectID := optionalSubject(ctx)
	cs, err := s.getAuthorizedChangeset(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	toSelector := req.Patchset
	if toSelector == "" {
		toSelector = req.ToPatchset
	}
	toPatchset, err := selectChangesetPatchset(cs, toSelector)
	if err != nil {
		return nil, err
	}
	var fromPatchset *corev1.Patchset
	if req.FromPatchset != "" {
		fromPatchset, err = selectChangesetPatchset(cs, req.FromPatchset)
		if err != nil {
			return nil, err
		}
	}
	paths := changedPathsForDiff(fromPatchset, toPatchset)
	chunks := make([]string, len(paths))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(diffFileConcurrency)
	for i, p := range paths {
		i, p := i, p
		group.Go(func() error {
			oldFile, newFile, err := s.diffFileSides(groupCtx, fromPatchset, toPatchset, p)
			if err != nil {
				return err
			}
			chunks[i] = diffutil.UnifiedFileDiff(oldFile, newFile)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, chunk := range chunks {
		if chunk != "" {
			out.WriteString(chunk)
		}
	}
	fromID := ""
	if fromPatchset != nil {
		fromID = fromPatchset.Id
	}
	return &corev1.DiffChangesetResponse{
		ChangesetId:    cs.Id,
		FromPatchsetId: fromID,
		ToPatchsetId:   toPatchset.Id,
		ChangedPaths:   paths,
		Diff:           out.String(),
	}, nil
}

func (s *ChangesetService) UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := requireEditBlobsAccessible(ctx, s.Blobs, slice, req.FileEdits); err != nil {
		return nil, err
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		baseCommitID = cs.BaseCommitId
	}
	baseKind, basePatchsetID, baseTreeID, err := s.patchsetBaseSource(ctx, cs, req, baseCommitID)
	if err != nil {
		return nil, err
	}
	validation, err := s.validator.validateFileEdits(ctx, slice, baseCommitID, baseTreeID, req.FileEdits, true)
	if err != nil {
		return nil, err
	}
	patchset := &corev1.Patchset{
		BaseCommitId:          baseCommitID,
		Author:                subjectID,
		ChangedPaths:          validation.AffectedPaths,
		FileEdits:             req.FileEdits,
		Coverage:              validation.Coverage,
		SubmitRequirements:    validation.SubmitRequirements,
		PathBases:             validation.PathBases,
		ReadSet:               validation.ReadSet,
		WriteSet:              validation.WriteSet,
		Conflicts:             req.Conflicts,
		Kind:                  req.PatchsetKind,
		BaseKind:              baseKind,
		BasePatchsetId:        basePatchsetID,
		BaseTreeId:            baseTreeID,
		StackParentPatchsetId: basePatchsetID,
	}
	if err := s.applyAuthoringConversation(ctx, patchset, slice.Id, req.ConversationId); err != nil {
		return nil, err
	}
	priorPatchsetID := cs.CurrentPatchsetId
	patchset, err = s.Changesets.AddPatchset(ctx, cs.Id, req.ExpectedCurrentPatchsetId, patchset)
	if err != nil {
		return nil, grpcError(err)
	}
	if priorPatchsetID == "" || patchset.Id != priorPatchsetID {
		s.recordBundledCheckRuns(ctx, cs.Id, patchset.Id, req.BundledCheckRuns)
		if s.dispatcher != nil {
			s.dispatcher.cancelOpenCheckRunsBeforePatchset(ctx, cs.Id, patchset.Id)
		}
		s.dispatchOutOfSliceChecks(ctx, cs, slice, patchset)
	}
	if patchset.Author != "" {
		usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{patchset.Author})
		if err != nil {
			return nil, grpcError(err)
		}
		if username := usernames[patchset.Author]; username != "" {
			patchset.Author = username
		}
	}
	return patchset, nil
}

// requireEditBlobsAccessible rejects edits that reference content hashes the
// authoring slice cannot already access, closing the claim-by-hash hole at
// ingestion. BlobId references are resolved to their content hash first.
func requireEditBlobsAccessible(ctx context.Context, blobs storage.BlobStore, slice *corev1.Slice, edits []*corev1.FileEdit) error {
	type blobReference struct {
		contentHash string
		path        string
	}

	references := make([]blobReference, 0, len(edits))
	hashes := make([]string, 0, len(edits))
	seenHashes := make(map[string]struct{}, len(edits))
	resolvedBlobIDs := make(map[string]string)
	unavailable := func(path string) error {
		return status.Errorf(codes.FailedPrecondition, "content for %s is not available to this slice; upload the blob first", path)
	}

	for _, edit := range edits {
		if edit == nil {
			continue
		}
		normalized, err := normalizeEdit(edit, false)
		if err != nil {
			// The normal validator reports malformed edits as InvalidArgument.
			// They cannot be ingested, so leave their error precedence unchanged.
			continue
		}
		edit = normalized
		path := edit.Path
		if path == "" {
			path = edit.OldPath
		}

		contentHash := edit.ContentHash
		if edit.BlobId != "" {
			var ok bool
			contentHash, ok = resolvedBlobIDs[edit.BlobId]
			if !ok {
				blob, err := blobs.GetByID(ctx, edit.BlobId)
				if errors.Is(err, storage.ErrNotFound) {
					return unavailable(path)
				}
				if err != nil {
					return grpcError(err)
				}
				contentHash = blob.ContentHash
				resolvedBlobIDs[edit.BlobId] = contentHash
			}
		}
		if contentHash == "" {
			continue
		}
		if _, ok := seenHashes[contentHash]; ok {
			continue
		}
		seenHashes[contentHash] = struct{}{}
		hashes = append(hashes, contentHash)
		references = append(references, blobReference{contentHash: contentHash, path: path})
	}
	if len(hashes) == 0 {
		return nil
	}

	accessible, err := accessibleBlobHashes(ctx, blobs, slice, hashes)
	if err != nil {
		return grpcError(err)
	}
	for _, reference := range references {
		if !accessible[reference.contentHash] {
			return unavailable(reference.path)
		}
	}
	return nil
}

func (s *ChangesetService) recordBundledCheckRuns(ctx context.Context, changesetID, patchsetID string, bundled []*corev1.BundledCheckRun) {
	if len(bundled) == 0 || s.Checks == nil {
		return
	}
	for _, bundledRun := range bundled {
		if bundledRun == nil {
			continue
		}
		run, err := s.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
			ChangesetID: changesetID,
			PatchsetID:  patchsetID,
			CheckName:   bundledRun.Name,
			Provenance:  "self",
			Status:      "running",
		})
		if err != nil {
			slog.Warn("failed to create bundled check run", "changeset_id", changesetID, "patchset_id", patchsetID, "check", bundledRun.Name, "error", err)
			continue
		}
		status := strings.ToLower(strings.TrimSpace(bundledRun.Status))
		summary := bundledRun.Summary
		if status != "passed" && status != "failed" && status != "errored" && status != "skipped" {
			status = "errored"
			summary = fmt.Sprintf("invalid bundled check status %q", strings.TrimSpace(bundledRun.Status))
		}
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, run.Id, status, bundledRun.ExitCode, summary); err != nil {
			slog.Warn("failed to record bundled check status", "run_id", run.Id, "check", bundledRun.Name, "status", status, "error", err)
		}
		if bundledRun.Log != "" {
			if _, err := s.Checks.AppendCheckRunLog(ctx, run.Id, 1, "stdout", bundledRun.Log); err != nil {
				slog.Warn("failed to record bundled check log", "run_id", run.Id, "check", bundledRun.Name, "error", err)
			}
		}
	}
}

// applyAuthoringConversation stamps the agent conversation that produced a
// patchset, recording the conversation's current latest event seq as the cutoff
// so the patchset's exchange is the events in (prevCutoff, seq]. The conversation
// must belong to the same slice as the changeset.
func (s *ChangesetService) applyAuthoringConversation(ctx context.Context, patchset *corev1.Patchset, sliceID, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || s.Agents == nil {
		return nil
	}
	conv, err := s.Agents.GetConversation(ctx, conversationID)
	if err != nil {
		return grpcError(err)
	}
	if conv.SliceId != sliceID {
		return status.Error(codes.InvalidArgument, "conversation does not belong to the changeset slice")
	}
	seq, err := s.Agents.LatestEventSeq(ctx, conversationID)
	if err != nil {
		return grpcError(err)
	}
	patchset.AuthoringConversationId = conversationID
	patchset.AuthoringConversationSeq = seq
	return nil
}

func (s *ChangesetService) patchsetBaseSource(ctx context.Context, cs *corev1.Changeset, req *corev1.UpdateChangesetRequest, baseCommitID string) (baseKind, basePatchsetID, baseTreeID string, err error) {
	baseKind = strings.TrimSpace(req.BaseKind)
	if baseKind == "" {
		if req.BasePatchsetId != "" || cs.ParentChangesetId != "" {
			baseKind = "patchset"
		} else {
			baseKind = "commit"
		}
	}
	switch baseKind {
	case "commit":
		baseTreeID, err = s.Repository.RootTreeForCommit(ctx, baseCommitID)
		if err != nil {
			return "", "", "", grpcError(err)
		}
		return baseKind, "", baseTreeID, nil
	case "patchset":
		parentID := strings.TrimSpace(cs.ParentChangesetId)
		if parentID == "" {
			return "", "", "", status.Error(codes.InvalidArgument, "patchset base requires a parent changeset")
		}
		parent, err := s.Changesets.Get(ctx, parentID)
		if err != nil {
			return "", "", "", grpcError(err)
		}
		if cs.StackId == "" || parent.StackId != cs.StackId || parent.TargetRef != cs.TargetRef || !sameSliceRef(parent.AuthoringSlice, cs.AuthoringSlice) {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent changeset is not in the same stack")
		}
		currentParentPatchset := currentPatchset(parent)
		if currentParentPatchset == nil {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent changeset has no current patchset")
		}
		basePatchsetID = strings.TrimSpace(req.BasePatchsetId)
		if basePatchsetID == "" {
			basePatchsetID = currentParentPatchset.Id
		}
		if req.ExpectedParentPatchsetId != "" && req.ExpectedParentPatchsetId != basePatchsetID {
			return "", "", "", status.Error(codes.Aborted, "parent patchset changed")
		}
		if basePatchsetID != currentParentPatchset.Id {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent patchset is not current")
		}
		if currentParentPatchset.ResultTreeId == "" {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent patchset has no result tree")
		}
		return baseKind, basePatchsetID, currentParentPatchset.ResultTreeId, nil
	default:
		return "", "", "", status.Errorf(codes.InvalidArgument, "unsupported base kind %q", baseKind)
	}
}

// resolveAuthors rewrites every Changeset.Author and Patchset.Author from the
// internal subject id to the author's username. Unresolved subjects (no personal
// account) are left unchanged so the response still carries a stable identifier.
func (s *ChangesetService) resolveAuthors(ctx context.Context, changesets ...*corev1.Changeset) error {
	seen := map[string]struct{}{}
	var subjectIDs []string
	addSubject := func(subjectID string) {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			return
		}
		if _, ok := seen[subjectID]; ok {
			return
		}
		seen[subjectID] = struct{}{}
		subjectIDs = append(subjectIDs, subjectID)
	}

	for _, cs := range changesets {
		if cs == nil {
			continue
		}
		addSubject(cs.Author)
		for _, patchset := range cs.Patchsets {
			if patchset != nil {
				addSubject(patchset.Author)
			}
		}
	}
	if len(subjectIDs) == 0 {
		return nil
	}

	usernames, err := s.Auth.UsernamesForSubjects(ctx, subjectIDs)
	if err != nil {
		return err
	}
	for _, cs := range changesets {
		if cs == nil {
			continue
		}
		if username := usernames[cs.Author]; username != "" {
			cs.Author = username
		}
		for _, patchset := range cs.Patchsets {
			if patchset == nil {
				continue
			}
			if username := usernames[patchset.Author]; username != "" {
				patchset.Author = username
			}
		}
	}
	return nil
}

func selectChangesetPatchset(cs *corev1.Changeset, selector string) (*corev1.Patchset, error) {
	if len(cs.Patchsets) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no patchsets")
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if cs.CurrentPatchsetId == "" {
			return cs.Patchsets[len(cs.Patchsets)-1], nil
		}
		selector = cs.CurrentPatchsetId
	}
	if n, err := strconv.ParseInt(selector, 10, 64); err == nil {
		for _, patchset := range cs.Patchsets {
			if patchset.Number == n {
				return patchset, nil
			}
		}
		return nil, status.Errorf(codes.NotFound, "patchset number %d not found", n)
	}
	if changesetSelector, n, ok := parsePatchsetIDSelector(selector); ok {
		if !changesetSelectorMatchesID(changesetSelector, cs.Id) {
			return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
		}
		for _, patchset := range cs.Patchsets {
			if patchset.Number == n {
				return patchset, nil
			}
		}
		return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == selector {
			return patchset, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	if len(cs.Patchsets) == 0 {
		return nil
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func parsePatchsetIDSelector(selector string) (changesetSelector string, patchsetNumber int64, ok bool) {
	dot := strings.LastIndex(selector, ".")
	if dot <= 0 || dot == len(selector)-1 {
		return "", 0, false
	}
	changesetSelector = selector[:dot]
	if _, ok := storage.ChangesetIDLookupPrefix(changesetSelector); !ok {
		return "", 0, false
	}
	n, err := strconv.ParseInt(selector[dot+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return changesetSelector, n, true
}

func changesetSelectorMatchesID(selector, id string) bool {
	prefix, ok := storage.ChangesetIDLookupPrefix(selector)
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.ToLower(id), prefix)
}

func sameSliceRef(a, b *corev1.SliceRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Account == b.Account && a.Slice == b.Slice
}

func changedPathsForDiff(from, to *corev1.Patchset) []string {
	seen := map[string]struct{}{}
	for _, patchset := range []*corev1.Patchset{from, to} {
		if patchset == nil {
			continue
		}
		for _, edit := range patchset.FileEdits {
			for _, p := range editPaths(edit) {
				seen[p] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type diffBaseFileKey struct {
	commitID string
	treeID   string
	path     string
}

type diffContentReader func(context.Context, string) ([]byte, error)
type diffBaseFileReader func(context.Context, string, string, string) (diffutil.File, error)

func (s *ChangesetService) diffFileSides(ctx context.Context, from, to *corev1.Patchset, p string) (diffutil.File, diffutil.File, error) {
	contentByHash := map[string][]byte{}
	readContent := func(ctx context.Context, contentHash string) ([]byte, error) {
		if data, ok := contentByHash[contentHash]; ok {
			return data, nil
		}
		data, err := s.readContentHash(ctx, contentHash)
		if err != nil {
			return nil, err
		}
		contentByHash[contentHash] = data
		return data, nil
	}

	// The object-store cache is write-through and does not cache read misses, so
	// this request-local dedup is what removes duplicate base fetches while
	// resolving the old and new sides for one changed path.
	baseFiles := map[diffBaseFileKey]diffutil.File{}
	readBase := func(ctx context.Context, commitID, treeID, p string) (diffutil.File, error) {
		key := diffBaseFileKey{commitID: commitID, treeID: treeID, path: p}
		if file, ok := baseFiles[key]; ok {
			return file, nil
		}
		file, err := s.baseFileWithReader(ctx, commitID, treeID, p, readContent)
		if err != nil {
			return diffutil.File{}, err
		}
		baseFiles[key] = file
		return file, nil
	}

	var oldFile diffutil.File
	var err error
	if from == nil {
		oldFile, err = readBase(ctx, to.BaseCommitId, to.BaseTreeId, p)
	} else {
		oldFile, err = s.patchsetFileWithReaders(ctx, from, p, readBase, readContent)
	}
	if err != nil {
		return diffutil.File{}, diffutil.File{}, err
	}
	newFile, err := s.patchsetFileWithReaders(ctx, to, p, readBase, readContent)
	if err != nil {
		return diffutil.File{}, diffutil.File{}, err
	}
	return oldFile, newFile, nil
}

func (s *ChangesetService) patchsetFile(ctx context.Context, patchset *corev1.Patchset, p string) (diffutil.File, error) {
	return s.patchsetFileWithReaders(ctx, patchset, p, func(ctx context.Context, commitID, treeID, p string) (diffutil.File, error) {
		return s.baseFileWithReader(ctx, commitID, treeID, p, s.readContentHash)
	}, s.readContentHash)
}

func (s *ChangesetService) patchsetFileWithReaders(ctx context.Context, patchset *corev1.Patchset, p string, readBase diffBaseFileReader, readContent diffContentReader) (diffutil.File, error) {
	var file diffutil.File
	fileSet := false
	for _, edit := range patchset.FileEdits {
		switch edit.Op {
		case "delete":
			if edit.Path == p {
				file = diffutil.File{Path: p}
				fileSet = true
			}
		case "rename":
			if edit.OldPath == p {
				file = diffutil.File{Path: p}
				fileSet = true
			}
			if edit.Path == p {
				if edit.BlobId != "" || edit.ContentHash != "" {
					return s.editFileWithReader(ctx, edit, p, readContent)
				}
				oldFile, err := readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, edit.OldPath)
				if err != nil {
					return diffutil.File{}, err
				}
				oldFile.Path = p
				file = oldFile
				fileSet = true
			}
		case "upsert", "add", "update":
			if edit.Path == p {
				return s.editFileWithReader(ctx, edit, p, readContent)
			}
		}
	}
	if !fileSet {
		return readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, p)
	}
	return file, nil
}

func (s *ChangesetService) baseFile(ctx context.Context, commitID, p string) (diffutil.File, error) {
	return s.baseFileWithReader(ctx, commitID, "", p, s.readContentHash)
}

func (s *ChangesetService) baseFileWithReader(ctx context.Context, commitID, treeID, p string, readContent diffContentReader) (diffutil.File, error) {
	var entry *storage.FileEntry
	var err error
	if treeID != "" {
		entry, err = s.Repository.GetFileAtTree(ctx, treeID, p)
	} else {
		entry, err = s.Repository.GetFile(ctx, commitID, p)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return diffutil.File{Path: p}, nil
	}
	if err != nil {
		return diffutil.File{}, grpcError(err)
	}
	data, err := readContent(ctx, entry.ContentHash)
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}

func (s *ChangesetService) editFile(ctx context.Context, edit *corev1.FileEdit, p string) (diffutil.File, error) {
	return s.editFileWithReader(ctx, edit, p, s.readContentHash)
}

func (s *ChangesetService) editFileWithReader(ctx context.Context, edit *corev1.FileEdit, p string, readContent diffContentReader) (diffutil.File, error) {
	contentHash := edit.ContentHash
	if contentHash == "" && edit.BlobId != "" {
		blob, err := s.Blobs.GetByID(ctx, edit.BlobId)
		if err != nil {
			return diffutil.File{}, grpcError(err)
		}
		contentHash = blob.ContentHash
	}
	if contentHash == "" {
		return diffutil.File{}, status.Errorf(codes.FailedPrecondition, "edit for %s has no content hash", p)
	}
	data, err := readContent(ctx, contentHash)
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}

func (s *ChangesetService) readContentHash(ctx context.Context, contentHash string) ([]byte, error) {
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, grpcError(err)
	}
	return data, nil
}

func (s *ChangesetService) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForWriteOrAuthor(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	extraCheckStatuses, err := s.skippedRequiredCheckStatuses(ctx, cs)
	if err != nil {
		return nil, grpcError(err)
	}
	if submitter, ok := s.Changesets.(changesetSubmitWithCheckStatuses); ok {
		res, err := submitter.SubmitWithCheckStatuses(ctx, cs.Id, req.ExpectedCurrentPatchsetId, extraCheckStatuses)
		if err != nil {
			return nil, grpcError(err)
		}
		return res, nil
	}
	res, err := s.Changesets.Submit(ctx, cs.Id, req.ExpectedCurrentPatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) ApproveChangeset(ctx context.Context, req *corev1.ApproveChangesetRequest) (*corev1.ApproveChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	res, err := s.Changesets.Approve(ctx, cs.Id, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) ReportCheckResult(ctx context.Context, req *corev1.ReportCheckResultRequest) (*corev1.ReportCheckResultResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	res, err := s.Changesets.ReportCheckResult(ctx, cs.Id, subjectID, req.CheckName, req.Status)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForWriteOrAuthor(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	if err := s.Changesets.Abandon(ctx, cs.Id); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *ChangesetService) getAuthorizedChangeset(ctx context.Context, subjectID, changesetID string) (*corev1.Changeset, error) {
	cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.resolveAuthors(ctx, cs); err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) getChangesetForAction(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if cs.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
		return nil, err
	}
	storage.PopulateChangesetHandles(cs)
	return cs, nil
}

func (s *ChangesetService) getChangesetForWriteOrAuthor(ctx context.Context, subjectID, changesetID string) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if cs.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	if cs.Author == subjectID {
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionWrite); err != nil {
		return nil, err
	}
	storage.PopulateChangesetHandles(cs)
	return cs, nil
}

func (v diffValidator) validateFileEdits(ctx context.Context, slice *corev1.Slice, baseCommitID, baseTreeID string, edits []*corev1.FileEdit, requireBlob bool) (*corev1.ValidateWorkspaceDiffResponse, error) {
	changed := map[string]struct{}{}
	for _, edit := range edits {
		normalized, err := normalizeEdit(edit, requireBlob)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if requireBlob {
			if err := v.hydrateBlobMetadata(ctx, normalized); err != nil {
				return nil, err
			}
		}
		edit.Op = normalized.Op
		edit.Path = normalized.Path
		edit.OldPath = normalized.OldPath
		edit.BlobId = normalized.BlobId
		edit.ContentHash = normalized.ContentHash
		edit.Mode = normalized.Mode
		for _, p := range editPaths(edit) {
			if !paths.InAnyPrefix(slice.Definition.IncludedPaths, p) {
				return nil, status.Errorf(codes.FailedPrecondition, "path %s is outside slice %s/%s", p, slice.Ref.Account, slice.Ref.Slice)
			}
			changed[p] = struct{}{}
		}
	}
	affected := make([]string, 0, len(changed))
	for p := range changed {
		affected = append(affected, p)
	}
	sort.Strings(affected)
	coverageByPath, err := v.Slices.CoveringIDsByPath(ctx, affected)
	if err != nil {
		return nil, grpcError(err)
	}
	coverage := make([]*corev1.PathCoverage, 0, len(affected))
	pathBases := make([]*corev1.PathBase, 0, len(affected))
	readSet := make([]*corev1.PathSetEntry, 0, len(affected))
	writeSet := make([]*corev1.PathSetEntry, 0, len(affected))
	for _, p := range affected {
		coverage = append(coverage, &corev1.PathCoverage{Path: p, CoveringSliceIds: coverageByPath[p]})
		base, err := v.pathBase(ctx, baseCommitID, baseTreeID, p)
		if err != nil {
			return nil, err
		}
		pathBases = append(pathBases, base)
		readSet = append(readSet, &corev1.PathSetEntry{Path: p})
		writeSet = append(writeSet, &corev1.PathSetEntry{Path: p})
	}
	return &corev1.ValidateWorkspaceDiffResponse{
		AffectedPaths: affected,
		Coverage:      coverage,
		SubmitRequirements: &corev1.SubmitRequirements{
			RequiredApprovals: slice.Definition.RequiredApprovals,
			RequiredChecks:    append([]string(nil), slice.Definition.RequiredChecks...),
			SourceSliceDefinitionHash: storage.SubmitRequirementsHash(
				slice.Definition.IncludedPaths,
				slice.Definition.RequiredApprovals,
				slice.Definition.RequiredChecks,
			),
		},
		PathBases: pathBases,
		ReadSet:   readSet,
		WriteSet:  writeSet,
	}, nil
}

func (v diffValidator) hydrateBlobMetadata(ctx context.Context, edit *corev1.FileEdit) error {
	if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.Op == "mkdir" {
		return nil
	}
	blob, err := v.Blobs.GetByID(ctx, edit.BlobId)
	if err != nil {
		return grpcError(err)
	}
	if edit.ContentHash != "" && edit.ContentHash != blob.ContentHash {
		return status.Errorf(codes.InvalidArgument, "content hash does not match blob %s", edit.BlobId)
	}
	edit.ContentHash = blob.ContentHash
	return nil
}

func (v diffValidator) pathBase(ctx context.Context, baseCommitID, baseTreeID, p string) (*corev1.PathBase, error) {
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		Check:            "entry_fingerprint",
		EntryFingerprint: storage.MissingEntryFingerprint(),
	}
	var entry *storage.TreeEntry
	var err error
	if baseTreeID != "" {
		entry, err = v.Repository.GetEntryAtTree(ctx, baseTreeID, p)
	} else {
		entry, err = v.Repository.GetEntry(ctx, baseCommitID, p)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return base, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	base.Exists = true
	base.EntryKind = entry.Kind
	switch entry.Kind {
	case "file":
		base.Mode = entry.Mode
		base.BlobId = entry.BlobID
		base.ContentHash = entry.ContentHash
		base.EntryFingerprint = storage.FileEntryFingerprint(storage.FileEntry{
			Path:        entry.Path,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
			Mode:        entry.Mode,
			Size:        entry.Size,
		})
	case "directory":
		base.TreeId = entry.TreeID
		base.EntryFingerprint = storage.DirectoryEntryFingerprint(entry.TreeID)
	default:
		base.EntryFingerprint = storage.MissingEntryFingerprint()
	}
	return base, nil
}

func normalizeEdit(edit *corev1.FileEdit, requireBlob bool) (*corev1.FileEdit, error) {
	if edit == nil {
		return nil, fmt.Errorf("file edit is nil")
	}
	out := &corev1.FileEdit{
		Op:          edit.Op,
		Path:        edit.Path,
		OldPath:     edit.OldPath,
		BlobId:      edit.BlobId,
		ContentHash: edit.ContentHash,
		Mode:        edit.Mode,
	}
	if out.Op == "" {
		out.Op = "upsert"
	}
	switch out.Op {
	case "add", "update":
		out.Op = "upsert"
	case "upsert", "delete", "rename", "mkdir":
	default:
		return nil, fmt.Errorf("unsupported file edit op %q", out.Op)
	}
	if out.Op == "rename" {
		if out.OldPath == "" {
			return nil, fmt.Errorf("old path is required for rename edit")
		}
		if out.Path == "" {
			return nil, fmt.Errorf("path is required for rename edit")
		}
	} else if out.Path == "" {
		return nil, fmt.Errorf("path is required for %s edit", out.Op)
	} else if out.OldPath != "" {
		return nil, fmt.Errorf("old path is only supported for rename edits")
	}
	if out.Path != "" {
		p, err := paths.Canonical(out.Path)
		if err != nil {
			return nil, err
		}
		out.Path = p
	}
	if out.OldPath != "" {
		p, err := paths.Canonical(out.OldPath)
		if err != nil {
			return nil, err
		}
		out.OldPath = p
	}
	if out.Op == "rename" && out.Path == out.OldPath {
		return nil, fmt.Errorf("rename source and destination must differ")
	}
	if requireBlob && out.Op != "delete" && out.Op != "rename" && out.Op != "mkdir" && out.BlobId == "" {
		return nil, fmt.Errorf("blob id is required for %s edit on %s", out.Op, out.Path)
	}
	if out.Mode == 0 && out.Op != "delete" && out.Op != "mkdir" {
		out.Mode = 0o100644
	}
	return out, nil
}

func editPaths(edit *corev1.FileEdit) []string {
	var out []string
	if edit.Path != "" {
		out = append(out, edit.Path)
	}
	if edit.OldPath != "" {
		out = append(out, edit.OldPath)
	}
	return out
}

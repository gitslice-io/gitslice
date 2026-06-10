package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/diffutil"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore
	validator   diffValidator
}

type diffValidator struct {
	Blobs      storage.BlobStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
}

func (s *ChangesetService) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionWrite); err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Create(ctx, subjectID, req)
	if err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	return s.getAuthorizedChangeset(ctx, subjectID, req.ChangesetId)
}

func (s *ChangesetService) ListChangesets(ctx context.Context, req *corev1.ListChangesetsRequest) (*corev1.ListChangesetsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
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
	return &corev1.ListChangesetsResponse{Changesets: changesets}, nil
}

func (s *ChangesetService) DiffChangeset(ctx context.Context, req *corev1.DiffChangesetRequest) (*corev1.DiffChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
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
	var out strings.Builder
	for _, p := range paths {
		var oldFile diffutil.File
		if fromPatchset == nil {
			oldFile, err = s.baseFile(ctx, toPatchset.BaseCommitId, p)
		} else {
			oldFile, err = s.patchsetFile(ctx, fromPatchset, p)
		}
		if err != nil {
			return nil, err
		}
		newFile, err := s.patchsetFile(ctx, toPatchset, p)
		if err != nil {
			return nil, err
		}
		chunk := diffutil.UnifiedFileDiff(oldFile, newFile)
		if chunk != "" {
			out.WriteString(chunk)
		}
	}
	fromID := ""
	if fromPatchset != nil {
		fromID = fromPatchset.Id
	}
	return &corev1.DiffChangesetResponse{
		ChangesetId:        cs.Id,
		FromPatchsetId:     fromID,
		ToPatchsetId:       toPatchset.Id,
		ChangedPaths:       paths,
		Diff:               out.String(),
		ChangesetHandle:    cs.Handle,
		FromPatchsetHandle: patchsetHandle(fromPatchset),
		ToPatchsetHandle:   patchsetHandle(toPatchset),
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
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		baseCommitID = cs.BaseCommitId
	}
	validation, err := s.validator.validateFileEdits(ctx, slice, baseCommitID, req.FileEdits, true)
	if err != nil {
		return nil, err
	}
	patchset := &corev1.Patchset{
		BaseCommitId:       baseCommitID,
		Author:             subjectID,
		ChangedPaths:       validation.AffectedPaths,
		FileEdits:          req.FileEdits,
		Coverage:           validation.Coverage,
		SubmitRequirements: validation.SubmitRequirements,
		PathBases:          validation.PathBases,
		ReadSet:            validation.ReadSet,
		WriteSet:           validation.WriteSet,
		Conflicts:          req.Conflicts,
		Kind:               req.PatchsetKind,
	}
	patchset, err = s.Changesets.AddPatchset(ctx, cs.Id, req.ExpectedCurrentPatchsetId, patchset)
	if err != nil {
		return nil, grpcError(err)
	}
	if patchset.Handle == "" && cs.Handle != "" {
		patchset.Handle = storage.PatchsetHandle(cs.AuthoringSlice, cs.Number, patchset.Number)
	}
	return patchset, nil
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
	if account, sliceName, changesetNumber, n, ok := storage.ParsePatchsetHandle(selector); ok {
		if cs.AuthoringSlice == nil || account != cs.AuthoringSlice.Account || sliceName != cs.AuthoringSlice.Slice || changesetNumber != cs.Number {
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

func patchsetHandle(patchset *corev1.Patchset) string {
	if patchset == nil {
		return ""
	}
	return patchset.Handle
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

func (s *ChangesetService) patchsetFile(ctx context.Context, patchset *corev1.Patchset, p string) (diffutil.File, error) {
	file, err := s.baseFile(ctx, patchset.BaseCommitId, p)
	if err != nil {
		return diffutil.File{}, err
	}
	for _, edit := range patchset.FileEdits {
		switch edit.Op {
		case "delete":
			if edit.Path == p {
				file = diffutil.File{Path: p}
			}
		case "rename":
			if edit.OldPath == p {
				file = diffutil.File{Path: p}
			}
			if edit.Path == p {
				if edit.BlobId != "" || edit.ContentHash != "" {
					return s.editFile(ctx, edit, p)
				}
				oldFile, err := s.baseFile(ctx, patchset.BaseCommitId, edit.OldPath)
				if err != nil {
					return diffutil.File{}, err
				}
				oldFile.Path = p
				file = oldFile
			}
		case "upsert", "add", "update":
			if edit.Path == p {
				return s.editFile(ctx, edit, p)
			}
		}
	}
	return file, nil
}

func (s *ChangesetService) baseFile(ctx context.Context, commitID, p string) (diffutil.File, error) {
	entry, err := s.Repository.GetFile(ctx, commitID, p)
	if errors.Is(err, storage.ErrNotFound) {
		return diffutil.File{Path: p}, nil
	}
	if err != nil {
		return diffutil.File{}, grpcError(err)
	}
	data, err := s.readContentHash(ctx, entry.ContentHash)
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}

func (s *ChangesetService) editFile(ctx context.Context, edit *corev1.FileEdit, p string) (diffutil.File, error) {
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
	data, err := s.readContentHash(ctx, contentHash)
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
	res, err := s.Changesets.Submit(ctx, cs.Id, req.ExpectedCurrentPatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	if res.ChangesetHandle == "" {
		res.ChangesetHandle = cs.Handle
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
	return s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionRead)
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

func (v diffValidator) validateFileEdits(ctx context.Context, slice *corev1.Slice, baseCommitID string, edits []*corev1.FileEdit, requireBlob bool) (*corev1.ValidateWorkspaceDiffResponse, error) {
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
		*edit = *normalized
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
	coverage := make([]*corev1.PathCoverage, 0, len(affected))
	pathBases := make([]*corev1.PathBase, 0, len(affected))
	readSet := make([]*corev1.PathSetEntry, 0, len(affected))
	writeSet := make([]*corev1.PathSetEntry, 0, len(affected))
	for _, p := range affected {
		covering, err := v.Slices.CoveringIDs(ctx, p)
		if err != nil {
			return nil, grpcError(err)
		}
		coverage = append(coverage, &corev1.PathCoverage{Path: p, CoveringSliceIds: covering})
		base, err := v.pathBase(ctx, baseCommitID, p)
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
			RequiredApprovals:         slice.Definition.RequiredApprovals,
			RequiredChecks:            append([]string(nil), slice.Definition.RequiredChecks...),
			SourceSliceDefinitionHash: slice.DefinitionHash,
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

func (v diffValidator) pathBase(ctx context.Context, baseCommitID, p string) (*corev1.PathBase, error) {
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		Check:            "entry_fingerprint",
		EntryFingerprint: storage.MissingEntryFingerprint(),
	}
	entry, err := v.Repository.GetEntry(ctx, baseCommitID, p)
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
	out := *edit
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
	return &out, nil
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

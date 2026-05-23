package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetService struct {
	Auth       *postgres.AuthStore
	Changesets *postgres.ChangesetStore
	Slices     *postgres.SliceStore
	validator  diffValidator
}

type diffValidator struct {
	Repository *postgres.RepositoryStore
	Slices     *postgres.SliceStore
}

func (s *ChangesetService) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, req.AuthoringSlice.Account); err != nil {
		return nil, grpcError(err)
	}
	cs, err := s.Changesets.Create(ctx, subjectID, req)
	if err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Get(ctx, req.ChangesetId)
	if err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Get(ctx, req.ChangesetId)
	if err != nil {
		return nil, grpcError(err)
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
	}
	patchset, err = s.Changesets.AddPatchset(ctx, req.ChangesetId, req.ExpectedCurrentPatchsetId, patchset)
	if err != nil {
		return nil, grpcError(err)
	}
	return patchset, nil
}

func (s *ChangesetService) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	res, err := s.Changesets.Submit(ctx, req.ChangesetId, req.ExpectedCurrentPatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	if err := s.Changesets.Abandon(ctx, req.ChangesetId); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (v diffValidator) validateFileEdits(ctx context.Context, slice *corev1.Slice, baseCommitID string, edits []*corev1.FileEdit, requireBlob bool) (*corev1.ValidateWorkspaceDiffResponse, error) {
	changed := map[string]struct{}{}
	for _, edit := range edits {
		normalized, err := normalizeEdit(edit, requireBlob)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
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
			SourceSliceDefinitionHash: slice.DefinitionHash,
		},
		PathBases: pathBases,
		ReadSet:   readSet,
		WriteSet:  writeSet,
	}, nil
}

func (v diffValidator) pathBase(ctx context.Context, baseCommitID, p string) (*corev1.PathBase, error) {
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		Check:            "entry_fingerprint",
		EntryFingerprint: postgres.MissingEntryFingerprint(),
	}
	entry, err := v.Repository.GetFile(ctx, baseCommitID, p)
	if errors.Is(err, postgres.ErrNotFound) {
		return base, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	base.Exists = true
	base.EntryKind = "file"
	base.Mode = entry.Mode
	base.BlobId = entry.BlobID
	base.ContentHash = entry.ContentHash
	base.EntryFingerprint = postgres.FileEntryFingerprint(*entry)
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
	if requireBlob && out.Op != "delete" && out.Op != "rename" && out.BlobId == "" {
		return nil, fmt.Errorf("blob id is required for %s edit on %s", out.Op, out.Path)
	}
	if out.Mode == 0 && out.Op != "delete" {
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

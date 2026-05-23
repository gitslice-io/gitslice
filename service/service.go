package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type Services struct {
	Store       *postgres.Store
	ObjectStore ObjectStore
}

func New(store *postgres.Store, objectStore ObjectStore) *Services {
	return &Services{Store: store, ObjectStore: objectStore}
}

func (s *Services) Login(ctx context.Context, req *corev1.LoginRequest) (*corev1.LoginResponse, error) {
	token, subjectID, err := s.Store.LoginDevUser(ctx, req.DevUser)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.LoginResponse{Token: token, SubjectId: subjectID}, nil
}

func (s *Services) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := s.Store.GetFile(ctx, req.CommitId, p)
	if err == nil {
		return &corev1.ResolvePathResponse{Entry: treeEntryFromFile(*entry)}, nil
	}
	if !errors.Is(err, postgres.ErrNotFound) {
		return nil, grpcError(err)
	}
	children, err := s.Store.ListFiles(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(children) == 0 {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ResolvePathResponse{Entry: &corev1.TreeEntry{
		Path:   p,
		Name:   path.Base(p),
		Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		TreeId: directoryTreeID(p, children),
	}}, nil
}

func (s *Services) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	p := req.Path
	if p == "" {
		p = "/"
	}
	if p != "/" {
		var err error
		p, err = paths.Canonical(p)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	files, err := s.Store.ListFiles(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	entries := immediateDirectoryEntries(p, files)
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func (s *Services) ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := s.Store.GetFile(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(entry.ContentHash), req.Offset, req.Length)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ReadFileResponse{Data: data, Offset: req.Offset, ContentHash: entry.ContentHash}, nil
}

func (s *Services) GetCommit(ctx context.Context, req *corev1.GetCommitRequest) (*corev1.Commit, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	commit, err := s.Store.GetCommit(ctx, req.CommitId)
	if err != nil {
		return nil, grpcError(err)
	}
	return commit, nil
}

func (s *Services) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	refName := req.RefName
	if refName == "" {
		refName = postgres.DefaultTargetRef
	}
	ref, err := s.Store.GetRef(ctx, refName)
	if err != nil {
		return nil, grpcError(err)
	}
	return ref, nil
}

func (s *Services) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	records, err := s.Store.GetBlobsByContentHash(ctx, req.ContentHashes)
	if err != nil {
		return nil, grpcError(err)
	}
	byHash := map[string]*corev1.BlobRecord{}
	for _, record := range records {
		byHash[record.ContentHash] = record
	}
	out := make([]*corev1.BlobRecord, 0, len(req.ContentHashes))
	for _, hash := range req.ContentHashes {
		if record := byHash[hash]; record != nil {
			out = append(out, record)
			continue
		}
		out = append(out, &corev1.BlobRecord{ContentHash: hash, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (s *Services) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	contentHash := objectid.RawContentHash(req.Data)
	if req.ContentHash != "" && req.ContentHash != contentHash {
		return nil, status.Error(codes.InvalidArgument, "content hash does not match blob bytes")
	}
	blobID := objectid.BlobID(req.Data)
	key := filesystem.BlobKey(contentHash)
	if err := s.ObjectStore.Put(ctx, key, bytes.NewReader(req.Data)); err != nil {
		return nil, grpcError(err)
	}
	if err := s.Store.UpsertBlob(ctx, blobID, contentHash, int64(len(req.Data)), key); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.UploadBlobResponse{BlobId: blobID, ContentHash: contentHash, Size: int64(len(req.Data))}, nil
}

func (s *Services) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Ref == nil {
		return nil, status.Error(codes.InvalidArgument, "slice ref is required")
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, req.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, req.Ref)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *Services) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	slice, err := s.Store.GetSlice(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *Services) ListSlices(ctx context.Context, req *corev1.ListSlicesRequest) (*corev1.ListSlicesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, req.Account); err != nil {
		return nil, grpcError(err)
	}
	slices, err := s.Store.ListSlices(ctx, req.Account, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSlicesResponse{Slices: slices}, nil
}

func (s *Services) UpdateSliceDefinition(ctx context.Context, req *corev1.UpdateSliceDefinitionRequest) (*corev1.SliceDefinition, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	definition, err := s.Store.UpdateSliceDefinition(ctx, req.SliceId, req.ExpectedDefinitionHash, req.Definition)
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}

func (s *Services) GetWorkspaceState(ctx context.Context, req *corev1.GetWorkspaceStateRequest) (*corev1.WorkspaceState, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := sliceRefFromWorkspace(req.Workspace)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.WorkspaceState{
		Ref:          req.Workspace,
		BaseCommitId: target.CommitId,
		Slice: &corev1.SliceBinding{
			Slice:               ref,
			SliceId:             slice.Id,
			SliceDefinitionHash: slice.DefinitionHash,
		},
		HydratedPaths: slice.Definition.IncludedPaths,
	}, nil
}

func (s *Services) HydratePaths(ctx context.Context, req *corev1.HydratePathsRequest) (*corev1.HydratePathsResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	if len(req.Paths) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one path is required")
	}
	target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	read, err := s.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: target.CommitId, Path: req.Paths[0]})
	if err != nil {
		return nil, err
	}
	resolved, err := s.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: target.CommitId, Path: req.Paths[0]})
	if err != nil {
		return nil, err
	}
	return &corev1.HydratePathsResponse{Path: req.Paths[0], Entry: resolved.Entry, Data: read.Data}, nil
}

func (s *Services) ValidateWorkspaceDiff(ctx context.Context, req *corev1.ValidateWorkspaceDiffRequest) (*corev1.ValidateWorkspaceDiffResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := sliceRefFromWorkspace(req.Workspace)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		baseCommitID = target.CommitId
	}
	validation, err := s.validateFileEdits(ctx, slice, baseCommitID, req.FileEdits, false)
	if err != nil {
		return nil, err
	}
	return validation, nil
}

func (s *Services) RecordWorkspaceOperation(ctx context.Context, req *corev1.RecordWorkspaceOperationRequest) (*corev1.RecordWorkspaceOperationResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	id, err := objectid.RandomID("op")
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.RecordWorkspaceOperationResponse{OperationId: id}, nil
}

func (s *Services) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, req.AuthoringSlice.Account); err != nil {
		return nil, grpcError(err)
	}
	cs, err := s.Store.CreateChangeset(ctx, subjectID, req)
	if err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *Services) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	cs, err := s.Store.GetChangeset(ctx, req.ChangesetId)
	if err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *Services) UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.Store.GetChangeset(ctx, req.ChangesetId)
	if err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		baseCommitID = cs.BaseCommitId
	}
	validation, err := s.validateFileEdits(ctx, slice, baseCommitID, req.FileEdits, true)
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
	patchset, err = s.Store.AddPatchset(ctx, req.ChangesetId, req.ExpectedCurrentPatchsetId, patchset)
	if err != nil {
		return nil, grpcError(err)
	}
	return patchset, nil
}

func (s *Services) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	res, err := s.Store.SubmitChangeset(ctx, req.ChangesetId, req.ExpectedCurrentPatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *Services) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	if err := s.Store.AbandonChangeset(ctx, req.ChangesetId); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *Services) validateFileEdits(ctx context.Context, slice *corev1.Slice, baseCommitID string, edits []*corev1.FileEdit, requireBlob bool) (*corev1.ValidateWorkspaceDiffResponse, error) {
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
		covering, err := s.Store.CoveringSliceIDs(ctx, p)
		if err != nil {
			return nil, grpcError(err)
		}
		coverage = append(coverage, &corev1.PathCoverage{Path: p, CoveringSliceIds: covering})
		base, err := s.pathBase(ctx, baseCommitID, p)
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

func (s *Services) pathBase(ctx context.Context, baseCommitID, p string) (*corev1.PathBase, error) {
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		Check:            "entry_fingerprint",
		EntryFingerprint: postgres.MissingEntryFingerprint(),
	}
	entry, err := s.Store.GetFile(ctx, baseCommitID, p)
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

func requireSubject(ctx context.Context) (string, error) {
	subjectID, ok := authctx.SubjectID(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing subject")
	}
	return subjectID, nil
}

func sliceRefFromWorkspace(ref *corev1.WorkspaceRef) (*corev1.SliceRef, error) {
	if ref == nil || ref.Id == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	parts := strings.Split(ref.Id, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("workspace id must be account/slice")
	}
	return &corev1.SliceRef{Account: parts[0], Slice: parts[1]}, nil
}

func treeEntryFromFile(entry postgres.FileEntry) *corev1.TreeEntry {
	return &corev1.TreeEntry{
		Path:        entry.Path,
		Name:        path.Base(entry.Path),
		Kind:        corev1.EntryKind_ENTRY_KIND_FILE,
		Mode:        entry.Mode,
		BlobId:      entry.BlobID,
		Size:        entry.Size,
		ContentHash: entry.ContentHash,
	}
}

func immediateDirectoryEntries(prefix string, files []postgres.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel := strings.TrimPrefix(file.Path, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{
				Path:   childPath,
				Name:   parts[0],
				Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
				TreeId: directoryTreeID(childPath, files),
			}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func directoryTreeID(prefix string, files []postgres.FileEntry) string {
	entries := immediateDirectoryEntriesWithoutIDs(prefix, files)
	return objectid.TreeID(entries)
}

func immediateDirectoryEntriesWithoutIDs(prefix string, files []postgres.FileEntry) []objectid.TreeEntry {
	uiEntries := immediateDirectoryEntriesNoTree(prefix, files)
	entries := make([]objectid.TreeEntry, 0, len(uiEntries))
	for _, entry := range uiEntries {
		entries = append(entries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind.String(),
			Mode:        entry.Mode,
			BlobID:      entry.BlobId,
			ContentHash: entry.ContentHash,
		})
	}
	return entries
}

func immediateDirectoryEntriesNoTree(prefix string, files []postgres.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel := strings.TrimPrefix(file.Path, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{Path: childPath, Name: parts[0], Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func grpcError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, postgres.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, postgres.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, postgres.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

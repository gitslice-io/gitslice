package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RepositoryService struct {
	Auth        *postgres.AuthStore
	Blobs       *postgres.BlobStore
	Changesets  *postgres.ChangesetStore
	Repository  *postgres.RepositoryStore
	Slices      *postgres.SliceStore
	ObjectStore ObjectStore
	validator   diffValidator
}

func (s *RepositoryService) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	return resolvePath(ctx, s.Repository, req)
}

func resolvePath(ctx context.Context, repository *postgres.RepositoryStore, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	p, err := repositoryReadPath(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if p == "/" {
		entries, err := repository.ListDirectory(ctx, req.CommitId, p)
		if err != nil {
			return nil, grpcError(err)
		}
		if len(entries) == 0 {
			return nil, status.Error(codes.NotFound, "path not found")
		}
		return &corev1.ResolvePathResponse{Entry: &corev1.TreeEntry{
			Path:   p,
			Name:   path.Base(p),
			Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
			TreeId: directoryTreeIDFromEntries(entries),
		}}, nil
	}
	entry, err := repository.GetEntry(ctx, req.CommitId, p)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ResolvePathResponse{Entry: treeEntryFromRepositoryEntry(*entry)}, nil
}

func repositoryReadPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "/", nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must be absolute: %s", p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "/", nil
	}
	return cleaned, nil
}

func (s *RepositoryService) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	p := req.Path
	var err error
	p, err = repositoryReadPath(p)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entries, err := s.Repository.ListDirectory(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, treeEntryFromRepositoryEntry(entry))
	}
	return &corev1.ListDirectoryResponse{Entries: out}, nil
}

func (s *RepositoryService) ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	return readFile(ctx, s.Repository, s.ObjectStore, req)
}

func readFile(ctx context.Context, repository *postgres.RepositoryStore, objectStore ObjectStore, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := repository.GetFile(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	rc, err := objectStore.Get(ctx, filesystem.BlobKey(entry.ContentHash), req.Offset, req.Length)
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

func (s *RepositoryService) GetCommit(ctx context.Context, req *corev1.GetCommitRequest) (*corev1.Commit, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	commit, err := s.Repository.GetCommit(ctx, req.CommitId)
	if err != nil {
		return nil, grpcError(err)
	}
	return commit, nil
}

func (s *RepositoryService) ListCommits(ctx context.Context, req *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	commits, err := s.Repository.ListCommits(ctx, req.RefName, int(req.Limit))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListCommitsResponse{Commits: commits}, nil
}

func (s *RepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	refName := req.RefName
	if refName == "" {
		refName = postgres.DefaultTargetRef
	}
	ref, err := s.Repository.GetRef(ctx, refName)
	if err != nil {
		return nil, grpcError(err)
	}
	return ref, nil
}

func (s *RepositoryService) ImportGitRepository(ctx context.Context, req *corev1.ImportGitRepositoryRequest) (*corev1.ImportGitRepositoryResponse, error) {
	return s.importGitRepository(ctx, req, nil)
}

func (s *RepositoryService) ImportGitRepositoryStream(req *corev1.ImportGitRepositoryRequest, stream corev1.RepositoryService_ImportGitRepositoryStreamServer) error {
	_, err := s.importGitRepository(stream.Context(), req, func(progress *corev1.ImportGitRepositoryProgress) error {
		return stream.Send(progress)
	})
	return err
}

func (s *RepositoryService) importGitRepository(ctx context.Context, req *corev1.ImportGitRepositoryRequest, progress func(*corev1.ImportGitRepositoryProgress) error) (*corev1.ImportGitRepositoryResponse, error) {
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
	slice, err := s.Slices.Resolve(ctx, req.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	mountPath, err := paths.Canonical(req.MountPath)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !paths.InAnyPrefix(slice.Definition.IncludedPaths, mountPath) {
		return nil, status.Errorf(codes.FailedPrecondition, "mount path %s is outside slice %s/%s", mountPath, slice.Ref.Account, slice.Ref.Slice)
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "shallow"
	}
	if mode != "shallow" && mode != "deep" {
		return nil, status.Error(codes.InvalidArgument, "import mode must be shallow or deep")
	}
	if req.MaxCommits < 0 {
		return nil, status.Error(codes.InvalidArgument, "max_commits must be non-negative")
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = postgres.DefaultTargetRef
	}
	source := normalizeGitHubSource(req.Source)
	if source == "" {
		return nil, status.Error(codes.InvalidArgument, "git source is required")
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "cloning",
		Message: "cloning git repository",
	}); err != nil {
		return nil, err
	}
	repoDir, cleanup, err := cloneForImport(ctx, source, mode, int(req.MaxCommits))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "clone git repository: %v", err)
	}
	defer cleanup()
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "listing_commits",
		Message: "listing git commits",
	}); err != nil {
		return nil, err
	}
	gitCommits, err := selectedGitCommits(ctx, repoDir, mode, int(req.MaxCommits))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "list git commits: %v", err)
	}
	if len(gitCommits) == 0 {
		return nil, status.Error(codes.InvalidArgument, "git repository has no commits")
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "listed_commits",
		Message: "listed git commits",
		Total:   int64(len(gitCommits)),
	}); err != nil {
		return nil, err
	}
	previous, err := s.currentMountedSnapshot(ctx, targetRef, mountPath)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &corev1.ImportGitRepositoryResponse{
		Source:    source,
		MountPath: mountPath,
		Mode:      mode,
		TargetRef: targetRef,
	}
	var importID string
	completed := map[string]postgres.GitImportedCommitRecord{}
	if req.Resume {
		record, err := s.Repository.GetOrCreateGitImport(ctx, subjectID, source, mountPath, slice.Ref, slice.Id, targetRef, mode, len(gitCommits))
		if err != nil {
			return nil, grpcError(err)
		}
		importID = record.ID
		rows, err := s.Repository.ListGitImportCommits(ctx, importID)
		if err != nil {
			return nil, grpcError(err)
		}
		for _, row := range rows {
			completed[row.GitCommitID] = row
		}
	}
	var previousGitCommitID string
	for i, gitCommitID := range gitCommits {
		if existing, ok := completed[gitCommitID]; ok {
			response.Commits = append(response.Commits, &corev1.ImportedGitCommit{
				GitCommitId:    gitCommitID,
				NativeCommitId: existing.NativeCommitID,
				Message:        existing.Message,
			})
			response.FinalCommitId = existing.NativeCommitID
			previousGitCommitID = gitCommitID
			if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:            "skipped",
				Message:          existing.Message,
				Current:          int64(i + 1),
				Total:            int64(len(gitCommits)),
				GitCommitId:      gitCommitID,
				NativeCommitId:   existing.NativeCommitID,
				ChangedPathCount: int32(existing.ChangedPathCount),
			}); err != nil {
				return nil, err
			}
			continue
		}
		message, err := gitCommitSubject(ctx, repoDir, gitCommitID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "read git commit message %s: %v", gitCommitID, err)
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:       "reading_commit",
			Message:     message,
			Current:     int64(i + 1),
			Total:       int64(len(gitCommits)),
			GitCommitId: gitCommitID,
		}); err != nil {
			return nil, err
		}
		var edits []*corev1.FileEdit
		var snapshot importSnapshot
		if previousGitCommitID == "" {
			snapshot, err = gitSnapshot(ctx, repoDir, gitCommitID, mountPath)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "read git commit %s: %v", gitCommitID, err)
			}
			edits = diffImportSnapshots(previous, snapshot)
		} else {
			edits, snapshot, err = gitDeltaEdits(ctx, repoDir, previousGitCommitID, gitCommitID, mountPath)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "read git commit %s: %v", gitCommitID, err)
			}
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "uploading_blobs",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		if err := s.uploadImportBlobs(ctx, edits, snapshot); err != nil {
			return nil, grpcError(err)
		}
		ref, err := s.Repository.GetRef(ctx, targetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "submitting",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		patchset, err := s.createImportPatchset(ctx, subjectID, slice, targetRef, ref.CommitId, message, edits)
		if err != nil {
			return nil, err
		}
		if _, err := s.Changesets.Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
			return nil, grpcError(err)
		}
		nativeCommitID, err := s.waitForImportPublished(ctx, patchset.ChangesetId, len(edits))
		if err != nil {
			return nil, grpcError(err)
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "published",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			NativeCommitId:   nativeCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		response.Commits = append(response.Commits, &corev1.ImportedGitCommit{
			GitCommitId:    gitCommitID,
			NativeCommitId: nativeCommitID,
			Message:        message,
		})
		response.FinalCommitId = nativeCommitID
		if importID != "" {
			if err := s.Repository.RecordGitImportCommit(ctx, importID, gitCommitID, nativeCommitID, message, i+1, len(edits)); err != nil {
				return nil, grpcError(err)
			}
		}
		previousGitCommitID = gitCommitID
	}
	if importID != "" && response.FinalCommitId != "" {
		if err := s.Repository.CompleteGitImport(ctx, importID, response.FinalCommitId); err != nil {
			return nil, grpcError(err)
		}
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "done",
		Message: "import complete",
		Current: int64(len(gitCommits)),
		Total:   int64(len(gitCommits)),
		Result:  response,
	}); err != nil {
		return nil, err
	}
	return response, nil
}

func emitImportProgress(progress func(*corev1.ImportGitRepositoryProgress) error, event *corev1.ImportGitRepositoryProgress) error {
	if progress == nil {
		return nil
	}
	return progress(event)
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

func treeEntryFromRepositoryEntry(entry postgres.TreeEntry) *corev1.TreeEntry {
	kind := corev1.EntryKind_ENTRY_KIND_UNSPECIFIED
	switch entry.Kind {
	case "file":
		kind = corev1.EntryKind_ENTRY_KIND_FILE
	case "directory":
		kind = corev1.EntryKind_ENTRY_KIND_DIRECTORY
	case "symlink":
		kind = corev1.EntryKind_ENTRY_KIND_SYMLINK
	}
	return &corev1.TreeEntry{
		Path:        entry.Path,
		Name:        entry.Name,
		Kind:        kind,
		Mode:        entry.Mode,
		TreeId:      entry.TreeID,
		BlobId:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Size:        entry.Size,
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

func directoryTreeIDFromEntries(entries []postgres.TreeEntry) string {
	idEntries := make([]objectid.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		idEntries = append(idEntries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind,
			Mode:        entry.Mode,
			TreeID:      entry.TreeID,
			BlobID:      entry.BlobID,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return objectid.TreeID(idEntries)
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
			Size:        entry.Size,
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

type importFile struct {
	Path        string
	GitPath     string
	GitBlobID   string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
	Data        []byte
}

type importSnapshot map[string]importFile

func normalizeGitHubSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") ||
		strings.HasPrefix(source, "git@") ||
		strings.HasPrefix(source, "/") ||
		strings.HasPrefix(source, ".") {
		return source
	}
	if strings.Count(source, "/") == 1 {
		return "https://github.com/" + strings.TrimSuffix(source, ".git") + ".git"
	}
	return source
}

func cloneForImport(ctx context.Context, source, mode string, maxCommits int) (string, func(), error) {
	parent, err := os.MkdirTemp("", "gitslice-import-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	repoDir := path.Join(parent, "repo")
	args := []string{"clone", "--quiet"}
	if mode == "shallow" {
		args = append(args, "--depth", "1")
	} else if maxCommits > 0 {
		args = append(args, "--depth", strconv.Itoa(maxCommits))
	}
	args = append(args, source, repoDir)
	if err := runGitImport(ctx, "", args...); err != nil {
		cleanup()
		return "", nil, err
	}
	return repoDir, cleanup, nil
}

func selectedGitCommits(ctx context.Context, repoDir, mode string, maxCommits int) ([]string, error) {
	if mode == "shallow" {
		head, err := gitOutputImport(ctx, repoDir, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		return []string{strings.TrimSpace(head)}, nil
	}
	args := []string{"rev-list", "--topo-order"}
	if maxCommits > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", maxCommits))
	}
	args = append(args, "HEAD")
	out, err := gitOutputImport(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

func gitSnapshot(ctx context.Context, repoDir, commitID, mountPath string) (importSnapshot, error) {
	raw, err := gitOutputBytesImport(ctx, repoDir, "ls-tree", "-r", "-z", "--full-tree", commitID)
	if err != nil {
		return nil, err
	}
	return gitSnapshotFromTreeRecords(ctx, repoDir, raw, mountPath)
}

func gitSnapshotFromTreeRecords(ctx context.Context, repoDir string, raw []byte, mountPath string) (importSnapshot, error) {
	snapshot := importSnapshot{}
	blobIDs := make([]string, 0)
	seenBlobIDs := map[string]struct{}{}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, gitPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		fields := strings.Fields(string(meta))
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		gitBlobID := fields[2]
		modeValue, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, err
		}
		pathText := string(gitPath)
		globalPath, err := paths.FromWorkspacePath(mountPath, pathText)
		if err != nil {
			return nil, err
		}
		snapshot[globalPath] = importFile{
			Path:      globalPath,
			GitPath:   pathText,
			GitBlobID: gitBlobID,
			Mode:      uint32(modeValue),
		}
		if _, ok := seenBlobIDs[gitBlobID]; !ok {
			seenBlobIDs[gitBlobID] = struct{}{}
			blobIDs = append(blobIDs, gitBlobID)
		}
	}
	blobs, err := gitBlobContents(ctx, repoDir, blobIDs)
	if err != nil {
		return nil, err
	}
	for path, file := range snapshot {
		data, ok := blobs[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing git blob %s for %s", file.GitBlobID, file.GitPath)
		}
		file.BlobID = objectid.BlobID(data)
		file.ContentHash = objectid.RawContentHash(data)
		file.Size = int64(len(data))
		file.Data = data
		snapshot[path] = file
	}
	return snapshot, nil
}

func gitDeltaEdits(ctx context.Context, repoDir, previousCommitID, commitID, mountPath string) ([]*corev1.FileEdit, importSnapshot, error) {
	raw, err := gitOutputBytesImport(ctx, repoDir, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", previousCommitID, commitID)
	if err != nil {
		return nil, nil, err
	}
	deleted, upsertGitPaths, err := parseGitDiffNameStatus(raw)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := gitFilesAtPaths(ctx, repoDir, commitID, mountPath, upsertGitPaths)
	if err != nil {
		return nil, nil, err
	}
	edits := make([]*corev1.FileEdit, 0, len(deleted)+len(snapshot))
	foundUpserts := make(map[string]struct{}, len(snapshot))
	for _, file := range snapshot {
		foundUpserts[file.GitPath] = struct{}{}
	}
	for _, gitPath := range deleted {
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, gitPath := range upsertGitPaths {
		if _, ok := foundUpserts[gitPath]; ok {
			continue
		}
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, file := range snapshot {
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.Path,
			BlobId:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Path == edits[j].Path {
			return edits[i].Op < edits[j].Op
		}
		return edits[i].Path < edits[j].Path
	})
	return edits, snapshot, nil
}

func parseGitDiffNameStatus(raw []byte) ([]string, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	tokens := bytes.Split(raw, []byte{0})
	deletedSet := map[string]struct{}{}
	upsertSet := map[string]struct{}{}
	for i := 0; i < len(tokens); {
		if len(tokens[i]) == 0 {
			i++
			continue
		}
		statusValue := string(tokens[i])
		i++
		statusKind := statusValue[0]
		switch statusKind {
		case 'A', 'C', 'M', 'T':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			upsertSet[pathText] = struct{}{}
		case 'D':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			deletedSet[pathText] = struct{}{}
		case 'R':
			if i+1 >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git rename status %q", statusValue)
			}
			oldPath := string(tokens[i])
			newPath := string(tokens[i+1])
			i += 2
			deletedSet[oldPath] = struct{}{}
			upsertSet[newPath] = struct{}{}
		default:
			return nil, nil, fmt.Errorf("unsupported git diff status %q", statusValue)
		}
	}
	deleted := make([]string, 0, len(deletedSet))
	for p := range deletedSet {
		if _, upserted := upsertSet[p]; upserted {
			continue
		}
		deleted = append(deleted, p)
	}
	upserts := make([]string, 0, len(upsertSet))
	for p := range upsertSet {
		upserts = append(upserts, p)
	}
	sort.Strings(deleted)
	sort.Strings(upserts)
	return deleted, upserts, nil
}

func gitFilesAtPaths(ctx context.Context, repoDir, commitID, mountPath string, gitPaths []string) (importSnapshot, error) {
	snapshot := importSnapshot{}
	if len(gitPaths) == 0 {
		return snapshot, nil
	}
	const chunkSize = 512
	for start := 0; start < len(gitPaths); start += chunkSize {
		end := start + chunkSize
		if end > len(gitPaths) {
			end = len(gitPaths)
		}
		args := []string{"ls-tree", "-r", "-z", "--full-tree", commitID, "--"}
		for _, p := range gitPaths[start:end] {
			args = append(args, ":(literal)"+p)
		}
		raw, err := gitOutputBytesImport(ctx, repoDir, args...)
		if err != nil {
			return nil, err
		}
		chunk, err := gitSnapshotFromTreeRecords(ctx, repoDir, raw, mountPath)
		if err != nil {
			return nil, err
		}
		for p, file := range chunk {
			snapshot[p] = file
		}
	}
	return snapshot, nil
}

func gitBlobContents(ctx context.Context, repoDir string, blobIDs []string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(blobIDs))
	if len(blobIDs) == 0 {
		return contents, nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repoDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	writeErrCh := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, blobID := range blobIDs {
			if _, err := fmt.Fprintln(w, blobID); err != nil {
				_ = stdin.Close()
				writeErrCh <- err
				return
			}
		}
		if err := w.Flush(); err != nil {
			_ = stdin.Close()
			writeErrCh <- err
			return
		}
		writeErrCh <- stdin.Close()
	}()
	reader := bufio.NewReader(stdout)
	for range blobIDs {
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file header %q", strings.TrimSpace(header))
		}
		if fields[1] != "blob" {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("git object %s is %s, want blob", fields[0], fields[1])
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("invalid git blob size %q", fields[2])
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		trailing, err := reader.ReadByte()
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		if trailing != '\n' {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file payload for %s", fields[0])
		}
		contents[fields[0]] = data
	}
	if err := <-writeErrCh; err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git cat-file --batch: %s", msg)
	}
	return contents, nil
}

func diffImportSnapshots(previous, current importSnapshot) []*corev1.FileEdit {
	seen := map[string]struct{}{}
	for p := range previous {
		seen[p] = struct{}{}
	}
	for p := range current {
		seen[p] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	edits := make([]*corev1.FileEdit, 0, len(paths))
	for _, p := range paths {
		before, hadBefore := previous[p]
		after, hasAfter := current[p]
		if !hasAfter {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
			continue
		}
		if !hadBefore || before.ContentHash != after.ContentHash || before.Mode != after.Mode {
			edits = append(edits, &corev1.FileEdit{
				Op:          "upsert",
				Path:        p,
				BlobId:      after.BlobID,
				ContentHash: after.ContentHash,
				Mode:        after.Mode,
			})
		}
	}
	return edits
}

func (s *RepositoryService) currentMountedSnapshot(ctx context.Context, targetRef, mountPath string) (importSnapshot, error) {
	ref, err := s.Repository.GetRef(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	files, err := s.Repository.ListFiles(ctx, ref.CommitId, mountPath)
	if err != nil {
		return nil, err
	}
	snapshot := importSnapshot{}
	for _, file := range files {
		snapshot[file.Path] = importFile{
			Path:        file.Path,
			BlobID:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
			Size:        file.Size,
		}
	}
	return snapshot, nil
}

func (s *RepositoryService) uploadImportBlobs(ctx context.Context, edits []*corev1.FileEdit, snapshot importSnapshot) error {
	for _, edit := range edits {
		if edit.Op == "delete" {
			continue
		}
		file, ok := snapshot[edit.Path]
		if !ok {
			return fmt.Errorf("missing import file for %s", edit.Path)
		}
		key := filesystem.BlobKey(file.ContentHash)
		if err := s.ObjectStore.Put(ctx, key, bytes.NewReader(file.Data)); err != nil {
			return err
		}
		if err := s.Blobs.Upsert(ctx, file.BlobID, file.ContentHash, file.Size, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *RepositoryService) createImportPatchset(ctx context.Context, subjectID string, slice *corev1.Slice, targetRef, baseCommitID, message string, edits []*corev1.FileEdit) (*corev1.Patchset, error) {
	validation, err := s.validator.validateFileEdits(ctx, slice, baseCommitID, edits, true)
	if err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Create(ctx, subjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Title:          message,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	patchset, err := s.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId:       baseCommitID,
		Author:             subjectID,
		ChangedPaths:       validation.AffectedPaths,
		FileEdits:          edits,
		Coverage:           validation.Coverage,
		SubmitRequirements: validation.SubmitRequirements,
		PathBases:          validation.PathBases,
		ReadSet:            validation.ReadSet,
		WriteSet:           validation.WriteSet,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return patchset, nil
}

func (s *RepositoryService) waitForImportPublished(ctx context.Context, changesetID string, changedFileCount int) (string, error) {
	deadline := time.Now().Add(importPublishTimeout(changedFileCount))
	for {
		cs, err := s.Changesets.Get(ctx, changesetID)
		if err != nil {
			return "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, nil
		}
		if _, err := s.Changesets.PublishPending(ctx, 128); err != nil && !errors.Is(err, postgres.ErrConflict) {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for imported changeset %s to publish", changesetID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func importPublishTimeout(changedFileCount int) time.Duration {
	timeout := 30 * time.Second
	if changedFileCount > 0 {
		timeout += time.Duration(changedFileCount/250) * time.Second
	}
	if timeout > 30*time.Minute {
		return 30 * time.Minute
	}
	return timeout
}

func gitCommitSubject(ctx context.Context, repoDir, commitID string) (string, error) {
	out, err := gitOutputImport(ctx, repoDir, "log", "-1", "--format=%s", commitID)
	if err != nil {
		return "", err
	}
	message := strings.TrimSpace(out)
	if message == "" {
		message = "Import git commit " + shortCommit(commitID)
	}
	return message, nil
}

func shortCommit(commitID string) string {
	if len(commitID) <= 12 {
		return commitID
	}
	return commitID[:12]
}

func gitOutputImport(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutputBytesImport(ctx, dir, args...)
	return string(out), err
}

func gitOutputBytesImport(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

func runGitImport(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutputBytesImport(ctx, dir, args...)
	return err
}

package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type Stores struct {
	Auth       *AuthStore
	Blobs      *BlobStore
	Changesets *ChangesetStore
	Repository *RepositoryStore
	Slices     *SliceStore
	Objects    *ObjectStore

	backend *backend
}

type backend struct {
	mu   sync.Mutex
	next int64

	subjects       map[string]storage.Subject
	accountMembers map[string]map[string]struct{}
	sessions       map[string]string

	blobs   map[string]*corev1.BlobRecord
	objects map[string][]byte

	refs        map[string]*corev1.Ref
	commits     map[string]*corev1.Commit
	commitFiles map[string]map[string]storage.FileEntry

	slices    map[string]*corev1.Slice
	sliceRefs map[string]string

	changesets map[string]*corev1.Changeset

	imports         map[string]*storage.GitImportRecord
	importsByKey    map[string]string
	importedCommits map[string][]storage.GitImportedCommitRecord

	entitiesByPath map[string]storage.CurrentPathEntity
	entityChanges  map[string][]storage.HistoryEntityRef
}

type AuthStore struct{ b *backend }
type BlobStore struct{ b *backend }
type ChangesetStore struct{ b *backend }
type RepositoryStore struct{ b *backend }
type SliceStore struct{ b *backend }
type ObjectStore struct{ b *backend }

func New() *Stores {
	b := &backend{
		subjects:        map[string]storage.Subject{},
		accountMembers:  map[string]map[string]struct{}{},
		sessions:        map[string]string{},
		blobs:           map[string]*corev1.BlobRecord{},
		objects:         map[string][]byte{},
		refs:            map[string]*corev1.Ref{},
		commits:         map[string]*corev1.Commit{},
		commitFiles:     map[string]map[string]storage.FileEntry{},
		slices:          map[string]*corev1.Slice{},
		sliceRefs:       map[string]string{},
		changesets:      map[string]*corev1.Changeset{},
		imports:         map[string]*storage.GitImportRecord{},
		importsByKey:    map[string]string{},
		importedCommits: map[string][]storage.GitImportedCommitRecord{},
		entitiesByPath:  map[string]storage.CurrentPathEntity{},
		entityChanges:   map[string][]storage.HistoryEntityRef{},
	}
	root := &corev1.Commit{
		Id:         "mem_root",
		RootTreeId: "mem_tree_root",
		Message:    "root",
		CreatedAt:  time.Unix(0, 0).UTC().Format(time.RFC3339Nano),
	}
	b.commits[root.Id] = cloneCommit(root)
	b.commitFiles[root.Id] = map[string]storage.FileEntry{}
	b.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: root.Id, UpdatedAt: root.CreatedAt}
	return &Stores{
		Auth:       &AuthStore{b: b},
		Blobs:      &BlobStore{b: b},
		Changesets: &ChangesetStore{b: b},
		Repository: &RepositoryStore{b: b},
		Slices:     &SliceStore{b: b},
		Objects:    &ObjectStore{b: b},
		backend:    b,
	}
}

func (s *Stores) AddAccount(subjectID, accountSlug string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.addAccountLocked(subjectID, accountSlug)
}

func (s *Stores) PutSlice(ref *corev1.SliceRef, includedPaths []string, visibility string) *corev1.Slice {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	slice, _ := s.backend.putSliceLocked(ref, includedPaths, visibility)
	return slice
}

func (s *Stores) PutCommitWithFiles(commitID string, files []storage.FileEntry, changedPaths []string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.putCommitWithFilesLocked(commitID, files, changedPaths, "memory commit")
}

func (s *Stores) PutObject(key string, data []byte) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.objects[key] = append([]byte(nil), data...)
}

func (b *backend) addAccountLocked(subjectID, accountSlug string) {
	subjectID = strings.TrimSpace(subjectID)
	accountSlug = strings.TrimSpace(accountSlug)
	if subjectID == "" || accountSlug == "" {
		return
	}
	b.subjects[subjectID] = storage.Subject{ID: subjectID, DisplayName: strings.TrimPrefix(subjectID, "user_")}
	if b.accountMembers[subjectID] == nil {
		b.accountMembers[subjectID] = map[string]struct{}{}
	}
	b.accountMembers[subjectID][accountSlug] = struct{}{}
	home := &corev1.SliceRef{Account: accountSlug, Slice: "home"}
	if _, ok := b.sliceRefs[sliceRefKey(home)]; !ok {
		_, _ = b.putSliceLocked(home, []string{"/" + accountSlug}, "account")
	}
}

func (b *backend) putSliceLocked(ref *corev1.SliceRef, includedPaths []string, visibility string) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	includedPaths, visibility, err = validateSliceDefinition(ref, includedPaths, visibility)
	if err != nil {
		return nil, err
	}
	id := sliceID(ref.Account, ref.Slice)
	version := int64(1)
	if existing := b.slices[id]; existing != nil && existing.Definition != nil {
		version = existing.Definition.Version + 1
	}
	definitionHash := fmt.Sprintf("mem_slice_def_%s_%d", id, version)
	slice := &corev1.Slice{
		Id:             id,
		Ref:            cloneSliceRef(ref),
		DefinitionHash: definitionHash,
		Definition: &corev1.SliceDefinition{
			SliceId:       id,
			Version:       version,
			IncludedPaths: append([]string(nil), includedPaths...),
			Visibility:    visibility,
		},
	}
	b.slices[id] = cloneSlice(slice)
	b.sliceRefs[sliceRefKey(ref)] = id
	return cloneSlice(slice), nil
}

func (b *backend) putCommitWithFilesLocked(commitID string, files []storage.FileEntry, changedPaths []string, message string) {
	if commitID == "" {
		commitID = b.nextIDLocked("commit")
	}
	parent := ""
	if ref := b.refs[storage.DefaultTargetRef]; ref != nil {
		parent = ref.CommitId
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	commit := &corev1.Commit{
		Id:           commitID,
		RootTreeId:   "mem_tree_" + commitID,
		Message:      message,
		CreatedAt:    createdAt,
		ChangedPaths: append([]string(nil), changedPaths...),
	}
	if parent != "" {
		commit.ParentIds = []string{parent}
	}
	byPath := map[string]storage.FileEntry{}
	for _, file := range files {
		byPath[file.Path] = file
		b.entitiesByPath[file.Path] = storage.CurrentPathEntity{
			Path:        file.Path,
			AccountID:   accountFromPath(file.Path),
			EntityID:    "ent_" + strings.ReplaceAll(strings.Trim(file.Path, "/"), "/", "_"),
			Kind:        "file",
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		}
	}
	b.commits[commitID] = cloneCommit(commit)
	b.commitFiles[commitID] = byPath
	b.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: commitID, UpdatedAt: createdAt, UpdatedBy: "memory"}
}

func (b *backend) nextIDLocked(prefix string) string {
	b.next++
	return fmt.Sprintf("%s_%d", prefix, b.next)
}

func (s *ObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *ObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	data, ok := s.b.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset < 0 || length < 0 {
		return nil, storage.ErrInvalid
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data[offset:end]...))), nil
}

func (s *ObjectStore) Delete(ctx context.Context, key string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.objects, key)
	return nil
}

func (s *AuthStore) LoginDevUser(ctx context.Context, devUser string) (string, string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID := normalizeDevSubject(devUser)
	if _, ok := s.b.subjects[subjectID]; !ok {
		return "", "", storage.ErrNotFound
	}
	token := s.b.nextIDLocked("tok")
	s.b.sessions[token] = subjectID
	return token, subjectID, nil
}

func (s *AuthStore) SignupUser(ctx context.Context, username string) (string, string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	username = normalizeSlug(username)
	if username == "" {
		return "", "", storage.ErrInvalid
	}
	subjectID := "user_" + strings.ReplaceAll(username, "-", "_")
	s.b.addAccountLocked(subjectID, username)
	token := s.b.nextIDLocked("tok")
	s.b.sessions[token] = subjectID
	return token, subjectID, nil
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*storage.Subject, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID := s.b.sessions[strings.TrimSpace(token)]
	if subjectID == "" {
		return nil, storage.ErrUnauthenticated
	}
	subject := s.b.subjects[subjectID]
	return &subject, nil
}

func (s *AuthStore) EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.accountMembers[subjectID][accountSlug]; !ok {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *AuthStore) ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	memberships := s.b.accountMembers[subjectID]
	if len(memberships) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(memberships))
	for slug := range memberships {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out, nil
}

func (s *BlobStore) Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.blobs[blobID] = &corev1.BlobRecord{Id: blobID, ContentHash: contentHash, Size: size, StorageLocation: storageLocation, State: "available"}
	return nil
}

func (s *BlobStore) GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	blob := s.b.blobs[blobID]
	if blob == nil {
		return nil, storage.ErrNotFound
	}
	return cloneBlob(blob), nil
}

func (s *BlobStore) GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]struct{}{}
	for _, hash := range hashes {
		wanted[hash] = struct{}{}
	}
	var out []*corev1.BlobRecord
	for _, blob := range s.b.blobs {
		if _, ok := wanted[blob.ContentHash]; ok {
			out = append(out, cloneBlob(blob))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentHash < out[j].ContentHash })
	return out, nil
}

func (s *ChangesetStore) Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if req.AuthoringSlice == nil {
		return nil, storage.ErrInvalid
	}
	sliceID := s.b.sliceRefs[sliceRefKey(req.AuthoringSlice)]
	if sliceID == "" {
		return nil, storage.ErrNotFound
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref := s.b.refs[targetRef]
		if ref == nil {
			return nil, storage.ErrNotFound
		}
		baseCommitID = ref.CommitId
	}
	id := s.b.nextIDLocked("cs")
	cs := &corev1.Changeset{
		Id:             id,
		AuthoringSlice: cloneSliceRef(req.AuthoringSlice),
		Author:         subjectID,
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Status:         "open",
		Title:          req.Title,
	}
	s.b.changesets[id] = cloneChangeset(cs)
	return cloneChangeset(cs), nil
}

func (s *ChangesetStore) Get(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	return cloneChangeset(cs), nil
}

func (s *ChangesetStore) List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.Changeset
	for _, cs := range s.b.changesets {
		if req.AuthoringSlice != nil && !sameSliceRef(cs.AuthoringSlice, req.AuthoringSlice) {
			continue
		}
		if req.Status != "" && cs.Status != req.Status {
			continue
		}
		out = append(out, cloneChangeset(cs))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	next := clonePatchset(patchset)
	next.Id = s.b.nextIDLocked("ps")
	next.ChangesetId = changesetID
	next.Number = int64(len(cs.Patchsets) + 1)
	cs.Patchsets = append(cs.Patchsets, next)
	cs.CurrentPatchsetId = next.Id
	return clonePatchset(next), nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	cs.Status = "pending_publish"
	return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
}

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 128
	}
	published := 0
	for _, cs := range sortedChangesets(s.b.changesets) {
		if published >= limit {
			break
		}
		if cs.Status != "pending_publish" {
			continue
		}
		patchset := currentPatchset(cs)
		if patchset == nil {
			continue
		}
		ref := s.b.refs[cs.TargetRef]
		if ref == nil {
			return published, storage.ErrNotFound
		}
		baseFiles := cloneFileMap(s.b.commitFiles[ref.CommitId])
		for _, edit := range patchset.FileEdits {
			switch edit.Op {
			case "delete":
				delete(baseFiles, edit.Path)
			case "rename":
				file, ok := baseFiles[edit.OldPath]
				if ok {
					delete(baseFiles, edit.OldPath)
					file.Path = edit.Path
					baseFiles[edit.Path] = file
				}
			case "mkdir":
			default:
				blob := s.b.blobs[edit.BlobId]
				if blob == nil && edit.ContentHash == "" {
					return published, storage.ErrNotFound
				}
				contentHash := edit.ContentHash
				size := int64(0)
				if blob != nil {
					contentHash = blob.ContentHash
					size = blob.Size
				}
				baseFiles[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
			}
		}
		files := make([]storage.FileEntry, 0, len(baseFiles))
		for _, file := range baseFiles {
			files = append(files, file)
		}
		commitID := s.b.nextIDLocked("commit")
		s.b.putCommitWithFilesLocked(commitID, files, patchset.ChangedPaths, cs.Title)
		cs.Status = "submitted"
		cs.CommitId = commitID
		published++
	}
	return published, nil
}

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return storage.ErrConflict
	}
	cs.Status = "abandoned"
	return nil
}

func (s *RepositoryStore) GetRef(ctx context.Context, name string) (*corev1.Ref, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if name == "" {
		name = storage.DefaultTargetRef
	}
	ref := s.b.refs[name]
	if ref == nil {
		return nil, storage.ErrNotFound
	}
	return cloneRef(ref), nil
}

func (s *RepositoryStore) GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*storage.GitImportRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	key := gitImportKey(source, mountPath, sliceID, targetRef, mode)
	if id := s.b.importsByKey[key]; id != "" {
		record := *s.b.imports[id]
		record.Status = "running"
		record.TotalCommits = totalCommits
		s.b.imports[id] = &record
		return cloneGitImport(&record), nil
	}
	record := &storage.GitImportRecord{
		ID:               s.b.nextIDLocked("gimp"),
		SubjectID:        subjectID,
		Source:           source,
		MountPath:        mountPath,
		AuthoringAccount: sliceRef.Account,
		AuthoringSlice:   sliceRef.Slice,
		AuthoringSliceID: sliceID,
		TargetRef:        targetRef,
		Mode:             mode,
		Status:           "running",
		TotalCommits:     totalCommits,
	}
	s.b.imports[record.ID] = cloneGitImport(record)
	s.b.importsByKey[key] = record.ID
	return cloneGitImport(record), nil
}

func (s *RepositoryStore) GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*storage.GitImportRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.importsByKey[gitImportKey(source, mountPath, sliceID, targetRef, mode)]
	if id == "" {
		return nil, storage.ErrNotFound
	}
	return cloneGitImport(s.b.imports[id]), nil
}

func (s *RepositoryStore) ListGitImportCommits(ctx context.Context, importID string) ([]storage.GitImportedCommitRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return append([]storage.GitImportedCommitRecord(nil), s.b.importedCommits[importID]...), nil
}

func (s *RepositoryStore) RecordGitImportCommit(ctx context.Context, importID, gitCommitID, nativeCommitID, message string, position, changedPathCount int) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.imports[importID] == nil {
		return storage.ErrNotFound
	}
	s.b.importedCommits[importID] = append(s.b.importedCommits[importID], storage.GitImportedCommitRecord{
		ImportID: importID, GitCommitID: gitCommitID, NativeCommitID: nativeCommitID,
		Message: message, Position: position, ChangedPathCount: changedPathCount,
	})
	return nil
}

func (s *RepositoryStore) CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	record := s.b.imports[importID]
	if record == nil {
		return storage.ErrNotFound
	}
	record.Status = "completed"
	record.FinalNativeCommitID = finalNativeCommitID
	record.ImportedCount = len(s.b.importedCommits[importID])
	return nil
}

func (s *RepositoryStore) GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	commit := s.b.commits[commitID]
	if commit == nil {
		return nil, storage.ErrNotFound
	}
	return cloneCommit(commit), nil
}

func (s *RepositoryStore) ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPage(ctx, refName, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool { return commit.Id != "mem_root" }), nil
}

func (s *RepositoryStore) ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool {
		for _, changed := range commit.ChangedPaths {
			for _, prefix := range prefixes {
				if pathContains(prefix, changed) {
					return true
				}
			}
		}
		return false
	}), nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefs(ctx context.Context, refName string, refs []storage.HistoryEntityRef, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	refSet := map[string]struct{}{}
	for _, ref := range refs {
		refSet[ref.AccountID+"\x00"+ref.EntityID] = struct{}{}
	}
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool {
		for _, changed := range commit.ChangedPaths {
			for _, ref := range s.b.entityChanges[changed] {
				if _, ok := refSet[ref.AccountID+"\x00"+ref.EntityID]; ok {
					return true
				}
			}
		}
		return false
	}), nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, refs []storage.HistoryEntityRef, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	entityPage, err := s.ListCommitPageByEntityRefs(ctx, refName, refs, 0, "")
	if err != nil {
		return nil, err
	}
	pathPage, err := s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, 0, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]*corev1.Commit{}
	for _, commit := range entityPage.Commits {
		seen[commit.Id] = commit
	}
	for _, commit := range pathPage.Commits {
		seen[commit.Id] = commit
	}
	var commits []*corev1.Commit
	for _, commit := range seen {
		commits = append(commits, commit)
	}
	sortCommits(commits)
	if limit <= 0 {
		limit = 50
	}
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return &storage.CommitListPage{Commits: commits}, nil
}

func (s *RepositoryStore) CurrentPathEntitiesByPrefixes(ctx context.Context, refName string, prefixes []string) ([]storage.CurrentPathEntity, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.CurrentPathEntity
	for _, entity := range s.b.entitiesByPath {
		for _, prefix := range prefixes {
			if pathContains(prefix, entity.Path) {
				out = append(out, entity)
				break
			}
		}
	}
	return out, nil
}

func (s *RepositoryStore) CurrentPathEntitiesByPaths(ctx context.Context, refName string, in []string) ([]storage.CurrentPathEntity, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.CurrentPathEntity
	for _, p := range in {
		if entity, ok := s.b.entitiesByPath[p]; ok {
			out = append(out, entity)
		}
	}
	return out, nil
}

func (s *RepositoryStore) GetFile(ctx context.Context, commitID, p string) (*storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	file, ok := s.b.commitFiles[commitID][p]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneFile(file), nil
}

func (s *RepositoryStore) GetEntry(ctx context.Context, commitID, p string) (*storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	if file, ok := files[p]; ok {
		return &storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}, nil
	}
	if p == "/" || hasDescendant(files, p) {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	return nil, storage.ErrNotFound
}

func (s *RepositoryStore) ListDirectory(ctx context.Context, commitID, p string) ([]storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	entries := directoryEntries(p, files)
	if len(entries) == 0 && p != "/" && !hasDescendant(files, p) {
		return nil, storage.ErrNotFound
	}
	return entries, nil
}

func (s *RepositoryStore) ListFiles(ctx context.Context, commitID, prefix string) ([]storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	var out []storage.FileEntry
	for _, file := range files {
		if pathContains(prefix, file.Path) {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *SliceStore) Create(ctx context.Context, ref *corev1.SliceRef, includedPaths []string, visibility string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.sliceRefs[sliceRefKey(ref)]; ok {
		return nil, storage.ErrConflict
	}
	return s.b.putSliceLocked(ref, includedPaths, visibility)
}

func (s *SliceStore) ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string) ([]string, string, error) {
	return validateSliceDefinition(ref, includedPaths, visibility)
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.sliceRefs[sliceRefKey(ref)]
	if id == "" {
		return nil, storage.ErrNotFound
	}
	return cloneSlice(s.b.slices[id]), nil
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	return cloneSlice(slice), nil
}

func (s *SliceStore) List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	var out []*corev1.Slice
	for _, slice := range s.b.slices {
		if slice.Ref.Account == account {
			out = append(out, cloneSlice(slice))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Slice < out[j].Ref.Slice })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SliceStore) UpdateDefinition(ctx context.Context, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.slices[sliceID]
	if current == nil {
		return nil, storage.ErrNotFound
	}
	if expectedHash != "" && current.DefinitionHash != expectedHash {
		return nil, storage.ErrConflict
	}
	included, visibility, err := validateSliceDefinition(current.Ref, definition.IncludedPaths, definition.Visibility)
	if err != nil {
		return nil, err
	}
	current.Definition.Version++
	current.Definition.IncludedPaths = included
	current.Definition.Visibility = visibility
	current.DefinitionHash = fmt.Sprintf("mem_slice_def_%s_%d", sliceID, current.Definition.Version)
	return cloneSlice(current).Definition, nil
}

func (s *SliceStore) Delete(ctx context.Context, sliceID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return storage.ErrNotFound
	}
	delete(s.b.sliceRefs, sliceRefKey(slice.Ref))
	delete(s.b.slices, sliceID)
	return nil
}

func (s *SliceStore) CoveringIDs(ctx context.Context, p string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var ids []string
	for _, slice := range s.b.slices {
		for _, prefix := range slice.Definition.IncludedPaths {
			if pathContains(prefix, p) {
				ids = append(ids, slice.Id)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (b *backend) listCommitPageLocked(limit int, include func(*corev1.Commit) bool) *storage.CommitListPage {
	if limit <= 0 {
		limit = 50
	}
	var commits []*corev1.Commit
	for _, commit := range b.commits {
		if include(commit) {
			commits = append(commits, cloneCommit(commit))
		}
	}
	sortCommits(commits)
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return &storage.CommitListPage{Commits: commits}
}

func validateSliceDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string) ([]string, string, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, "", err
	}
	if visibility == "" {
		visibility = "account"
	}
	if visibility != "account" && visibility != "public" {
		return nil, "", storage.ErrInvalid
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(includedPaths))
	for _, raw := range includedPaths {
		cleaned, err := canonicalIncludedPath(ref, raw)
		if err != nil {
			return nil, "", err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	if len(out) == 0 {
		return nil, "", storage.ErrInvalid
	}
	return out, visibility, nil
}

func canonicalIncludedPath(ref *corev1.SliceRef, raw string) (string, error) {
	cleaned, err := paths.Canonical(raw)
	if err != nil {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if !strings.HasPrefix(value, "/") {
			value = "/" + value
		}
		value = path.Clean(value)
		if ref.Slice != "home" || strings.Count(strings.Trim(value, "/"), "/") != 0 {
			return "", storage.ErrInvalid
		}
		cleaned = value
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) == 0 || segments[0] != ref.Account {
		return "", storage.ErrInvalid
	}
	if len(segments) == 1 && ref.Slice != "home" {
		return "", storage.ErrInvalid
	}
	return cleaned, nil
}

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, storage.ErrInvalid
	}
	account := normalizeSlug(ref.Account)
	slice := normalizeSlug(ref.Slice)
	if account == "" || slice == "" {
		return nil, storage.ErrInvalid
	}
	return &corev1.SliceRef{Account: account, Slice: slice}, nil
}

func normalizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" || len(value) > 63 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}
	return value
}

func normalizeDevSubject(devUser string) string {
	devUser = strings.TrimSpace(devUser)
	if devUser == "" {
		devUser = "alice"
	}
	devUser = strings.ReplaceAll(devUser, "-", "_")
	if strings.HasPrefix(devUser, "user_") || strings.HasSuffix(devUser, "_bot") {
		return devUser
	}
	return "user_" + devUser
}

func sliceID(account, slice string) string { return "slice_" + account + "_" + slice }

func sliceRefKey(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return normalizeSlug(ref.Account) + "/" + normalizeSlug(ref.Slice)
}

func sameSliceRef(a, b *corev1.SliceRef) bool { return sliceRefKey(a) == sliceRefKey(b) }

func accountFromPath(p string) string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return ""
	}
	return strings.Split(trimmed, "/")[0]
}

func gitImportKey(source, mountPath, sliceID, targetRef, mode string) string {
	return source + "\x00" + mountPath + "\x00" + sliceID + "\x00" + targetRef + "\x00" + mode
}

func pathContains(prefix, p string) bool {
	if prefix == "/" {
		return strings.HasPrefix(p, "/")
	}
	prefix = strings.TrimRight(prefix, "/")
	p = strings.TrimRight(p, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func hasDescendant(files map[string]storage.FileEntry, prefix string) bool {
	for p := range files {
		if pathContains(prefix, p) {
			return true
		}
	}
	return false
}

func directoryEntries(prefix string, files map[string]storage.FileEntry) []storage.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]storage.TreeEntry{}
	for _, file := range files {
		rel, ok := relativeDirectoryPath(prefix, file.Path)
		if !ok {
			continue
		}
		if rel == "" {
			byPath[file.Path] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
			continue
		}
		parts := strings.Split(rel, "/")
		child := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			child = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[child] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
		} else if _, ok := byPath[child]; !ok {
			byPath[child] = storage.TreeEntry{Path: child, Name: parts[0], Kind: "directory", TreeID: "mem_tree_" + strings.Trim(child, "/")}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]storage.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func relativeDirectoryPath(prefix, filePath string) (string, bool) {
	if prefix == "/" {
		if !strings.HasPrefix(filePath, "/") {
			return "", false
		}
		return strings.TrimPrefix(filePath, "/"), true
	}
	if filePath == prefix {
		return "", true
	}
	if !strings.HasPrefix(filePath, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(filePath, prefix+"/"), true
}

func sortedChangesets(changesets map[string]*corev1.Changeset) []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(changesets))
	for _, cs := range changesets {
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
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

func cloneFileMap(in map[string]storage.FileEntry) map[string]storage.FileEntry {
	out := map[string]storage.FileEntry{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneFile(file storage.FileEntry) *storage.FileEntry {
	out := file
	return &out
}

func cloneBlob(in *corev1.BlobRecord) *corev1.BlobRecord {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneRef(in *corev1.Ref) *corev1.Ref {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneCommit(in *corev1.Commit) *corev1.Commit {
	if in == nil {
		return nil
	}
	out := *in
	out.ParentIds = append([]string(nil), in.ParentIds...)
	out.ChangedPaths = append([]string(nil), in.ChangedPaths...)
	return &out
}

func cloneSliceRef(in *corev1.SliceRef) *corev1.SliceRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneSlice(in *corev1.Slice) *corev1.Slice {
	if in == nil {
		return nil
	}
	out := *in
	out.Ref = cloneSliceRef(in.Ref)
	if in.Definition != nil {
		def := *in.Definition
		def.IncludedPaths = append([]string(nil), in.Definition.IncludedPaths...)
		out.Definition = &def
	}
	return &out
}

func clonePatchset(in *corev1.Patchset) *corev1.Patchset {
	if in == nil {
		return nil
	}
	out := *in
	out.ChangedPaths = append([]string(nil), in.ChangedPaths...)
	out.FileEdits = append([]*corev1.FileEdit(nil), in.FileEdits...)
	out.Coverage = append([]*corev1.PathCoverage(nil), in.Coverage...)
	out.PathBases = append([]*corev1.PathBase(nil), in.PathBases...)
	out.ReadSet = append([]*corev1.PathSetEntry(nil), in.ReadSet...)
	out.WriteSet = append([]*corev1.PathSetEntry(nil), in.WriteSet...)
	return &out
}

func cloneChangeset(in *corev1.Changeset) *corev1.Changeset {
	if in == nil {
		return nil
	}
	out := *in
	out.AuthoringSlice = cloneSliceRef(in.AuthoringSlice)
	out.Patchsets = make([]*corev1.Patchset, 0, len(in.Patchsets))
	for _, patchset := range in.Patchsets {
		out.Patchsets = append(out.Patchsets, clonePatchset(patchset))
	}
	return &out
}

func cloneGitImport(in *storage.GitImportRecord) *storage.GitImportRecord {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func sortCommits(commits []*corev1.Commit) {
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].CreatedAt == commits[j].CreatedAt {
			return commits[i].Id > commits[j].Id
		}
		return commits[i].CreatedAt > commits[j].CreatedAt
	})
}

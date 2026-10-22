package memory

import (
	"context"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

func (s *RepositoryStore) RootTreeForCommit(ctx context.Context, commitID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	commit := s.b.commits[commitID]
	if commit == nil {
		return "", storage.ErrNotFound
	}
	return commit.RootTreeId, nil
}

func (s *RepositoryStore) GetFileAtTree(ctx context.Context, rootTreeID, p string) (*storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, _, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	file, ok := files[p]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneFile(file), nil
}

func (s *RepositoryStore) GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, dirs, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	return treeEntryFromMemorySnapshot(files, dirs, p)
}

func (s *RepositoryStore) ListTreeAtTree(ctx context.Context, rootTreeID, p string) ([]storage.TreeEntry, error) {
	s.b.mu.Lock()
	files, dirs, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	s.b.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	var out []storage.TreeEntry
	var walk func(dir string)
	walk = func(dir string) {
		for _, entry := range directoryEntries(dir, files, dirs) {
			out = append(out, entry)
			if entry.Kind == "directory" {
				walk(entry.Path)
			}
		}
	}
	walk(p)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *RepositoryStore) ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, dirs, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	entries := directoryEntries(p, files, dirs)
	if len(entries) == 0 && p != "/" && !hasDescendant(files, p) && !hasDescendantDir(dirs, p) {
		if _, ok := dirs[p]; ok {
			return nil, nil
		}
		return nil, storage.ErrNotFound
	}
	return entries, nil
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

func (s *RepositoryStore) GetCommits(ctx context.Context, commitIDs []string) (map[string]*corev1.Commit, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := make(map[string]*corev1.Commit, len(commitIDs))
	for _, id := range commitIDs {
		if commit := s.b.commits[id]; commit != nil {
			out[id] = cloneCommit(commit)
		}
	}
	return out, nil
}

func (s *RepositoryStore) ResolveCommitCandidates(ctx context.Context, filter storage.CommitResolveFilter) ([]*corev1.Commit, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	idPrefix := strings.TrimSpace(filter.IDPrefix)
	if idPrefix == "" {
		return nil, storage.ErrInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 20 {
		limit = 2
	}
	prefixes := normalizePathPrefixes(filter.PathPrefixes)
	refSet := map[string]struct{}{}
	for _, ref := range filter.EntityRefs {
		ref.AccountID = strings.TrimSpace(ref.AccountID)
		ref.EntityID = strings.TrimSpace(ref.EntityID)
		if ref.AccountID == "" || ref.EntityID == "" {
			continue
		}
		refSet[ref.AccountID+"\x00"+ref.EntityID] = struct{}{}
	}
	commits := make([]*corev1.Commit, 0, limit)
	for _, commit := range s.b.commits {
		if commit.Id == "mem_root" || !strings.HasPrefix(commit.Id, idPrefix) {
			continue
		}
		if !commitMatchesResolveFilter(s.b, commit, prefixes, refSet, filter.IncludePrefixesWithEntities) {
			continue
		}
		commits = append(commits, cloneCommit(commit))
	}
	sortCommits(commits)
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return commits, nil
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

func (s *RepositoryStore) ListCommitChain(ctx context.Context, refName, stopCommitID string, prefixes []string) (*storage.CommitChain, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	ref := s.b.refs[refName]
	if ref == nil || ref.CommitId == "" {
		return nil, storage.ErrNotFound
	}
	chain := &storage.CommitChain{HeadCommitID: ref.CommitId}
	var newestFirst []*corev1.Commit
	for id := ref.CommitId; id != ""; {
		if stopCommitID != "" && id == stopCommitID {
			chain.FoundStop = true
			break
		}
		commit := s.b.commits[id]
		if commit == nil {
			break
		}
		if commitTouchesPrefixes(commit.ChangedPaths, prefixes) {
			newestFirst = append(newestFirst, cloneCommit(commit))
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		id = commit.ParentIds[0]
	}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		chain.Commits = append(chain.Commits, newestFirst[i])
	}
	return chain, nil
}

// commitTouchesPrefixes reports whether any changed path lies inside a prefix
// or is an ancestor directory of one (for example an account root change).
func commitTouchesPrefixes(changed, prefixes []string) bool {
	for _, p := range changed {
		for _, prefix := range prefixes {
			prefix = strings.TrimRight(prefix, "/")
			if prefix == "" || p == prefix ||
				strings.HasPrefix(p, prefix+"/") || strings.HasPrefix(prefix, p+"/") {
				return true
			}
		}
	}
	return false
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
	dirs := s.b.commitDirs[commitID]
	if file, ok := files[p]; ok {
		return &storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}, nil
	}
	if _, ok := dirs[p]; ok {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	if p == "/" || hasDescendant(files, p) || hasDescendantDir(dirs, p) {
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
	dirs := s.b.commitDirs[commitID]
	entries := directoryEntries(p, files, dirs)
	if len(entries) == 0 && p != "/" && !hasDescendant(files, p) && !hasDescendantDir(dirs, p) {
		if _, ok := dirs[p]; ok {
			return nil, nil
		}
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

func normalizePathPrefixes(prefixes []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
		if prefix == "" {
			prefix = "/"
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func commitMatchesResolveFilter(b *backend, commit *corev1.Commit, prefixes []string, entityRefs map[string]struct{}, includePrefixesWithEntities bool) bool {
	if len(prefixes) == 0 && len(entityRefs) == 0 {
		return true
	}
	if includePrefixesWithEntities || len(entityRefs) == 0 {
		for _, changed := range commit.ChangedPaths {
			for _, prefix := range prefixes {
				if pathContains(prefix, changed) {
					return true
				}
			}
		}
	}
	for _, changed := range commit.ChangedPaths {
		for _, ref := range b.entityChanges[changed] {
			if _, ok := entityRefs[ref.AccountID+"\x00"+ref.EntityID]; ok {
				return true
			}
		}
	}
	return false
}

func sortCommits(commits []*corev1.Commit) {
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].CreatedAt == commits[j].CreatedAt {
			return commits[i].Id > commits[j].Id
		}
		return commits[i].CreatedAt > commits[j].CreatedAt
	})
}

func (s *RepositoryStore) CommitAncestry(ctx context.Context, startCommitID string, limit int) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []string
	for id := startCommitID; id != "" && (limit <= 0 || len(out) < limit); {
		commit := s.b.commits[id]
		if commit == nil {
			if len(out) == 0 {
				return nil, storage.ErrNotFound
			}
			break
		}
		out = append(out, id)
		if len(commit.ParentIds) == 0 {
			break
		}
		id = commit.ParentIds[0]
	}
	return out, nil
}

package memory

import (
	"context"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

func (s *RepositoryStore) RecordGitImportCommit(ctx context.Context, record storage.GitImportedCommitRecord) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.imports[record.ImportID] == nil {
		return storage.ErrNotFound
	}
	s.b.importedCommits[record.ImportID] = append(s.b.importedCommits[record.ImportID], record)
	return nil
}

func (s *RepositoryStore) ChangesetsForCommits(ctx context.Context, commitIDs []string) (map[string]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range commitIDs {
		wanted[id] = true
	}
	out := map[string]string{}
	for id, cs := range s.b.changesets {
		if cs != nil && cs.CommitId != "" && wanted[cs.CommitId] {
			out[cs.CommitId] = id
		}
	}
	return out, nil
}

func (s *RepositoryStore) GitImportsForCommits(ctx context.Context, nativeCommitIDs []string) (map[string]storage.GitImportedCommitRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range nativeCommitIDs {
		wanted[id] = true
	}
	out := map[string]storage.GitImportedCommitRecord{}
	for _, records := range s.b.importedCommits {
		for _, record := range records {
			if wanted[record.NativeCommitID] {
				out[record.NativeCommitID] = record
			}
		}
	}
	return out, nil
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

func gitImportKey(source, mountPath, sliceID, targetRef, mode string) string {
	return source + "\x00" + mountPath + "\x00" + sliceID + "\x00" + targetRef + "\x00" + mode
}

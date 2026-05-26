package postgres

import "github.com/gitslice-io/gitslice/internal/storage"

const DefaultTargetRef = storage.DefaultTargetRef

type Subject = storage.Subject

type FileEntry = storage.FileEntry

type TreeEntry = storage.TreeEntry

type PathHead = storage.PathHead

type pendingPublishRow struct {
	ID          string
	ChangesetID string
	PatchsetID  string
	TargetRef   string
}

type GitImportRecord = storage.GitImportRecord

type GitImportedCommitRecord = storage.GitImportedCommitRecord

type scanner interface {
	Scan(dest ...any) error
}

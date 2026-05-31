package storage

import (
	"context"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type AuthStore interface {
	LoginDevUser(ctx context.Context, devUser string) (string, string, error)
	SignupUser(ctx context.Context, username string) (string, string, error)
	SubjectForToken(ctx context.Context, token string) (*Subject, error)
	EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error
	ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error)
}

type BlobStore interface {
	Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error
	GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error)
	GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error)
}

type ChangesetStore interface {
	Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	Get(ctx context.Context, changesetID string) (*corev1.Changeset, error)
	List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error)
	AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error)
	Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error)
	PublishPending(ctx context.Context, limit int) (int, error)
	Abandon(ctx context.Context, changesetID string) error
}

type CommitListPage struct {
	Commits       []*corev1.Commit
	NextPageToken string
}

type CommitResolveFilter struct {
	RefName                     string
	IDPrefix                    string
	PathPrefixes                []string
	EntityRefs                  []HistoryEntityRef
	IncludePrefixesWithEntities bool
	Limit                       int
}

type RepositoryStore interface {
	GetRef(ctx context.Context, name string) (*corev1.Ref, error)
	GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*GitImportRecord, error)
	GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*GitImportRecord, error)
	ListGitImportCommits(ctx context.Context, importID string) ([]GitImportedCommitRecord, error)
	RecordGitImportCommit(ctx context.Context, importID, gitCommitID, nativeCommitID, message string, position, changedPathCount int) error
	CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error
	GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error)
	ResolveCommitCandidates(ctx context.Context, filter CommitResolveFilter) ([]*corev1.Commit, error)
	ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error)
	ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*CommitListPage, error)
	ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error)
	ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*CommitListPage, error)
	ListCommitPageByEntityRefs(ctx context.Context, refName string, refs []HistoryEntityRef, limit int, pageToken string) (*CommitListPage, error)
	ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, refs []HistoryEntityRef, prefixes []string, limit int, pageToken string) (*CommitListPage, error)
	CurrentPathEntitiesByPrefixes(ctx context.Context, refName string, prefixes []string) ([]CurrentPathEntity, error)
	CurrentPathEntitiesByPaths(ctx context.Context, refName string, paths []string) ([]CurrentPathEntity, error)
	GetFile(ctx context.Context, commitID, p string) (*FileEntry, error)
	GetEntry(ctx context.Context, commitID, p string) (*TreeEntry, error)
	ListDirectory(ctx context.Context, commitID, p string) ([]TreeEntry, error)
	ListFiles(ctx context.Context, commitID, prefix string) ([]FileEntry, error)
}

type SliceStore interface {
	Create(ctx context.Context, ref *corev1.SliceRef, includedPaths []string, visibility string) (*corev1.Slice, error)
	ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string) ([]string, string, error)
	Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error)
	Get(ctx context.Context, sliceID string) (*corev1.Slice, error)
	List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error)
	UpdateDefinition(ctx context.Context, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error)
	Delete(ctx context.Context, sliceID string) error
	CoveringIDs(ctx context.Context, p string) ([]string, error)
}

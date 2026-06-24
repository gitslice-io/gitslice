package storage

import (
	"context"
	"time"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type AuthStore interface {
	StartCliLogin(ctx context.Context) (code string, expiresAt time.Time, err error)
	CompleteCliLogin(ctx context.Context, code, subjectID string) error
	PollCliLogin(ctx context.Context, code string) (status, token, subjectID string, err error)
	// EnsureExternalSubject idempotently provisions a subject only for an
	// externally authenticated identity (for example a verified Clerk user) and
	// returns the internal subject ID.
	EnsureExternalSubject(ctx context.Context, externalID, email string) (string, error)
	// UsernameAvailable reports whether username (after normalization) is a
	// valid, unclaimed personal-account slug. normalized is the canonical form;
	// reason is a short explanation when available is false (invalid or taken).
	UsernameAvailable(ctx context.Context, username string) (available bool, normalized string, reason string, err error)
	// ChooseUsername provisions the personal account (and home slice) for an
	// already-provisioned subject using the chosen username, returning the
	// account slug. It errors if the subject already has a personal account or
	// the username is taken/invalid.
	ChooseUsername(ctx context.Context, subjectID, username string) (account string, err error)
	// UsernamesForSubjects maps each given subject id to its personal account slug
	// (the username). Subject ids without a personal account are omitted from the map.
	UsernamesForSubjects(ctx context.Context, subjectIDs []string) (map[string]string, error)
	SubjectForToken(ctx context.Context, token string) (*Subject, error)
	EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error
	AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error)
	ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error)
}

type BlobStore interface {
	Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error
	GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error)
	GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error)
}

type ChangesetStore interface {
	CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error)
	GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error)
	ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error)
	SetStackStatus(ctx context.Context, stackID, stackStatus string) error
	MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error)
	ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error)
	DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error)
	Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	Get(ctx context.Context, changesetID string) (*corev1.Changeset, error)
	List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error)
	AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error)
	Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error)
	ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, status string) (*corev1.ReportCheckResultResponse, error)
	Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error)
	PublishPending(ctx context.Context, limit int) (int, error)
	PendingPublishDepth(ctx context.Context) (int, error)
	Abandon(ctx context.Context, changesetID string) error
}

type OutboxProcessResult struct {
	Processed int
	Failed    int
}

type DerivedIndexStore interface {
	ProcessOutbox(ctx context.Context, limit int) (OutboxProcessResult, error)
	OutboxDepth(ctx context.Context) (int, error)
	WaitForOutboxDrain(ctx context.Context) error
	RebuildDerivedIndexes(ctx context.Context, targetRef string) error
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
	RootTreeForCommit(ctx context.Context, commitID string) (string, error)
	GetFileAtTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error)
	GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error)
	ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error)
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
	Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error)
	ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error)
	Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error)
	Get(ctx context.Context, sliceID string) (*corev1.Slice, error)
	List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error)
	ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error)
	UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error)
	Delete(ctx context.Context, sliceID string) error
	CoveringIDsByPath(ctx context.Context, paths []string) (map[string][]string, error)
}

// AgentDaemonInput is the registration payload for an agent daemon.
type AgentDaemonInput struct {
	SubjectID string
	Account   string
	Name      string
	Runtime   string
	Version   string
}

// ConversationInput is the creation payload for an agent conversation.
type ConversationInput struct {
	DaemonID  string
	SubjectID string
	SliceID   string
	Account   string
	SliceName string
	Title     string
}

// ConversationFilter narrows ListConversations. Empty fields are ignored.
type ConversationFilter struct {
	SliceID   string
	DaemonID  string
	SubjectID string
}

// AgentStore persists agent daemons, conversations, and conversation events for
// the bring-your-own-agent feature. See design/16_bring_your_own_agent.md.
type AgentStore interface {
	// RegisterDaemon upserts a daemon row and marks it online, returning the
	// daemon row (id generated when not already present for the subject+name).
	RegisterDaemon(ctx context.Context, in AgentDaemonInput) (*corev1.AgentDaemon, error)
	SetDaemonStatus(ctx context.Context, daemonID, status string) error
	GetDaemon(ctx context.Context, daemonID string) (*corev1.AgentDaemon, error)
	ListDaemons(ctx context.Context, subjectID string) ([]*corev1.AgentDaemon, error)

	CreateConversation(ctx context.Context, in ConversationInput) (*corev1.Conversation, error)
	GetConversation(ctx context.Context, conversationID string) (*corev1.Conversation, error)
	ListConversations(ctx context.Context, filter ConversationFilter) ([]*corev1.Conversation, error)
	SetConversationStatus(ctx context.Context, conversationID, status string) error

	// AppendEvent assigns the next per-conversation seq atomically and returns
	// the stored event. When clientSeq > 0 it is the daemon's per-conversation
	// sequence and AppendEvent dedups on (conversationID, clientSeq): a repeat is
	// not inserted, does not advance the server seq, and returns inserted=false
	// (with the previously stored event). clientSeq <= 0 is always inserted.
	AppendEvent(ctx context.Context, conversationID, role, eventType, text, dataJSON, itemID string, clientSeq int64) (ev *corev1.ConversationEvent, inserted bool, err error)
	ListEvents(ctx context.Context, conversationID string, afterSeq int64) ([]*corev1.ConversationEvent, error)
	// ListEventsRange returns events with afterSeq < seq <= beforeSeq. A
	// beforeSeq <= 0 means no upper bound.
	ListEventsRange(ctx context.Context, conversationID string, afterSeq, beforeSeq int64) ([]*corev1.ConversationEvent, error)
	// LatestEventSeq returns the highest event seq for a conversation, or 0 when
	// it has no events.
	LatestEventSeq(ctx context.Context, conversationID string) (int64, error)
}

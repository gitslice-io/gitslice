package storage

import (
	"context"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type AuthStore interface {
	RoleLookupStore
	APIKeyStore
	AccountGroupStore

	StartCliLogin(ctx context.Context) (code string, expiresAt time.Time, err error)
	CompleteCliLogin(ctx context.Context, code, subjectID string) error
	PollCliLogin(ctx context.Context, code string) (status, token, subjectID string, err error)
	EnsureExternalSubject(ctx context.Context, provider, externalID, email string) (string, error)
	ExternalIdentity(ctx context.Context, subjectID string) (provider, externalID string, err error)
	SetVerifiedEmails(ctx context.Context, subjectID, source string, emails []string) error
	VerifiedEmails(ctx context.Context, subjectID string) ([]string, error)
	ListPendingClaims(ctx context.Context, subjectID string) ([]PendingClaim, error)
	AcceptClaim(ctx context.Context, subjectID, agentSubjectID string) (account string, err error)
	ListOwnedAgents(ctx context.Context, subjectID string) ([]OwnedAgent, error)
	UsernameAvailable(ctx context.Context, username string) (available bool, normalized string, reason string, err error)
	ChooseUsername(ctx context.Context, subjectID, username string) (account string, err error)
	UsernamesForSubjects(ctx context.Context, subjectIDs []string) (map[string]string, error)
	RegisterAgent(ctx context.Context, in RegisterAgentInput) (*RegisteredAgent, error)
	CreateDelegatedAgent(ctx context.Context, in CreateDelegatedAgentInput) (*CreateDelegatedAgentResult, error)
	CreateAgentInvite(ctx context.Context, in CreateAgentInviteInput) (*CreateAgentInviteResult, error)
	ListAgentInvites(ctx context.Context, actorID string) ([]AgentInvite, error)
	RevokeAgentInvite(ctx context.Context, actorID, inviteID string) error
	JoinAgentInvite(ctx context.Context, in JoinAgentInviteInput) (*JoinAgentInviteResult, error)
	EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error
	ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error)
	SubjectIDForUsername(ctx context.Context, username string) (string, error)
	SubjectsByIDs(ctx context.Context, subjectIDs []string) (map[string]Subject, error)
	AccountKind(ctx context.Context, accountSlug string) (string, error)
	CreateOrganization(ctx context.Context, slug string, ownerSubjectIDs []string, createdBy string) error
	ListAccountMembers(ctx context.Context, accountSlug string) ([]AccountMember, error)
	SetAccountMemberRole(ctx context.Context, accountSlug, subjectID, role string) error
	RemoveAccountMember(ctx context.Context, accountSlug, subjectID string) error
	CountOrganizationsCreatedBy(ctx context.Context, subjectID string) (int, error)
	UpsertAccountInvitation(ctx context.Context, accountSlug, subjectID, role, invitedBy string) (*AccountInvitation, error)
	ListAccountInvitations(ctx context.Context, accountSlug string) ([]AccountInvitation, error)
	ListSubjectInvitations(ctx context.Context, subjectID string) ([]AccountInvitation, error)
	GetAccountInvitation(ctx context.Context, accountSlug, subjectID string) (*AccountInvitation, error)
	AcceptAccountInvitation(ctx context.Context, accountSlug, subjectID string) (string, error)
	DeleteAccountInvitation(ctx context.Context, accountSlug, subjectID string) error
	GetAccountProfile(ctx context.Context, accountSlug string) (*AccountProfile, error)
	UpdateAccountProfile(ctx context.Context, accountSlug string, profile AccountProfile) (*AccountProfile, error)
	SetAccountAvatar(ctx context.Context, accountSlug, avatarKey string) (string, error)
}

type BlobStore interface {
	Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error
	UpsertMany(ctx context.Context, blobs []BlobUpsert) error
	GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error)
	GetByIDs(ctx context.Context, blobIDs []string) (map[string]*corev1.BlobRecord, error)
	GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error)
	AssociateSlices(ctx context.Context, sliceID string, contentHashes []string) error
	SliceAssociations(ctx context.Context, sliceID string, contentHashes []string) (map[string]bool, error)
	PathsByContentHash(ctx context.Context, contentHashes []string) (map[string][]string, error)
	SetGitBlobIDs(ctx context.Context, ids map[string]string) error
	GitBlobIDs(ctx context.Context, contentHashes []string) (map[string]string, error)
	ListMissingGitBlobIDs(ctx context.Context, limit int) ([]*corev1.BlobRecord, error)
	BlobsByGitIDs(ctx context.Context, gitBlobIDs []string) (map[string]*corev1.BlobRecord, error)
}

type ChangesetStore interface {
	StackStore
	PathIntentStore

	Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	Get(ctx context.Context, changesetID string) (*corev1.Changeset, error)
	List(ctx context.Context, filter ChangesetListFilter) ([]*corev1.Changeset, string, error)
	PatchsetsByConversation(ctx context.Context, conversationID string) ([]*corev1.Patchset, error)
	AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error)
	Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error)
	ApprovalSubjects(ctx context.Context, changesetID, patchsetID string) ([]string, error)
	ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, status string) (*corev1.ReportCheckResultResponse, error)
	Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error)
	SubmitChangeset(ctx context.Context, in SubmitInput) (*corev1.SubmitChangesetResponse, error)
	RecordSubmitBlockedReason(ctx context.Context, changesetID, expectedCurrentPatchsetID, reason string) error
	PublishPending(ctx context.Context, limit int) (int, error)
	PendingPublishDepth(ctx context.Context) (int, error)
	Abandon(ctx context.Context, changesetID string) error
	UpdateInfo(ctx context.Context, changesetID string, title, description *string) (*corev1.Changeset, error)
	AddCollaborator(ctx context.Context, changesetID, subjectID, addedBy string) (*corev1.Changeset, error)
	RemoveCollaborator(ctx context.Context, changesetID, subjectID string) (*corev1.Changeset, error)
	Handoff(ctx context.Context, changesetID, newAuthorSubjectID string) (*corev1.Changeset, error)
}

type DerivedIndexStore interface {
	ProcessOutbox(ctx context.Context, limit int) (OutboxProcessResult, error)
	OutboxDepth(ctx context.Context) (int, error)
	WaitForOutboxDrain(ctx context.Context) error
	RebuildDerivedIndexes(ctx context.Context, targetRef string) error
}

type RepositoryStore interface {
	GetRef(ctx context.Context, name string) (*corev1.Ref, error)
	RootTreeForCommit(ctx context.Context, commitID string) (string, error)
	GetFileAtTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error)
	GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error)
	ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error)
	ListTreeAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error)
	DiffTreeAtTree(ctx context.Context, oldRootTreeID, newRootTreeID, p string) (changed []TreeEntry, removed []string, err error)
	GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*GitImportRecord, error)
	GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*GitImportRecord, error)
	ListGitImportCommits(ctx context.Context, importID string) ([]GitImportedCommitRecord, error)
	RecordGitImportCommit(ctx context.Context, record GitImportedCommitRecord) error
	GitImportsForCommits(ctx context.Context, nativeCommitIDs []string) (map[string]GitImportedCommitRecord, error)
	ChangesetsForCommits(ctx context.Context, commitIDs []string) (map[string]string, error)
	CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error
	GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error)
	GetCommits(ctx context.Context, commitIDs []string) (map[string]*corev1.Commit, error)
	ResolveCommitCandidates(ctx context.Context, filter CommitResolveFilter) ([]*corev1.Commit, error)
	ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error)
	ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*CommitListPage, error)
	ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error)
	ListCommitChain(ctx context.Context, refName, stopCommitID string, prefixes []string) (*CommitChain, error)
	CommitAncestry(ctx context.Context, startCommitID string, limit int) ([]string, error)
	ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*CommitListPage, error)
	ListCommitPageByEntityRefs(ctx context.Context, refName string, refs []HistoryEntityRef, limit int, pageToken string) (*CommitListPage, error)
	ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, refs []HistoryEntityRef, prefixes []string, limit int, pageToken string) (*CommitListPage, error)
	CurrentPathEntitiesByPrefixes(ctx context.Context, refName string, prefixes []string) ([]CurrentPathEntity, error)
	CurrentPathEntitiesByPaths(ctx context.Context, refName string, paths []string) ([]CurrentPathEntity, error)
	FileChanges(ctx context.Context, refName string, commitIDs []string) ([]FileChange, error)
	GetFile(ctx context.Context, commitID, p string) (*FileEntry, error)
	GetEntry(ctx context.Context, commitID, p string) (*TreeEntry, error)
	ListDirectory(ctx context.Context, commitID, p string) ([]TreeEntry, error)
	ListFiles(ctx context.Context, commitID, prefix string) ([]FileEntry, error)
}

type SliceStore interface {
	SliceSecretStore
	SliceMemberStore
	SliceTagStore

	Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error)
	CreateDefinition(ctx context.Context, subjectID string, ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.Slice, error)
	ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error)
	ValidateSliceDefinition(ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error)
	Get(ctx context.Context, sliceID string) (*corev1.Slice, error)
	List(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	ListPublic(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	ListVisible(ctx context.Context, account, subjectID, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	ListSorted(ctx context.Context, account, visibilityMode, subjectID, afterCursor string, limit int, includeArchived bool, sortOrder SliceSort) (slices []*corev1.Slice, nextCursor string, err error)
	ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error)
	UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error)
	SetCIDaemons(ctx context.Context, sliceID string, daemonIDs []string) (*corev1.Slice, error)
	ListByCIDaemon(ctx context.Context, daemonIDs []string) ([]*corev1.Slice, error)
	AccountCIDaemons(ctx context.Context, account string) (daemonIDs []string, daemonNames []string, err error)
	SetAccountCIDaemons(ctx context.Context, account string, daemonIDs []string) (ids []string, names []string, err error)
	ListAccountsByCIDaemon(ctx context.Context, daemonIDs []string) (map[string][]string, error)
	Delete(ctx context.Context, sliceID string) error
	Retire(ctx context.Context, sliceID string) error
	ChangesetCounts(ctx context.Context, sliceID string) (total, open int, err error)
	ResolveRetired(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error)
	Rename(ctx context.Context, sliceID, newName string) (*corev1.Slice, error)
	SetArchived(ctx context.Context, sliceID string, archived bool) (*corev1.Slice, error)
	SetDescription(ctx context.Context, sliceID, description string) (*corev1.Slice, error)
	SetMergeOnSubmit(ctx context.Context, sliceID string, enabled bool) (*corev1.Slice, error)
	CoveringIDsByPath(ctx context.Context, paths []string) (map[string][]string, error)
}

type CheckStore interface {
	CreateCheckRun(ctx context.Context, in CheckRunInput) (*corev1.CheckRun, error)
	GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error)
	ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error)
	ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error)
	CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error)
	UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error)
	AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (inserted bool, err error)
	ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error)
	SetCheckRunDetails(ctx context.Context, runID string, details CheckRunDetails) (*corev1.CheckRun, error)
	AllowPatchsetSecrets(ctx context.Context, changesetID, patchsetID, allowedBySubjectID string) (allowance PatchsetSecretAllowance, created bool, err error)
	GetPatchsetSecretAllowance(ctx context.Context, changesetID, patchsetID string) (PatchsetSecretAllowance, error)
	ClaimCheckRuns(ctx context.Context, daemonID, instance string, limit int, lease time.Duration) ([]*corev1.CheckRun, error)
	ListClaimedCheckRuns(ctx context.Context, instance string) ([]*corev1.CheckRun, error)
	RenewCheckRunClaim(ctx context.Context, runID, instance string, lease time.Duration) error
	RenewCheckRunClaims(ctx context.Context, instance string, lease time.Duration) error
	ReleaseCheckRunClaims(ctx context.Context, instance string) error
	CountUnclaimedCheckRuns(ctx context.Context, daemonID string) (int, error)
	HasPendingCheckRuns(ctx context.Context, daemonID, instance string) (bool, error)
	TakeLapsedCheckRuns(ctx context.Context, limit int) ([]*corev1.CheckRun, error)
	ListStrandedCheckRuns(ctx context.Context, minAge time.Duration) ([]*corev1.CheckRun, error)
	ReassignCheckRun(ctx context.Context, runID, fromDaemon, toDaemon string) (bool, error)
}

type AgentStore interface {
	RegisterDaemon(ctx context.Context, in AgentDaemonInput) (*corev1.AgentDaemon, error)
	SetDaemonStatus(ctx context.Context, daemonID, status string) error
	SetDaemonStandby(ctx context.Context, daemonID string, until time.Time) error
	GetDaemon(ctx context.Context, daemonID string) (*corev1.AgentDaemon, error)
	GetDaemonsByIDs(ctx context.Context, daemonIDs []string) (map[string]*corev1.AgentDaemon, error)
	ListDaemons(ctx context.Context, subjectID string) ([]*corev1.AgentDaemon, error)
	ListDaemonsForAccounts(ctx context.Context, accounts []string) ([]*corev1.AgentDaemon, error)
	DaemonSubject(ctx context.Context, daemonID string) (string, error)
	RenameDaemon(ctx context.Context, daemonID, displayName string) (*corev1.AgentDaemon, error)
	ExposeDaemon(ctx context.Context, daemonID, sliceID, exposedBy string) error
	UnexposeDaemon(ctx context.Context, daemonID, sliceID string) error
	DaemonExposedTo(ctx context.Context, daemonID, sliceID string) (bool, error)
	ListDaemonExposures(ctx context.Context, daemonIDs []string) (map[string][]string, error)
	ListDaemonsExposedToSlice(ctx context.Context, sliceID string) ([]*corev1.AgentDaemon, error)
	CreateConversation(ctx context.Context, in ConversationInput) (*corev1.Conversation, error)
	GetConversation(ctx context.Context, conversationID string) (*corev1.Conversation, error)
	ListConversations(ctx context.Context, filter ConversationFilter) ([]*corev1.Conversation, error)
	SetConversationStatus(ctx context.Context, conversationID, status string) error
	AppendEvent(ctx context.Context, conversationID, role, eventType, text, dataJSON, itemID string, clientSeq int64) (ev *corev1.ConversationEvent, inserted bool, err error)
	ListEvents(ctx context.Context, conversationID string, afterSeq int64) ([]*corev1.ConversationEvent, error)
	UnansweredUserEvents(ctx context.Context, conversationID string) ([]*corev1.ConversationEvent, error)
	ListEventsRange(ctx context.Context, conversationID string, afterSeq, beforeSeq, limit int64) ([]*corev1.ConversationEvent, error)
	LatestEventSeq(ctx context.Context, conversationID string) (int64, error)
}

package storage

import (
	"context"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type AuthStore interface {
	StartCliLogin(ctx context.Context) (code string, expiresAt time.Time, err error)
	CompleteCliLogin(ctx context.Context, code, subjectID string) error
	PollCliLogin(ctx context.Context, code string) (status, token, subjectID string, err error)
	// EnsureExternalSubject idempotently provisions a subject only for an
	// externally authenticated identity (for example a verified Clerk user) and
	// returns the internal subject ID. provider names the identity provider
	// ("clerk", "service") and is recorded with externalID so the subject's
	// provider profile (e.g. verified emails) can be looked up later.
	EnsureExternalSubject(ctx context.Context, provider, externalID, email string) (string, error)
	// ExternalIdentity returns the provider and provider user id recorded for an
	// externally authenticated subject; both are empty for other subjects.
	ExternalIdentity(ctx context.Context, subjectID string) (provider, externalID string, err error)
	// SetVerifiedEmails replaces the set of verified email addresses the given
	// source ("clerk", "service") vouches for on subjectID. Emails are normalized;
	// malformed ones are dropped.
	SetVerifiedEmails(ctx context.Context, subjectID, source string, emails []string) error
	// ListPendingClaims returns unclaimed agent registrations whose owner email is
	// one of subjectID's verified emails, oldest first.
	ListPendingClaims(ctx context.Context, subjectID string) ([]PendingClaim, error)
	// AcceptClaim makes subjectID an owner of the agent's account and marks the
	// registration claimed. It returns ErrNotFound unless the registration is
	// unclaimed and its owner email is one of subjectID's verified emails.
	AcceptClaim(ctx context.Context, subjectID, agentSubjectID string) (account string, err error)
	// ListOwnedAgents returns the agents whose personal accounts subjectID holds
	// an owner membership on, ordered by registration time.
	ListOwnedAgents(ctx context.Context, subjectID string) ([]OwnedAgent, error)
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
	// RegisterAgent creates an agent subject together with its personal account
	// (admin membership, home slice), records the owner email that may later
	// claim co-ownership, and issues the agent's first API key.
	RegisterAgent(ctx context.Context, in RegisterAgentInput) (*RegisteredAgent, error)
	// SubjectForToken resolves a bearer token to a subject. Tokens starting with
	// APIKeyPrefix resolve against unrevoked, unexpired API keys; all others
	// against login sessions.
	SubjectForToken(ctx context.Context, token string) (*Subject, error)
	EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error
	AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error)
	ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error)
	// SubjectIDForUsername returns the subject whose personal account has the
	// given slug, or ErrNotFound.
	SubjectIDForUsername(ctx context.Context, username string) (string, error)
	// AccountKind returns "personal" or "organization", or ErrNotFound.
	AccountKind(ctx context.Context, accountSlug string) (string, error)
	// CreateOrganization creates an organization account with owner
	// memberships, a private home slice covering /<slug>, and the account root
	// directory. It returns ErrConflict when the slug is taken. Callers
	// validate the slug; this does not consult the reserved-name list.
	CreateOrganization(ctx context.Context, slug string, ownerSubjectIDs []string, createdBy string) error
	// ListAccountMembers lists an account's members, owners first, with each
	// member's highest role.
	ListAccountMembers(ctx context.Context, accountSlug string) ([]AccountMember, error)
	// SetAccountMemberRole makes role the subject's only role on an
	// organization account. It returns ErrConflict for personal accounts and
	// when it would demote the last owner.
	SetAccountMemberRole(ctx context.Context, accountSlug, subjectID, role string) error
	// RemoveAccountMember removes every membership of the subject on an
	// organization account. It returns ErrConflict for personal accounts and
	// for the last owner, and ErrNotFound when the subject is not a member.
	RemoveAccountMember(ctx context.Context, accountSlug, subjectID string) error
}

// AccountMember is one member of an account and their highest role.
type AccountMember struct {
	SubjectID string
	Username  string
	Role      string
}

// Account kinds.
const (
	AccountKindPersonal     = "personal"
	AccountKindOrganization = "organization"
)

// AccountRoles lists membership roles from most to least privileged.
var AccountRoles = []string{"owner", "admin", "writer", "member", "reader"}

// AccountRoleRank orders roles (lower is more privileged); unknown roles rank
// after every known one.
func AccountRoleRank(role string) int {
	for i, known := range AccountRoles {
		if role == known {
			return i
		}
	}
	return len(AccountRoles)
}

type BlobStore interface {
	Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error
	GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error)
	GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error)
	AssociateSlices(ctx context.Context, sliceID string, contentHashes []string) error
	SliceAssociations(ctx context.Context, sliceID string, contentHashes []string) (map[string]bool, error)
	PathsByContentHash(ctx context.Context, contentHashes []string) (map[string][]string, error)
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
	// PatchsetsByConversation returns every patchset stamped with the given
	// authoring conversation id, across all changesets, ordered by
	// authoring_conversation_seq ascending.
	PatchsetsByConversation(ctx context.Context, conversationID string) ([]*corev1.Patchset, error)
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

// CommitChain is a segment of a ref's first-parent history, read in one
// consistent snapshot. It is built from the commit rows themselves, not from
// asynchronously derived indexes, so it never lags the ref.
type CommitChain struct {
	// HeadCommitID is the ref's head when the chain was read.
	HeadCommitID string
	// FoundStop reports whether the walk reached the requested stop commit.
	// When a stop was requested but not found, Commits run back to the root.
	FoundStop bool
	// Commits are the commits after the stop (exclusive) whose changed paths
	// overlap the requested prefixes, oldest first. A changed path overlaps a
	// prefix when either one contains the other.
	Commits []*corev1.Commit
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
	RecordGitImportCommit(ctx context.Context, record GitImportedCommitRecord) error
	// GitImportsForCommits returns the import record behind each native commit
	// that came from a Git import, keyed by native commit id.
	GitImportsForCommits(ctx context.Context, nativeCommitIDs []string) (map[string]GitImportedCommitRecord, error)
	CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error
	GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error)
	ResolveCommitCandidates(ctx context.Context, filter CommitResolveFilter) ([]*corev1.Commit, error)
	ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error)
	ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*CommitListPage, error)
	ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error)
	// ListCommitChain walks refName's first-parent history from its head back
	// to stopCommitID (or the root when stopCommitID is empty or never
	// reached) and returns the commits that touch prefixes, oldest first.
	ListCommitChain(ctx context.Context, refName, stopCommitID string, prefixes []string) (*CommitChain, error)
	// CommitAncestry returns startCommitID and its first-parent ancestors,
	// newest first, at most limit ids (no limit when limit <= 0).
	CommitAncestry(ctx context.Context, startCommitID string, limit int) ([]string, error)
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
	SetCIDaemon(ctx context.Context, sliceID, daemonID string) (*corev1.Slice, error)
	SetSliceSecret(ctx context.Context, sliceID, name, value string) error
	DeleteSliceSecret(ctx context.Context, sliceID, name string) error
	ListSliceSecretNames(ctx context.Context, sliceID string) ([]string, error)
	GetSliceSecrets(ctx context.Context, sliceID string) (map[string]string, error)
	Delete(ctx context.Context, sliceID string) error
	CoveringIDsByPath(ctx context.Context, paths []string) (map[string][]string, error)
	// CreateTag records an immutable tag. It returns the stored tag and whether
	// it was created; an existing tag with the same commit is returned as is,
	// and one with a different commit fails with ErrConflict.
	CreateTag(ctx context.Context, tag SliceTag) (*SliceTag, bool, error)
	// ListTags returns a slice's tags, newest first.
	ListTags(ctx context.Context, sliceID string) ([]SliceTag, error)
}

// SliceTag is an immutable name for a native commit, scoped to a slice.
type SliceTag struct {
	SliceID           string
	Name              string
	CommitID          string
	DefinitionVersion int64
	Message           string
	CreatedBy         string // subject id
	CreatedAt         string // RFC 3339
}

// ValidSliceSecretName reports whether name matches ^[A-Z_][A-Z0-9_]*$.
func ValidSliceSecretName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i == 0 {
			if c == '_' || (c >= 'A' && c <= 'Z') {
				continue
			}
			return false
		}
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
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

// CheckRunInput is the creation payload for a persisted CI/self-check run.
type CheckRunInput struct {
	ChangesetID string
	PatchsetID  string
	CheckName   string
	DaemonID    string
	Provenance  string
	Status      string
}

// CheckStore persists check runs and replayable check logs. Terminal passed,
// failed, and errored runs update check_results so the existing submit gate stays
// the single source of truth for required-check satisfaction.
type CheckStore interface {
	CreateCheckRun(ctx context.Context, in CheckRunInput) (*corev1.CheckRun, error)
	GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error)
	ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error)
	ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error)
	// CancelOpenCheckRunsBeforePatchset marks every non-superseded queued/running
	// run of the changeset whose patchset is not currentPatchsetID as canceled
	// and returns the runs it canceled.
	CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error)
	UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error)
	AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (inserted bool, err error)
	ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error)
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
	// UnansweredUserEvents returns the conversation's trailing user "message"
	// events that have no later non-user event — i.e. user messages the daemon
	// has not (durably) responded to. Ordered by seq ascending. Used to redeliver
	// user messages that were lost while the daemon's Connect stream was down.
	UnansweredUserEvents(ctx context.Context, conversationID string) ([]*corev1.ConversationEvent, error)
	// ListEventsRange returns events with afterSeq < seq <= beforeSeq, ordered
	// ascending by seq. A beforeSeq <= 0 means no upper bound. When limit > 0,
	// only the newest `limit` events in that window are returned (still ordered
	// ascending); limit <= 0 means no limit.
	ListEventsRange(ctx context.Context, conversationID string, afterSeq, beforeSeq, limit int64) ([]*corev1.ConversationEvent, error)
	// LatestEventSeq returns the highest event seq for a conversation, or 0 when
	// it has no events.
	LatestEventSeq(ctx context.Context, conversationID string) (int64, error)
}

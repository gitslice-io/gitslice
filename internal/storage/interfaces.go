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
	// ListAPIKeys lists the API keys of the agent whose personal account is
	// agentAccount ("" means actorID itself), newest first. actorID must be that
	// agent or an owner of its account (ErrUnauthorized otherwise); ErrNotFound
	// if the account is not an agent's.
	ListAPIKeys(ctx context.Context, actorID, agentAccount string) (agentSubjectID, account string, keys []APIKey, err error)
	// CreateAPIKey issues a new key for the agent and returns it with its
	// secret, which is not stored. Same callers as ListAPIKeys. ErrConflict once
	// the agent holds MaxActiveAPIKeys active keys.
	CreateAPIKey(ctx context.Context, actorID, agentAccount, name string) (key APIKey, secret, agentSubjectID, account string, err error)
	// RevokeAPIKey ends a key at once (idempotent). Same callers as ListAPIKeys;
	// ErrNotFound if the key is not the agent's.
	RevokeAPIKey(ctx context.Context, actorID, agentAccount, keyID string) (APIKey, error)
	// SubjectForToken resolves a bearer token to a subject. Tokens starting with
	// APIKeyPrefix resolve against unrevoked, unexpired API keys; all others
	// against login sessions.
	SubjectForToken(ctx context.Context, token string) (*Subject, error)
	EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error
	AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error)
	ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error)
	// SliceRole returns the role the subject was given on the slice itself
	// (design/26_slice_members.md), or ErrNotFound when it has none.
	SliceRole(ctx context.Context, subjectID, sliceID string) (string, error)
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
	// CountOrganizationsCreatedBy counts the organizations the subject created.
	CountOrganizationsCreatedBy(ctx context.Context, subjectID string) (int, error)
	// UpsertAccountInvitation invites a subject to an organization with a role,
	// replacing any pending invitation of theirs to it. It returns ErrConflict
	// for personal accounts and ErrNotFound for unknown accounts.
	UpsertAccountInvitation(ctx context.Context, accountSlug, subjectID, role, invitedBy string) (*AccountInvitation, error)
	// ListAccountInvitations lists an organization's pending invitations,
	// newest first.
	ListAccountInvitations(ctx context.Context, accountSlug string) ([]AccountInvitation, error)
	// ListSubjectInvitations lists the subject's pending invitations, newest
	// first.
	ListSubjectInvitations(ctx context.Context, subjectID string) ([]AccountInvitation, error)
	// GetAccountInvitation returns one pending invitation, or ErrNotFound.
	GetAccountInvitation(ctx context.Context, accountSlug, subjectID string) (*AccountInvitation, error)
	// AcceptAccountInvitation turns the invitation into a membership with its
	// role and deletes it, in one transaction. It returns the role, or
	// ErrNotFound when there is no invitation.
	AcceptAccountInvitation(ctx context.Context, accountSlug, subjectID string) (string, error)
	// DeleteAccountInvitation removes a pending invitation, or returns
	// ErrNotFound.
	DeleteAccountInvitation(ctx context.Context, accountSlug, subjectID string) error
	// GetAccountProfile returns an account's profile, or ErrNotFound.
	GetAccountProfile(ctx context.Context, accountSlug string) (*AccountProfile, error)
	// UpdateAccountProfile sets an account's display name, description and
	// website (already validated) and returns the profile.
	UpdateAccountProfile(ctx context.Context, accountSlug string, profile AccountProfile) (*AccountProfile, error)
	// SetAccountAvatar sets the object key of an account's uploaded picture
	// ('' for none) and returns the key it replaced, or ErrNotFound.
	SetAccountAvatar(ctx context.Context, accountSlug, avatarKey string) (string, error)
}

// AccountInvitation is a pending invitation to join an organization.
type AccountInvitation struct {
	Account            string
	SubjectID          string
	Role               string
	InvitedBySubjectID string
	CreatedAt          time.Time
}

// AccountProfile is an account's public profile.
type AccountProfile struct {
	Account     string
	Kind        string
	DisplayName string
	Description string
	Website     string
	CreatedAt   time.Time
	// AvatarKey is the object key of the uploaded picture, '' for none.
	AvatarKey string
}

// AccountMember is one member of an account and their highest role.
type AccountMember struct {
	SubjectID string
	Username  string
	Role      string
	// DisplayName is the display name on the member's own account profile.
	DisplayName string
	// Kind is the member's subject kind: user or agent.
	Kind string
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
	// UpsertMany is Upsert for many blobs at once.
	UpsertMany(ctx context.Context, blobs []BlobUpsert) error
	GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error)
	// GetByIDs returns the blobs with the given ids, keyed by id; ids no blob
	// has are left out.
	GetByIDs(ctx context.Context, blobIDs []string) (map[string]*corev1.BlobRecord, error)
	GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error)
	AssociateSlices(ctx context.Context, sliceID string, contentHashes []string) error
	SliceAssociations(ctx context.Context, sliceID string, contentHashes []string) (map[string]bool, error)
	PathsByContentHash(ctx context.Context, contentHashes []string) (map[string][]string, error)
	// SetGitBlobIDs records Git blob ids (SHA-1 of "blob <size>\0" and the
	// bytes), keyed by content hash. It is idempotent.
	SetGitBlobIDs(ctx context.Context, ids map[string]string) error
	// GitBlobIDs returns the Git blob ids known for the content hashes.
	GitBlobIDs(ctx context.Context, contentHashes []string) (map[string]string, error)
	// ListMissingGitBlobIDs returns up to limit blobs that have no Git blob id
	// yet, with the size and storage location needed to compute it.
	ListMissingGitBlobIDs(ctx context.Context, limit int) ([]*corev1.BlobRecord, error)
	// BlobsByGitIDs returns the blobs that have the given Git blob ids, keyed
	// by id. Ids no blob has are left out.
	BlobsByGitIDs(ctx context.Context, gitBlobIDs []string) (map[string]*corev1.BlobRecord, error)
}

// BlobUpsert is one blob for BlobStore.UpsertMany.
type BlobUpsert struct {
	ID              string
	ContentHash     string
	Size            int64
	StorageLocation string
}

type ChangesetStore interface {
	CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error)
	GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error)
	ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error)
	SetStackStatus(ctx context.Context, stackID, stackStatus string) error
	// SetStackBaseCommit records the commit a restack moved the tree's root
	// changesets onto.
	SetStackBaseCommit(ctx context.Context, stackID, baseCommitID string) error
	MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error)
	ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error)
	DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error)
	Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	Get(ctx context.Context, changesetID string) (*corev1.Changeset, error)
	// List returns a page of changesets, most recently updated first, and the
	// token that continues after it ("" when there are no more).
	List(ctx context.Context, filter ChangesetListFilter) ([]*corev1.Changeset, string, error)
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
	// UpdateInfo changes a changeset's title and description (each only when
	// non-nil), while its status is draft. A landed or landing changeset's text
	// is its commit's message, so it is refused with ErrConflict. Both are
	// stored as NormalizeChangesetText gives them.
	UpdateInfo(ctx context.Context, changesetID string, title, description *string) (*corev1.Changeset, error)
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

// FileChange is one file's recorded change in one commit (change_kind is
// added, modified, moved or deleted). ContentHash is the file's content after
// the commit, empty for a deletion.
type FileChange struct {
	CommitID    string
	Path        string
	OldPath     string
	ChangeKind  string
	ContentHash string
}

type RepositoryStore interface {
	GetRef(ctx context.Context, name string) (*corev1.Ref, error)
	RootTreeForCommit(ctx context.Context, commitID string) (string, error)
	GetFileAtTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error)
	GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error)
	ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error)
	// ListTreeAtTree lists everything under p, files and directories, sorted
	// by path.
	ListTreeAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error)
	GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*GitImportRecord, error)
	GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*GitImportRecord, error)
	ListGitImportCommits(ctx context.Context, importID string) ([]GitImportedCommitRecord, error)
	RecordGitImportCommit(ctx context.Context, record GitImportedCommitRecord) error
	// GitImportsForCommits returns the import record behind each native commit
	// that came from a Git import, keyed by native commit id.
	GitImportsForCommits(ctx context.Context, nativeCommitIDs []string) (map[string]GitImportedCommitRecord, error)
	// ChangesetsForCommits maps each given commit to the changeset that landed
	// it; commits no changeset landed are left out.
	ChangesetsForCommits(ctx context.Context, commitIDs []string) (map[string]string, error)
	CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error
	GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error)
	// GetCommits reads many commits in one round trip. Ids with no commit are
	// left out of the result, not an error.
	GetCommits(ctx context.Context, commitIDs []string) (map[string]*corev1.Commit, error)
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
	// FileChanges returns what the history index recorded about files in the
	// given commits on refName: for each, the path, where it came from when it
	// moved, and the content it had after the commit. Commits the index has
	// not reached yet have none.
	FileChanges(ctx context.Context, refName string, commitIDs []string) ([]FileChange, error)
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
	// List lists an account's slices in name order, up to limit, starting after
	// the slice named after ("" for the first page). Archived slices are left
	// out unless includeArchived is set; retired ones are never listed.
	List(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	// ListPublic lists the account's public slices the same way, whoever is asking.
	ListPublic(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	// ListVisible lists the account's slices someone outside the account can
	// see: the public ones and those shared with subjectID.
	ListVisible(ctx context.Context, account, subjectID, after string, limit int, includeArchived bool) ([]*corev1.Slice, error)
	// ListMembers lists the people and agents given a role on the slice.
	ListMembers(ctx context.Context, sliceID string) ([]SliceMember, error)
	// SetMember gives the subject a role on the slice, or changes it.
	SetMember(ctx context.Context, sliceID, subjectID, role, addedBy string) (*SliceMember, error)
	// RemoveMember takes the subject's slice role away, or returns ErrNotFound.
	RemoveMember(ctx context.Context, sliceID, subjectID string) error
	// ListShared lists the slices shared with the subject, with its role on each.
	ListShared(ctx context.Context, subjectID string) ([]SharedSlice, error)
	ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error)
	UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error)
	SetCIDaemon(ctx context.Context, sliceID, daemonID string) (*corev1.Slice, error)
	// ListByCIDaemon lists the slices (not retired) that use one of the
	// daemons as their CI daemon.
	ListByCIDaemon(ctx context.Context, daemonIDs []string) ([]*corev1.Slice, error)
	SetSliceSecret(ctx context.Context, sliceID, name, value string) error
	DeleteSliceSecret(ctx context.Context, sliceID, name string) error
	ListSliceSecretNames(ctx context.Context, sliceID string) ([]string, error)
	GetSliceSecrets(ctx context.Context, sliceID string) (map[string]string, error)
	// Delete removes a slice that authored no changesets, or returns
	// ErrConflict.
	Delete(ctx context.Context, sliceID string) error
	// Retire removes a slice that has changesets: they stay, and so does the
	// slice's name, but it no longer resolves, lists or covers paths. It
	// returns ErrConflict while a changeset is open.
	Retire(ctx context.Context, sliceID string) error
	// ChangesetCounts returns how many changesets the slice authored, and how
	// many of those are open (neither landed nor abandoned).
	ChangesetCounts(ctx context.Context, sliceID string) (total, open int, err error)
	// ResolveRetired finds a retired slice by the name it had, so the
	// changesets it authored can still be read. ErrNotFound for any other slice.
	ResolveRetired(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error)
	// Rename gives the slice a new name. The old name keeps resolving to the
	// slice (Slice.FormerNames) and stays reserved. ErrConflict if the name is
	// taken or reserved by another slice.
	Rename(ctx context.Context, sliceID, newName string) (*corev1.Slice, error)
	// SetArchived archives the slice or restores it; either is idempotent.
	SetArchived(ctx context.Context, sliceID string, archived bool) (*corev1.Slice, error)
	// SetDescription sets the slice's description; "" clears it.
	SetDescription(ctx context.Context, sliceID, description string) (*corev1.Slice, error)
	// SetMergeOnSubmit turns merging stale changesets on submit on or off.
	SetMergeOnSubmit(ctx context.Context, sliceID string, enabled bool) (*corev1.Slice, error)
	CoveringIDsByPath(ctx context.Context, paths []string) (map[string][]string, error)
	// CreateTag records an immutable tag. It returns the stored tag and whether
	// it was created; an existing tag with the same commit is returned as is,
	// and one with a different commit fails with ErrConflict.
	CreateTag(ctx context.Context, tag SliceTag) (*SliceTag, bool, error)
	// ListTags returns a slice's tags, newest first.
	ListTags(ctx context.Context, sliceID string) ([]SliceTag, error)
}

// SliceRoles are the roles a slice member can have (design/26_slice_members.md).
var SliceRoles = []string{"admin", "writer", "reader"}

// SliceMember is a person or agent given a role on one slice.
type SliceMember struct {
	SliceID   string
	SubjectID string
	// Username is the member's own account; DisplayName its profile name.
	Username    string
	DisplayName string
	// Kind is the member's subject kind: user or agent.
	Kind string
	Role string
	// AddedBy is the username of whoever gave the role.
	AddedBy   string
	CreatedAt time.Time
}

// SharedSlice is a slice shared with someone, and their role on it.
type SharedSlice struct {
	Slice *corev1.Slice
	Role  string
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
	// ListDaemons lists the daemons the subject registered.
	ListDaemons(ctx context.Context, subjectID string) ([]*corev1.AgentDaemon, error)
	// ListDaemonsForAccounts lists the daemons registered under the accounts.
	ListDaemonsForAccounts(ctx context.Context, accounts []string) ([]*corev1.AgentDaemon, error)
	// DaemonSubject returns the subject that registered the daemon.
	DaemonSubject(ctx context.Context, daemonID string) (string, error)
	// RenameDaemon sets the name a daemon is shown by ("" clears it), keeping
	// the name it registered with. ErrConflict when another daemon of the
	// account is shown by that name.
	RenameDaemon(ctx context.Context, daemonID, displayName string) (*corev1.AgentDaemon, error)
	// ExposeDaemon exposes the daemon to the slice, so the slice's owners may
	// choose it as their CI daemon. Exposing it again changes nothing.
	ExposeDaemon(ctx context.Context, daemonID, sliceID, exposedBy string) error
	// UnexposeDaemon takes the slice off the daemon's list. It is not an error
	// if the daemon was not exposed to it.
	UnexposeDaemon(ctx context.Context, daemonID, sliceID string) error
	// DaemonExposedTo reports whether the daemon is exposed to the slice.
	DaemonExposedTo(ctx context.Context, daemonID, sliceID string) (bool, error)
	// ListDaemonExposures returns the ids of the slices each daemon is exposed to.
	ListDaemonExposures(ctx context.Context, daemonIDs []string) (map[string][]string, error)
	// ListDaemonsExposedToSlice lists the daemons exposed to the slice.
	ListDaemonsExposedToSlice(ctx context.Context, sliceID string) ([]*corev1.AgentDaemon, error)

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

// ChangesetListFilter narrows and pages a list of changesets.
type ChangesetListFilter struct {
	// Slice limits the list to changesets authored against it; nil is all.
	Slice *corev1.SliceRef
	// Statuses limits the list to changesets in one of these statuses; empty
	// is every status.
	Statuses []string
	// Query matches a title containing it (any case) or an id starting with it.
	Query string
	// AuthorSubjectID limits the list to one author's changesets when
	// FilterAuthor is set (an author nobody has is "" and matches none).
	AuthorSubjectID string
	FilterAuthor    bool
	// Limit is 1 to 200; anything else is 50.
	Limit int
	// PageToken is the previous page's token.
	PageToken string
	// Summary leaves out each changeset's patchsets.
	Summary bool
}

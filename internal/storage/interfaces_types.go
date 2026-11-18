package storage

import (
	"time"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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
var AccountRoles = model.AccountRoles

// AccountRoleRank orders roles (lower is more privileged); unknown roles rank
// after every known one.
func AccountRoleRank(role string) int {
	return model.AccountRoleRank(role)
}

// BlobUpsert is one blob for BlobStore.UpsertMany.
type BlobUpsert struct {
	ID              string
	ContentHash     string
	Size            int64
	StorageLocation string
}

type OutboxProcessResult struct {
	Processed int
	Failed    int
}

type CommitListPage struct {
	Commits       []*corev1.Commit
	NextPageToken string
}

// CommitChain is a segment of a ref's first-parent history, read in one
// consistent snapshot. It is built from the commit rows themselves, not from
// asynchronously derived indexes, so it never lags the ref.
type CommitChain struct {
	HeadCommitID string
	FoundStop    bool
	Commits      []*corev1.Commit
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

// SliceRoles are the roles a slice member can have (design/26_slice_members.md,
// design/36_multi_agent_collaboration.md).
var SliceRoles = model.SliceRoles

// SliceRoleRank orders slice roles (lower is more privileged); unknown roles
// rank after every known one.
func SliceRoleRank(role string) int {
	return model.SliceRoleRank(role)
}

// SliceMember is a person or agent given a role on one slice.
type SliceMember struct {
	SliceID     string
	SubjectID   string
	Username    string
	DisplayName string
	Kind        string
	Role        string
	AddedBy     string
	CreatedAt   time.Time
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
	Attribution       ActorAttribution
}

// ValidSliceSecretName reports whether name matches ^[A-Z_][A-Z0-9_]*$.
func ValidSliceSecretName(name string) bool {
	return model.ValidSliceSecretName(name)
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
	Role      string
}

// ConversationFilter narrows ListConversations. Empty fields are ignored.
type ConversationFilter struct {
	SliceID   string
	DaemonID  string
	SubjectID string
}

// CheckRunInput is the creation payload for a persisted CI/self-check run.
type CheckRunInput struct {
	ChangesetID  string
	PatchsetID   string
	CheckName    string
	DaemonID     string
	Provenance   string
	Status       string
	TriggerRunID string
}

// PatchsetSecretAllowance records that a reviewer allowed a specific
// patchset's CI checks to run with folder/account secrets (design/36 §1).
type PatchsetSecretAllowance struct {
	ChangesetID        string
	PatchsetID         string
	AllowedBySubjectID string
	CreatedAt          time.Time
	Attribution        ActorAttribution
}

// ChangesetListFilter narrows and pages a list of changesets.
type ChangesetListFilter struct {
	Slice           *corev1.SliceRef
	Statuses        []string
	Query           string
	AuthorSubjectID string
	FilterAuthor    bool
	Limit           int
	PageToken       string
	Summary         bool
}

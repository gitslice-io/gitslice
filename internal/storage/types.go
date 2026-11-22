package storage

import (
	"errors"
	"time"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const DefaultTargetRef = model.DefaultTargetRef

var (
	ErrAlreadyExists     = errors.New("already exists")
	ErrConflict          = errors.New("conflict")
	ErrInvalid           = model.ErrInvalid
	ErrNotFound          = errors.New("not found")
	ErrResourceExhausted = errors.New("resource exhausted")
	ErrUnauthenticated   = errors.New("unauthenticated")
	ErrUnauthorized      = errors.New("unauthorized")
)

type Subject struct {
	ID                  string
	Kind                string
	ControllerSubjectID string
	DisplayName         string
}

// APIKeyPrefix marks a long-lived API key (see design/20_agent_signup_and_claim.md).
// SubjectForToken resolves tokens with this prefix against api keys rather than
// login sessions.
const APIKeyPrefix = model.APIKeyPrefix

// RegisterAgentInput is the request to self-register an agent subject with its
// own personal account. OwnerEmail names the human who may later claim
// co-ownership; it is not verified at registration time.
type RegisterAgentInput struct {
	Username    string
	OwnerEmail  string
	DisplayName string
	// RegistrationToken, when set, makes registration idempotent: a retry with
	// the same token, username and owner email resumes the existing agent.
	RegistrationToken string
	// Optional slice + role the agent asks its human owner to grant on claim.
	RequestedSliceAccount string
	RequestedSliceSlug    string
	RequestedSliceRole    string
}

// MinRegistrationTokenLength is the minimum length of a client-generated
// registration token; it is a secret, so it must not be guessable.
const MinRegistrationTokenLength = model.MinRegistrationTokenLength

// AgentInviteTokenPrefix marks a single-use human-minted agent join token.
const (
	AgentInviteTokenPrefix = "gsi_"
	DefaultAgentInviteTTL  = 24 * time.Hour
	MaxAgentInviteTTL      = 7 * 24 * time.Hour
	MaxActiveAgentInvites  = 50
)

// PendingClaim is an unclaimed agent registration that a signed-in human may
// accept because its owner email is one of their verified emails.
type PendingClaim struct {
	AgentSubjectID        string
	AgentDisplayName      string
	Account               string
	OwnerEmail            string
	CreatedAt             time.Time
	RequestedSliceAccount string
	RequestedSliceSlug    string
	RequestedSliceRole    string
}

// AgentInvite is a short-lived, single-use join invite minted by a human owner.
type AgentInvite struct {
	ID                string
	CreatedBy         string
	SliceID           string
	SliceAccount      string
	SliceSlug         string
	SliceRole         string
	SuggestedUsername string
	ExpiresAt         time.Time
	CreatedAt         time.Time
	ConsumedAt        time.Time
	ConsumedByAccount string
	RevokedAt         time.Time
}

type CreateAgentInviteInput struct {
	ActorID           string
	SliceID           string
	SliceAccount      string
	SliceSlug         string
	SliceRole         string
	SuggestedUsername string
	ExpiresAt         time.Time
}

type CreateAgentInviteResult struct {
	Invite AgentInvite
	Token  string
}

type JoinAgentInviteInput struct {
	Token       string
	Username    string
	DisplayName string
}

type JoinAgentInviteResult struct {
	AgentSubjectID string
	Account        string
	APIKey         string
	SliceAccount   string
	SliceSlug      string
	SliceRole      string
}

// OwnedAgent is a self-registered agent whose personal account a human owns.
// ClaimedAt and LastActiveAt are zero when unknown.
type OwnedAgent struct {
	AgentSubjectID   string
	AgentDisplayName string
	Account          string
	RegisteredAt     time.Time
	ClaimedAt        time.Time
	LastActiveAt     time.Time
}

// Identity providers recorded by EnsureExternalSubject and used as
// SetVerifiedEmails sources.
const (
	ProviderClerk   = "clerk"
	ProviderService = "service"
)

// RegisteredAgent is the result of RegisterAgent. APIKey is the plaintext key and
// is only available here; stores keep only its hash.
// APIKey is the record of one API key (user or agent). It never holds the secret.
type APIKey struct {
	ID             string
	SubjectID      string
	Name           string
	CreatedAt      time.Time
	LastUsedAt     time.Time // zero until first used
	ExpiresAt      time.Time // zero: no expiry
	RevokedAt      time.Time // zero unless revoked
	HasSliceScope  bool
	SliceIDs       []string
	MaxRole        string
	ParentKeyID    string
	Purpose        string
	DaemonID       string
	ConversationID string
}

// CreateAPIKeyInput specifies the parameters for issuing an API key.
type CreateAPIKeyInput struct {
	ActorID        string
	AgentAccount   string
	Name           string
	HasSliceScope  bool
	SliceIDs       []string
	MaxRole        string
	ExpiresAt      time.Time // zero: no explicit expiry requested (inherits parent if any)
	ParentKeyID    string    // set when minted via another API key
	Purpose        string
	DaemonID       string
	ConversationID string
}

// Active reports whether the key still authenticates at now.
func (k APIKey) Active(now time.Time) bool {
	return k.RevokedAt.IsZero() && (k.ExpiresAt.IsZero() || now.Before(k.ExpiresAt))
}

// MaxActiveAPIKeys caps the keys an agent can hold at once.
const MaxActiveAPIKeys = 20

// MaxAPIKeyNameLength is the longest label an API key can have.
const MaxAPIKeyNameLength = model.MaxAPIKeyNameLength

type RegisteredAgent struct {
	SubjectID string
	Account   string
	APIKey    string
	// Resumed is true when an earlier registration with the same token was
	// found and a fresh key was issued for it.
	Resumed bool
}

// CreateDelegatedAgentInput specifies parameters for creating a user-owned,
// controller-bound agent account and its initial API key.
type CreateDelegatedAgentInput struct {
	ActorID     string
	Username    string
	DisplayName string
	SliceID     string
	SliceRole   string
}

// CreateDelegatedAgentResult describes the newly created delegated agent.
type CreateDelegatedAgentResult struct {
	AgentSubjectID string
	Account        string
	Key            APIKey
	Secret         string
	Role           string
}

type FileEntry struct {
	Path        string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
}

type TreeEntry struct {
	Path        string
	Name        string
	Kind        string
	Mode        uint32
	TreeID      string
	BlobID      string
	ContentHash string
	Size        int64
}

type PathHead struct {
	Path             string
	Exists           bool
	EntryFingerprint string
	BlobID           string
	ContentHash      string
	Mode             uint32
	Size             int64
}

type GitImportRecord struct {
	ID                  string
	SubjectID           string
	Source              string
	MountPath           string
	AuthoringAccount    string
	AuthoringSlice      string
	AuthoringSliceID    string
	TargetRef           string
	Mode                string
	Status              string
	TotalCommits        int
	ImportedCount       int
	LastGitCommitID     string
	FinalNativeCommitID string
}

type GitImportedCommitRecord struct {
	ImportID         string
	GitCommitID      string
	NativeCommitID   string
	Message          string
	Position         int
	ChangedPathCount int
	// Original Git metadata. Commits imported before it was recorded have
	// empty values.
	AuthorName  string
	AuthorEmail string
	AuthoredAt  string // RFC 3339; empty when unknown
	FullMessage string
}

type GitImportCommitSpec struct {
	GitCommitID  string
	Position     int
	Title        string
	Description  string
	FullMessage  string
	AuthorName   string
	AuthorEmail  string
	AuthoredAt   string
	ChangedPaths []string
	FileEdits    []*corev1.FileEdit
}

type GitImportBatchInput struct {
	ImportID  string
	SubjectID string
	SliceID   string
	TargetRef string
	Commits   []GitImportCommitSpec
}

type GitImportPublishedCommit struct {
	GitCommitID    string
	NativeCommitID string
	Message        string
}

type HistoryEntityRef struct {
	AccountID string
	EntityID  string
}

type CurrentPathEntity struct {
	Path        string
	AccountID   string
	EntityID    string
	Kind        string
	ContentHash string
	Mode        uint32
}

// PathIntent records an advisory reservation on a canonical path or glob
// pattern inside a slice (design/36 section 6).
type PathIntent struct {
	ID          string
	SliceID     string
	Account     string
	Slice       string
	SubjectID   string
	Pattern     string
	Message     string
	ChangesetID string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

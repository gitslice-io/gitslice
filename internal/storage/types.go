package storage

import "errors"

const DefaultTargetRef = "refs/global/main"

var (
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid")
	ErrNotFound        = errors.New("not found")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUnauthorized    = errors.New("unauthorized")
)

type Subject struct {
	ID          string
	DisplayName string
}

// APIKeyPrefix marks a long-lived API key (see design/20_agent_signup_and_claim.md).
// SubjectForToken resolves tokens with this prefix against api keys rather than
// login sessions.
const APIKeyPrefix = "gsk_"

// RegisterAgentInput is the request to self-register an agent subject with its
// own personal account. OwnerEmail names the human who may later claim
// co-ownership; it is not verified at registration time.
type RegisterAgentInput struct {
	Username    string
	OwnerEmail  string
	DisplayName string
}

// RegisteredAgent is the result of RegisterAgent. APIKey is the plaintext key and
// is only available here; stores keep only its hash.
type RegisteredAgent struct {
	SubjectID string
	Account   string
	APIKey    string
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

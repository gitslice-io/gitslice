package postgres

const DefaultTargetRef = "refs/global/main"

type Subject struct {
	ID          string
	DisplayName string
}

type FileEntry struct {
	Path        string
	BlobID      string
	ContentHash string
	Mode        uint32
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

type pendingPublishRow struct {
	ID          string
	ChangesetID string
	PatchsetID  string
	TargetRef   string
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

type scanner interface {
	Scan(dest ...any) error
}

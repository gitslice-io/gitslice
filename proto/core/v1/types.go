package corev1

import "time"

type LoginRequest struct {
	DevUser string `json:"dev_user,omitempty"`
}

type LoginResponse struct {
	Token     string `json:"token,omitempty"`
	SubjectID string `json:"subject_id,omitempty"`
}

type Empty struct{}

type SliceRef struct {
	Account string `json:"account,omitempty"`
	Slice   string `json:"slice,omitempty"`
}

type Ref struct {
	Name      string    `json:"name,omitempty"`
	CommitID  string    `json:"commit_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

type Commit struct {
	ID           string    `json:"id,omitempty"`
	ParentIDs    []string  `json:"parent_ids,omitempty"`
	RootTreeID   string    `json:"root_tree_id,omitempty"`
	Author       string    `json:"author,omitempty"`
	Message      string    `json:"message,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	ChangedPaths []string  `json:"changed_paths,omitempty"`
}

type EntryKind string

const (
	EntryKindUnspecified EntryKind = ""
	EntryKindFile        EntryKind = "file"
	EntryKindDirectory   EntryKind = "directory"
	EntryKindSymlink     EntryKind = "symlink"
)

type TreeEntry struct {
	Path          string    `json:"path,omitempty"`
	Name          string    `json:"name,omitempty"`
	Kind          EntryKind `json:"kind,omitempty"`
	Mode          uint32    `json:"mode,omitempty"`
	TreeID        string    `json:"tree_id,omitempty"`
	BlobID        string    `json:"blob_id,omitempty"`
	SymlinkTarget string    `json:"symlink_target,omitempty"`
	Size          int64     `json:"size,omitempty"`
	ContentHash   string    `json:"content_hash,omitempty"`
}

type ResolvePathRequest struct {
	CommitID string `json:"commit_id,omitempty"`
	Path     string `json:"path,omitempty"`
}

type ResolvePathResponse struct {
	Entry *TreeEntry `json:"entry,omitempty"`
}

type ListDirectoryRequest struct {
	CommitID string `json:"commit_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	PageSize int32  `json:"page_size,omitempty"`
}

type ListDirectoryResponse struct {
	Entries    []*TreeEntry `json:"entries,omitempty"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type ReadFileRequest struct {
	CommitID string `json:"commit_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Length   int64  `json:"length,omitempty"`
}

type ReadFileResponse struct {
	Data        []byte `json:"data,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

type GetCommitRequest struct {
	CommitID string `json:"commit_id,omitempty"`
}

type GetRefRequest struct {
	RefName string `json:"ref_name,omitempty"`
}

type GetBlobStatusRequest struct {
	ContentHashes []string `json:"content_hashes,omitempty"`
}

type BlobRecord struct {
	ID              string `json:"id,omitempty"`
	ContentHash     string `json:"content_hash,omitempty"`
	Size            int64  `json:"size,omitempty"`
	StorageLocation string `json:"storage_location,omitempty"`
	State           string `json:"state,omitempty"`
}

type GetBlobStatusResponse struct {
	Blobs []*BlobRecord `json:"blobs,omitempty"`
}

type UploadBlobRequest struct {
	ContentHash string `json:"content_hash,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type UploadBlobResponse struct {
	BlobID      string `json:"blob_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type Slice struct {
	ID             string           `json:"id,omitempty"`
	Ref            *SliceRef        `json:"ref,omitempty"`
	Definition     *SliceDefinition `json:"definition,omitempty"`
	DefinitionHash string           `json:"definition_hash,omitempty"`
}

type SliceDefinition struct {
	SliceID       string   `json:"slice_id,omitempty"`
	Version       int64    `json:"version,omitempty"`
	IncludedPaths []string `json:"included_paths,omitempty"`
	Visibility    string   `json:"visibility,omitempty"`
}

type ResolveSliceRequest struct {
	Ref *SliceRef `json:"ref,omitempty"`
}

type GetSliceRequest struct {
	SliceID string `json:"slice_id,omitempty"`
}

type ListSlicesRequest struct {
	Account  string `json:"account,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	PageSize int32  `json:"page_size,omitempty"`
}

type ListSlicesResponse struct {
	Slices     []*Slice `json:"slices,omitempty"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type UpdateSliceDefinitionRequest struct {
	SliceID                string           `json:"slice_id,omitempty"`
	ExpectedDefinitionHash string           `json:"expected_definition_hash,omitempty"`
	Definition             *SliceDefinition `json:"definition,omitempty"`
}

type WorkspaceRef struct {
	ID string `json:"id,omitempty"`
}

type WorkspaceState struct {
	Ref                *WorkspaceRef `json:"ref,omitempty"`
	Slice              *SliceBinding `json:"slice,omitempty"`
	HydratedPaths      []string      `json:"hydrated_paths,omitempty"`
	BaseCommitID       string        `json:"base_commit_id,omitempty"`
	CurrentChangesetID string        `json:"current_changeset_id,omitempty"`
	CurrentPatchsetID  string        `json:"current_patchset_id,omitempty"`
}

type SliceBinding struct {
	Slice               *SliceRef `json:"slice,omitempty"`
	SliceID             string    `json:"slice_id,omitempty"`
	SliceDefinitionHash string    `json:"slice_definition_hash,omitempty"`
}

type GetWorkspaceStateRequest struct {
	Workspace *WorkspaceRef `json:"workspace,omitempty"`
}

type HydratePathsRequest struct {
	Workspace *WorkspaceRef `json:"workspace,omitempty"`
	Paths     []string      `json:"paths,omitempty"`
	Mode      string        `json:"mode,omitempty"`
}

type HydratePathsResponse struct {
	Path  string     `json:"path,omitempty"`
	Entry *TreeEntry `json:"entry,omitempty"`
	Data  []byte     `json:"data,omitempty"`
}

type ValidateWorkspaceDiffRequest struct {
	Workspace    *WorkspaceRef `json:"workspace,omitempty"`
	BaseCommitID string        `json:"base_commit_id,omitempty"`
	FileEdits    []*FileEdit   `json:"file_edits,omitempty"`
}

type ValidateWorkspaceDiffResponse struct {
	AffectedPaths      []string            `json:"affected_paths,omitempty"`
	Coverage           []*PathCoverage     `json:"coverage,omitempty"`
	SubmitRequirements *SubmitRequirements `json:"submit_requirements,omitempty"`
	PathBases          []*PathBase         `json:"path_bases,omitempty"`
	ReadSet            []*PathSetEntry     `json:"read_set,omitempty"`
	WriteSet           []*PathSetEntry     `json:"write_set,omitempty"`
}

type WorkspaceOperation struct {
	ID            string        `json:"id,omitempty"`
	Workspace     *WorkspaceRef `json:"workspace,omitempty"`
	OperationType string        `json:"operation_type,omitempty"`
	Description   string        `json:"description,omitempty"`
	CreatedAt     time.Time     `json:"created_at,omitempty"`
	Actor         string        `json:"actor,omitempty"`
	AffectedPaths []string      `json:"affected_paths,omitempty"`
	ChangesetID   string        `json:"changeset_id,omitempty"`
	PatchsetID    string        `json:"patchset_id,omitempty"`
}

type RecordWorkspaceOperationRequest struct {
	Operation *WorkspaceOperation `json:"operation,omitempty"`
}

type RecordWorkspaceOperationResponse struct {
	OperationID string `json:"operation_id,omitempty"`
}

type Changeset struct {
	ID                    string              `json:"id,omitempty"`
	AuthoringSlice        *SliceRef           `json:"authoring_slice,omitempty"`
	Author                string              `json:"author,omitempty"`
	TargetRef             string              `json:"target_ref,omitempty"`
	BaseCommitID          string              `json:"base_commit_id,omitempty"`
	Title                 string              `json:"title,omitempty"`
	Description           string              `json:"description,omitempty"`
	Patchsets             []*Patchset         `json:"patchsets,omitempty"`
	CurrentPatchsetID     string              `json:"current_patchset_id,omitempty"`
	CurrentPatchsetNumber int64               `json:"current_patchset_number,omitempty"`
	Status                string              `json:"status,omitempty"`
	AffectedPaths         []string            `json:"affected_paths,omitempty"`
	SubmitRequirements    *SubmitRequirements `json:"submit_requirements,omitempty"`
}

type Patchset struct {
	ID                 string              `json:"id,omitempty"`
	ChangesetID        string              `json:"changeset_id,omitempty"`
	Number             int64               `json:"number,omitempty"`
	BaseCommitID       string              `json:"base_commit_id,omitempty"`
	Author             string              `json:"author,omitempty"`
	CreatedAt          time.Time           `json:"created_at,omitempty"`
	ChangedPaths       []string            `json:"changed_paths,omitempty"`
	FileEdits          []*FileEdit         `json:"file_edits,omitempty"`
	Coverage           []*PathCoverage     `json:"coverage,omitempty"`
	SubmitRequirements *SubmitRequirements `json:"submit_requirements,omitempty"`
	PathBases          []*PathBase         `json:"path_bases,omitempty"`
	ReadSet            []*PathSetEntry     `json:"read_set,omitempty"`
	WriteSet           []*PathSetEntry     `json:"write_set,omitempty"`
}

type FileEdit struct {
	Op          string `json:"op,omitempty"`
	Path        string `json:"path,omitempty"`
	OldPath     string `json:"old_path,omitempty"`
	BlobID      string `json:"blob_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
}

type PathCoverage struct {
	Path             string   `json:"path,omitempty"`
	CoveringSliceIDs []string `json:"covering_slice_ids,omitempty"`
}

type PathBase struct {
	Path             string `json:"path,omitempty"`
	BaseCommitID     string `json:"base_commit_id,omitempty"`
	Exists           bool   `json:"exists,omitempty"`
	EntryKind        string `json:"entry_kind,omitempty"`
	Mode             uint32 `json:"mode,omitempty"`
	BlobID           string `json:"blob_id,omitempty"`
	ContentHash      string `json:"content_hash,omitempty"`
	TreeID           string `json:"tree_id,omitempty"`
	SymlinkTarget    string `json:"symlink_target,omitempty"`
	EntryFingerprint string `json:"entry_fingerprint,omitempty"`
	Check            string `json:"check,omitempty"`
}

type PathSetEntry struct {
	Path      string `json:"path,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
}

type SubmitRequirements struct {
	RequiredApprovals         []string `json:"required_approvals,omitempty"`
	RequiredChecks            []string `json:"required_checks,omitempty"`
	PathLockIDs               []string `json:"path_lock_ids,omitempty"`
	SourceSliceDefinitionHash string   `json:"source_slice_definition_hash,omitempty"`
	SourcePathLockSetHash     string   `json:"source_path_lock_set_hash,omitempty"`
}

type CreateChangesetRequest struct {
	AuthoringSlice *SliceRef `json:"authoring_slice,omitempty"`
	TargetRef      string    `json:"target_ref,omitempty"`
	BaseCommitID   string    `json:"base_commit_id,omitempty"`
	Title          string    `json:"title,omitempty"`
	Description    string    `json:"description,omitempty"`
}

type GetChangesetRequest struct {
	ChangesetID string `json:"changeset_id,omitempty"`
}

type UpdateChangesetRequest struct {
	ChangesetID               string      `json:"changeset_id,omitempty"`
	ExpectedCurrentPatchsetID string      `json:"expected_current_patchset_id,omitempty"`
	BaseCommitID              string      `json:"base_commit_id,omitempty"`
	FileEdits                 []*FileEdit `json:"file_edits,omitempty"`
}

type SubmitChangesetRequest struct {
	ChangesetID               string `json:"changeset_id,omitempty"`
	ExpectedCurrentPatchsetID string `json:"expected_current_patchset_id,omitempty"`
}

type SubmitChangesetResponse struct {
	CommitID       string `json:"commit_id,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	NewRefCommitID string `json:"new_ref_commit_id,omitempty"`
}

type AbandonChangesetRequest struct {
	ChangesetID string `json:"changeset_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

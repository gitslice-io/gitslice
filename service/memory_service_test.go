package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestServicesRunAgainstInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("hello\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := handlers.Blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{uploaded.ContentHash, "sha256:missing"}, Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blobs) != 2 || status.Blobs[0].State != "available" || status.Blobs[1].State != "missing" {
		t.Fatalf("unexpected blob status: %#v", status.Blobs)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "add note",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/notes.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(patchset.PathBases) != 1 || patchset.PathBases[0].Exists {
		t.Fatalf("unexpected patchset path bases: %#v", patchset.PathBases)
	}
	if patchset.ResultTreeId == "" {
		t.Fatal("patchset result tree id is empty")
	}
	previewRead, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{RootTreeId: patchset.ResultTreeId, Path: "/acme/notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(previewRead.Data) != "hello\n" {
		t.Fatalf("preview read data = %q", string(previewRead.Data))
	}
	previewListed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{RootTreeId: patchset.ResultTreeId, Path: "/acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(previewListed.Entries) != 1 || previewListed.Entries[0].Name != "notes.txt" {
		t.Fatalf("unexpected preview directory entries: %#v", previewListed.Entries)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if published, err := mem.Changesets.PublishPending(ctx, 10); err != nil || published != 1 {
		t.Fatalf("PublishPending = %d, %v; want 1, nil", published, err)
	}

	ref, err = handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != "hello\n" {
		t.Fatalf("read data = %q", string(read.Data))
	}
	listed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "notes.txt" {
		t.Fatalf("unexpected directory entries: %#v", listed.Entries)
	}

	state, err := handlers.Workspace.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: &corev1.WorkspaceRef{Id: "acme/home"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId != ref.CommitId || state.Slice.SliceId == "" {
		t.Fatalf("unexpected workspace state: %#v", state)
	}
}

func TestSimpleServiceMethodsUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()

	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authStatus, err := handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.SubjectId != "user_alice" {
		t.Fatalf("auth subject = %q, want user_alice", authStatus.SubjectId)
	}

	mem.AddAccountRole("user_alice", "alice", "admin")
	authStatus, err = handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(authStatus.Accounts) < 2 || authStatus.Accounts[0] != "alice" {
		t.Fatalf("auth accounts = %#v, want personal account first", authStatus.Accounts)
	}

	data := []byte("read me\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_readme", []storage.FileEntry{{
		Path:        "/acme/readme.txt",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/readme.txt"})

	resolved, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: "commit_readme", Path: "/acme/readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("unexpected resolved entry: %#v", resolved.Entry)
	}
	commit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: "commit_readme"})
	if err != nil {
		t.Fatal(err)
	}
	if commit.Id != "commit_readme" {
		t.Fatalf("commit id = %q", commit.Id)
	}
	hydrated, err := handlers.Workspace.HydratePaths(ctx, &corev1.HydratePathsRequest{Paths: []string{"/acme/readme.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated.Data) != "read me\n" {
		t.Fatalf("hydrated data = %q", string(hydrated.Data))
	}
	validated, err := handlers.Workspace.ValidateWorkspaceDiff(ctx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/home"},
		BaseCommitId: "commit_readme",
		FileEdits:    []*corev1.FileEdit{{Op: "delete", Path: "/acme/readme.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.PathBases) != 1 || !validated.PathBases[0].Exists {
		t.Fatalf("unexpected validation path bases: %#v", validated.PathBases)
	}
	recorded, err := handlers.Workspace.RecordWorkspaceOperation(ctx, &corev1.RecordWorkspaceOperationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.OperationId == "" {
		t.Fatalf("empty operation id")
	}
}

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestServicesRunAgainstInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := handlers.Blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{uploaded.ContentHash, "sha256:missing"}})
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

func TestSliceServiceUsesInMemoryRepositoryValidation(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	_, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "missing"},
		IncludedPaths: []string{"/acme/missing"},
		Visibility:    "account",
	})
	if err == nil || !strings.Contains(err.Error(), "included path does not exist") {
		t.Fatalf("CreateSlice missing path error = %v", err)
	}

	data := []byte("package payment\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_payment", []storage.FileEntry{{
		Path:        "/acme/payment/main.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/payment/main.go"})

	slice, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "payment"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if slice.Ref.Account != "acme" || slice.Ref.Slice != "payment" {
		t.Fatalf("unexpected slice: %#v", slice)
	}
}

func TestChangesetDiffListAndAbandonUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	oldData := []byte("old\n")
	oldHash := objectid.RawContentHash(oldData)
	mem.PutObject(filesystem.BlobKey(oldHash), oldData)
	mem.PutCommitWithFiles("commit_old", []storage.FileEntry{{
		Path:        "/acme/payment/value.txt",
		BlobID:      objectid.BlobID(oldData),
		ContentHash: oldHash,
		Mode:        0o100644,
		Size:        int64(len(oldData)),
	}}, []string{"/acme/payment/value.txt"})
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "account")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("new\n")})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "update value",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/value.txt",
			BlobId:      newBlob.BlobId,
			ContentHash: newBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{ChangesetId: cs.Id, Patchset: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-old") || !strings.Contains(diff.Diff, "+new") {
		t.Fatalf("diff did not include old/new content:\n%s", diff.Diff)
	}
	listed, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Changesets) != 1 || listed.Changesets[0].Id != cs.Id {
		t.Fatalf("unexpected changeset list: %#v", listed.Changesets)
	}
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	abandoned, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Status != "abandoned" {
		t.Fatalf("status = %q, want abandoned", abandoned.Status)
	}
}

func TestSliceCRUDAndCommitHistoryUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	data := []byte("package api\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_api", []storage.FileEntry{{
		Path:        "/acme/payment/api/main.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/payment/api/main.go"})

	slice, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "api"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "account",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !sliceListContains(listed.Slices, "api") {
		t.Fatalf("ListSlices did not include api: %#v", listed.Slices)
	}
	definition, err := handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/api"},
			Visibility:    "account",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(definition.IncludedPaths, ",") != "/acme/payment/api" {
		t.Fatalf("updated included paths = %#v", definition.IncludedPaths)
	}
	history, err := handlers.Repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		Path:  "/acme/payment/api",
		Slice: &corev1.SliceRef{Account: "acme", Slice: "api"},
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Commits) != 1 || history.Commits[0].Id != "commit_api" {
		t.Fatalf("unexpected commit history: %#v", history.Commits)
	}
	if _, err := handlers.Slice.DeleteSlice(ctx, &corev1.DeleteSliceRequest{SliceId: slice.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: slice.Id}); err == nil {
		t.Fatalf("GetSlice after delete succeeded")
	}
}

func TestFakeSignupUsesInMemoryAuthAndCreatesHomeSlice(t *testing.T) {
	_, handlers := newMemoryHandlers()
	res, err := handlers.FakeAccount.ApproveSignup(context.Background(), &corev1.ApproveSignupRequest{
		Username:    "nic",
		State:       "state-1",
		CallbackUrl: "http://127.0.0.1:12345/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.SubjectId != "user_nic" || !strings.Contains(res.RedirectUrl, "token=") {
		t.Fatalf("unexpected signup response: %#v", res)
	}
	ctx := authctx.WithSubjectID(context.Background(), res.SubjectId)
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nic", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(slice.Definition.IncludedPaths, ",") != "/nic" {
		t.Fatalf("home included paths = %#v", slice.Definition.IncludedPaths)
	}
}

func TestSimpleServiceMethodsUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()

	login, err := handlers.FakeAccount.Login(context.Background(), &corev1.LoginRequest{DevUser: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == "" || login.SubjectId != "user_alice" {
		t.Fatalf("unexpected login response: %#v", login)
	}
	ctx := authctx.WithSubjectID(context.Background(), login.SubjectId)
	authStatus, err := handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.SubjectId != login.SubjectId {
		t.Fatalf("auth subject = %q, want %q", authStatus.SubjectId, login.SubjectId)
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

func newMemoryHandlers() (*memory.Stores, *Handlers) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
	}, mem.Objects)
	return mem, handlers
}

func sliceListContains(slices []*corev1.Slice, slug string) bool {
	for _, slice := range slices {
		if slice.Ref != nil && slice.Ref.Slice == slug {
			return true
		}
	}
	return false
}

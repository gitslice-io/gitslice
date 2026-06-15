package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestSubmitRejectsPatchsetConflicts(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "conflicted sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		PatchsetKind: "sync",
		Conflicts: []*corev1.PatchsetConflict{{
			Path:          "/acme/conflict.txt",
			ConflictClass: "content",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "unresolved patchset conflicts") {
		t.Fatalf("SubmitChangeset error = %v, want unresolved conflict FailedPrecondition", err)
	}
}

func TestSliceServiceUsesInMemoryRepositoryValidation(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	_, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "missing"},
		IncludedPaths: []string{"/acme/missing"},
		Visibility:    "private",
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
		Visibility:    "private",
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
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("new\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
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
		Visibility:    "private",
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
			Visibility:    "private",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(definition.IncludedPaths, ",") != "/acme/payment/api" {
		t.Fatalf("updated included paths = %#v", definition.IncludedPaths)
	}
	definitionHistory, err := handlers.Slice.ListSliceDefinitionVersions(ctx, &corev1.ListSliceDefinitionVersionsRequest{SliceId: slice.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitionHistory.Versions) != 2 || definitionHistory.Versions[0].Version != 2 || definitionHistory.Versions[1].Version != 1 {
		t.Fatalf("unexpected definition history: %#v", definitionHistory.Versions)
	}
	for _, version := range definitionHistory.Versions {
		if version.CreatedBy != "user_alice" || version.CreatedAt == "" {
			t.Fatalf("unexpected definition history metadata: %#v", version)
		}
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
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/nic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("home account root entry = %#v, want directory", resolved.Entry)
	}
}

func TestUpdateSliceDefinitionRejectsHomeIncludedPathChange(t *testing.T) {
	_, handlers := newMemoryHandlers()
	res, err := handlers.FakeAccount.ApproveSignup(context.Background(), &corev1.ApproveSignupRequest{
		Username:    "nic",
		State:       "state-1",
		CallbackUrl: "http://127.0.0.1:12345/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), res.SubjectId)
	home, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nic", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Changing the home slice's included paths must be rejected.
	_, err = handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                home.Id,
		ExpectedDefinitionHash: home.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/nic", "/nic/extra"},
			Visibility:    home.Definition.Visibility,
		},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changing home included paths: got err=%v, want InvalidArgument", err)
	}

	// Other fields (visibility) may still change while paths are unchanged.
	updated, err := handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                home.Id,
		ExpectedDefinitionHash: home.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: home.Definition.IncludedPaths,
			Visibility:    "private",
		},
	})
	if err != nil {
		t.Fatalf("changing home visibility (paths unchanged): %v", err)
	}
	if updated.Visibility != "private" {
		t.Fatalf("home visibility = %q, want private", updated.Visibility)
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

func TestRepositoryListDirectoryPaginationUsesCursor(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("page\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	files := make([]storage.FileEntry, 0, 7)
	for i := range 7 {
		files = append(files, storage.FileEntry{
			Path:        fmt.Sprintf("/acme/page/file_%02d.txt", i),
			BlobID:      objectid.BlobID(data),
			ContentHash: hash,
			Mode:        0o100644,
			Size:        int64(len(data)),
		})
	}
	mem.PutCommitWithFiles("commit_page", files, nil)

	first, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %#v cursor=%q, want 3 entries and next cursor", first.Entries, first.NextCursor)
	}
	second, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
		Cursor:   first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 3 || second.NextCursor == "" {
		t.Fatalf("second page = %#v cursor=%q, want 3 entries and next cursor", second.Entries, second.NextCursor)
	}
	third, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
		Cursor:   second.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 1 || third.NextCursor != "" {
		t.Fatalf("third page = %#v cursor=%q, want final single entry", third.Entries, third.NextCursor)
	}
	if first.Entries[0].Name != "file_00.txt" || third.Entries[0].Name != "file_06.txt" {
		t.Fatalf("unexpected page ordering: first=%#v third=%#v", first.Entries, third.Entries)
	}
}

func TestRepositoryReadFileRejectsNegativeRange(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("range\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_range", []storage.FileEntry{{
		Path:        "/acme/range.txt",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, nil)

	for _, req := range []*corev1.ReadFileRequest{
		{CommitId: "commit_range", Path: "/acme/range.txt", Offset: -1},
		{CommitId: "commit_range", Path: "/acme/range.txt", Length: -1},
	} {
		_, err := handlers.Repository.ReadFile(ctx, req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("ReadFile(%#v) error = %v, want InvalidArgument", req, err)
		}
	}
}

func TestCreateChangesetRejectsNonDefaultTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	_, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      "refs/global/branches/feature",
		Title:          "branch attempt",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateChangeset target ref error = %v, want InvalidArgument", err)
	}
}

func TestCreateChangesetDefaultsEmptyTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:          "omitted target ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.TargetRef != storage.DefaultTargetRef {
		t.Fatalf("CreateChangeset target ref = %q, want %q", cs.TargetRef, storage.DefaultTargetRef)
	}
}

func TestChangesetUpdateRejectsMalformedFileEdits(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit *corev1.FileEdit
	}{
		{name: "empty delete path", edit: &corev1.FileEdit{Op: "delete"}},
		{name: "empty mkdir path", edit: &corev1.FileEdit{Op: "mkdir"}},
		{name: "rename missing old path", edit: &corev1.FileEdit{Op: "rename", Path: "/acme/new.txt"}},
		{name: "rename missing new path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/old.txt"}},
		{name: "rename same path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/same.txt", Path: "/acme/same.txt"}},
		{name: "old path on upsert", edit: &corev1.FileEdit{Op: "upsert", OldPath: "/acme/old.txt", Path: "/acme/new.txt", BlobId: "blob_missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
				AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
				TargetRef:      storage.DefaultTargetRef,
				BaseCommitId:   ref.CommitId,
				Title:          tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
				ChangesetId:  cs.Id,
				BaseCommitId: ref.CommitId,
				FileEdits:    []*corev1.FileEdit{tc.edit},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("UpdateChangeset error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestChangesetUpdateValidatesAndHydratesBlobContentHash(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("blob metadata\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "blob metadata",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/wrong-hash.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: "sha256:not-the-uploaded-bytes",
			Mode:        0o100644,
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateChangeset wrong hash error = %v, want InvalidArgument", err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/hydrated-hash.txt",
			BlobId: uploaded.BlobId,
			Mode:   0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patchset.FileEdits[0].ContentHash; got != uploaded.ContentHash {
		t.Fatalf("patchset content hash = %q, want %q", got, uploaded.ContentHash)
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

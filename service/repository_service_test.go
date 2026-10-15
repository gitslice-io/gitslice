package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCommitAuthorsResolveToPersonalUsername(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_riley", "riley@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "riley"}); err != nil {
		t.Fatal(err)
	}

	// A commit authored by a known subject resolves to the username; an unknown
	// author (no personal account) is left as the raw subject id.
	authored := &corev1.Commit{Id: "commit_known", Author: subjectID}
	imported := &corev1.Commit{Id: "commit_unknown", Author: "user_ghost"}
	authorless := &corev1.Commit{Id: "commit_none"}
	if err := handlers.Repository.resolveCommitAuthors(ctx, authored, imported, authorless); err != nil {
		t.Fatal(err)
	}
	if authored.Author != "riley" {
		t.Fatalf("authored commit author = %q, want riley", authored.Author)
	}
	if imported.Author != "user_ghost" {
		t.Fatalf("unresolved commit author = %q, want user_ghost", imported.Author)
	}
	if authorless.Author != "" {
		t.Fatalf("authorless commit author = %q, want empty", authorless.Author)
	}
}

func TestPublicSliceReadsAllowAnonymousContext(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := context.Background()

	publicData := []byte("public readme\n")
	publicHash := objectid.RawContentHash(publicData)
	privateData := []byte("private secret\n")
	privateHash := objectid.RawContentHash(privateData)
	mem.PutObject(filesystem.BlobKey(publicHash), publicData)
	mem.PutObject(filesystem.BlobKey(privateHash), privateData)
	mem.PutCommitWithFiles("commit_public_slice", []storage.FileEntry{
		{
			Path:        "/acme/public/readme.txt",
			BlobID:      objectid.BlobID(publicData),
			ContentHash: publicHash,
			Mode:        0o100644,
			Size:        int64(len(publicData)),
		},
		{
			Path:        "/acme/private/secret.txt",
			BlobID:      objectid.BlobID(privateData),
			ContentHash: privateHash,
			Mode:        0o100644,
			Size:        int64(len(privateData)),
		},
	}, []string{"/acme/public/readme.txt", "/acme/private/secret.txt"})
	publicSlice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "public"}, []string{"/acme/public"}, "public")
	privateSlice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "private"}, []string{"/acme/private"}, "private")

	resolvedSlice, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: publicSlice.Id})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSlice.Definition.GetVisibility() != "public" {
		t.Fatalf("public slice visibility = %q", resolvedSlice.Definition.GetVisibility())
	}
	if _, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: privateSlice.Id}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("private anonymous GetSlice error = %v, want Unauthenticated", err)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.CommitId != "commit_public_slice" {
		t.Fatalf("ref commit = %q, want commit_public_slice", ref.CommitId)
	}
	listed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "readme.txt" {
		t.Fatalf("public listed entries = %#v, want readme.txt", listed.Entries)
	}
	resolvedPath, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPath.Entry.GetKind() != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("resolved public path = %#v", resolvedPath.Entry)
	}
	read, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != string(publicData) {
		t.Fatalf("public read data = %q", string(read.Data))
	}
	history, err := handlers.Repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		Slice: publicSlice.Ref,
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Commits) != 1 || history.Commits[0].Id != "commit_public_slice" {
		t.Fatalf("public history = %#v, want commit_public_slice", history.Commits)
	}
	authorCtx := authctx.WithSubjectID(context.Background(), "user_alice")
	nextData := []byte("public readme v2\n")
	uploaded, err := handlers.Blob.UploadBlob(authorCtx, &corev1.UploadBlobRequest{
		Data:  nextData,
		Slice: publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	changeset, err := handlers.Changeset.CreateChangeset(authorCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: publicSlice.Ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "public changeset",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(authorCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  changeset.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/public/readme.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	listedChangesets, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: publicSlice.Ref,
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listedChangesets.Changesets) != 1 || listedChangesets.Changesets[0].Id != changeset.Id {
		t.Fatalf("public changesets = %#v, want %s", listedChangesets.Changesets, changeset.Id)
	}
	gotChangeset, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changeset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotChangeset.Id != changeset.Id {
		t.Fatalf("public GetChangeset id = %q, want %q", gotChangeset.Id, changeset.Id)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: changeset.Id,
		Patchset:    patchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-public readme") || !strings.Contains(diff.Diff, "+public readme v2") {
		t.Fatalf("public changeset diff missing expected content:\n%s", diff.Diff)
	}
	_, err = handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: privateSlice.Ref,
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("private anonymous ListChangesets error = %v, want Unauthenticated", err)
	}

	_, err = handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/private/secret.txt",
		Slice:    publicSlice.Ref,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("outside public slice ResolvePath error = %v, want PermissionDenied", err)
	}
	_, err = handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous unscoped ReadFile error = %v, want Unauthenticated", err)
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

// TestRepositoryListDirectoryRecursive: a recursive listing returns
// everything under the path in path order, paged by path, and says so; the
// root is listed a level at a time.
func TestRepositoryListDirectoryRecursive(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("tree\n")
	hash := objectid.RawContentHash(data)
	var files []storage.FileEntry
	for _, p := range []string{"/acme/tree/a.txt", "/acme/tree/src/b.go", "/acme/tree/src/deep/c.go"} {
		files = append(files, storage.FileEntry{Path: p, BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))})
	}
	mem.PutCommitWithFiles("commit_tree", files, nil)

	var got []string
	cursor := ""
	for page := 0; ; page++ {
		res, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: "commit_tree", Path: "/acme/tree", PageSize: 2, Cursor: cursor, Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Recursive {
			t.Fatalf("page %d is not marked recursive", page)
		}
		for _, entry := range res.Entries {
			got = append(got, entry.Path)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	want := []string{"/acme/tree/a.txt", "/acme/tree/src", "/acme/tree/src/b.go", "/acme/tree/src/deep", "/acme/tree/src/deep/c.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive listing = %v, want %v", got, want)
	}

	root, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: "commit_tree", Path: "/", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if root.Recursive {
		t.Fatalf("the root was listed recursively: %v", root.Entries)
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

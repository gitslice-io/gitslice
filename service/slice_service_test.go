package service

import (
	"context"
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

// An account page lists a person's public slices for anyone; the account's own
// members also see the private ones.
func TestListSlicesShowsNonMembersOnlyPublicSlices(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")

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
	for name, visibility := range map[string]string{"secret": "private", "open": "public"} {
		if _, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
			Ref:           &corev1.SliceRef{Account: "acme", Slice: name},
			IncludedPaths: []string{"/acme/payment"},
			Visibility:    visibility,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for who, ctx := range map[string]context.Context{
		"a signed-in outsider": authctx.WithSubjectID(context.Background(), "user_bob"),
		"an anonymous visitor": context.Background(),
	} {
		listed, err := handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme"})
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		if !sliceListContains(listed.Slices, "open") || sliceListContains(listed.Slices, "secret") {
			t.Fatalf("%s should see only the public slice: %#v", who, listed.Slices)
		}
	}
	listed, err := handlers.Slice.ListSlices(alice, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !sliceListContains(listed.Slices, "open") || !sliceListContains(listed.Slices, "secret") {
		t.Fatalf("a member should see every slice: %#v", listed.Slices)
	}
}

func TestListSlicesPagesThroughAnAccountAndRejectsUnknownAccounts(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
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
	for _, name := range []string{"delta", "alpha", "charlie", "bravo", "echo"} {
		if _, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
			Ref:           &corev1.SliceRef{Account: "acme", Slice: name},
			IncludedPaths: []string{"/acme/payment"},
			Visibility:    "public",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var names []string
	cursor := ""
	pages := 0
	for {
		page, err := handlers.Slice.ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "acme", Cursor: cursor, PageSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, slice := range page.Slices {
			names = append(names, slice.Ref.Slice)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	// Only public slices count, and "payment" and "home" fixtures are not public.
	want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	if len(names) != len(want) || pages != 3 {
		t.Fatalf("paged names = %v in %d pages, want %v in 3", names, pages, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("paged names = %v, want %v", names, want)
		}
	}

	first, err := handlers.Slice.ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil || first.AccountKind == "" {
		t.Fatalf("ListSlices should say what the account is: %+v, %v", first, err)
	}

	_, err = handlers.Slice.ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "nobody-here"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("an unknown account should be not found, got %v", err)
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

func TestUpdateSliceDefinitionRejectsHomeIncludedPathChange(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_nico", "nico@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "nico"}); err != nil {
		t.Fatal(err)
	}
	home, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nico", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Changing the home slice's included paths must be rejected.
	_, err = handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                home.Id,
		ExpectedDefinitionHash: home.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/nico", "/nico/extra"},
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

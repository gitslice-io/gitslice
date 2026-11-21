package gitcompat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestTagRefConflicts(t *testing.T) {
	want := map[string]string{
		"refs/tags/payment":      "aaa",
		"refs/tags/api/v1.0.0":   "bbb",
		"refs/tags/standalone-1": "ccc",
	}
	cases := []struct {
		ref  string
		want bool
	}{
		{"refs/tags/payment", true},
		{"refs/tags/payment/v1.0.0", true},
		{"refs/tags/api", true},
		{"refs/tags/api/v1.0.0", true},
		{"refs/tags/api/v2.0.0", false},
		{"refs/tags/payment2/v1.0.0", false},
		{"refs/tags/v1.0.0", false},
	}
	for _, tc := range cases {
		if got := tagRefConflicts(want, tc.ref); got != tc.want {
			t.Errorf("tagRefConflicts(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestCollectProjectedTagsPublicHomeExcludesPrivateSlices(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	home, err := mem.Slices.Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "home"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.UpdateDefinition(ctx, owner, home.Id, home.DefinitionHash, &corev1.SliceDefinition{
		IncludedPaths: home.Definition.IncludedPaths,
		Visibility:    "public",
	}); err != nil {
		t.Fatal(err)
	}
	home, err = mem.Slices.Get(ctx, home.Id)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "pub"}, []string{"/acme/pub"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "priv"}, []string{"/acme/priv"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sliceID, name, commit string
	}{
		{home.Id, "v1.0.0", "c1"},
		{pub.Id, "v1.0.0", "c2"},
		{priv.Id, "secret-v1", "c3"},
	} {
		if _, _, err := mem.Slices.CreateTag(ctx, storage.SliceTag{
			SliceID:  tc.sliceID,
			Name:     tc.name,
			CommitID: tc.commit,
		}); err != nil {
			t.Fatal(err)
		}
	}

	p := &Projector{slices: mem.Slices}
	sources, err := p.collectProjectedTags(ctx, home)
	if err != nil {
		t.Fatalf("collectProjectedTags: %v", err)
	}
	got := map[string]projectedTagSource{}
	for _, s := range sources {
		got[s.name] = s
	}
	if _, leaked := got["priv/secret-v1"]; leaked {
		t.Fatalf("public home projection leaked private slice tag: %+v", sources)
	}
	if got["v1.0.0"].commitID != "c1" || !got["v1.0.0"].own {
		t.Fatalf("home own tag = %+v, want own c1", got["v1.0.0"])
	}
	if got["pub/v1.0.0"].commitID != "c2" || got["pub/v1.0.0"].own {
		t.Fatalf("public sub-slice tag = %+v, want non-own c2", got["pub/v1.0.0"])
	}
}

func TestHomeProjectionAggregatesSubSliceTags(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	home, err := mem.Slices.Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "home"})
	if err != nil {
		t.Fatal(err)
	}
	payment, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "backend"}, []string{"/acme/backend"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	var allFiles []storage.FileEntry
	commitFile := func(commitID, path, content string) {
		t.Helper()
		data := []byte(content)
		hash := objectid.RawContentHash(data)
		blobID := objectid.BlobID(data)
		mem.PutObject(filesystem.BlobKey(hash), data)
		if err := mem.Blobs.Upsert(ctx, blobID, hash, int64(len(data)), filesystem.BlobKey(hash)); err != nil {
			t.Fatal(err)
		}
		allFiles = append(allFiles, storage.FileEntry{
			Path:        path,
			BlobID:      blobID,
			ContentHash: hash,
			Mode:        0o100644,
			Size:        int64(len(data)),
		})
		mem.PutCommitWithFiles(commitID, append([]storage.FileEntry(nil), allFiles...), []string{path})
	}

	commitFile("c_pay", "/acme/payment/p.go", "package payment\n")
	commitFile("c_back", "/acme/backend/b.go", "package backend\n")

	for _, tc := range []struct {
		sliceID, name, commit string
	}{
		{payment.Id, "v1.0.0", "c_pay"},
		{payment.Id, "pay-snap", "c_pay"},
		{backend.Id, "v1.0.0", "c_back"},
		{backend.Id, "back-snap", "c_pay"},
		{home.Id, "home-v1", "c_back"},
		// Home's own tag literally matching a sub-slice namespaced tag wins.
		{home.Id, "payment/override", "c_back"},
		{payment.Id, "override", "c_pay"},
	} {
		if _, _, err := mem.Slices.CreateTag(ctx, storage.SliceTag{
			SliceID:  tc.sliceID,
			Name:     tc.name,
			CommitID: tc.commit,
		}); err != nil {
			t.Fatal(err)
		}
	}

	projector, err := NewProjector(ProjectorStores{
		Auth:       mem.Auth,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Blobs:      mem.Blobs,
	}, mem.Objects, filepath.Join(t.TempDir(), "git-cache"))
	if err != nil {
		t.Fatal(err)
	}

	repoPath, proj, err := projector.EnsureProjectedRepo(ctx, owner, "acme", "home")
	if err != nil {
		t.Fatalf("EnsureProjectedRepo(acme/home): %v", err)
	}
	rawTags, err := gitOutput(ctx, repoPath, nil, "tag", "-l")
	if err != nil {
		t.Fatalf("git tag -l: %v", err)
	}
	gotTags := strings.Fields(rawTags)
	wantTags := map[string]string{
		"home-v1":           "c_back",
		"payment/v1.0.0":    "c_pay",
		"payment/pay-snap":  "c_pay",
		"payment/override":  "c_back",
		"backend/v1.0.0":    "c_back",
		"backend/back-snap": "c_pay",
	}
	if len(gotTags) != len(wantTags) {
		t.Fatalf("projected home tags = %q, want %d tags", gotTags, len(wantTags))
	}
	for name, wantNative := range wantTags {
		gitSHA, err := gitOutput(ctx, repoPath, nil, "rev-parse", name+"^{commit}")
		if err != nil {
			t.Fatalf("rev-parse %s: %v", name, err)
		}
		nativeID, ok := proj.NativeCommitFor(strings.TrimSpace(gitSHA))
		if !ok || nativeID != wantNative {
			t.Errorf("tag %s -> native %q (ok=%v), want %q", name, nativeID, ok, wantNative)
		}
	}
}

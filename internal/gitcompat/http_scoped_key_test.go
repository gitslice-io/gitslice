package gitcompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestGitHTTPHandlerEnforcesPrincipalScopedAndRoleCappedKeys(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	allowedSlice, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "allowed"}, []string{"/acme/allowed"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "public-other"}, []string{"/acme/public-other"}, "public", 0, nil); err != nil {
		t.Fatal(err)
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

	_, readerSecret, _, _, err := mem.Auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:       owner,
		Name:          "git-reader",
		HasSliceScope: true,
		SliceIDs:      []string{allowedSlice.Id},
		MaxRole:       "reader",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, writerSecret, _, _, err := mem.Auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:       owner,
		Name:          "git-writer",
		HasSliceScope: true,
		SliceIDs:      []string{allowedSlice.Id},
		MaxRole:       "writer",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := NewHandlerWithPrincipal(func(ctx context.Context, token string) (*authctx.Principal, error) {
		return mem.Auth.AuthenticateToken(ctx, token)
	}, projector, nil, nil)

	doReq := func(method, target, token string) int {
		req := httptest.NewRequest(method, target, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Scoped reader key reads its scoped slice, but is denied another slice (even public).
	if code := doReq(http.MethodGet, "/git/acme/allowed.git/info/refs?service=git-upload-pack", readerSecret); code != http.StatusOK {
		t.Fatalf("scoped reader upload-pack allowed = %d, want 200", code)
	}
	if code := doReq(http.MethodGet, "/git/acme/public-other.git/info/refs?service=git-upload-pack", readerSecret); code != http.StatusForbidden {
		t.Fatalf("scoped reader upload-pack out-of-scope public = %d, want 403", code)
	}

	// Verify receive-pack authorization checks MaxRole when blobs/changesets are configured.
	h.blobs = &recordingBlobAPI{}
	h.changesets = stubChangesetAPI{}
	if code := doReq(http.MethodGet, "/git/acme/allowed.git/info/refs?service=git-receive-pack", readerSecret); code != http.StatusForbidden {
		t.Fatalf("reader key receive-pack = %d, want 403", code)
	}
	if code := doReq(http.MethodGet, "/git/acme/allowed.git/info/refs?service=git-receive-pack", writerSecret); code != http.StatusOK {
		t.Fatalf("writer key receive-pack = %d, want 200", code)
	}
}

type stubChangesetAPI struct{}

func (stubChangesetAPI) CreateChangeset(context.Context, *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	return nil, nil
}
func (stubChangesetAPI) GetChangeset(context.Context, *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return nil, nil
}
func (stubChangesetAPI) UpdateChangeset(context.Context, *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	return nil, nil
}

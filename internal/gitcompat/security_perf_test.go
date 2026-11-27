package gitcompat

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestHydrateRefusesUnreachableCrossTenantBlobAndMalformedBodies(t *testing.T) {
	f := newLazyFixture(t)
	ctx := context.Background()

	// Store a confidential blob belonging to another tenant in the shared blob
	// and object stores.
	secret := []byte("PRIVATE_TENANT_API_KEY=super-secret\n")
	secretHash := objectid.RawContentHash(secret)
	secretKey := objectstore.BlobKey(secretHash)
	secretGitID := objectid.GitBlobID(secret)
	if err := f.store.Put(ctx, secretKey, bytes.NewReader(secret)); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.Blobs.Upsert(ctx, objectid.BlobID(secret), secretHash, int64(len(secret)), secretKey); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.Blobs.SetGitBlobIDs(ctx, map[string]string{secretHash: secretGitID}); err != nil {
		t.Fatal(err)
	}

	// Asking HydrateFor to hydrate the foreign SHA-1 must not materialize it in
	// acme/p.git.
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("", 0, secretGitID)); err != nil {
		t.Fatalf("unexpected HydrateFor error: %v", err)
	}
	if err := exec.Command("git", "--git-dir="+f.repo, "cat-file", "-e", secretGitID).Run(); err == nil {
		t.Fatal("foreign tenant blob was hydrated into projected repository")
	}

	// Malformed upload-pack bodies must be rejected rather than hydrating --all.
	if err := f.p.HydrateFor(ctx, f.repo, []byte("not-a-pkt-line")); !errors.Is(err, errInvalidUploadRequest) {
		t.Fatalf("malformed upload-pack body err = %v, want errInvalidUploadRequest", err)
	}
	if f.missing(t) != 3 {
		t.Fatalf("malformed upload-pack body hydrated objects: missing = %d, want 3", f.missing(t))
	}

	// Sequential partial-clone single-blob fetches keep working without falsely
	// marking the repository complete after the first blob.
	wantA := objectid.GitBlobID(f.contents["acme/p/a.txt"])
	wantB := objectid.GitBlobID(f.contents["acme/p/dir/b.txt"])
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("", 0, wantA)); err != nil {
		t.Fatalf("first reachable blob hydrate: %v", err)
	}
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("", 0, wantB)); err != nil {
		t.Fatalf("second reachable blob hydrate: %v", err)
	}
	if got := gitIn(t, f.repo, nil, "cat-file", "-t", wantB); strings.TrimSpace(got) != "blob" {
		t.Fatalf("second reachable blob missing after sequential fetch: %q", got)
	}
}

func TestHTTPHandlerBlocksCrossTenantBlobWantAndPathTraversal(t *testing.T) {
	ctx := context.Background()
	objectRoot := t.TempDir()
	store, err := filesystem.New(objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	cacheRoot := t.TempDir()
	projector, err := NewProjector(ProjectorStores{
		Auth:       mem.Auth,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Blobs:      mem.Blobs,
	}, store, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a public slice (acme/pub) and a private slice (corp/secret).
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{"user-acme"}, "user-acme"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "corp", []string{"user-corp"}, "user-corp"); err != nil {
		t.Fatal(err)
	}
	pubSlice, err := mem.Slices.Create(ctx, "user-acme", &corev1.SliceRef{Account: "acme", Slice: "pub"}, []string{"/acme/pub"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	privSlice, err := mem.Slices.Create(ctx, "user-corp", &corev1.SliceRef{Account: "corp", Slice: "secret"}, []string{"/corp/secret"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	buildLazySliceRepo := func(account, slug string, slice *corev1.Slice, relPath string, data []byte) (string, string) {
		t.Helper()
		hash := objectid.RawContentHash(data)
		key := objectstore.BlobKey(hash)
		if err := store.Put(ctx, key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if err := mem.Blobs.Upsert(ctx, objectid.BlobID(data), hash, int64(len(data)), key); err != nil {
			t.Fatal(err)
		}
		gitID := objectid.GitBlobID(data)
		if err := mem.Blobs.SetGitBlobIDs(ctx, map[string]string{hash: gitID}); err != nil {
			t.Fatal(err)
		}
		repoPath := filepath.Join(cacheRoot, account, slug+".git")
		if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ensureProjectedRepo(ctx, repoPath); err != nil {
			t.Fatal(err)
		}
		state := newProjectionState(account, slug, slice)
		pending := []pendingCommit{{
			native:  &corev1.Commit{Id: "sha256:" + slug, Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "add " + relPath},
			changes: []fileChange{{Path: relPath, Mode: "100644", ContentHash: hash, Size: int64(len(data))}},
		}}
		if _, err := projector.appendLazy(ctx, repoPath, state, map[string]projectedFile{}, pending, map[string]string{}, map[string]string{}, []string{hash}); err != nil {
			t.Fatal(err)
		}
		// Keep NativeHeadID aligned with the empty in-memory global ref so
		// EnsureProjectedRepo serves the lazily projected commits without rebuilding.
		state.NativeHeadID = ""
		if err := writeProjectionState(repoPath, state); err != nil {
			t.Fatal(err)
		}
		return repoPath, gitID
	}

	pubRepoPath, pubBlobID := buildLazySliceRepo("acme", "pub", pubSlice, "acme/pub/readme.txt", []byte("public readme\n"))
	privPlaintext := "TOP_SECRET_CORP_TOKEN_999"
	_, privBlobID := buildLazySliceRepo("corp", "secret", privSlice, "corp/secret/credentials.env", []byte(privPlaintext+"\n"))

	handler := NewHandler(func(_ context.Context, token string) (string, error) {
		if token == "pub" {
			return "user-acme", nil
		}
		return "", storage.ErrUnauthenticated
	}, projector, nil, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1. Encoded and dot-segment path traversal attempts must return 404.
	for _, rawPath := range []string{
		"/git/acme/pub.git/../../corp/secret.git/HEAD",
		"/git/acme/pub.git/%2e%2e/%2e%2e/corp/secret.git/info/refs?service=git-upload-pack",
		"/git/acme%2fcorp/secret.git/HEAD",
		"/git/.mirrors/corp/secret.git/HEAD",
		"/git/acme/pub.git/objects/info/packs",
	} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+rawPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", rawPath, resp.StatusCode)
		}
	}

	// 2. Partial clone of acme/pub + on-demand reachable blob fetch succeeds.
	cloneDir := filepath.Join(t.TempDir(), "pub")
	cmd := exec.Command("git", "clone", "--filter=blob:none", "--no-checkout", srv.URL+"/git/acme/pub.git", cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("partial clone failed: %v\n%s", err, out)
	}
	gotPub, err := exec.Command("git", "-C", cloneDir, "cat-file", "-p", pubBlobID).CombinedOutput()
	if err != nil || string(gotPub) != "public readme\n" {
		t.Fatalf("reachable partial-clone blob fetch failed: %v (%q)", err, gotPub)
	}

	// 3. Requesting corp/secret's blob SHA-1 through acme/pub's upload-pack
	// must fail and never leak privPlaintext.
	postReq, err := http.NewRequest(http.MethodPost, srv.URL+"/git/acme/pub.git/git-upload-pack", bytes.NewReader(fetchBody("", 0, privBlobID)))
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	postReq.Header.Set("Git-Protocol", "version=2")
	resp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	var respBuf bytes.Buffer
	_, _ = respBuf.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(respBuf.String(), privPlaintext) {
		t.Fatal("cross-tenant secret plaintext leaked through git-upload-pack")
	}
	if err := exec.Command("git", "--git-dir="+pubRepoPath, "cat-file", "-e", privBlobID).Run(); err == nil {
		t.Fatal("cross-tenant blob was written into public projected repository")
	}

	// 4. Valid gzip-compressed upload-pack request succeeds; malformed gzip returns 400.
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write(fetchBody("", 0, pubBlobID)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	gzReq, err := http.NewRequest(http.MethodPost, srv.URL+"/git/acme/pub.git/git-upload-pack", bytes.NewReader(gzBuf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	gzReq.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	gzReq.Header.Set("Content-Encoding", "gzip")
	gzReq.Header.Set("Git-Protocol", "version=2")
	gzResp, err := http.DefaultClient.Do(gzReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = gzResp.Body.Close()
	if gzResp.StatusCode != http.StatusOK {
		t.Fatalf("gzipped upload-pack status = %d, want 200", gzResp.StatusCode)
	}

	badGzReq, err := http.NewRequest(http.MethodPost, srv.URL+"/git/acme/pub.git/git-upload-pack", strings.NewReader("not-gzip"))
	if err != nil {
		t.Fatal(err)
	}
	badGzReq.Header.Set("Content-Encoding", "gzip")
	badGzResp, err := http.DefaultClient.Do(badGzReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = badGzResp.Body.Close()
	if badGzResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("corrupt gzip upload-pack status = %d, want 400", badGzResp.StatusCode)
	}
}

func TestDecodeRequestBodyCapsGzipExpansion(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(bytes.Repeat([]byte("0"), 64<<10)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/git/acme/p.git/git-upload-pack", nil)
	req.Header.Set("Content-Encoding", "gzip")
	_, err := decodeRequestBody(req, compressed.Bytes(), 16<<10)
	if !errors.Is(err, errRequestBodyTooLarge) {
		t.Fatalf("decodeRequestBody gzip bomb err = %v, want errRequestBodyTooLarge", err)
	}
}

func TestParseUploadRequestBoundsWantsAndHaves(t *testing.T) {
	var b strings.Builder
	b.WriteString(pkt("command=fetch\n", "DELIM"))
	for i := 0; i <= maxUploadWants; i++ {
		oid := strings.Repeat("0", 32) + strings.ToLower(leftPadHex(i, 8))
		b.WriteString(pkt("want " + oid + "\n"))
	}
	b.WriteString(pkt("done\n", "FLUSH"))
	if _, ok := parseUploadRequest([]byte(b.String())); ok {
		t.Fatal("expected parseUploadRequest to reject > maxUploadWants unique wants")
	}
}

func leftPadHex(v, width int) string {
	const digits = "0123456789abcdef"
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf)
}

func TestPushPackBlobSizeAndCumulativeCaps(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo.git")
	if err := runGit(ctx, "", nil, "init", "--bare", repo); err != nil {
		t.Fatal(err)
	}
	writeBlob := func(data []byte) string {
		t.Helper()
		out, err := gitOutputInput(ctx, repo, nil, data, "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	b1 := writeBlob(bytes.Repeat([]byte("a"), 4096))
	b2 := writeBlob(bytes.Repeat([]byte("b"), 4096))

	// Single blob cap triggers before reading payload.
	rem := int64(1 << 20)
	if _, err := gitBlobContentsWithLimits(ctx, repo, []string{b1}, &rem, 1024); err == nil || !strings.Contains(err.Error(), "exceeds maximum blob size") {
		t.Fatalf("single blob limit err = %v", err)
	}

	// Cumulative cap triggers across multiple blobs.
	rem = 6000
	if _, err := gitBlobContentsWithLimits(ctx, repo, []string{b1, b2}, &rem, 8192); err == nil || !strings.Contains(err.Error(), "cumulative") {
		t.Fatalf("cumulative blob limit err = %v", err)
	}

	// Duplicate blob references in ls-tree records also count against cumulative
	// budget so many tree entries pointing to one blob cannot bypass the limit.
	var treeRaw bytes.Buffer
	treeRaw.WriteString("100644 blob " + b1 + "\tacme/p/one.bin\x00")
	treeRaw.WriteString("100644 blob " + b1 + "\tacme/p/two.bin\x00")
	rem = 6000
	if _, err := gitSnapshotFromTreeRecords(ctx, repo, treeRaw.Bytes(), &rem, 8192); err == nil || !strings.Contains(err.Error(), "cumulative") {
		t.Fatalf("duplicate tree entry cumulative limit err = %v", err)
	}
}

func TestProjectorRefCountedLocksReleaseEntries(t *testing.T) {
	p := &Projector{}
	unlockRepo := p.acquireLock("/tmp/acme/p.git")
	unlockServe := p.rLockServe("/tmp/acme/p.git")
	if len(p.locks) != 1 || len(p.serveLocks) != 1 {
		t.Fatalf("locks while held = %d/%d, want 1/1", len(p.locks), len(p.serveLocks))
	}
	unlockServe()
	unlockRepo()
	if len(p.locks) != 0 || len(p.serveLocks) != 0 {
		t.Fatalf("locks after release = %d/%d, want 0/0", len(p.locks), len(p.serveLocks))
	}
	if err := os.MkdirAll(t.TempDir(), 0o755); err != nil {
		t.Fatal(err)
	}
}

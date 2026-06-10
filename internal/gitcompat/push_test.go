package gitcompat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestParseReceivePackRequest(t *testing.T) {
	var body bytes.Buffer
	appendPktLine(&body, []byte(zeroGitOID+" 1111111111111111111111111111111111111111 refs/changes/new\x00report-status side-band-64k agent=git/2.0\n"))
	appendFlush(&body)
	body.WriteString("PACKdata")

	req, err := parseReceivePackRequest(body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(req.commands))
	}
	cmd := req.commands[0]
	if cmd.Ref != "refs/changes/new" || cmd.OldOID != zeroGitOID || cmd.NewOID != "1111111111111111111111111111111111111111" {
		t.Fatalf("unexpected command: %#v", cmd)
	}
	if _, ok := req.capabilities["report-status"]; !ok {
		t.Fatalf("missing report-status capability: %#v", req.capabilities)
	}
	if string(req.packfile) != "PACKdata" {
		t.Fatalf("packfile = %q", string(req.packfile))
	}
}

func TestPushedCommitFileEdits(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGitPushTest(t, repo, "init")
	runGitPushTest(t, repo, "config", "user.name", "Push Tester")
	runGitPushTest(t, repo, "config", "user.email", "push@example.invalid")
	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "a.go"), "package payment\nconst A = 1\n")
	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "b.go"), "package payment\nconst B = 1\n")
	runGitPushTest(t, repo, "add", ".")
	runGitPushTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runGitPushTest(t, repo, "rev-parse", "HEAD"))

	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "a.go"), "package payment\nconst A = 2\n")
	if err := os.Remove(filepath.Join(repo, "acme", "payment", "b.go")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "c.go"), "package payment\nconst C = 1\n")
	runGitPushTest(t, repo, "add", "-A")
	runGitPushTest(t, repo, "commit", "-m", "git push change")
	head := strings.TrimSpace(runGitPushTest(t, repo, "rev-parse", "HEAD"))

	blobs := &recordingBlobAPI{}
	edits, title, err := pushedCommitFileEdits(ctx, ctx, repo, base, head, &corev1.SliceRef{Account: "acme", Slice: "payment"}, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if title != "git push change" {
		t.Fatalf("title = %q", title)
	}
	got := map[string]string{}
	for _, edit := range edits {
		got[edit.Path] = edit.Op
		if edit.Op == "upsert" && (edit.BlobId == "" || edit.ContentHash == "") {
			t.Fatalf("upsert edit missing blob metadata: %#v", edit)
		}
	}
	want := map[string]string{
		"/acme/payment/a.go": "upsert",
		"/acme/payment/b.go": "delete",
		"/acme/payment/c.go": "upsert",
	}
	if len(got) != len(want) {
		t.Fatalf("edits = %#v, want %#v", got, want)
	}
	for path, op := range want {
		if got[path] != op {
			t.Fatalf("edit %s = %q, want %q; all edits %#v", path, got[path], op, got)
		}
	}
	if len(blobs.uploads) != 2 {
		t.Fatalf("uploaded blobs = %d, want 2", len(blobs.uploads))
	}
}

func TestIndexPushPackUsesProjectedRepoAsAlternate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	projected := filepath.Join(root, "projected.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitPushTest(t, repo, "init")
	runGitPushTest(t, repo, "config", "user.name", "Pack Tester")
	runGitPushTest(t, repo, "config", "user.email", "pack@example.invalid")
	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "base.go"), "package payment\nconst Base = true\n")
	runGitPushTest(t, repo, "add", ".")
	runGitPushTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runGitPushTest(t, repo, "rev-parse", "HEAD"))
	runGitPushTest(t, "", "init", "--bare", projected)
	runGitPushTest(t, repo, "push", projected, "HEAD:refs/heads/main")

	mustWriteFile(t, filepath.Join(repo, "acme", "payment", "next.go"), "package payment\nconst Next = true\n")
	runGitPushTest(t, repo, "add", ".")
	runGitPushTest(t, repo, "commit", "-m", "next")
	head := strings.TrimSpace(runGitPushTest(t, repo, "rev-parse", "HEAD"))
	pack := runGitPushTestInput(t, repo, "^"+base+"\n"+head+"\n", "pack-objects", "--stdout", "--thin", "--revs")

	pushRepo, cleanup, err := indexPushPack(ctx, projected, pack)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ok, err := gitObjectExists(ctx, pushRepo, head+"^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("indexed push repo does not contain commit %s", head)
	}
}

type recordingBlobAPI struct {
	uploads [][]byte
}

func (b *recordingBlobAPI) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	b.uploads = append(b.uploads, append([]byte(nil), req.Data...))
	id := "blob_test_" + strconv.Itoa(len(b.uploads))
	return &corev1.UploadBlobResponse{BlobId: id, ContentHash: "hash_" + id, Size: int64(len(req.Data))}, nil
}

func runGitPushTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func runGitPushTestInput(t *testing.T, dir, input string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return out
}

func mustWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

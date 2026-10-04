package gitcompat

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
)

func gitIn(t *testing.T, repo string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPackBuilderWritesObjectsGitCanRead(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repo); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a large file, repeated. "), 40_000) // ~1 MB, so the size header spans several bytes
	pack, err := newPackBuilder(repo)
	if err != nil {
		t.Fatal(err)
	}
	// A tree that points at a blob which is not in the pack, and a commit on it:
	// exactly what the lazy projection writes.
	missing := objectid.GitBlobID([]byte("not in the repository\n"))
	tree := encodeTree(map[string]treeEntry{"later.txt": {mode: "100644", sha: missing}})
	treeID := gitObjectID("tree", tree)
	commit := encodeCommit(treeID, "", "A", "a@x", 1_000_000_000, "A", "a@x", 1_000_000_000, "one\n")
	commitID := gitObjectID("commit", commit)
	blobID, err := pack.addBlob(int64(len(big)), bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	if blobID != objectid.GitBlobID(big) {
		t.Fatalf("addBlob returned %s", blobID)
	}
	if err := pack.add(objTree, tree); err != nil {
		t.Fatal(err)
	}
	if err := pack.add(objCommit, commit); err != nil {
		t.Fatal(err)
	}
	name, err := pack.finish(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "pack-") {
		t.Fatalf("pack name %q", name)
	}
	if packs, _ := listPacks(repo); len(packs) != 1 || packs[0] != name {
		t.Fatalf("packs in the repository: %v, want [%s]", packs, name)
	}
	if got := gitIn(t, repo, nil, "cat-file", "-t", commitID); got != "commit" {
		t.Fatalf("commit object: %q", got)
	}
	if got := gitIn(t, repo, nil, "cat-file", "-p", treeID); !strings.Contains(got, missing) {
		t.Fatalf("tree: %q", got)
	}
	if got := gitIn(t, repo, nil, "rev-parse", blobID+"^{blob}"); got != blobID {
		t.Fatalf("blob: %q", got)
	}
	if size := gitIn(t, repo, nil, "cat-file", "-s", blobID); size != strconv.Itoa(len(big)) {
		t.Fatalf("blob size %q", size)
	}
	// The blob the tree names is reported missing, which is what hydration acts on.
	gitIn(t, repo, nil, "update-ref", "refs/heads/main", commitID)
	if got := gitIn(t, repo, nil, "rev-list", "--objects", "--missing=print", "main"); !strings.Contains(got, "?"+missing) {
		t.Fatalf("rev-list --missing=print: %q", got)
	}
}

func TestPackBuilderRejectsAWrongSizedBlob(t *testing.T) {
	repo := t.TempDir()
	pack, err := newPackBuilder(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer pack.discard()
	if _, err := pack.addBlob(10, strings.NewReader("short")); err == nil {
		t.Fatal("a blob shorter than declared must be rejected")
	}
}

func TestEmptyPackBuilderWritesNothing(t *testing.T) {
	repo := t.TempDir()
	pack, _ := newPackBuilder(repo)
	if name, err := pack.finish(context.Background(), repo); name != "" || err != nil {
		t.Fatalf("empty pack: %q, %v", name, err)
	}
}

func TestCompactHistoryPacksMergesRunsOfSmallPacks(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repo); err != nil {
		t.Fatal(err)
	}
	state := &projectionState{}
	var blobs []string
	// Forty landings, each its own pack of a few objects.
	for i := 0; i < 40; i++ {
		pack, err := newPackBuilder(repo)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte("landing " + strconv.Itoa(i) + "\n")
		id, err := pack.addBlob(int64(len(data)), bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		name, err := pack.finish(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		state.HistoryPacks = append(state.HistoryPacks, name)
		blobs = append(blobs, id)
	}
	if err := compactHistoryPacks(ctx, repo, state); err != nil {
		t.Fatal(err)
	}
	if len(state.HistoryPacks) >= compactFanout {
		t.Fatalf("%d history packs left after compaction", len(state.HistoryPacks))
	}
	onDisk, _ := listPacks(repo)
	if len(onDisk) != len(state.HistoryPacks) {
		t.Fatalf("%d packs on disk, %d in the state: %v vs %v", len(onDisk), len(state.HistoryPacks), onDisk, state.HistoryPacks)
	}
	for i, id := range blobs {
		if got := gitIn(t, repo, nil, "cat-file", "-p", id); got != "landing "+strconv.Itoa(i) {
			t.Fatalf("object %d after merging = %q", i, got)
		}
	}
	gitIn(t, repo, nil, "fsck", "--full")
	if loaded := loadProjectionState(repo); loaded == nil || len(loaded.HistoryPacks) != len(state.HistoryPacks) {
		t.Fatalf("the state on disk does not name the merged packs: %#v", loaded)
	}
}

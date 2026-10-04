package gitcompat

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
)

// historyRepo makes a projected repository whose history is n commits, written
// as one pack, and returns it with the state describing it.
func historyRepo(t *testing.T, n int, salt string) (string, *projectionState) {
	t.Helper()
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "acme", "payment.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectedRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	pack, err := newPackBuilder(repo)
	if err != nil {
		t.Fatal(err)
	}
	builder, _ := newTreeBuilder(map[string]projectedFile{})
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "payment", Files: map[string]projectedFile{}}
	parent := ""
	for i := 0; i < n; i++ {
		content := []byte(salt + strings.Repeat("x", i) + "\n")
		path := "acme/payment/f" + string(rune('a'+i)) + ".txt"
		blob := objectid.GitBlobID(content)
		change := fileChange{Path: path, Mode: "100644", ContentHash: "sha256:" + path}
		builder.apply(change, blob)
		tree := builder.root()
		for _, o := range builder.takeObjects() {
			if err := pack.add(o.kind, o.data); err != nil {
				t.Fatal(err)
			}
		}
		data := encodeCommit(tree, parent, "A", "a@x", int64(1_000_000_000+i), "A", "a@x", int64(1_000_000_000+i), "commit "+salt+"\n")
		parent = gitObjectID("commit", data)
		if err := pack.add(objCommit, data); err != nil {
			t.Fatal(err)
		}
		state.Files[path] = projectedFile{Mode: "100644", ContentHash: change.ContentHash, Blob: blob}
		state.Commits = append(state.Commits, projectedCommit{Native: "sha256:n" + string(rune('a'+i)), Git: parent})
	}
	name, err := pack.finish(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateProjectedBranch(ctx, repo, parent, ""); err != nil {
		t.Fatal(err)
	}
	state.GitHead, state.NativeHeadID, state.HistoryPacks = parent, "sha256:n"+string(rune('a'+n-1)), []string{name}
	if err := writeProjectionState(repo, state); err != nil {
		t.Fatal(err)
	}
	return repo, state
}

func newTestStore(t *testing.T) *filesystem.Store {
	t.Helper()
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func waitForManifest(t *testing.T, store PackStore, head string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if rc, err := store.Get(context.Background(), mirrorPrefix("acme", "payment")+"manifest.json", 0, 0); err == nil {
			buf := make([]byte, 1<<16)
			n, _ := rc.Read(buf)
			rc.Close()
			if strings.Contains(string(buf[:n]), head) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	rc, err := store.Get(context.Background(), mirrorPrefix("acme", "payment")+"manifest.json", 0, 0)
	if err == nil {
		raw, _ := io.ReadAll(rc)
		rc.Close()
		t.Logf("manifest on disk: %s", raw)
	}
	t.Fatalf("the manifest for %s never appeared", head)
}

func TestPackMirrorRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	mirror := NewPackMirror(store, []string{"acme/payment"})
	mirror.pub.interval = 5 * time.Millisecond
	old := packPartBytes
	packPartBytes = 100 // spread every object over several parts
	t.Cleanup(func() { packPartBytes = old })

	if !mirror.Enabled("acme", "payment") || mirror.Enabled("acme", "other") {
		t.Fatal("only acme/payment is mirrored")
	}
	if restored, err := mirror.Restore(ctx, "acme", "payment", filepath.Join(t.TempDir(), "x.git")); restored || err != nil {
		t.Fatalf("nothing is mirrored yet: restored=%v err=%v", restored, err)
	}

	repo, state := historyRepo(t, 5, "one")
	mirror.Publish(ctx, "acme", "payment", repo, state)
	waitForManifest(t, store, state.GitHead)

	restoredPath := filepath.Join(t.TempDir(), "acme", "payment.git")
	// A fresh instance has not seen the manifest: it must read it from the store.
	fresh := NewPackMirror(store, []string{"acme/payment"})
	ok, err := fresh.Restore(ctx, "acme", "payment", restoredPath)
	if err != nil || !ok {
		t.Fatalf("restore: ok=%v err=%v", ok, err)
	}
	if got := gitIn(t, restoredPath, nil, "rev-parse", "refs/heads/main"); got != state.GitHead {
		t.Fatalf("restored head %s, want %s", got, state.GitHead)
	}
	if got := gitIn(t, restoredPath, nil, "rev-list", "--count", "main"); got != "5" {
		t.Fatalf("restored %s commits, want 5", got)
	}
	loaded := loadProjectionState(restoredPath)
	if loaded == nil || loaded.GitHead != state.GitHead || len(loaded.Files) != 5 || len(loaded.HistoryPacks) != 1 {
		t.Fatalf("restored state: %#v", loaded)
	}
	// The files are not in the mirror: the history names them, and hydration
	// would fetch them from the object store.
	if got := gitIn(t, restoredPath, nil, "rev-list", "--objects", "--missing=print", "main"); strings.Count(got, "?") != 5 {
		t.Fatalf("expected five missing files:\n%s", got)
	}

	// More history is another pack, not a rewrite of the first.
	repo2, state2 := historyRepo(t, 5, "two")
	state2.HistoryPacks = append(append([]string(nil), state.HistoryPacks...), state2.HistoryPacks...)
	mirror.Publish(ctx, "acme", "payment", repo2, state2)
	// repo2 does not have the first pack on disk, but the manifest already has it.
	waitForManifest(t, store, state2.GitHead)

	// A rebuild replaces the packs. The old ones are not deleted by the push
	// that drops them, since another instance may be restoring from the manifest
	// that names them, but by the one after.
	oldPack := mirrorPrefix("acme", "payment") + "packs/" + state.HistoryPacks[0] + ".pack.0"
	repo3, state3 := historyRepo(t, 3, "three")
	mirror.Publish(ctx, "acme", "payment", repo3, state3)
	waitForManifest(t, store, state3.GitHead)
	if err := mirror.Settle(ctx, "acme", "payment"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, oldPack, 0, 0); err != nil {
		t.Fatalf("the dropped pack must outlive the push that dropped it: %v", err)
	}
	repo4, state4 := historyRepo(t, 2, "four")
	mirror.Publish(ctx, "acme", "payment", repo4, state4)
	waitForManifest(t, store, state4.GitHead)
	if err := mirror.Settle(ctx, "acme", "payment"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, oldPack, 0, 0); err == nil {
		t.Fatal("the dropped pack was not deleted by the next push")
	}
}

// A manifest names each object's part size, so changing the size for new
// uploads must not break restoring what was uploaded with the old one.
func TestPackMirrorRestoresWhateverPartSizeItWasWrittenWith(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	mirror := NewPackMirror(store, []string{"*"})
	old := packPartBytes
	t.Cleanup(func() { packPartBytes = old })
	packPartBytes = 101
	repo, state := historyRepo(t, 4, "sizes")
	mirror.Publish(ctx, "acme", "payment", repo, state)
	if err := mirror.Settle(ctx, "acme", "payment"); err != nil {
		t.Fatal(err)
	}
	packPartBytes = 37
	restoredPath := filepath.Join(t.TempDir(), "acme", "payment.git")
	if ok, err := NewPackMirror(store, []string{"*"}).Restore(ctx, "acme", "payment", restoredPath); err != nil || !ok {
		t.Fatalf("restore: ok=%v err=%v", ok, err)
	}
	if got := gitIn(t, restoredPath, nil, "rev-list", "--count", "main"); got != "4" {
		t.Fatalf("restored %s commits, want 4", got)
	}
}

// Settle holds a request open until the publish it started is done, which is
// how it gets CPU on an instance that is only given CPU while serving.
func TestPackMirrorSettleWaitsForThePublish(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	mirror := NewPackMirror(store, []string{"*"})
	repo, state := historyRepo(t, 3, "settle")
	mirror.Publish(ctx, "acme", "payment", repo, state)
	if err := mirror.Settle(ctx, "acme", "payment"); err != nil {
		t.Fatal(err)
	}
	// No polling: the mirror must already be complete.
	restoredPath := filepath.Join(t.TempDir(), "acme", "payment.git")
	if ok, err := NewPackMirror(store, []string{"*"}).Restore(ctx, "acme", "payment", restoredPath); err != nil || !ok {
		t.Fatalf("restore right after Settle: ok=%v err=%v", ok, err)
	}
}

func TestPublisherRetriesAndReportsFailure(t *testing.T) {
	attempts := 0
	pub := newPublisher("test publish", func(context.Context, string, int) error {
		attempts++
		if attempts < 3 {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	pub.interval = time.Millisecond
	pub.submit("a/b", 1)
	if err := pub.wait(context.Background(), "a/b", 5*time.Second); err != nil || attempts != 3 {
		t.Fatalf("a failed publish is tried again: attempts=%d err=%v", attempts, err)
	}

	always := newPublisher("test publish", func(context.Context, string, int) error { return io.ErrUnexpectedEOF })
	always.interval = time.Millisecond
	always.submit("a/b", 1)
	if err := always.wait(context.Background(), "a/b", 5*time.Second); err == nil {
		t.Fatal("a publish that keeps failing must be reported to the waiter")
	}

	// A newer snapshot replaces a failing one rather than retrying it.
	var seen []int
	release := make(chan struct{})
	latest := newPublisher("test publish", func(_ context.Context, _ string, n int) error {
		seen = append(seen, n)
		if n == 1 {
			<-release
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	latest.interval = time.Millisecond
	latest.submit("a/b", 1)
	time.Sleep(20 * time.Millisecond)
	latest.submit("a/b", 2)
	close(release)
	if err := latest.wait(context.Background(), "a/b", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("snapshots run: %v, want [1 2]", seen)
	}

	// Waiting does not outlast its limit.
	stuck := newPublisher("test publish", func(ctx context.Context, _ string, _ int) error { <-ctx.Done(); return ctx.Err() })
	stuck.submit("a/b", 1)
	started := time.Now()
	if err := stuck.wait(context.Background(), "a/b", 50*time.Millisecond); err != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("wait must give up quietly at its limit: err=%v after %s", err, time.Since(started))
	}
}

func TestPackMirrorRefusesAStateThatDoesNotMatch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	mirror := NewPackMirror(store, []string{"*"})
	mirror.pub.interval = 5 * time.Millisecond
	repo, state := historyRepo(t, 2, "x")
	state.GitHead = "0000000000000000000000000000000000000001" // not the branch's head
	mirror.Publish(ctx, "acme", "payment", repo, state)
	waitForManifest(t, store, state.GitHead)
	if ok, err := NewPackMirror(store, []string{"*"}).Restore(ctx, "acme", "payment", filepath.Join(t.TempDir(), "r.git")); ok || err == nil {
		t.Fatalf("a state that names a commit the packs lack must not restore: ok=%v err=%v", ok, err)
	}
}

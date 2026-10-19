package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// countingStore counts the objects an import stores.
type countingStore struct {
	ObjectStore
	puts atomic.Int32
}

func (c *countingStore) Put(ctx context.Context, key string, r io.Reader) error {
	c.puts.Add(1)
	return c.ObjectStore.Put(ctx, key, r)
}

// movedFilesRepo makes a repository whose second commit moves every file of
// the first into another folder, and whose third adds one file.
func movedFilesRepo(t *testing.T, files int) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.name", "Mover")
	runGitTest(t, repo, "config", "user.email", "mover@example.invalid")
	for i := 0; i < files; i++ {
		p := filepath.Join(repo, "proj", fmt.Sprintf("d%d", i%4), fmt.Sprintf("f%03d.txt", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(fmt.Sprintf("file %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-q", "-m", "add projects")
	runGitTest(t, repo, "mv", "proj", "src")
	runGitTest(t, repo, "commit", "-q", "-m", "move projects to src")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-q", "-m", "add readme")
	return repo
}

func importRequest(source string, resume bool) *corev1.ImportGitRepositoryRequest {
	return &corev1.ImportGitRepositoryRequest{
		Source:         source,
		MountPath:      "/acme/imported",
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		Mode:           "deep",
		Resume:         resume,
	}
}

// A commit that moves files neither reads nor stores them again.
func TestImportDoesNotUploadMovedFilesAgain(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	_, handlers := newMemoryHandlers()
	store := &countingStore{ObjectStore: handlers.Repository.ObjectStore}
	handlers.Repository.ObjectStore = store
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	const files = 24
	res, err := handlers.Repository.importGitRepository(ctx, importRequest(movedFilesRepo(t, files), true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 3 {
		t.Fatalf("imported %d commits, want 3", len(res.Commits))
	}
	// 24 files and the README: the move's 24 new paths are the same content.
	if got := store.puts.Load(); got != files+1 {
		t.Fatalf("stored %d objects, want %d (the move uploads nothing)", got, files+1)
	}
}

// A retry after a dropped connection does not store again what the attempt
// before it already stored.
func TestImportSkipsContentTheServerHolds(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	_, handlers := newMemoryHandlers()
	store := &countingStore{ObjectStore: handlers.Repository.ObjectStore}
	handlers.Repository.ObjectStore = store
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	repo := movedFilesRepo(t, 12)

	if _, err := handlers.Repository.importGitRepository(ctx, importRequest(repo, true), nil); err != nil {
		t.Fatal(err)
	}
	first := store.puts.Load()
	// Same content imported again somewhere else: every object is there.
	req := importRequest(repo, true)
	req.MountPath = "/acme/imported-again"
	if _, err := handlers.Repository.importGitRepository(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.puts.Load(); got != first {
		t.Fatalf("a second import stored %d more objects, want none", got-first)
	}
}

// A client that drops while a commit is being published does not leave the
// commit half done: it is published, and the retry resumes after it.
func TestImportFinishesTheCommitWhenTheClientDrops(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	repo := movedFilesRepo(t, 12)

	dropCtx, drop := context.WithCancel(ctx)
	defer drop()
	_, err := handlers.Repository.importGitRepository(dropCtx, importRequest(repo, true), func(event *corev1.ImportGitRepositoryProgress) error {
		if event.Phase == "uploading_blobs" && event.Current == 2 {
			drop() // the connection drops during the second commit
		}
		return nil
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("first attempt error = %v, want Canceled once the commit it was publishing is done", err)
	}

	var published, skipped []int64
	res, err := handlers.Repository.importGitRepository(ctx, importRequest(repo, true), func(event *corev1.ImportGitRepositoryProgress) error {
		switch event.Phase {
		case "published":
			published = append(published, event.Current)
		case "skipped":
			skipped = append(skipped, event.Current)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 3 {
		t.Fatalf("resumed import has %d commits, want 3", len(res.Commits))
	}
	if fmt.Sprint(skipped) != "[1 2]" || fmt.Sprint(published) != "[3]" {
		t.Fatalf("resume skipped %v and published %v; want the two commits the first attempt finished skipped, and the third published", skipped, published)
	}
	// And history holds each commit once.
	seen := map[string]int{}
	for _, c := range res.Commits {
		seen[c.NativeCommitId]++
	}
	if len(seen) != 3 {
		t.Fatalf("native commits = %v, want three distinct", seen)
	}
}

func TestImportProgressIgnoresAClientThatLeft(t *testing.T) {
	var sent atomic.Int32
	p := newImportProgress(func(*corev1.ImportGitRepositoryProgress) error {
		if sent.Add(1) > 2 {
			return errors.New("transport is closing")
		}
		return nil
	})
	for i := 0; i < 5; i++ {
		if err := p.emit(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: int64(i)}); err != nil {
			t.Fatalf("emit returned %v; a client that left is not an error", err)
		}
	}
	if sent.Load() != 3 {
		t.Fatalf("sent %d events, want to stop after the first failure (3)", sent.Load())
	}
}

func TestImportHeartbeatRepeatsTheLastPosition(t *testing.T) {
	var mu sync.Mutex
	var beats []*corev1.ImportGitRepositoryProgress
	p := newImportProgress(func(e *corev1.ImportGitRepositoryProgress) error {
		mu.Lock()
		defer mu.Unlock()
		if e.Phase == "working" {
			beats = append(beats, e)
		}
		return nil
	})
	p.heartbeatEvery = 20 * time.Millisecond
	stop := p.heartbeat()
	_ = p.emit(&corev1.ImportGitRepositoryProgress{Phase: "uploading_blobs", Message: "move", Current: 2731, Total: 4018})
	time.Sleep(120 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(beats) < 2 || beats[0].Current != 2731 || beats[0].Total != 4018 || beats[0].Message != "move" {
		t.Fatalf("heartbeats = %v, want repeats of position 2731/4018", beats)
	}
}

func TestImportFlightsRunOneAtATime(t *testing.T) {
	var f importFlights
	ctx := context.Background()
	release, err := f.acquire(ctx, "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		r, err := f.acquire(ctx, "k", nil)
		if err == nil {
			close(acquired)
			r()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("a second import of the same source ran beside the first")
	case <-time.After(50 * time.Millisecond):
	}
	other, err := f.acquire(ctx, "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("the waiting import never ran after the first finished")
	}

	// A waiter whose client leaves stops waiting.
	release, _ = f.acquire(ctx, "k", nil)
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := f.acquire(waitCtx, "k", nil); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("waiting error = %v, want DeadlineExceeded", err)
	}
}

func TestImportReadErrorSaysWhetherToRetry(t *testing.T) {
	if got := status.Code(importReadError(context.Canceled, "read git commit abc")); got != codes.Unavailable {
		t.Errorf("a cancelled read = %s, want Unavailable", got)
	}
	if got := status.Code(importReadError(errors.New("bad object"), "read git commit abc")); got != codes.InvalidArgument {
		t.Errorf("a failed read = %s, want InvalidArgument", got)
	}
	if !strings.Contains(importReadError(errors.New("x"), "read git commit abc").Error(), "read git commit abc") {
		t.Error("the message names the commit")
	}
}

// A blob the import has already read is not read from git again: a commit
// that moves files reuses it.
func TestGitDeltaEditsReuseBlobsAlreadyRead(t *testing.T) {
	ctx := context.Background()
	repo := movedFilesRepo(t, 6)
	commits := strings.Fields(runGitTest(t, repo, "rev-list", "--reverse", "HEAD"))
	known := knownBlobs{}
	first, err := gitSnapshot(ctx, repo, commits[0], "/acme/imported", known)
	if err != nil {
		t.Fatal(err)
	}
	for p, file := range first {
		if file.Reused || len(file.Data) == 0 {
			t.Fatalf("%s: first sight must read the blob: %+v", p, file)
		}
	}
	edits, moved, err := gitDeltaEdits(ctx, repo, commits[0], commits[1], "/acme/imported", known)
	if err != nil {
		t.Fatal(err)
	}
	upserts := 0
	for _, e := range edits {
		if e.Op != "upsert" {
			continue
		}
		upserts++
		file := moved[e.Path]
		if !file.Reused || file.Data != nil || file.ContentHash == "" || file.BlobID == "" || e.ContentHash != file.ContentHash {
			t.Fatalf("%s: a moved file must reuse the blob already read, with its hashes: %+v", e.Path, file)
		}
	}
	if upserts != 6 {
		t.Fatalf("move upserts = %d, want 6", upserts)
	}
	// Without the memory of what was read, the same move reads every blob.
	_, again, err := gitDeltaEdits(ctx, repo, commits[0], commits[1], "/acme/imported", nil)
	if err != nil {
		t.Fatal(err)
	}
	for p, file := range again {
		if file.Reused || len(file.Data) == 0 {
			t.Fatalf("%s: without known blobs the file is read: %+v", p, file)
		}
	}
}

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
	"gitslice.io/gitslice/internal/gitimport"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type countingStore struct {
	ObjectStore
	puts atomic.Int32
}

func (c *countingStore) Put(ctx context.Context, key string, r io.Reader) error {
	c.puts.Add(1)
	return c.ObjectStore.Put(ctx, key, r)
}

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
	if got := store.puts.Load(); got != files+1 {
		t.Fatalf("stored %d objects, want %d (the move uploads nothing)", got, files+1)
	}
}

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
	req := importRequest(repo, true)
	req.MountPath = "/acme/imported-again"
	if _, err := handlers.Repository.importGitRepository(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.puts.Load(); got != first {
		t.Fatalf("a second import stored %d more objects, want none", got-first)
	}
}

func TestImportFinishesTheCommitWhenTheClientDrops(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	repo := movedFilesRepo(t, 12)

	dropCtx, drop := context.WithCancel(ctx)
	defer drop()
	_, err := handlers.Repository.importGitRepository(dropCtx, importRequest(repo, true), func(event *corev1.ImportGitRepositoryProgress) error {
		if event.Phase == "uploading_blobs" && event.Current == 2 {
			drop()
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
		t.Fatalf("resume skipped %v and published %v; want [1 2] and [3]", skipped, published)
	}
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
	p := gitimport.NewProgress(func(*corev1.ImportGitRepositoryProgress) error {
		if sent.Add(1) > 2 {
			return errors.New("transport is closing")
		}
		return nil
	})
	for i := 0; i < 5; i++ {
		if err := p.Emit(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: int64(i)}); err != nil {
			t.Fatalf("Emit returned %v; a client that left is not an error", err)
		}
	}
	if sent.Load() != 3 {
		t.Fatalf("sent %d events, want to stop after the first failure (3)", sent.Load())
	}
}

func TestImportHeartbeatRepeatsTheLastPosition(t *testing.T) {
	var mu sync.Mutex
	var beats []*corev1.ImportGitRepositoryProgress
	p := gitimport.NewProgress(func(e *corev1.ImportGitRepositoryProgress) error {
		mu.Lock()
		defer mu.Unlock()
		if e.Phase == "working" {
			beats = append(beats, e)
		}
		return nil
	})
	p.HeartbeatEvery = 20 * time.Millisecond
	stop := p.Heartbeat()
	_ = p.Emit(&corev1.ImportGitRepositoryProgress{Phase: "uploading_blobs", Message: "move", Current: 2731, Total: 4018})
	time.Sleep(120 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(beats) < 2 || beats[0].Current != 2731 || beats[0].Total != 4018 || beats[0].Message != "move" {
		t.Fatalf("heartbeats = %v, want repeats of position 2731/4018", beats)
	}
}

func TestImportFlightsRunOneAtATime(t *testing.T) {
	var f gitimport.Flights
	ctx := context.Background()
	release, err := f.Acquire(ctx, "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		r, err := f.Acquire(ctx, "k", nil)
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
	other, err := f.Acquire(ctx, "other", nil)
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

	release, _ = f.Acquire(ctx, "k", nil)
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := f.Acquire(waitCtx, "k", nil); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("waiting error = %v, want DeadlineExceeded", err)
	}
}

func TestImportReadErrorSaysWhetherToRetry(t *testing.T) {
	if got := status.Code(gitimport.ReadError(context.Canceled, "read git commit abc")); got != codes.Unavailable {
		t.Errorf("a cancelled read = %s, want Unavailable", got)
	}
	if got := status.Code(gitimport.ReadError(errors.New("bad object"), "read git commit abc")); got != codes.InvalidArgument {
		t.Errorf("a failed read = %s, want InvalidArgument", got)
	}
	if !strings.Contains(gitimport.ReadError(errors.New("x"), "read git commit abc").Error(), "read git commit abc") {
		t.Error("the message names the commit")
	}
}

func TestGitDeltaEditsReuseBlobsAlreadyRead(t *testing.T) {
	ctx := context.Background()
	repo := movedFilesRepo(t, 6)
	commits := strings.Fields(runGitTest(t, repo, "rev-list", "--reverse", "HEAD"))
	known := gitimport.KnownBlobs{}
	first, err := gitimport.ReadSnapshot(ctx, repo, commits[0], "/acme/imported", known)
	if err != nil {
		t.Fatal(err)
	}
	for p, file := range first {
		if file.Reused || len(file.Data) == 0 {
			t.Fatalf("%s: first sight must read the blob: %+v", p, file)
		}
	}
	edits, moved, err := gitimport.DeltaEdits(ctx, repo, commits[0], commits[1], "/acme/imported", known)
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
	_, again, err := gitimport.DeltaEdits(ctx, repo, commits[0], commits[1], "/acme/imported", nil)
	if err != nil {
		t.Fatal(err)
	}
	for p, file := range again {
		if file.Reused || len(file.Data) == 0 {
			t.Fatalf("%s: without known blobs the file is read: %+v", p, file)
		}
	}
}

func TestImportStreamsBlobsInBoundedBatches(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	prevBatchBytes, prevBatchCount := gitimport.MaxBlobBatchBytes, gitimport.MaxBlobBatchCount
	t.Cleanup(func() {
		gitimport.MaxBlobBatchBytes = prevBatchBytes
		gitimport.MaxBlobBatchCount = prevBatchCount
	})
	gitimport.MaxBlobBatchBytes = 16
	gitimport.MaxBlobBatchCount = 3

	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.name", "Streamer")
	runGitTest(t, repo, "config", "user.email", "streamer@example.invalid")
	wantFiles := map[string]string{
		"small-1.txt": "alpha\n",
		"small-2.txt": "beta\n",
		"small-3.txt": "gamma\n",
		"small-4.txt": "delta\n",
		"big.txt":     strings.Repeat("large-blob-line\n", 4),
	}
	for name, content := range wantFiles {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	_, handlers := newMemoryHandlers()
	store := &countingStore{ObjectStore: handlers.Repository.ObjectStore}
	handlers.Repository.ObjectStore = store
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	res, err := handlers.Repository.importGitRepository(ctx, importRequest(repo, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 1 {
		t.Fatalf("imported %d commits, want 1", len(res.Commits))
	}
	if got := store.puts.Load(); got != int32(len(wantFiles)) {
		t.Fatalf("stored %d objects, want %d", got, len(wantFiles))
	}
	for name, want := range wantFiles {
		file, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
			CommitId: res.FinalCommitId,
			Path:     "/acme/imported/" + name,
		})
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(file.Data) != want {
			t.Fatalf("content of %s = %q, want %q", name, string(file.Data), want)
		}
	}
}

func TestImportCommitLimitPreservesResourceExhausted(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	prevCommitMax := gitimport.MaxCommitBytes
	t.Cleanup(func() {
		gitimport.MaxCommitBytes = prevCommitMax
	})
	gitimport.MaxCommitBytes = 32

	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	_, err := handlers.Repository.importGitRepository(ctx, importRequest(movedFilesRepo(t, 12), true), nil)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("import over commit limit = %v, want ResourceExhausted", err)
	}
}

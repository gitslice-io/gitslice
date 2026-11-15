package cli

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestCheckMountDir(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) string {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	workspace := mk("checkout", ".gs/slice.json")
	damaged := mk("damaged", ".gs/state.json")

	for _, tc := range []struct {
		name string
		dir  string
		code string // "" for a directory that can be mounted
	}{
		{"missing directory", filepath.Join(root, "new"), ""},
		{"empty directory", mk("empty"), ""},
		{"not empty", mk("full", "keep.txt"), "mount_dir_not_empty"},
		{"inside a checkout", filepath.Join(workspace, "sub", "mnt"), "already_in_workspace"},
		{"directly inside a checkout", filepath.Join(workspace, "mnt"), "already_in_workspace"},
		{"inside a workspace that lost its binding", filepath.Join(damaged, "mnt"), "workspace_damaged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMountDir(tc.dir)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("checkMountDir = %v, want nil", err)
				}
				return
			}
			if !isUserErrorCode(err, tc.code) {
				t.Fatalf("checkMountDir = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestRetryUnavailable(t *testing.T) {
	pauses := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	unavailable := grpcstatus.Error(codes.Unavailable, "error reading from server: EOF")
	run := func(ctx context.Context, results ...error) (int, error) {
		calls := 0
		err := retryUnavailable(ctx, pauses, func() error {
			calls++
			if calls > len(results) {
				t.Fatalf("called %d times", calls)
			}
			return results[calls-1]
		})
		return calls, err
	}
	if calls, err := run(context.Background(), unavailable, unavailable, nil); calls != 3 || err != nil {
		t.Fatalf("recovers on the third try: calls = %d, err = %v", calls, err)
	}
	if calls, err := run(context.Background(), unavailable, unavailable, unavailable, unavailable); calls != 4 || grpcstatus.Code(err) != codes.Unavailable {
		t.Fatalf("gives up after the pauses: calls = %d, err = %v", calls, err)
	}
	notFound := grpcstatus.Error(codes.NotFound, "no such content")
	if calls, err := run(context.Background(), notFound); calls != 1 || !errors.Is(err, notFound) {
		t.Fatalf("other errors are not retried: calls = %d, err = %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if calls, _ := run(ctx, unavailable); calls != 1 {
		t.Fatalf("a cancelled context stops the retries: calls = %d", calls)
	}
}

// After a fetch gave up on an unreachable server, the next ones fail at once.
func TestMountFetcherFailsFastDuringAnOutage(t *testing.T) {
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &mountFetcher{cache: cache, inflight: map[string]*fetchCall{}, downUntil: time.Now().Add(time.Minute)}
	started := time.Now()
	_, err = f.fetchContent(context.Background(), "/a.txt", "sha256:abc", 3)
	if !errors.Is(err, errServerUnreachable) {
		t.Fatalf("fetchContent during an outage = %v, want errServerUnreachable", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("fetchContent took %s during an outage; it should fail at once", time.Since(started))
	}
}

func shortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	base := "/tmp"
	if _, err := os.Stat(base); err != nil {
		base = ""
	}
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestMountIDValidationAndPathTraversal(t *testing.T) {
	runDir := shortTempDir(t, "gsrun-")
	t.Setenv("XDG_RUNTIME_DIR", runDir)

	validID, err := newMountID()
	if err != nil {
		t.Fatal(err)
	}
	if !isValidMountID(validID) || validateMountID(validID) != nil {
		t.Fatalf("expected generated mount id %q to be valid", validID)
	}

	r := Runner{Home: t.TempDir()}
	mountsRoot := r.mountsRoot()
	for _, bad := range []string{
		"",
		"short",
		"0123456789abcde",
		"0123456789abcdef0",
		"0123456789ABCDEF",
		"0123456789abcdeg",
		"../../etc/passwd",
		"../outside",
		"01234567/89abcdef",
	} {
		if isValidMountID(bad) {
			t.Fatalf("isValidMountID(%q) = true, want false", bad)
		}
		if err := validateMountID(bad); !errors.Is(err, errInvalidMountID) {
			t.Fatalf("validateMountID(%q) = %v, want errInvalidMountID", bad, err)
		}
		stateDir := r.mountStateDir(bad)
		rel, relErr := filepath.Rel(mountsRoot, stateDir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("mountStateDir(%q) = %q escaped mountsRoot %q", bad, stateDir, mountsRoot)
		}
		sock := mountControlPath(bad)
		if filepath.Dir(sock) != filepath.Clean(runDir) {
			t.Fatalf("mountControlPath(%q) = %q escaped runtime dir %q", bad, sock, runDir)
		}
		if _, err := r.readMountRecord(bad); !errors.Is(err, errInvalidMountID) {
			t.Fatalf("readMountRecord(%q) = %v, want errInvalidMountID", bad, err)
		}
		if err := r.writeMountRecord(mountRecord{ID: bad}); !errors.Is(err, errInvalidMountID) {
			t.Fatalf("writeMountRecord(%q) = %v, want errInvalidMountID", bad, err)
		}
	}

	// Mismatched ID inside mount.json must be rejected.
	otherID := "0123456789abcdef"
	if otherID == validID {
		otherID = "fedcba9876543210"
	}
	if err := os.MkdirAll(r.mountStateDir(validID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(r.mountStateDir(validID), "mount.json"), mountRecord{ID: otherID}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.readMountRecord(validID); !errors.Is(err, errInvalidMountID) {
		t.Fatalf("mismatched record id: got %v, want errInvalidMountID", err)
	}

	// Poisoned .gs/lazy.json must not produce a traversal workspace ID.
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".gs", "lazy.json"), []byte(`{"id":"../../etc/passwd"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lazyWorkspaceID(wsDir); got != "" {
		t.Fatalf("lazyWorkspaceID with traversal id = %q, want empty", got)
	}

	// mountd.attach must reject invalid IDs.
	sup := &mountSupervisor{
		r:          r,
		ctx:        context.Background(),
		logger:     log.New(io.Discard, "", 0),
		workspaces: map[string]*attachedWorkspace{},
		attaching:  map[string]bool{},
	}
	reply := sup.attach("../../etc/passwd")
	if reply.ErrorCode != "invalid_workspace_id" {
		t.Fatalf("mountSupervisor.attach traversal reply = %+v, want invalid_workspace_id", reply)
	}
}

func TestMountRuntimeDirAndSocketSecurity(t *testing.T) {
	shortHome := shortTempDir(t, "gshome-")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", shortHome)

	gotRun := mountRuntimeDir()
	wantRun := filepath.Join(shortHome, ".gitslice", "run")
	if gotRun != wantRun {
		t.Fatalf("mountRuntimeDir() = %q, want %q", gotRun, wantRun)
	}
	if err := ensureMountRuntimeDir(gotRun); err != nil {
		t.Fatalf("ensureMountRuntimeDir: %v", err)
	}
	info, err := os.Lstat(gotRun)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir mode = %04o, want 0700", info.Mode().Perm())
	}

	// Reject world/group accessible runtime dir.
	unsafeDir := filepath.Join(shortHome, "unsafe-run")
	if err := os.MkdirAll(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountRuntimeDir(unsafeDir); err == nil {
		t.Fatal("expected verifyMountRuntimeDir to reject 0755 directory")
	}

	// Reject symlink runtime dir even when target is 0700.
	symlinkDir := filepath.Join(shortHome, "symlink-run")
	if err := os.Symlink(gotRun, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountRuntimeDir(symlinkDir); err == nil {
		t.Fatal("expected verifyMountRuntimeDir to reject symlink")
	}

	// Refuse to overwrite a regular file at socket path.
	regularFile := filepath.Join(gotRun, "not-a-socket.sock")
	if err := os.WriteFile(regularFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnixControlSocket(regularFile); err == nil {
		t.Fatal("expected listenUnixControlSocket to refuse overwriting regular file")
	}

	// Control socket must have 0600 permissions and pass peer UID verification.
	t.Setenv("XDG_RUNTIME_DIR", gotRun)
	id, err := newMountID()
	if err != nil {
		t.Fatal(err)
	}
	listener, cleanup := listenMountControl(id)
	if listener == nil {
		t.Fatal("listenMountControl returned nil listener")
	}
	defer cleanup()

	sockInfo, err := os.Lstat(mountControlPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if sockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("control socket mode = %04o, want 0600", sockInfo.Mode().Perm())
	}

	go serveMountControl(listener, func(req mountControlRequest) mountControlReply {
		return mountControlReply{PID: os.Getpid(), BaseCommit: "commit-ok"}
	})
	reply, err := callMountDaemon(id, mountControlRequest{Op: "status"})
	if err != nil {
		t.Fatalf("callMountDaemon over verified peer socket failed: %v", err)
	}
	if reply.BaseCommit != "commit-ok" {
		t.Fatalf("reply.BaseCommit = %q, want commit-ok", reply.BaseCommit)
	}
}

func TestMountFetcherContextCancellationDoesNotAbortSharedBatch(t *testing.T) {
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contentA := []byte("hello world\n")
	tmpCache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	objA, err := tmpCache.PutBytes(contentA)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := lazyfs.NewManifest("commit-1", []lazyfs.BaseEntry{
		{Path: "/acme/a.txt", Kind: lazyfs.KindFile, Mode: 0o100644, Size: int64(len(contentA)), ContentHash: objA.ContentHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newMountFetcher(Runner{}, nil, nil, cache, manifest)
	started := make(chan struct{})
	release := make(chan struct{})
	f.fetchFn = func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
		close(started)
		<-release
		if _, err := cache.PutBytes(contentA); err != nil {
			return 0, 0, err
		}
		return 1, int64(len(contentA)), nil
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() {
		_, err := f.fetchContent(cancelCtx, "/acme/a.txt", objA.ContentHash, int64(len(contentA)))
		errCh1 <- err
	}()

	<-started
	errCh2 := make(chan error, 1)
	go func() {
		_, err := f.fetchContent(context.Background(), "/acme/a.txt", objA.ContentHash, int64(len(contentA)))
		errCh2 <- err
	}()
	time.Sleep(15 * time.Millisecond)

	cancelStart := time.Now()
	cancel()
	select {
	case err := <-errCh1:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled fetchContent = %v, want context.Canceled", err)
		}
		if time.Since(cancelStart) > time.Second {
			t.Fatalf("cancelled fetchContent took %s", time.Since(cancelStart))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetchContent ignored context cancellation")
	}

	close(release)
	select {
	case err := <-errCh2:
		if err != nil {
			t.Fatalf("second waiter failed after first cancelled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second waiter timed out")
	}
}

//go:build linux || darwin

package mountd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
)

func TestMountTimeoutHintNamesTheMacFUSEApprovalSteps(t *testing.T) {
	mac := MountTimeoutHint("darwin")
	for _, want := range []string{"macFUSE", "Privacy & Security", "Allow", "restart", "gs doctor", "gs init"} {
		if !strings.Contains(mac, want) {
			t.Fatalf("macOS hint %q is missing %q", mac, want)
		}
	}
	linux := MountTimeoutHint("linux")
	if strings.Contains(linux, "macFUSE") || !strings.Contains(linux, "/dev/fuse") {
		t.Fatalf("Linux hint %q", linux)
	}
}

func TestFetcherFailsFastDuringAnOutage(t *testing.T) {
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &Fetcher{cache: cache, inflight: map[string]*fetchCall{}, downUntil: time.Now().Add(time.Minute)}
	started := time.Now()
	_, err = f.FetchContent(context.Background(), "/a.txt", "sha256:abc", 3)
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("FetchContent during an outage = %v, want ErrServerUnreachable", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("FetchContent took %s during an outage; it should fail at once", time.Since(started))
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

func TestIDValidationAndControlSocketSecurity(t *testing.T) {
	runDir := shortTempDir(t, "gsrun-")
	mountsRoot := t.TempDir()

	validID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !IsValidID(validID) || ValidateID(validID) != nil {
		t.Fatalf("expected generated mount id %q to be valid", validID)
	}

	for _, bad := range []string{"", "short", "../../etc/passwd", "0123456789ABCDEF"} {
		if IsValidID(bad) {
			t.Fatalf("IsValidID(%q) = true, want false", bad)
		}
		if err := ValidateID(bad); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("ValidateID(%q) = %v, want ErrInvalidID", bad, err)
		}
		stateDir := StateDir(mountsRoot, bad)
		rel, relErr := filepath.Rel(mountsRoot, stateDir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("StateDir(%q) = %q escaped mountsRoot %q", bad, stateDir, mountsRoot)
		}
	}

	sup := NewSupervisor(context.Background(), Env{MountsRoot: mountsRoot, RuntimeDir: runDir}, io.Discard)
	reply := sup.Attach("../../etc/passwd")
	if reply.ErrorCode != "invalid_workspace_id" {
		t.Fatalf("Supervisor.Attach traversal reply = %+v, want invalid_workspace_id", reply)
	}

	listener, cleanup := ListenMountControl(runDir, validID)
	if listener == nil {
		t.Fatal("ListenMountControl returned nil listener")
	}
	defer cleanup()

	go ServeMountControl(listener, func(req ControlRequest) ControlReply {
		return ControlReply{PID: os.Getpid(), BaseCommit: "commit-ok"}
	})
	cReply, err := CallDaemon(runDir, validID, ControlRequest{Op: "status"})
	if err != nil || cReply.BaseCommit != "commit-ok" {
		t.Fatalf("CallDaemon = %+v, err=%v", cReply, err)
	}
}

func TestFetcherContextCancellationDoesNotAbortSharedBatch(t *testing.T) {
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
	started := make(chan struct{})
	release := make(chan struct{})
	f := NewFetcher(nil, nil, cache, manifest, func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
		close(started)
		<-release
		if _, err := cache.PutBytes(contentA); err != nil {
			return 0, 0, err
		}
		return 1, int64(len(contentA)), nil
	})

	cancelCtx, cancel := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() {
		_, err := f.FetchContent(cancelCtx, "/acme/a.txt", objA.ContentHash, int64(len(contentA)))
		errCh1 <- err
	}()

	<-started
	errCh2 := make(chan error, 1)
	go func() {
		_, err := f.FetchContent(context.Background(), "/acme/a.txt", objA.ContentHash, int64(len(contentA)))
		errCh2 <- err
	}()
	time.Sleep(15 * time.Millisecond)

	cancel()
	select {
	case err := <-errCh1:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled FetchContent = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FetchContent ignored context cancellation")
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

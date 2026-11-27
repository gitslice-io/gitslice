package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/mountd"
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
		code string
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
		sock := Runner{}.mountControlPath(bad)
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

	sup := mountd.NewSupervisor(context.Background(), r.mountEnv(context.Background()), io.Discard)
	reply := sup.Attach("../../etc/passwd")
	if reply.ErrorCode != "invalid_workspace_id" {
		t.Fatalf("Supervisor.Attach traversal reply = %+v, want invalid_workspace_id", reply)
	}
}

func TestMountRuntimeDirAndSocketSecurity(t *testing.T) {
	shortHome := shortTempDir(t, "gshome-")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", shortHome)

	gotRun := Runner{}.mountRuntimeDir()
	wantRun := filepath.Join(shortHome, ".gitslice", "run")
	if gotRun != wantRun {
		t.Fatalf("mountRuntimeDir() = %q, want %q", gotRun, wantRun)
	}
	if err := mountd.EnsureRuntimeDir(gotRun); err != nil {
		t.Fatalf("EnsureRuntimeDir: %v", err)
	}
	info, err := os.Lstat(gotRun)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir mode = %04o, want 0700", info.Mode().Perm())
	}

	unsafeDir := filepath.Join(shortHome, "unsafe-run")
	if err := os.MkdirAll(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mountd.VerifyRuntimeDir(unsafeDir); err == nil {
		t.Fatal("expected VerifyRuntimeDir to reject 0755 directory")
	}

	symlinkDir := filepath.Join(shortHome, "symlink-run")
	if err := os.Symlink(gotRun, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if err := mountd.VerifyRuntimeDir(symlinkDir); err == nil {
		t.Fatal("expected VerifyRuntimeDir to reject symlink")
	}

	regularFile := filepath.Join(gotRun, "not-a-socket.sock")
	if err := os.WriteFile(regularFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mountd.ListenControlSocket(regularFile); err == nil {
		t.Fatal("expected ListenControlSocket to refuse overwriting regular file")
	}

	t.Setenv("XDG_RUNTIME_DIR", gotRun)
	id, err := newMountID()
	if err != nil {
		t.Fatal(err)
	}
	listener, cleanup := Runner{}.listenMountControl(id)
	if listener == nil {
		t.Fatal("listenMountControl returned nil listener")
	}
	defer cleanup()

	sockInfo, err := os.Lstat(Runner{}.mountControlPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if sockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("control socket mode = %04o, want 0600", sockInfo.Mode().Perm())
	}

	go mountd.ServeMountControl(listener, func(req mountControlRequest) mountControlReply {
		return mountControlReply{PID: os.Getpid(), BaseCommit: "commit-ok"}
	})
	reply, err := Runner{}.callMountDaemon(id, mountControlRequest{Op: "status"})
	if err != nil {
		t.Fatalf("callMountDaemon over verified peer socket failed: %v", err)
	}
	if reply.BaseCommit != "commit-ok" {
		t.Fatalf("reply.BaseCommit = %q, want commit-ok", reply.BaseCommit)
	}
}

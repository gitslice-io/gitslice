package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
)

// maxMountRuntimeDirLen keeps "<runtimeDir>/gs-mount-<16hex>.sock" within
// macOS's 104-byte sockaddr_un.sun_path limit (103 non-NUL bytes).
const maxMountRuntimeDirLen = 72

// mountRuntimeDir is where the mount sockets live, in a user-owned 0700
// directory short enough for Unix domain sockets (104 bytes on macOS).
//
// A Runner with its own Home (an isolated per-conversation run, a test) keeps
// its sockets under that home, never the host user's: it ignores the host's
// XDG_RUNTIME_DIR, and its /tmp fallback is private to that home.
func (r Runner) mountRuntimeDir() string {
	if dir, named := r.Env["XDG_RUNTIME_DIR"]; named && dir != "" {
		return filepath.Clean(dir)
	}
	if r.Home == "" {
		if dir := r.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Clean(dir)
		}
	}
	if home := r.homeDir(); home != "" {
		candidate := filepath.Join(home, ".gitslice", "run")
		if len(candidate) <= maxMountRuntimeDirLen {
			return candidate
		}
	}
	// Fallback when HOME is unset or exceeds sun_path budget: verified via
	// strict Lstat ownership, non-symlink, and 0700 mode before use.
	tmp := os.TempDir()
	if runtime.GOOS == "darwin" {
		tmp = "/tmp"
	}
	name := fmt.Sprintf("gitslice-%d", os.Getuid())
	if r.Home != "" {
		sum := sha256.Sum256([]byte(r.Home))
		name += "-" + hex.EncodeToString(sum[:4])
	}
	return filepath.Join(tmp, name)
}

func verifyMountRuntimeDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("mount runtime directory %s is not a real directory", dir)
	}
	if uid, ok := fileOwnerUID(info); !ok || uid != os.Getuid() {
		return fmt.Errorf("mount runtime directory %s is not owned by current user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("mount runtime directory %s has unsafe mode %04o (want 0700)", dir, info.Mode().Perm())
	}
	return nil
}

func ensureMountRuntimeDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		if pInfo, err := os.Lstat(parent); err != nil || pInfo.Mode()&os.ModeSymlink != 0 || !pInfo.IsDir() {
			if err != nil {
				return err
			}
			return fmt.Errorf("mount runtime parent %s is not a real directory", parent)
		}
		created := false
		if err := os.Mkdir(dir, 0o700); err == nil {
			created = true
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if created {
			if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
				if uid, ok := fileOwnerUID(info); ok && uid == os.Getuid() {
					_ = os.Chmod(dir, 0o700)
				}
			}
		}
	}
	return verifyMountRuntimeDir(dir)
}

func listenUnixControlSocket(socket string) (net.Listener, error) {
	if err := ensureMountRuntimeDir(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to overwrite non-socket %s", socket)
		}
		_ = os.Remove(socket)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socket)
		return nil, err
	}
	return listener, nil
}

func verifyUnixPeerUID(conn net.Conn) error {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("unix connection does not support peer credential check")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var peerUID int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		peerUID, credErr = socketPeerUID(int(fd))
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if peerUID != os.Getuid() {
		return fmt.Errorf("rejected unix socket peer uid %d (expected %d)", peerUID, os.Getuid())
	}
	return nil
}

// mountControlPath is a lazy workspace's control socket, served by whichever
// process has it mounted.
func (r Runner) mountControlPath(id string) string {
	if !isValidMountID(id) {
		return filepath.Join(r.mountRuntimeDir(), "gs-mount-_invalid.sock")
	}
	return filepath.Join(r.mountRuntimeDir(), "gs-mount-"+id+".sock")
}

// mountSupervisorSocket is the socket of the mount supervisor (gs mountd) for
// this state directory: one process serves all of a user's mounts.
func (r Runner) mountSupervisorSocket() string {
	sum := sha256.Sum256([]byte(r.mountsRoot()))
	return filepath.Join(r.mountRuntimeDir(), "gs-mountd-"+hex.EncodeToString(sum[:4])+".sock")
}

// errMountUnreachable is returned when no daemon answers a workspace's or
// supervisor's control socket.
var errMountUnreachable = errors.New("no mount daemon answers")

// callUnixControl performs one framed JSON request/reply exchange over a
// user-verified Unix domain control socket.
func callUnixControl[Req, Reply any](socket string, timeout time.Duration, req Req) (Reply, error) {
	var reply Reply
	if err := verifyMountRuntimeDir(filepath.Dir(socket)); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	defer conn.Close()
	if err := verifyUnixPeerUID(conn); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply, err
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return reply, err
	}
	return reply, nil
}

// mountdRequest and mountdReply are the supervisor's own protocol, besides the
// per-workspace one: it attaches workspaces and says what it serves.
type mountdRequest struct {
	Op string `json:"op"` // "attach", "ping" or "list"
	ID string `json:"id,omitempty"`
}

type mountdReply struct {
	PID        int      `json:"pid"`
	Workspaces []string `json:"workspaces,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
	Error      string   `json:"error,omitempty"`
	Hint       string   `json:"hint,omitempty"`
}

// callMountd sends one request to the supervisor. An unreachable supervisor
// is errMountUnreachable; an error it reports comes back as a user error.
func callMountd(socket string, req mountdRequest) (mountdReply, error) {
	reply, err := callUnixControl[mountdRequest, mountdReply](socket, time.Minute, req)
	if err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, commandError{Code: firstNonEmpty(reply.ErrorCode, "mount_failed"), Message: reply.Error, Hint: reply.Hint}
	}
	return reply, nil
}

// mountControlRequest and mountControlReply are the daemon's control
// protocol: one JSON request and one JSON reply per connection.
type mountControlRequest struct {
	// Op is "status", "edits", "restore", "rebase", "switch" or "unmount".
	Op    string   `json:"op"`
	Paths []string `json:"paths,omitempty"`
	// Commit, for rebase and switch, is the base commit to move to, whose
	// manifest gs wrote to the workspace's state first; "" keeps the base.
	Commit string `json:"commit,omitempty"`
	// Resolutions, for rebase, decide each path both the local layer and
	// the new base changed (lazyfs.NeedsMerge).
	Resolutions map[string]mountResolution `json:"resolutions,omitempty"`
	// Diff, Keep and Expect are lazyfs.FS.Switch's arguments.
	Diff   map[string]*lazyfs.BaseEntry `json:"diff,omitempty"`
	Keep   []string                     `json:"keep,omitempty"`
	Expect []lazyfs.Edit                `json:"expect,omitempty"`
}

// mountResolution resolves a path in a rebase, decided by gs for the local
// change Ours (editFingerprint); a different local change fails the rebase,
// as it was not decided on.
type mountResolution struct {
	Ours     string `json:"ours"`
	Keep     bool   `json:"keep,omitempty"`
	Theirs   bool   `json:"theirs,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Mode     uint32 `json:"mode,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

func (res mountResolution) result() lazyfs.MergeResult {
	return lazyfs.MergeResult{Keep: res.Keep, Theirs: res.Theirs, Delete: res.Delete, Content: res.Content, Mode: res.Mode, Conflict: res.Conflict}
}

// editFingerprint names a local change: "delete", or its content and mode.
func editFingerprint(edit lazyfs.Edit) string {
	if edit.Op == "delete" {
		return "delete"
	}
	return fmt.Sprintf("%s:%o", edit.ContentHash, normalizedSnapshotMode(edit.Mode))
}

type mountControlReply struct {
	PID          int           `json:"pid"`
	MountedAt    string        `json:"mounted_at"`
	Opens        int64         `json:"opens"`
	Fetches      int64         `json:"fetches"`
	FetchedFiles int64         `json:"fetched_files"`
	FetchedBytes int64         `json:"fetched_bytes"`
	Errors       int64         `json:"errors"`
	LastError    string        `json:"last_error,omitempty"`
	Edits        []lazyfs.Edit `json:"edits,omitempty"`
	// BaseCommit is the layers' base commit.
	BaseCommit string `json:"base_commit,omitempty"`
	// BaseFiles holds base entries from the daemon's in-memory manifest for
	// touched paths in Edits, and BaseFilesDigest identifies the full manifest
	// multiset so clients avoid loading manifest-<commit> when live.
	BaseFiles       map[string]BaseSnapshotFile `json:"base_files,omitempty"`
	BaseFilesDigest string                      `json:"base_files_digest,omitempty"`
	// Conflicts and Changed report a rebase or a switch: the paths the
	// rebase marked as conflicts, and how many paths the view changed at.
	Conflicts []string `json:"conflicts,omitempty"`
	Changed   int      `json:"changed,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func (r Runner) callMountDaemon(id string, req mountControlRequest) (mountControlReply, error) {
	var reply mountControlReply
	if err := validateMountID(id); err != nil {
		return reply, err
	}
	reply, err := callUnixControl[mountControlRequest, mountControlReply](r.mountControlPath(id), 5*time.Minute, req)
	if err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

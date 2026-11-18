// Package mountd implements the FUSE lazy-workspace mount supervisor, control
// socket IPC protocol, on-demand blob fetcher, walk-order read-ahead, and
// platform mount-table inspection.
package mountd

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

// MaxRuntimeDirLen keeps "<runtimeDir>/gs-mount-<16hex>.sock" within macOS's
// 104-byte sockaddr_un.sun_path limit (103 non-NUL bytes).
const MaxRuntimeDirLen = 72

// Error represents a structured mount/supervisor error carrying a CLI error
// code and optional hint.
type Error struct {
	Code    string
	Message string
	Hint    string
}

func (e *Error) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func (e *Error) UserError() (string, string, string) {
	return e.Code, e.Message, e.Hint
}

// NewError creates a structured mount error.
func NewError(code, message, hint string) error {
	return &Error{Code: code, Message: message, Hint: hint}
}

// RuntimeDir resolves the user-owned 0700 directory for mount Unix sockets.
// When home is isolated (non-empty), host XDG_RUNTIME_DIR is ignored unless
// explicitly set in envOverrides.
func RuntimeDir(home, resolvedHome string, envOverrides map[string]string, getenv func(string) string) string {
	if dir, named := envOverrides["XDG_RUNTIME_DIR"]; named && dir != "" {
		return filepath.Clean(dir)
	}
	if home == "" && getenv != nil {
		if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Clean(dir)
		}
	}
	if resolvedHome != "" {
		candidate := filepath.Join(resolvedHome, ".gitslice", "run")
		if len(candidate) <= MaxRuntimeDirLen {
			return candidate
		}
	}
	tmp := os.TempDir()
	if runtime.GOOS == "darwin" {
		tmp = "/tmp"
	}
	name := fmt.Sprintf("gitslice-%d", os.Getuid())
	if home != "" {
		sum := sha256.Sum256([]byte(home))
		name += "-" + hex.EncodeToString(sum[:4])
	}
	return filepath.Join(tmp, name)
}

// VerifyRuntimeDir verifies that dir is a real directory owned by the current
// user with 0700 permissions.
func VerifyRuntimeDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("mount runtime directory %s is not a real directory", dir)
	}
	if uid, ok := FileOwnerUID(info); !ok || uid != os.Getuid() {
		return fmt.Errorf("mount runtime directory %s is not owned by current user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("mount runtime directory %s has unsafe mode %04o (want 0700)", dir, info.Mode().Perm())
	}
	return nil
}

// EnsureRuntimeDir creates dir with 0700 permissions if missing and verifies it.
func EnsureRuntimeDir(dir string) error {
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
				if uid, ok := FileOwnerUID(info); ok && uid == os.Getuid() {
					_ = os.Chmod(dir, 0o700)
				}
			}
		}
	}
	return VerifyRuntimeDir(dir)
}

// ListenControlSocket binds a 0600 Unix domain socket inside a verified 0700
// runtime directory, refusing to overwrite non-sockets.
func ListenControlSocket(socket string) (net.Listener, error) {
	if err := EnsureRuntimeDir(filepath.Dir(socket)); err != nil {
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

// VerifyPeerUID verifies that the connected Unix peer matches os.Getuid().
func VerifyPeerUID(conn net.Conn) error {
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
		peerUID, credErr = SocketPeerUID(int(fd))
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

// ControlPath returns the control socket path for workspace id under runtimeDir.
func ControlPath(runtimeDir, id string) string {
	if !IsValidID(id) {
		return filepath.Join(runtimeDir, "gs-mount-_invalid.sock")
	}
	return filepath.Join(runtimeDir, "gs-mount-"+id+".sock")
}

// SupervisorSocket returns the supervisor socket path for mountsRoot under runtimeDir.
func SupervisorSocket(runtimeDir, mountsRoot string) string {
	sum := sha256.Sum256([]byte(mountsRoot))
	return filepath.Join(runtimeDir, "gs-mountd-"+hex.EncodeToString(sum[:4])+".sock")
}

// ErrUnreachable is returned when no daemon or supervisor answers on its socket.
var ErrUnreachable = errors.New("no mount daemon answers")

// CallUnixControl performs one framed JSON request/reply exchange over a
// user-verified Unix domain control socket.
func CallUnixControl[Req, Reply any](socket string, timeout time.Duration, req Req) (Reply, error) {
	var reply Reply
	if err := VerifyRuntimeDir(filepath.Dir(socket)); err != nil {
		return reply, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return reply, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer conn.Close()
	if err := VerifyPeerUID(conn); err != nil {
		return reply, fmt.Errorf("%w: %v", ErrUnreachable, err)
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

// SupervisorRequest and SupervisorReply are the mount supervisor's control protocol.
type SupervisorRequest struct {
	Op string `json:"op"` // "attach", "ping" or "list"
	ID string `json:"id,omitempty"`
}

type SupervisorReply struct {
	PID        int      `json:"pid"`
	Workspaces []string `json:"workspaces,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
	Error      string   `json:"error,omitempty"`
	Hint       string   `json:"hint,omitempty"`
}

// CallSupervisor sends one request to the mount supervisor socket.
func CallSupervisor(socket string, req SupervisorRequest) (SupervisorReply, error) {
	reply, err := CallUnixControl[SupervisorRequest, SupervisorReply](socket, time.Minute, req)
	if err != nil {
		return reply, err
	}
	if reply.Error != "" {
		code := reply.ErrorCode
		if code == "" {
			code = "mount_failed"
		}
		return reply, NewError(code, reply.Error, reply.Hint)
	}
	return reply, nil
}

// ControlRequest and ControlReply are a mounted workspace's control protocol.
type ControlRequest struct {
	Op          string                       `json:"op"`
	Paths       []string                     `json:"paths,omitempty"`
	Commit      string                       `json:"commit,omitempty"`
	Resolutions map[string]Resolution        `json:"resolutions,omitempty"`
	Diff        map[string]*lazyfs.BaseEntry `json:"diff,omitempty"`
	Keep        []string                     `json:"keep,omitempty"`
	Expect      []lazyfs.Edit                `json:"expect,omitempty"`
}

// Resolution resolves a path in a rebase decided for Ours (EditFingerprint).
type Resolution struct {
	Ours     string `json:"ours"`
	Keep     bool   `json:"keep,omitempty"`
	Theirs   bool   `json:"theirs,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Mode     uint32 `json:"mode,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

func (res Resolution) Result() lazyfs.MergeResult {
	return lazyfs.MergeResult{Keep: res.Keep, Theirs: res.Theirs, Delete: res.Delete, Content: res.Content, Mode: res.Mode, Conflict: res.Conflict}
}

// EditFingerprint names a local change: "delete", or its content and normalized mode.
func EditFingerprint(edit lazyfs.Edit) string {
	if edit.Op == "delete" {
		return "delete"
	}
	return fmt.Sprintf("%s:%o", edit.ContentHash, NormalizeSnapshotMode(edit.Mode))
}

// NormalizeSnapshotMode normalizes file permissions to Git's 0120000, 0100755, or 0100644.
func NormalizeSnapshotMode(mode uint32) uint32 {
	if mode&0o170000 == 0o120000 {
		return 0o120000
	}
	if mode&0o111 != 0 {
		return 0o100755
	}
	return 0o100644
}

// BaseFile describes one base snapshot file returned by the mount daemon.
type BaseFile struct {
	Path        string `json:"path"`
	RelPath     string `json:"rel_path"`
	ContentHash string `json:"content_hash"`
	Mode        uint32 `json:"mode"`
	Size        int64  `json:"size"`
}

type ControlReply struct {
	PID             int                 `json:"pid"`
	MountedAt       string              `json:"mounted_at"`
	Opens           int64               `json:"opens"`
	Fetches         int64               `json:"fetches"`
	FetchedFiles    int64               `json:"fetched_files"`
	FetchedBytes    int64               `json:"fetched_bytes"`
	Errors          int64               `json:"errors"`
	LastError       string              `json:"last_error,omitempty"`
	Edits           []lazyfs.Edit       `json:"edits,omitempty"`
	BaseCommit      string              `json:"base_commit,omitempty"`
	BaseFiles       map[string]BaseFile `json:"base_files,omitempty"`
	BaseFilesDigest string              `json:"base_files_digest,omitempty"`
	Conflicts       []string            `json:"conflicts,omitempty"`
	Changed         int                 `json:"changed,omitempty"`
	Error           string              `json:"error,omitempty"`
}

// CallDaemon sends one request to workspace id's control socket under runtimeDir.
func CallDaemon(runtimeDir, id string, req ControlRequest) (ControlReply, error) {
	var reply ControlReply
	if err := ValidateID(id); err != nil {
		return reply, err
	}
	reply, err := CallUnixControl[ControlRequest, ControlReply](ControlPath(runtimeDir, id), 5*time.Minute, req)
	if err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

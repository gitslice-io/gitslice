package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// isMounted reports whether a file system is mounted at dir. It reads the
// mount table (getfsstat, without waiting on any file system), never dir
// itself, which hangs when its daemon is gone.
func isMounted(dir string) bool {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n == 0 {
		return false
	}
	stats := make([]unix.Statfs_t, n+8)
	n, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if err != nil {
		return false
	}
	// /tmp and /var are symlinks into /private on macOS; the table has the
	// resolved paths.
	resolved := dir
	if real, err := filepath.EvalSymlinks(filepath.Dir(dir)); err == nil {
		resolved = filepath.Join(real, filepath.Base(dir))
	}
	for _, st := range stats[:n] {
		onName := unix.ByteSliceToString(st.Mntonname[:])
		if onName == dir || onName == resolved {
			return true
		}
	}
	return false
}

func fileOwnerUID(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

func socketPeerUID(fd int) (int, error) {
	const darwinXucredVersion = 0
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return -1, err
	}
	if cred.Version != darwinXucredVersion {
		return -1, fmt.Errorf("unexpected xucred version %d", cred.Version)
	}
	return int(cred.Uid), nil
}

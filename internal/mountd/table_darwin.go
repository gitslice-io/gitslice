package mountd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// IsMounted reports whether a file system is mounted at dir by inspecting the
// kernel mount table without statting dir itself.
func IsMounted(dir string) bool {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n == 0 {
		return false
	}
	stats := make([]unix.Statfs_t, n+8)
	n, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if err != nil {
		return false
	}
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

func FileOwnerUID(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

func SocketPeerUID(fd int) (int, error) {
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

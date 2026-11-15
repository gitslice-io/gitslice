package cli

import (
	"path/filepath"

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

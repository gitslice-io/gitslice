//go:build !linux && !darwin

package mountd

import (
	"errors"
	"os"
)

func IsMounted(dir string) bool { return false }

func FileOwnerUID(info os.FileInfo) (int, bool) { return os.Getuid(), true }

func SocketPeerUID(fd int) (int, error) {
	return -1, errors.New("unix peer credentials are not supported on this platform")
}

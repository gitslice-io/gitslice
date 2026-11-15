//go:build !linux && !darwin

package cli

import (
	"errors"
	"os"
)

func isMounted(dir string) bool { return false }

func fileOwnerUID(info os.FileInfo) (int, bool) { return os.Getuid(), true }

func socketPeerUID(fd int) (int, error) {
	return -1, errors.New("unix peer credentials are not supported on this platform")
}

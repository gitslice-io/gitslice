//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package agentd

import "errors"

var ErrDoctorDiskUnknown = errors.New("disk space is unknown")

func DoctorFreeBytes(string) (uint64, error) {
	return 0, ErrDoctorDiskUnknown
}

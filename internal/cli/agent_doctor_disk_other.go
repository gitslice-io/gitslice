//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package cli

import "errors"

var errAgentDoctorDiskUnknown = errors.New("disk space is unknown")

func agentDoctorFreeBytes(string) (uint64, error) {
	return 0, errAgentDoctorDiskUnknown
}

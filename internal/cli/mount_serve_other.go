//go:build !linux && !darwin

package cli

import "context"

func errMountUnsupported() error {
	return userError("mount_unsupported", "gs mount needs FUSE, which this system does not have", "Use gs init for a full checkout.")
}

func (r Runner) serveMount(ctx context.Context, rec mountRecord, debug bool, ready func()) error {
	return errMountUnsupported()
}

func (r Runner) startMountDaemon(rec mountRecord) error { return errMountUnsupported() }

func forceUnmount(dir string) error { return errMountUnsupported() }

package cli

import "fmt"

// addFUSECheck records whether lazy workspaces (gs init --lazy) can mount.
// installed says whether macFUSE is on this machine; probe tries a real mount
// and says whether it ran out of time, which is how macFUSE looks while its
// system extension is waiting to be approved.
func (d *doctor) addFUSECheck(installed bool, probe func() (timedOut bool, err error)) {
	if !installed {
		d.add("fuse", "skip", "macFUSE is not installed; only gs init --lazy needs it", "Install it from https://macfuse.github.io, or use gs init to check files out.")
		return
	}
	timedOut, err := probe()
	switch {
	case timedOut:
		d.add("fuse", "warn", "macFUSE is installed but a test mount did not finish: its system extension is probably not approved yet", mountTimeoutHint("darwin"))
	case err != nil:
		d.add("fuse", "warn", fmt.Sprintf("macFUSE is installed but a test mount failed: %v", err), "Reinstall macFUSE and restart your Mac; gs init (without --lazy) works without it.")
	default:
		d.add("fuse", "ok", "macFUSE works: a test mount succeeded", "")
	}
}

// mountTimeoutHint says what to do when the kernel did not bring a mount up.
// On macOS that is almost always macFUSE installed but not yet allowed to
// load: the mount call waits for an approval nobody has given.
func mountTimeoutHint(goos string) string {
	if goos == "darwin" {
		return "macFUSE is probably installed but its system extension is not approved yet. " +
			"Open System Settings > Privacy & Security, scroll to the message about blocked system software, " +
			"click Allow for macFUSE, and restart your Mac (on Apple silicon you may first need to allow " +
			"third-party extensions in Recovery mode: Startup Security Utility > Reduced Security). " +
			"Then run gs doctor to check it works. Until then, gs init (without --lazy) checks the slice out as plain files."
	}
	return "The kernel did not answer the FUSE mount. Check that /dev/fuse exists, fusermount3 is installed (package fuse3) and you may use it, then try again. Without FUSE, use gs init."
}

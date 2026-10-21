package cli

import (
	"errors"
	"os"

	"gitslice.io/gitslice/internal/lazyfs/fusefs"
)

// checkFUSE tries a real mount when macFUSE is installed, since "installed"
// and "approved to load" are different states.
func (d *doctor) checkFUSE() {
	_, statErr := os.Stat("/Library/Filesystems/macfuse.fs")
	d.addFUSECheck(statErr == nil, func() (bool, error) {
		err := fusefs.Probe(fusefs.DefaultMountTimeout)
		return errors.Is(err, fusefs.ErrMountTimeout), err
	})
}

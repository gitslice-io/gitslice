//go:build !darwin

package cli

// checkFUSE is for macOS, where macFUSE can be installed but not yet allowed
// to load. Elsewhere gs mount reports what is missing when it is used.
func (d *doctor) checkFUSE() {}

//go:build !linux && !darwin

package cli

func isMounted(dir string) bool { return false }

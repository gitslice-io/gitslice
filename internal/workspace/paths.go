package workspace

import (
	"path"
	"strings"

	"gitslice.io/gitslice/internal/paths"
)

// CleanGlobalPath normalizes a repository-global slash path.
func CleanGlobalPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "/", nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/", nil
	}
	return cleaned, nil
}

// RelPath maps a repository-global path into its workspace-relative slash path,
// rejecting reserved .gs paths and paths outside ws.IncludedPaths.
func RelPath(ws Config, globalPath string) (string, error) {
	cleaned, err := CleanGlobalPath(globalPath)
	if err != nil {
		return "", err
	}
	if err := EnsureNotReservedGsPath(cleaned); err != nil {
		return "", err
	}
	if paths.InAnyPrefix(ws.IncludedPaths, cleaned) {
		rel := strings.TrimPrefix(cleaned, "/")
		if rel == "" {
			return "", &OutsideSliceError{Path: globalPath}
		}
		return rel, nil
	}
	return "", &OutsideSliceError{Path: globalPath}
}

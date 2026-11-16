package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupUncommittedEdits copies the workspace files at logical paths (as
// /account/...) into .gs/sync-backup/<time>/ before gs sync merges into them,
// and returns that folder relative to the workspace root. A path deleted
// locally has nothing to copy.
func (r Runner) backupUncommittedEdits(root string, logicalPaths []string) (string, error) {
	rel := filepath.Join(".gs", "sync-backup", time.Now().UTC().Format("20060102T150405Z"))
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, p := range logicalPaths {
		name := strings.TrimPrefix(p, "/")
		data, err := readConfinedWorkspaceFile(root, name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("back up %s: %w", p, err)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return "", err
		}
	}
	return rel, nil
}

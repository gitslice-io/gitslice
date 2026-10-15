package gitcompat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ensureProjectedRepo creates the bare repository for a projection if needed.
func ensureProjectedRepo(ctx context.Context, repoPath string) error {
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return err
	}
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repoPath); err != nil {
		return err
	}
	return configureProjectedRepo(ctx, repoPath)
}

// resetProjectedRepo drops the projected branch (objects stay until git gc)
// and reapplies the repository settings, which older caches may lack.
func resetProjectedRepo(ctx context.Context, repoPath string) error {
	if _, err := gitOutput(ctx, repoPath, nil, "rev-parse", "--verify", "-q", projectedBranch); err == nil {
		if err := runGit(ctx, repoPath, nil, "update-ref", "-d", projectedBranch); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(repoPath, projectionStateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return configureProjectedRepo(ctx, repoPath)
}

func configureProjectedRepo(ctx context.Context, repoPath string) error {
	settings := [][]string{
		{"config", "http.receivepack", "false"},
		// Go fetches pseudo-versions by commit id.
		{"config", "uploadpack.allowReachableSHA1InWant", "true"},
		// The history packs are what a mirror copies; a repack would replace
		// them with packs it has not seen.
		{"config", "gc.auto", "0"},
		{"symbolic-ref", "HEAD", projectedBranch},
	}
	for _, args := range settings {
		if err := runGit(ctx, repoPath, nil, args...); err != nil {
			return err
		}
	}
	return nil
}

// newPackNames returns the names in after that are not in before.
func newPackNames(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, name := range before {
		had[name] = true
	}
	var out []string
	for _, name := range after {
		if !had[name] {
			out = append(out, name)
		}
	}
	return out
}

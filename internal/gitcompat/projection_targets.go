package gitcompat

import (
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// projectionTargets returns the slice paths a commit may have changed: each
// changed path inside an included prefix, or the prefix itself when the
// changed path is an ancestor of it (an account root, a moved parent). Nested
// targets are dropped because their parent target already covers them.
func projectionTargets(changed, prefixes []string) []string {
	set := map[string]struct{}{}
	for _, p := range changed {
		p = "/" + strings.Trim(p, "/")
		for _, prefix := range prefixes {
			switch {
			case paths.Contains(prefix, p):
				set[p] = struct{}{}
			case paths.Contains(p, prefix):
				set[prefix] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(set))
	for target := range set {
		covered := false
		for other := range set {
			if other != target && paths.Contains(other, target) {
				covered = true
				break
			}
		}
		if !covered {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	return targets
}

// applyListing replaces everything under each target with the listed files,
// updates files in place, and returns the resulting changes sorted by path.
func applyListing(files map[string]projectedFile, targets []string, listing map[string][]storage.FileEntry) []fileChange {
	next := map[string]*fileChange{}
	for _, target := range targets {
		gitTarget := strings.Trim(target, "/")
		for path := range files {
			if gitTarget == "" || path == gitTarget || strings.HasPrefix(path, gitTarget+"/") {
				next[path] = &fileChange{Path: path, Delete: true}
			}
		}
		for _, file := range listing[target] {
			path := strings.TrimPrefix(file.Path, "/")
			if !projectablePath(path) {
				continue
			}
			next[path] = &fileChange{Path: path, Mode: gitFileMode(file.Mode), ContentHash: file.ContentHash, Size: file.Size}
		}
	}
	changes := make([]fileChange, 0, len(next))
	for path, change := range next {
		old, existed := files[path]
		if change.Delete {
			if existed {
				delete(files, path)
				changes = append(changes, *change)
			}
			continue
		}
		if existed && old.Mode == change.Mode && old.ContentHash == change.ContentHash {
			continue
		}
		blob := ""
		if existed && old.ContentHash == change.ContentHash {
			blob = old.Blob
		}
		files[path] = projectedFile{Mode: change.Mode, ContentHash: change.ContentHash, Blob: blob}
		changes = append(changes, *change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func projectionPrefixes(slice *corev1.Slice) ([]string, error) {
	var prefixes []string
	for _, prefix := range slice.Definition.IncludedPaths {
		// Included paths may be account roots (one segment) for home slices,
		// so use CanonicalPrefix rather than Canonical.
		canonical, err := paths.CanonicalPrefix(prefix)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, canonical)
	}
	sort.Strings(prefixes)
	return prefixes, nil
}

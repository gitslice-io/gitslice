package storage

import (
	"sort"

	"github.com/gitslice-io/gitslice/internal/paths"
)

func CoverageAncestorPrefixes(p string) []string {
	return paths.AncestorPrefixes(p)
}

func CoveragePrefixUnion(changedPaths []string) []string {
	seen := map[string]struct{}{}
	for _, p := range changedPaths {
		for _, prefix := range CoverageAncestorPrefixes(p) {
			seen[prefix] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for prefix := range seen {
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func AssembleCoverageByPath(changedPaths []string, prefixSliceIDs map[string][]string) map[string][]string {
	out := make(map[string][]string, len(changedPaths))
	for _, p := range changedPaths {
		seen := map[string]struct{}{}
		for _, prefix := range CoverageAncestorPrefixes(p) {
			for _, sliceID := range prefixSliceIDs[prefix] {
				seen[sliceID] = struct{}{}
			}
		}
		ids := make([]string, 0, len(seen))
		for sliceID := range seen {
			ids = append(ids, sliceID)
		}
		sort.Strings(ids)
		out[p] = ids
	}
	return out
}

package triggers

import (
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// ChangesFromPatchset says what a patchset did to each file, from its edits:
// add and copy add, update modifies, upsert adds or modifies depending on
// whether the base had the file, delete deletes, and rename moves. A path
// edited several times keeps its last edit. Paths without edits (old
// patchsets) count as modified.
func ChangesFromPatchset(patchset *corev1.Patchset) []Change {
	if patchset == nil {
		return nil
	}
	existed := map[string]bool{}
	for _, base := range patchset.GetPathBases() {
		existed[base.GetPath()] = base.GetExists()
	}
	byPath := map[string]Change{}
	for _, edit := range patchset.GetFileEdits() {
		p := strings.TrimSpace(edit.GetPath())
		if p == "" {
			continue
		}
		c := Change{Path: p}
		switch edit.GetOp() {
		case "add", "copy":
			c.Kind = ChangeAdded
		case "update":
			c.Kind = ChangeModified
		case "upsert":
			c.Kind = ChangeModified
			if exists, known := existed[p]; known && !exists {
				c.Kind = ChangeAdded
			}
		case "delete":
			c.Kind = ChangeDeleted
		case "rename", "move":
			c.Kind = ChangeMoved
			c.OldPath = strings.TrimSpace(edit.GetOldPath())
		default:
			continue
		}
		byPath[p] = c
	}
	for _, p := range patchset.GetChangedPaths() {
		if _, ok := byPath[p]; !ok && !isMoveSource(byPath, p) {
			byPath[p] = Change{Path: p, Kind: ChangeModified}
		}
	}
	out := make([]Change, 0, len(byPath))
	for _, c := range byPath {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ChangesFromPaths treats every path as modified, for events that carry
// only paths.
func ChangesFromPaths(paths []string) []Change {
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		out = append(out, Change{Path: p, Kind: ChangeModified})
	}
	return out
}

func isMoveSource(byPath map[string]Change, p string) bool {
	for _, c := range byPath {
		if c.Kind == ChangeMoved && c.OldPath == p {
			return true
		}
	}
	return false
}

package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/diffutil"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// pathChange is one changed path of a workspace, as gs status and
// gs diff --stat show it.
type pathChange struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // added, modified, deleted, renamed
	OldPath string `json:"old_path,omitempty"`
	// Lines added and removed. Counted only when both sides are on this
	// machine (a lazy workspace may not have fetched the base); a binary file
	// is never counted.
	Added   int  `json:"lines_added,omitempty"`
	Removed int  `json:"lines_removed,omitempty"`
	Counted bool `json:"lines_counted"`
	Binary  bool `json:"binary,omitempty"`
}

// summarizeChanges labels each edit against base and counts its lines from
// content already on this machine; it never reads from the server.
func summarizeChanges(edits []*corev1.FileEdit, base BaseSnapshot, current map[string]workingFile, cache *clientcache.ObjectCache) []pathChange {
	baseData := func(p string) ([]byte, bool) {
		file, ok := base.Files[p]
		if !ok {
			return nil, true
		}
		if cache == nil || !cache.Exists(file.ContentHash) {
			return nil, false
		}
		data, err := cache.Read(file.ContentHash)
		return data, err == nil
	}
	currentData := func(p string) ([]byte, bool) {
		file, ok := current[p]
		if !ok {
			return nil, true
		}
		if file.AbsPath == "" && (cache == nil || !cache.Exists(file.ContentHash)) {
			return nil, false
		}
		data, err := readWorkingFile(file, cache)
		return data, err == nil
	}

	var out []pathChange
	for _, edit := range edits {
		if edit == nil || edit.Op == "mkdir" {
			continue
		}
		change := pathChange{Path: edit.Path}
		oldPath := edit.Path
		switch edit.Op {
		case "delete":
			change.Kind = "deleted"
		case "rename":
			change.Kind = "renamed"
			change.OldPath = edit.OldPath
			oldPath = edit.OldPath
		default:
			if _, ok := base.Files[edit.Path]; ok {
				change.Kind = "modified"
			} else {
				change.Kind = "added"
			}
		}
		oldData, oldOK := baseData(oldPath)
		newData, newOK := currentData(edit.Path)
		if edit.Op == "delete" {
			newData, newOK = nil, true
		}
		if oldOK && newOK {
			change.Added, change.Removed, change.Binary = diffutil.LineStats(oldData, newData)
			change.Counted = !change.Binary
		}
		out = append(out, change)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// printChangeSummary writes one line per change: its kind, path and line
// counts, followed by the totals.
func printChangeSummary(w io.Writer, changes []pathChange, indent string, color bool) {
	kindWidth := 0
	pathWidth := 0
	for _, c := range changes {
		kindWidth = max(kindWidth, len(c.Kind))
		pathWidth = max(pathWidth, len(changeLabel(c)))
	}
	for _, c := range changes {
		label := changeLabel(c)
		counts := changeCounts(c, color)
		pad := ""
		if counts != "" {
			pad = strings.Repeat(" ", pathWidth-len(label)) + "  "
		}
		fmt.Fprintf(w, "%s%-*s  %s%s%s\n", indent, kindWidth, c.Kind, colorize(color, ansiBlue, label), pad, counts)
	}
}

func changeLabel(c pathChange) string {
	if c.Kind == "renamed" && c.OldPath != "" {
		return c.OldPath + " -> " + c.Path
	}
	return c.Path
}

func changeCounts(c pathChange, color bool) string {
	switch {
	case c.Binary:
		return "binary"
	case !c.Counted:
		return ""
	case c.Added == 0 && c.Removed == 0:
		return ""
	}
	var parts []string
	if c.Added > 0 {
		parts = append(parts, colorize(color, ansiGreen, fmt.Sprintf("+%d", c.Added)))
	}
	if c.Removed > 0 {
		parts = append(parts, colorize(color, ansiRed, fmt.Sprintf("-%d", c.Removed)))
	}
	return strings.Join(parts, " ")
}

// changeTotals sums the counted lines, and says how many changes were not
// counted.
func changeTotals(changes []pathChange) (added, removed, uncounted int) {
	for _, c := range changes {
		if c.Counted {
			added += c.Added
			removed += c.Removed
		} else if !c.Binary {
			uncounted++
		}
	}
	return added, removed, uncounted
}

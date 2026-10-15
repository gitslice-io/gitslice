package service

import (
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func treeEntryFromFile(entry storage.FileEntry) *corev1.TreeEntry {
	return &corev1.TreeEntry{
		Path:        entry.Path,
		Name:        path.Base(entry.Path),
		Kind:        corev1.EntryKind_ENTRY_KIND_FILE,
		Mode:        entry.Mode,
		BlobId:      entry.BlobID,
		Size:        entry.Size,
		ContentHash: entry.ContentHash,
	}
}

func treeEntryFromRepositoryEntry(entry storage.TreeEntry) *corev1.TreeEntry {
	kind := corev1.EntryKind_ENTRY_KIND_UNSPECIFIED
	switch entry.Kind {
	case "file":
		kind = corev1.EntryKind_ENTRY_KIND_FILE
	case "directory":
		kind = corev1.EntryKind_ENTRY_KIND_DIRECTORY
	case "symlink":
		kind = corev1.EntryKind_ENTRY_KIND_SYMLINK
	}
	return &corev1.TreeEntry{
		Path:        entry.Path,
		Name:        entry.Name,
		Kind:        kind,
		Mode:        entry.Mode,
		TreeId:      entry.TreeID,
		BlobId:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Size:        entry.Size,
	}
}

// immediateDirectoryEntries reconstructs direct child entries for prefix from a
// projected file list.
//
// The input is already scoped to the selected slice, but it may contain files
// under sibling prefixes, for example "/acme/backend/..." and
// "/acme/payment/shared/...". When listing "/acme/payment", sibling files must
// be ignored rather than treated as relative paths. Files exactly at the
// requested prefix are returned as files; deeper descendants synthesize a
// directory entry for the first remaining path segment. Directory TreeIds are
// computed from the same projected file list so clients get stable IDs for the
// visible projection rather than IDs from the unfiltered global tree.
func immediateDirectoryEntries(prefix string, files []storage.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel, ok := relativeDirectoryPath(prefix, file.Path)
		if !ok {
			continue
		}
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{
				Path:   childPath,
				Name:   parts[0],
				Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
				TreeId: directoryTreeID(childPath, files),
			}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

// immediateProjectedDirectoryEntries reconstructs direct child entries for
// prefix from a projected tree-entry list. Unlike the file-only projection
// helper, this preserves empty directories that are explicitly present in the
// repository tree.
func immediateProjectedDirectoryEntries(prefix string, entries []storage.TreeEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, entry := range entries {
		rel, ok := relativeDirectoryPath(prefix, entry.Path)
		if !ok {
			continue
		}
		if rel == "" {
			if entry.Kind != "directory" {
				byPath[entry.Path] = treeEntryFromRepositoryEntry(entry)
			}
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			child := entry
			if child.Kind == "directory" {
				child.TreeID = projectedDirectoryTreeID(childPath, entries)
			}
			byPath[childPath] = treeEntryFromRepositoryEntry(child)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{
				Path:   childPath,
				Name:   parts[0],
				Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
				TreeId: projectedDirectoryTreeID(childPath, entries),
			}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func paginateDirectoryEntries(entries []*corev1.TreeEntry, pageSize int32, cursor string) ([]*corev1.TreeEntry, string) {
	entries = append([]*corev1.TreeEntry(nil), entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		leftName, rightName := entries[i].Name, entries[j].Name
		if leftName == rightName {
			return entries[i].Path < entries[j].Path
		}
		return leftName < rightName
	})
	start := 0
	cursor = strings.TrimSpace(cursor)
	if cursor != "" {
		for start < len(entries) && directoryEntryCursorCompare(entries[start], cursor) <= 0 {
			start++
		}
	}
	entries = entries[start:]
	if pageSize <= 0 || int(pageSize) >= len(entries) {
		return entries, ""
	}
	limit := int(pageSize)
	nextCursor := entries[limit-1].Name
	if nextCursor == "" {
		nextCursor = entries[limit-1].Path
	}
	return entries[:limit], nextCursor
}

func directoryEntryCursorCompare(entry *corev1.TreeEntry, cursor string) int {
	if strings.Contains(cursor, "/") {
		return strings.Compare(entry.Path, cursor)
	}
	return strings.Compare(entry.Name, cursor)
}

// relativeDirectoryPath returns filePath relative to prefix when filePath is
// exactly prefix or is contained below prefix.
//
// The boolean is the containment result. This is stricter than strings.TrimPrefix:
// "/acme/backend/a.go" is not inside "/acme/payment", even though a blind trim
// would leave the original absolute path and later make it look like an "acme"
// child. The root path is handled specially because every absolute repository
// path is below "/".
func relativeDirectoryPath(prefix, filePath string) (string, bool) {
	if prefix == "/" {
		if !strings.HasPrefix(filePath, "/") {
			return "", false
		}
		return strings.TrimPrefix(filePath, "/"), true
	}
	if filePath == prefix {
		return "", true
	}
	if !strings.HasPrefix(filePath, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(filePath, prefix+"/"), true
}

func immediateChildPath(prefix, child string) (string, bool) {
	rel, ok := relativeDirectoryPath(prefix, child)
	if !ok || rel == "" {
		return "", false
	}
	segment := strings.SplitN(rel, "/", 2)[0]
	if prefix == "/" {
		return "/" + segment, true
	}
	return strings.TrimRight(prefix, "/") + "/" + segment, true
}

func projectedDirectoryTreeID(prefix string, entries []storage.TreeEntry) string {
	uiEntries := immediateProjectedDirectoryEntriesNoTree(prefix, entries)
	idEntries := make([]objectid.TreeEntry, 0, len(uiEntries))
	for _, entry := range uiEntries {
		idEntries = append(idEntries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        projectedObjectKind(entry.Kind),
			Mode:        entry.Mode,
			BlobID:      entry.BlobId,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return objectid.TreeID(idEntries)
}

func immediateProjectedDirectoryEntriesNoTree(prefix string, entries []storage.TreeEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, entry := range entries {
		rel, ok := relativeDirectoryPath(prefix, entry.Path)
		if !ok {
			continue
		}
		if rel == "" {
			if entry.Kind != "directory" {
				byPath[entry.Path] = treeEntryFromRepositoryEntry(entry)
			}
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromRepositoryEntry(entry)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{Path: childPath, Name: parts[0], Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func projectedObjectKind(kind corev1.EntryKind) string {
	switch kind {
	case corev1.EntryKind_ENTRY_KIND_FILE:
		return "file"
	case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
		return "directory"
	case corev1.EntryKind_ENTRY_KIND_SYMLINK:
		return "symlink"
	default:
		return "unspecified"
	}
}

func directoryTreeIDFromEntries(entries []storage.TreeEntry) string {
	idEntries := make([]objectid.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		idEntries = append(idEntries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind,
			Mode:        entry.Mode,
			TreeID:      entry.TreeID,
			BlobID:      entry.BlobID,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return objectid.TreeID(idEntries)
}

// directoryTreeID computes a projection-local tree ID for a synthesized
// directory. The ID is based only on entries visible through the current slice
// projection, not on hidden global-tree siblings.
func directoryTreeID(prefix string, files []storage.FileEntry) string {
	entries := immediateDirectoryEntriesWithoutIDs(prefix, files)
	return objectid.TreeID(entries)
}

// immediateDirectoryEntriesWithoutIDs mirrors immediateDirectoryEntries for tree
// ID calculation while avoiding recursive TreeId population. That prevents a
// synthesized directory's ID from depending on itself through nested helper
// calls.
func immediateDirectoryEntriesWithoutIDs(prefix string, files []storage.FileEntry) []objectid.TreeEntry {
	uiEntries := immediateDirectoryEntriesNoTree(prefix, files)
	entries := make([]objectid.TreeEntry, 0, len(uiEntries))
	for _, entry := range uiEntries {
		entries = append(entries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind.String(),
			Mode:        entry.Mode,
			BlobID:      entry.BlobId,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return entries
}

// immediateDirectoryEntriesNoTree is the non-recursive entry builder used only
// while calculating directory TreeIds. It must keep the same filtering and
// direct-child rules as immediateDirectoryEntries, but it intentionally leaves
// synthesized directory TreeId fields empty.
func immediateDirectoryEntriesNoTree(prefix string, files []storage.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel, ok := relativeDirectoryPath(prefix, file.Path)
		if !ok {
			continue
		}
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{Path: childPath, Name: parts[0], Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

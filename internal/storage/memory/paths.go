package memory

import (
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

func accountFromPath(p string) string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return ""
	}
	return strings.Split(trimmed, "/")[0]
}

func pathContains(prefix, p string) bool {
	if prefix == "/" {
		return strings.HasPrefix(p, "/")
	}
	prefix = strings.TrimRight(prefix, "/")
	p = strings.TrimRight(p, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func pathInAnyPrefix(prefixes []string, p string) bool {
	for _, prefix := range prefixes {
		if pathContains(prefix, p) {
			return true
		}
	}
	return false
}

func hasDescendant(files map[string]storage.FileEntry, prefix string) bool {
	for p := range files {
		if pathContains(prefix, p) {
			return true
		}
	}
	return false
}

func hasDescendantDir(dirs map[string]struct{}, prefix string) bool {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	if prefix == "/" {
		return len(dirs) > 0
	}
	for dir := range dirs {
		if strings.HasPrefix(dir, prefix+"/") {
			return true
		}
	}
	return false
}

func directoryEntries(prefix string, files map[string]storage.FileEntry, dirs map[string]struct{}) []storage.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]storage.TreeEntry{}
	for _, file := range files {
		rel, ok := relativeDirectoryPath(prefix, file.Path)
		if !ok {
			continue
		}
		if rel == "" {
			byPath[file.Path] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
			continue
		}
		parts := strings.Split(rel, "/")
		child := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			child = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[child] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
		} else if _, ok := byPath[child]; !ok {
			byPath[child] = storage.TreeEntry{Path: child, Name: parts[0], Kind: "directory", TreeID: "mem_tree_" + strings.Trim(child, "/")}
		}
	}
	for dir := range dirs {
		rel, ok := relativeDirectoryPath(prefix, dir)
		if !ok || rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		child := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			child = "/" + parts[0]
		}
		if _, ok := byPath[child]; !ok {
			byPath[child] = storage.TreeEntry{Path: child, Name: parts[0], Kind: "directory", TreeID: "mem_tree_" + strings.Trim(child, "/")}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]storage.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

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

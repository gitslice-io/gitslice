package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (b *backend) baseTreeForPatchsetLocked(patchset *corev1.Patchset) (string, error) {
	if patchset.BaseTreeId != "" {
		return patchset.BaseTreeId, nil
	}
	if patchset.BaseKind == "patchset" {
		basePatchset := b.patchsetByIDLocked(patchset.BasePatchsetId)
		if basePatchset == nil || basePatchset.ResultTreeId == "" {
			return "", storage.ErrConflict
		}
		return basePatchset.ResultTreeId, nil
	}
	commit := b.commits[patchset.BaseCommitId]
	if commit == nil {
		return "", storage.ErrNotFound
	}
	return commit.RootTreeId, nil
}

func (b *backend) patchsetByIDLocked(patchsetID string) *corev1.Patchset {
	for _, cs := range b.changesets {
		for _, patchset := range cs.Patchsets {
			if patchset.Id == patchsetID {
				return patchset
			}
		}
	}
	return nil
}

func (b *backend) previewTreeForPatchsetLocked(baseTreeID string, edits []*corev1.FileEdit) (string, error) {
	files, dirs, ok := b.filesAndDirsForRootTreeLocked(baseTreeID)
	if !ok {
		return "", storage.ErrNotFound
	}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "delete":
			deleteMemoryPath(files, dirs, edit.Path)
		case "rename":
			renameMemoryPath(files, dirs, edit.OldPath, edit.Path)
		case "copy":
			copyMemoryPath(files, dirs, edit.OldPath, edit.Path)
		case "mkdir":
			ensureMemoryDir(dirs, edit.Path)
		default:
			blob := b.blobs[edit.BlobId]
			contentHash := edit.ContentHash
			size := int64(0)
			if blob != nil {
				contentHash = blob.ContentHash
				size = blob.Size
			}
			if contentHash == "" {
				return "", storage.ErrNotFound
			}
			ensureMemoryParentDirs(dirs, edit.Path)
			files[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
		}
	}
	if len(edits) == 0 {
		return baseTreeID, nil
	}
	treeID := memoryPreviewTreeID(files, dirs)
	b.previewFiles[treeID] = files
	b.previewDirs[treeID] = dirs
	return treeID, nil
}

func memoryPreviewTreeID(files map[string]storage.FileEntry, dirs map[string]struct{}) string {
	type snapshot struct {
		Files []storage.FileEntry `json:"files"`
		Dirs  []string            `json:"dirs"`
	}
	out := snapshot{
		Files: make([]storage.FileEntry, 0, len(files)),
		Dirs:  make([]string, 0, len(dirs)),
	}
	for _, file := range files {
		out.Files = append(out.Files, file)
	}
	sort.Slice(out.Files, func(i, j int) bool {
		return out.Files[i].Path < out.Files[j].Path
	})
	for dir := range dirs {
		out.Dirs = append(out.Dirs, dir)
	}
	sort.Strings(out.Dirs)
	payload, _ := json.Marshal(out)
	sum := sha256.Sum256(payload)
	return "mem_tree_preview_" + hex.EncodeToString(sum[:])
}

func (b *backend) filesAndDirsForRootTreeLocked(rootTreeID string) (map[string]storage.FileEntry, map[string]struct{}, bool) {
	if files, ok := b.previewFiles[rootTreeID]; ok {
		return cloneFileMap(files), cloneDirSet(b.previewDirs[rootTreeID]), true
	}
	for commitID, commit := range b.commits {
		if commit != nil && commit.RootTreeId == rootTreeID {
			return cloneFileMap(b.commitFiles[commitID]), cloneDirSet(b.commitDirs[commitID]), true
		}
	}
	return nil, nil, false
}

func deleteMemoryPath(files map[string]storage.FileEntry, dirs map[string]struct{}, p string) {
	delete(files, p)
	delete(dirs, p)
	for filePath := range files {
		if pathContains(p, filePath) {
			delete(files, filePath)
		}
	}
	for dirPath := range dirs {
		if pathContains(p, dirPath) {
			delete(dirs, dirPath)
		}
	}
}

func renameMemoryPath(files map[string]storage.FileEntry, dirs map[string]struct{}, oldPath, newPath string) {
	if file, ok := files[oldPath]; ok {
		delete(files, oldPath)
		file.Path = newPath
		files[newPath] = file
		ensureMemoryParentDirs(dirs, newPath)
	}
	if _, ok := dirs[oldPath]; ok {
		delete(dirs, oldPath)
		dirs[newPath] = struct{}{}
	}
	for filePath, file := range cloneFileMap(files) {
		if !pathContains(oldPath, filePath) || filePath == oldPath {
			continue
		}
		delete(files, filePath)
		renamed := strings.TrimRight(newPath, "/") + strings.TrimPrefix(filePath, strings.TrimRight(oldPath, "/"))
		file.Path = renamed
		files[renamed] = file
	}
	for dirPath := range cloneDirSet(dirs) {
		if !pathContains(oldPath, dirPath) || dirPath == oldPath {
			continue
		}
		delete(dirs, dirPath)
		renamed := strings.TrimRight(newPath, "/") + strings.TrimPrefix(dirPath, strings.TrimRight(oldPath, "/"))
		dirs[renamed] = struct{}{}
	}
	ensureMemoryParentDirs(dirs, newPath)
}

// copyMemoryPath copies the file or folder at oldPath, with everything under
// it, to newPath.
func copyMemoryPath(files map[string]storage.FileEntry, dirs map[string]struct{}, oldPath, newPath string) {
	moved := func(p string) string {
		return strings.TrimRight(newPath, "/") + strings.TrimPrefix(p, strings.TrimRight(oldPath, "/"))
	}
	for filePath, file := range cloneFileMap(files) {
		if !pathContains(oldPath, filePath) {
			continue
		}
		file.Path = moved(filePath)
		files[file.Path] = file
	}
	for dirPath := range cloneDirSet(dirs) {
		if pathContains(oldPath, dirPath) {
			dirs[moved(dirPath)] = struct{}{}
		}
	}
	ensureMemoryParentDirs(dirs, newPath)
}

func ensureMemoryDir(dirs map[string]struct{}, p string) {
	if p == "" || p == "/" || p == "." {
		return
	}
	dirs[p] = struct{}{}
	ensureMemoryParentDirs(dirs, p)
}

func ensureMemoryParentDirs(dirs map[string]struct{}, p string) {
	for parent := path.Dir(p); parent != "" && parent != "/" && parent != "."; parent = path.Dir(parent) {
		dirs[parent] = struct{}{}
	}
}

func treeEntryFromMemorySnapshot(files map[string]storage.FileEntry, dirs map[string]struct{}, p string) (*storage.TreeEntry, error) {
	if file, ok := files[p]; ok {
		return &storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}, nil
	}
	if p == "/" || hasDescendant(files, p) || hasDescendantDir(dirs, p) {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	if _, ok := dirs[p]; ok {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	return nil, storage.ErrNotFound
}

package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IsSymlinkMode reports whether a native file mode is a symbolic link.
func IsSymlinkMode(mode uint32) bool {
	return mode&0o170000 == 0o120000
}

// HasReservedGsSegment reports whether any path component names the workspace
// metadata directory (.gs).
func HasReservedGsSegment(p string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".gs" {
			return true
		}
	}
	return false
}

// EnsureNotReservedGsPath returns ErrReservedPath when p references .gs.
func EnsureNotReservedGsPath(p string) error {
	if HasReservedGsSegment(p) {
		return &ReservedPathError{Path: p}
	}
	return nil
}

// CleanConfinedRelPath validates and cleans a workspace-relative path,
// rejecting traversal segments and .gs segments.
func CleanConfinedRelPath(rel string) (string, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	normalized = strings.TrimPrefix(normalized, "/")
	if normalized == "" || normalized == "." {
		return "", fmt.Errorf("empty workspace relative path")
	}
	for _, seg := range strings.Split(normalized, "/") {
		switch seg {
		case "", ".", "..":
			return "", fmt.Errorf("invalid workspace relative path %q", rel)
		case ".gs":
			return "", EnsureNotReservedGsPath(rel)
		}
	}
	return filepath.FromSlash(normalized), nil
}

// IsSafeRelativeSymlink reports whether a symlink at absTarget with target link
// is a relative link that stays strictly within absRoot and outside .gs.
func IsSafeRelativeSymlink(absRoot, absTarget, link string) bool {
	if link == "" || strings.ContainsRune(link, 0) {
		return false
	}
	if filepath.IsAbs(link) || strings.HasPrefix(link, "/") || strings.HasPrefix(link, "\\") || filepath.VolumeName(link) != "" {
		return false
	}
	cleanRoot, err := filepath.Abs(absRoot)
	if err != nil {
		return false
	}
	cleanTarget, err := filepath.Abs(absTarget)
	if err != nil {
		return false
	}
	targetRel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || targetRel == "." || targetRel == ".." || strings.HasPrefix(targetRel, ".."+string(filepath.Separator)) || filepath.IsAbs(targetRel) {
		return false
	}
	if HasReservedGsSegment(filepath.ToSlash(targetRel)) {
		return false
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(cleanTarget), filepath.FromSlash(link)))
	relToRoot, err := filepath.Rel(cleanRoot, resolved)
	if err != nil || relToRoot == "." || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return false
	}
	if HasReservedGsSegment(filepath.ToSlash(relToRoot)) {
		return false
	}

	// Also walk existing components along link from Dir(cleanTarget) so chained
	// symlinks already on disk cannot redirect outside cleanRoot or into .gs.
	cur := filepath.Dir(cleanTarget)
	for _, part := range strings.Split(strings.ReplaceAll(link, "\\", "/"), "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
		default:
			cur = filepath.Join(cur, part)
		}
		relCur, err := filepath.Rel(cleanRoot, cur)
		if err != nil || relCur == ".." || strings.HasPrefix(relCur, ".."+string(filepath.Separator)) || filepath.IsAbs(relCur) {
			return false
		}
		if relCur != "." && HasReservedGsSegment(filepath.ToSlash(relCur)) {
			return false
		}
		info, lerr := os.Lstat(cur)
		if lerr != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			realCur, evalErr := filepath.EvalSymlinks(cur)
			if evalErr != nil {
				return false
			}
			realRoot, rootErr := filepath.EvalSymlinks(cleanRoot)
			if rootErr != nil {
				realRoot = cleanRoot
			}
			relReal, relErr := filepath.Rel(realRoot, realCur)
			if relErr != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) || filepath.IsAbs(relReal) {
				return false
			}
			if relReal != "." && HasReservedGsSegment(filepath.ToSlash(relReal)) {
				return false
			}
			cur = realCur
		}
	}
	return true
}

// EnsureConfinedDir creates relDir (or "." for root itself) under root while
// rejecting .gs segments and any symlink along the ancestor chain.
func EnsureConfinedDir(root, relDir string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return "", err
	}
	normalized := strings.TrimSpace(strings.ReplaceAll(relDir, "\\", "/"))
	normalized = strings.Trim(normalized, "/")
	if normalized == "" || normalized == "." {
		return absRoot, nil
	}
	osRel, err := CleanConfinedRelPath(normalized)
	if err != nil {
		return "", err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return "", err
	}
	defer osRoot.Close()
	if err := MkdirAllNoSymlinksInRoot(osRoot, osRel); err != nil {
		return "", err
	}
	return filepath.Join(absRoot, osRel), nil
}

func MkdirAllNoSymlinksInRoot(osRoot *os.Root, osRel string) error {
	if osRel == "" || osRel == "." {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(osRel), "/")
	var cur string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || part == ".gs" {
			return fmt.Errorf("refusing confined directory component %q", part)
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		info, err := osRoot.Lstat(cur)
		if err == nil {
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("refusing to traverse symlink directory %q", cur)
			}
			if !info.IsDir() {
				return fmt.Errorf("ancestor path %q is not a directory", cur)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if mkErr := osRoot.Mkdir(cur, 0o755); mkErr != nil {
			if !errors.Is(mkErr, os.ErrExist) {
				return mkErr
			}
			info, lerr := osRoot.Lstat(cur)
			if lerr != nil {
				return lerr
			}
			if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("refusing to traverse non-directory ancestor %q", cur)
			}
		}
	}
	return nil
}

func EnsureNoAncestorSymlinksInRoot(osRoot *os.Root, parentRel string) error {
	if parentRel == "" || parentRel == "." {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(parentRel), "/")
	var cur string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || part == ".gs" {
			return fmt.Errorf("refusing confined directory component %q", part)
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		info, err := osRoot.Lstat(cur)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to traverse symlink directory %q", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("ancestor path %q is not a directory", cur)
		}
	}
	return nil
}

// WriteConfinedFile writes a workspace file or validated relative
// symlink strictly inside root (never inside .gs, never traversing ancestor
// symlinks, and never overwriting through a leaf symlink).
func WriteConfinedFile(root, rel string, mode uint32, r io.Reader) (string, int64, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", 0, err
	}
	osRel, err := CleanConfinedRelPath(rel)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return "", 0, err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return "", 0, err
	}
	defer osRoot.Close()

	parentRel := filepath.Dir(osRel)
	if parentRel != "." {
		if err := MkdirAllNoSymlinksInRoot(osRoot, parentRel); err != nil {
			return "", 0, err
		}
	}
	absTarget := filepath.Join(absRoot, osRel)

	if info, err := osRoot.Lstat(osRel); err == nil {
		if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			return "", 0, fmt.Errorf("cannot overwrite directory %q with file", rel)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if err := osRoot.Remove(osRel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", 0, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}

	if IsSymlinkMode(mode) {
		data, err := io.ReadAll(r)
		if err != nil {
			return "", 0, err
		}
		link := string(data)
		if IsSafeRelativeSymlink(absRoot, absTarget, link) {
			if parentRel != "." {
				if err := EnsureNoAncestorSymlinksInRoot(osRoot, parentRel); err != nil {
					return "", 0, err
				}
			}
			if err := osRoot.Remove(osRel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", 0, err
			}
			if err := os.Symlink(link, absTarget); err == nil {
				return absTarget, int64(len(data)), nil
			}
		}
		r = bytes.NewReader(data)
		mode = 0o100644
	}

	fileMode := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		fileMode = 0o755
	}
	f, err := osRoot.OpenFile(osRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return "", 0, err
	}
	n, copyErr := io.Copy(f, r)
	chmodErr := f.Chmod(fileMode)
	closeErr := f.Close()
	if copyErr != nil {
		return absTarget, n, copyErr
	}
	if chmodErr != nil {
		return absTarget, n, chmodErr
	}
	if closeErr != nil {
		return absTarget, n, closeErr
	}
	return absTarget, n, nil
}

// RemoveConfinedFile removes a file or leaf symlink strictly inside
// root without traversing intermediate symlink directories.
func RemoveConfinedFile(root, rel string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	osRel, err := CleanConfinedRelPath(rel)
	if err != nil {
		return err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer osRoot.Close()

	parentRel := filepath.Dir(osRel)
	if parentRel != "." {
		if err := EnsureNoAncestorSymlinksInRoot(osRoot, parentRel); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
	}
	if err := osRoot.Remove(osRel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadConfinedFile reads a regular file strictly inside root, refusing
// .gs paths, ancestor symlinks, and unsafe leaf symlinks.
func ReadConfinedFile(root, rel string) ([]byte, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	osRel, err := CleanConfinedRelPath(rel)
	if err != nil {
		return nil, err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, err
	}
	defer osRoot.Close()

	parentRel := filepath.Dir(osRel)
	if parentRel != "." {
		if err := EnsureNoAncestorSymlinksInRoot(osRoot, parentRel); err != nil {
			return nil, err
		}
	}
	info, err := osRoot.Lstat(osRel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		absTarget := filepath.Join(absRoot, osRel)
		link, lerr := os.Readlink(absTarget)
		if lerr != nil || !IsSafeRelativeSymlink(absRoot, absTarget, link) {
			return nil, fmt.Errorf("refusing to read unsafe symlink %q", rel)
		}
	}
	f, err := osRoot.Open(osRel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// InferConfiningRootForTarget finds the nearest enclosing workspace root for
// target (or its parent directory if none exists).
func InferConfiningRootForTarget(target string) (string, string, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	cur := filepath.Dir(absTarget)
	for {
		if info, err := os.Lstat(filepath.Join(cur, ".gs")); err == nil && info.IsDir() {
			rel, relErr := filepath.Rel(cur, absTarget)
			if relErr != nil {
				return "", "", relErr
			}
			return cur, rel, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return filepath.Dir(absTarget), filepath.Base(absTarget), nil
}

// BackupUncommittedEdits copies the workspace files at logical paths (as
// /account/...) into .gs/sync-backup/<time>/ before gs sync merges into them,
// and returns that folder relative to the workspace root. A path deleted
// locally has nothing to copy.
func BackupUncommittedEdits(root string, logicalPaths []string) (string, error) {
	rel := filepath.Join(".gs", "sync-backup", time.Now().UTC().Format("20060102T150405Z"))
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, p := range logicalPaths {
		name := strings.TrimPrefix(p, "/")
		data, err := ReadConfinedFile(root, name)
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

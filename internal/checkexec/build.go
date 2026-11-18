package checkexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

const (
	builtImageRepoPrefix = "gitslice-ci/build-"
	builtImageFamilyKey  = "io.gitslice.ci.family"
	// keptBuiltImages is how many images of one Dockerfile a daemon keeps.
	// The newest serves current changesets; the one before it serves
	// changesets that have not picked up a dependency change yet.
	keptBuiltImages = 2
)

// builtImage is the image a check's build resolves to.
type builtImage struct {
	Tag        string
	Family     string
	files      []string // context-relative files the build reads, sorted
	dockerfile string   // context-relative
	root       string   // canonical build context on disk
}

// resolveBuiltImage hashes the Dockerfile and inputs of a build in the
// materialized tree while confining every read and symlink inside the context
// directory via os.OpenRoot.
func resolveBuiltImage(workspaceRoot, scope string, b *checks.ImageBuild) (builtImage, error) {
	if b == nil {
		return builtImage{}, fmt.Errorf("image build spec is nil")
	}
	contextRoot, err := workspaceDir(workspaceRoot, b.ContextDir)
	if err != nil {
		return builtImage{}, err
	}
	dockerfileRel, err := cleanContextRel(b.Dockerfile, false)
	if err != nil {
		return builtImage{}, err
	}

	srcRoot, err := os.OpenRoot(contextRoot)
	if err != nil {
		return builtImage{}, fmt.Errorf("open build context: %w", err)
	}
	defer srcRoot.Close()

	// Verify the Dockerfile itself exists inside contextRoot and resolves to a regular file.
	if err := ensureRealDirParents(srcRoot, dockerfileRel); err != nil {
		return builtImage{}, err
	}
	dfStat, err := srcRoot.Stat(filepath.FromSlash(dockerfileRel))
	if errors.Is(err, fs.ErrNotExist) {
		return builtImage{}, fmt.Errorf("build input %s is not in the tree", path.Join(logicalPathRel(b.ContextDir), dockerfileRel))
	}
	if err != nil {
		return builtImage{}, fmt.Errorf("dockerfile %s escapes build context: %w", dockerfileRel, err)
	}
	if !dfStat.Mode().IsRegular() {
		return builtImage{}, fmt.Errorf("dockerfile %s must be a regular file", dockerfileRel)
	}

	seen := map[string]struct{}{}
	var files []string
	addFile := func(rel string) {
		if _, ok := seen[rel]; !ok {
			seen[rel] = struct{}{}
			files = append(files, rel)
		}
	}

	inputs := make([]string, 0, 1+len(b.Inputs))
	inputs = append(inputs, dockerfileRel)
	for _, rawInput := range b.Inputs {
		cleaned, err := cleanContextRel(rawInput, true)
		if err != nil {
			return builtImage{}, err
		}
		inputs = append(inputs, cleaned)
	}

	for _, rel := range inputs {
		if rel != "." {
			if err := ensureRealDirParents(srcRoot, rel); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return builtImage{}, fmt.Errorf("build input %s is not in the tree", path.Join(logicalPathRel(b.ContextDir), rel))
				}
				return builtImage{}, err
			}
		}
		info, err := srcRoot.Lstat(filepath.FromSlash(rel))
		if errors.Is(err, fs.ErrNotExist) {
			return builtImage{}, fmt.Errorf("build input %s is not in the tree", path.Join(logicalPathRel(b.ContextDir), rel))
		}
		if err != nil {
			return builtImage{}, err
		}

		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if _, err := validateConfinedSymlink(srcRoot, contextRoot, rel); err != nil {
				return builtImage{}, err
			}
			addFile(rel)
		case info.IsDir():
			err = fs.WalkDir(srcRoot.FS(), rel, func(walkPath string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if d.IsDir() {
					return nil
				}
				if d.Type()&fs.ModeSymlink != 0 {
					if _, err := validateConfinedSymlink(srcRoot, contextRoot, walkPath); err != nil {
						return err
					}
					addFile(walkPath)
					return nil
				}
				if !d.Type().IsRegular() {
					return fmt.Errorf("build input %s has unsupported file mode", walkPath)
				}
				addFile(walkPath)
				return nil
			})
			if err != nil {
				return builtImage{}, err
			}
		case info.Mode().IsRegular():
			addFile(rel)
		default:
			return builtImage{}, fmt.Errorf("build input %s has unsupported file mode", rel)
		}
	}
	sort.Strings(files)

	h := sha256.New()
	fmt.Fprintf(h, "gitslice-ci build v1\ndockerfile %s\n", dockerfileRel)
	for _, rel := range files {
		sum, mode, err := hashContextEntry(srcRoot, contextRoot, rel)
		if err != nil {
			return builtImage{}, err
		}
		fmt.Fprintf(h, "%s %s %s\n", mode, sum, rel)
	}
	key := hex.EncodeToString(h.Sum(nil))[:24]
	family := builtImageFamily(scope, b.ContextDir, dockerfileRel)
	return builtImage{
		Tag:        builtImageRepoPrefix + family + ":" + key,
		Family:     family,
		files:      files,
		dockerfile: dockerfileRel,
		root:       contextRoot,
	}, nil
}

func cleanContextRel(raw string, allowDot bool) (string, error) {
	if strings.ContainsAny(raw, "\r\n\x00") {
		return "", fmt.Errorf("build path %q contains control characters", raw)
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if normalized == "" {
		return "", fmt.Errorf("build path is empty")
	}
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("build path %q must be relative to build context", raw)
	}
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return "", fmt.Errorf("build path %q escapes build context", raw)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("build path %q escapes build context", raw)
	}
	if cleaned == "." && !allowDot {
		return "", fmt.Errorf("build path %q must name a file", raw)
	}
	return cleaned, nil
}

// ensureRealDirParents guarantees no ancestor directory of rel inside root is a
// symlink, preventing both symlink-traversal reads and symlink-directory overwrites.
func ensureRealDirParents(root *os.Root, rel string) error {
	dir := path.Dir(rel)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	var cur string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		info, err := root.Lstat(filepath.FromSlash(cur))
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("build input %s traverses symlink directory %s", rel, cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("build input %s parent %s is not a directory", rel, cur)
		}
	}
	return nil
}

// validateConfinedSymlink ensures a symlink at rel (whose parent directories
// inside canonicalRoot are already verified real directories) uses a relative
// target that remains strictly within root.
func validateConfinedSymlink(root *os.Root, canonicalRoot, rel string) (string, error) {
	if err := ensureRealDirParents(root, rel); err != nil {
		return "", err
	}
	linkTarget, err := os.Readlink(filepath.Join(canonicalRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if linkTarget == "" || strings.ContainsAny(linkTarget, "\r\n\x00") {
		return "", fmt.Errorf("symlink %s has invalid target", rel)
	}
	slashTarget := strings.ReplaceAll(linkTarget, "\\", "/")
	if filepath.IsAbs(linkTarget) || strings.HasPrefix(slashTarget, "/") {
		return "", fmt.Errorf("symlink %s has absolute target %q outside build context", rel, linkTarget)
	}
	resolved := path.Clean(path.Join(path.Dir(rel), slashTarget))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("symlink %s target %q escapes build context", rel, linkTarget)
	}
	// Ask os.Root.Stat to resolve any multi-hop symlink chain within root.
	if _, statErr := root.Stat(filepath.FromSlash(rel)); statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("symlink %s escapes build context: %w", rel, statErr)
	}
	return linkTarget, nil
}

// builtImageFamily groups the images built from one Dockerfile of one
// slice; a daemon keeps only the newest few of each.
func builtImageFamily(scope, contextDir, dockerfile string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + logicalPathRel(contextDir) + "\x00" + path.Clean(dockerfile)))
	return hex.EncodeToString(sum[:])[:16]
}

func hashContextEntry(srcRoot *os.Root, canonicalRoot, rel string) (string, string, error) {
	osRel := filepath.FromSlash(rel)
	info, err := srcRoot.Lstat(osRel)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	mode := "file"
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := validateConfinedSymlink(srcRoot, canonicalRoot, rel)
		if err != nil {
			return "", "", err
		}
		mode = "link"
		_, _ = io.WriteString(h, target)
	case info.Mode().IsRegular():
		if info.Mode()&0o111 != 0 {
			mode = "exec"
		}
		f, err := srcRoot.Open(osRel)
		if err != nil {
			return "", "", err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("build input %s has unsupported file mode", rel)
	}
	return hex.EncodeToString(h.Sum(nil)), mode, nil
}

func copyContext(srcDir, dstDir string, files []string) error {
	realSrc, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return fmt.Errorf("resolve build source root: %w", err)
	}
	realDst, err := filepath.EvalSymlinks(dstDir)
	if err != nil {
		return fmt.Errorf("resolve build destination root: %w", err)
	}
	srcRoot, err := os.OpenRoot(realSrc)
	if err != nil {
		return fmt.Errorf("open build source root: %w", err)
	}
	defer srcRoot.Close()
	dstRoot, err := os.OpenRoot(realDst)
	if err != nil {
		return fmt.Errorf("open build destination root: %w", err)
	}
	defer dstRoot.Close()

	for _, rel := range files {
		if err := copyContextEntry(srcRoot, realSrc, dstRoot, realDst, rel); err != nil {
			return fmt.Errorf("copy build input %s: %w", rel, err)
		}
	}
	return nil
}

func copyContextFile(srcDir, dstDir, rel string) error {
	return copyContext(srcDir, dstDir, []string{rel})
}

func copyContextEntry(srcRoot *os.Root, realSrc string, dstRoot *os.Root, realDst, rel string) error {
	cleaned, err := cleanContextRel(rel, false)
	if err != nil {
		return err
	}
	if err := ensureRealDirParents(srcRoot, cleaned); err != nil {
		return err
	}
	if err := mkdirAllRealInRoot(dstRoot, path.Dir(cleaned)); err != nil {
		return err
	}

	osRel := filepath.FromSlash(cleaned)
	info, err := srcRoot.Lstat(osRel)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := validateConfinedSymlink(srcRoot, realSrc, cleaned)
		if err != nil {
			return err
		}
		if _, err := dstRoot.Lstat(osRel); err == nil {
			return fmt.Errorf("destination %s already exists", cleaned)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return os.Symlink(target, filepath.Join(realDst, osRel))
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported file mode for %s", cleaned)
	}

	in, err := srcRoot.Open(osRel)
	if err != nil {
		return err
	}
	defer in.Close()
	inInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !inInfo.Mode().IsRegular() {
		return fmt.Errorf("source %s is not a regular file", cleaned)
	}

	out, err := dstRoot.OpenFile(osRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func mkdirAllRealInRoot(root *os.Root, relDir string) error {
	if relDir == "." || relDir == "/" || relDir == "" {
		return nil
	}
	parts := strings.Split(relDir, "/")
	var cur string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		osRel := filepath.FromSlash(cur)
		info, err := root.Lstat(osRel)
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := root.Mkdir(osRel, 0o755); mkErr != nil {
				return mkErr
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("build destination path %s is not a real directory", cur)
		}
	}
	return nil
}

// pruneBuiltImages removes all but the newest images built from one
// Dockerfile, so a daemon's disk does not fill with one image per
// dependency change. An image a running check still uses stays.
func pruneBuiltImages(rt RuntimeInfo, family string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lsCmd := exec.CommandContext(ctx, rt.Path, "image", "ls",
		"--filter", "label="+builtImageFamilyKey+"="+family,
		"--format", "{{.CreatedAt}}\t{{.Repository}}:{{.Tag}}")
	lsCmd.Env = containerClientEnv()
	out, err := lsCmd.Output()
	if err != nil {
		return
	}
	pruneImageListing(ctx, rt.Path, string(out), keptBuiltImages)
}

func pruneImageListing(ctx context.Context, runtimePath, raw string, keep int) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return
	}
	lines := strings.Split(trimmed, "\n")
	sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	for i, line := range lines {
		if i < keep {
			continue
		}
		_, ref, ok := strings.Cut(line, "\t")
		ref = strings.TrimSpace(ref)
		if !ok || ref == "" || strings.HasSuffix(ref, ":<none>") || strings.HasPrefix(ref, "<none>:") {
			continue
		}
		if err := checks.ValidateImageRef(ref); err != nil {
			continue
		}
		rmCmd := exec.CommandContext(ctx, runtimePath, "image", "rm", "--", ref)
		rmCmd.Env = containerClientEnv()
		_ = rmCmd.Run()
	}
}

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
	"sync"
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
	root       string   // the build context on disk
}

// resolveBuiltImage hashes the Dockerfile and inputs of a build in the
// materialized tree. Its tag changes exactly when one of them does, so an
// image that already exists under it can be used as it is.
func resolveBuiltImage(workspaceRoot, scope string, b *checks.ImageBuild) (builtImage, error) {
	contextRoot, err := workspaceDir(workspaceRoot, b.ContextDir)
	if err != nil {
		return builtImage{}, err
	}
	seen := map[string]struct{}{}
	var files []string
	for _, rel := range append([]string{b.Dockerfile}, b.Inputs...) {
		rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
		target := filepath.Join(contextRoot, filepath.FromSlash(rel))
		if err := ensureInside(contextRoot, target); err != nil {
			return builtImage{}, err
		}
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return builtImage{}, fmt.Errorf("build input %s is not in the tree", path.Join(logicalPathRel(b.ContextDir), rel))
		}
		if err != nil {
			return builtImage{}, err
		}
		if !info.IsDir() {
			if _, ok := seen[rel]; !ok {
				seen[rel] = struct{}{}
				files = append(files, rel)
			}
			continue
		}
		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			relPath, err := filepath.Rel(contextRoot, p)
			if err != nil {
				return err
			}
			relPath = filepath.ToSlash(relPath)
			if _, ok := seen[relPath]; !ok {
				seen[relPath] = struct{}{}
				files = append(files, relPath)
			}
			return nil
		})
		if err != nil {
			return builtImage{}, err
		}
	}
	sort.Strings(files)

	h := sha256.New()
	fmt.Fprintf(h, "gitslice-ci build v1\ndockerfile %s\n", path.Clean(b.Dockerfile))
	for _, rel := range files {
		sum, mode, err := hashFile(filepath.Join(contextRoot, filepath.FromSlash(rel)))
		if err != nil {
			return builtImage{}, err
		}
		fmt.Fprintf(h, "%s %s %s\n", mode, sum, rel)
	}
	key := hex.EncodeToString(h.Sum(nil))[:24]
	family := builtImageFamily(scope, b.ContextDir, b.Dockerfile)
	return builtImage{
		Tag:        builtImageRepoPrefix + family + ":" + key,
		Family:     family,
		files:      files,
		dockerfile: path.Clean(strings.ReplaceAll(b.Dockerfile, "\\", "/")),
		root:       contextRoot,
	}, nil
}

// builtImageFamily groups the images built from one Dockerfile of one
// slice; a daemon keeps only the newest few of each.
func builtImageFamily(scope, contextDir, dockerfile string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + logicalPathRel(contextDir) + "\x00" + path.Clean(dockerfile)))
	return hex.EncodeToString(sum[:])[:16]
}

func hashFile(p string) (string, string, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	mode := "file"
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return "", "", err
		}
		mode = "link"
		io.WriteString(h, target)
	default:
		if info.Mode()&0o111 != 0 {
			mode = "exec"
		}
		f, err := os.Open(p)
		if err != nil {
			return "", "", err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), mode, nil
}

// buildLocks makes checks that need the same image wait for one build.
var buildLocks sync.Map // tag -> chan struct{}

func lockBuild(ctx context.Context, tag string) (func(), error) {
	value, _ := buildLocks.LoadOrStore(tag, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ensureBuiltImage returns the image for a build, building it unless an
// image with its tag exists. note says which happened, for the check's log.
func ensureBuiltImage(ctx context.Context, rt RuntimeInfo, img builtImage, timeout time.Duration) (setupMs int64, cached bool, note string, result *Result, err error) {
	unlock, err := lockBuild(ctx, img.Tag)
	if err != nil {
		return 0, false, "", nil, err
	}
	defer unlock()
	if exec.CommandContext(ctx, rt.Path, "image", "inspect", img.Tag).Run() == nil {
		return 0, true, "using image " + img.Tag + ", built earlier from the same Dockerfile and inputs", nil, nil
	}

	contextDir, err := os.MkdirTemp("", "gitslice-ci-build-*")
	if err != nil {
		return 0, false, "", nil, fmt.Errorf("create build context: %w", err)
	}
	defer os.RemoveAll(contextDir)
	for _, rel := range img.files {
		if err := copyContextFile(filepath.Join(img.root, filepath.FromSlash(rel)), filepath.Join(contextDir, filepath.FromSlash(rel))); err != nil {
			return 0, false, "", nil, fmt.Errorf("copy build input %s: %w", rel, err)
		}
	}

	if timeout <= 0 {
		timeout = checks.DefaultBuildTimeout
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dockerfile := filepath.Join(contextDir, filepath.FromSlash(img.dockerfile))
	cmd := exec.CommandContext(buildCtx, rt.Path, "build",
		"--label", builtImageFamilyKey+"="+img.Family,
		"-t", img.Tag,
		"-f", dockerfile,
		contextDir)
	configureProcessGroup(cmd)
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	start := time.Now()
	err = cmd.Run()
	setupMs = time.Since(start).Milliseconds()
	if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmd)
		return setupMs, false, "", &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("image build timed out after %s", timeout),
			Log:      output.String(),
			SetupMs:  setupMs,
		}, nil
	}
	if err != nil {
		summary := fmt.Sprintf("image build failed: %v", err)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			summary = fmt.Sprintf("image build failed: exit %d", exitErr.ExitCode())
		}
		return setupMs, false, "", &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  summary,
			Log:      output.String(),
			SetupMs:  setupMs,
		}, nil
	}
	pruneBuiltImages(rt, img.Family)
	return setupMs, false, fmt.Sprintf("built image %s in %s; later runs reuse it until the Dockerfile or its inputs change", img.Tag, time.Duration(setupMs)*time.Millisecond), nil, nil
}

func copyContextFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneBuiltImages removes all but the newest images built from one
// Dockerfile, so a daemon's disk does not fill with one image per
// dependency change. An image a running check still uses stays.
func pruneBuiltImages(rt RuntimeInfo, family string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, rt.Path, "image", "ls",
		"--filter", "label="+builtImageFamilyKey+"="+family,
		"--format", "{{.CreatedAt}}\t{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	for i, line := range lines {
		if i < keptBuiltImages {
			continue
		}
		_, ref, ok := strings.Cut(line, "\t")
		if !ok || strings.HasSuffix(ref, ":<none>") {
			continue
		}
		_ = exec.CommandContext(ctx, rt.Path, "image", "rm", ref).Run()
	}
}

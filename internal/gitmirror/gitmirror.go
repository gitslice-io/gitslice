// Package gitmirror keeps a Git mirror (such as a GitHub repository) in step
// with a slice's projected history on Gitslice (design/35_git_mirrors.md,
// and design/21_self_hosting.md, Phase 2).
//
// Every projected commit carries a "Gitslice-Commit: <native id>" trailer, and
// projected commits that came from a Git import also carry "Git-Commit: <sha>"
// naming the original commit. The exporter finds the newest mirror commit that
// maps to a projected commit (by either trailer), checks that the mirror has
// not drifted from Gitslice, and then replays every later projected commit:
// the slice subdirectory's tree, with the projected commit's author, committer
// and message. An empty mirror receives the whole history. New tags are pushed
// the same way; a tag the mirror has is never moved.
package gitmirror

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// The refs Run reads in Config.Dir. The caller points them at the source.
const (
	SourceBranchRef = "refs/mirror/main"
	SourceTagsRef   = "refs/mirror/tags/"
	targetRef       = "refs/mirror/target"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// maxMirrorWalk bounds how far back the exporter looks for the sync point.
const maxMirrorWalk = 2000

// tagPushBatchSize bounds how many tag refspecs a single git push sends.
const tagPushBatchSize = 128

// ErrDiverged is wrapped by errors about a mirror that has commits or
// contents that did not come from Gitslice.
var ErrDiverged = errors.New("the mirror has diverged from Gitslice")

// Config describes one export.
type Config struct {
	// Dir is a Git repository holding the source at SourceBranchRef and
	// SourceTagsRef*. Run writes the mirror's commits and refs/mirror/target.
	Dir string
	// Remote is the mirror: a remote name or URL.
	Remote string
	// Branch is the mirror branch, such as main.
	Branch string
	// Subdir is the slice path inside the projected repository whose tree
	// becomes the mirror's root, or "" for the whole tree.
	Subdir string
	// Push sends new commits and tags; without it Run only computes them.
	Push bool
	// RemoteEnv is added to the environment of commands that talk to the
	// remote (credentials as GIT_CONFIG_* entries, for example).
	RemoteEnv []string
	// Hydrate, when set, is given the ids of objects the push needs that Dir
	// cannot read (files a lazily built projection has not written yet), and
	// must make them readable before Run pushes.
	Hydrate func(ctx context.Context, ids []string) error
	// Redact, when set, cleans error text before Run returns it.
	Redact func(string) string
}

// Result reports an export.
type Result struct {
	// Exported lists the new mirror commits, oldest first.
	Exported []string
	NewTags  []string
	// Head is the mirror branch's head after the export ("" when the source
	// has nothing to export yet).
	Head string
	// SourceHead is the projected commit Head was built from.
	SourceHead string
}

type commitMeta struct {
	SHA    string
	Native string
	Git    string
}

func matchesMirrorEntry(proj, mir commitMeta) bool {
	if mir.Native != "" && proj.Native == mir.Native {
		return true
	}
	return proj.Git != "" && proj.Git == mir.SHA
}

// Run exports Dir's source refs to the mirror.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	g := runner{dir: cfg.Dir, redact: cfg.Redact}
	remote := runner{dir: cfg.Dir, env: cfg.RemoteEnv, redact: cfg.Redact}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if !g.refExists(ctx, SourceBranchRef) {
		return &Result{}, nil // the slice has no history yet
	}

	remoteBranch, remoteTags, err := remote.lsRemote(ctx, cfg.Remote, cfg.Branch)
	if err != nil {
		return nil, err
	}
	if remoteBranch != "" {
		if _, err := remote.run(ctx, nil, "fetch", "--quiet", "--no-tags", cfg.Remote,
			fmt.Sprintf("+refs/heads/%s:%s", cfg.Branch, targetRef)); err != nil {
			return nil, fmt.Errorf("fetch mirror branch: %w", err)
		}
	} else {
		_, _ = g.run(ctx, nil, "update-ref", "-d", targetRef)
	}

	var mirror []commitMeta
	if remoteBranch != "" {
		if mirror, err = g.streamFirstParentLog(ctx, targetRef, maxMirrorWalk, nil); err != nil {
			return nil, err
		}
		if len(mirror) == 0 {
			return nil, fmt.Errorf("%w: mirror branch %s has no commits", ErrDiverged, cfg.Branch)
		}
	}

	// Stream projected commits newest-first. When the remote tip matches a
	// projected commit (the normal incremental case), stop reading immediately
	// instead of loading full repository history into memory.
	tipMatched := false
	projected, err := g.streamFirstParentLog(ctx, SourceBranchRef, 0, func(entry commitMeta) bool {
		if len(mirror) > 0 && matchesMirrorEntry(entry, mirror[0]) {
			tipMatched = true
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}

	head, headTree := "", ""
	syncIndex := len(projected) // an empty mirror: replay everything
	if remoteBranch != "" {
		if tipMatched {
			syncIndex = len(projected) - 1
		} else {
			byNative := make(map[string]int, len(projected))
			byGit := make(map[string]int, len(projected))
			for i, entry := range projected {
				if entry.Native != "" {
					byNative[entry.Native] = i
				}
				if entry.Git != "" {
					byGit[entry.Git] = i
				}
			}
			syncIndex = -1
			for walked, entry := range mirror {
				index, ok := byNative[entry.Native]
				if !ok || entry.Native == "" {
					index, ok = byGit[entry.SHA]
				}
				if !ok {
					continue
				}
				if walked > 0 {
					return nil, fmt.Errorf("%w: %s has %d commit(s) after %s that did not come from Gitslice; land them in Gitslice and reset the mirror instead of exporting over them", ErrDiverged, cfg.Branch, walked, short(entry.SHA))
				}
				syncIndex = index
				break
			}
			if syncIndex < 0 {
				return nil, fmt.Errorf("%w: no commit on %s came from this slice (no Gitslice-Commit or Git-Commit match); mirror to an empty repository or branch", ErrDiverged, cfg.Branch)
			}
		}
		head = mirror[0].SHA
		if headTree, err = g.output(ctx, "rev-parse", head+"^{tree}"); err != nil {
			return nil, err
		}
		syncTree, err := g.subtree(ctx, projected[syncIndex].SHA, cfg.Subdir)
		if err != nil {
			return nil, err
		}
		if headTree != syncTree {
			return nil, fmt.Errorf("%w: mirror head %s does not match Gitslice commit %s; refusing to export", ErrDiverged, short(head), short(projected[syncIndex].SHA))
		}
	}

	mirrorByNative := make(map[string]string, len(mirror))
	for _, entry := range mirror {
		if entry.Native != "" {
			if _, seen := mirrorByNative[entry.Native]; !seen {
				mirrorByNative[entry.Native] = entry.SHA
			}
		}
	}
	projectedBySHA := make(map[string]commitMeta, len(projected))
	for _, entry := range projected {
		projectedBySHA[entry.SHA] = entry
	}

	res := &Result{}
	exported := map[string]string{}
	if syncIndex < len(projected) {
		exported[projected[syncIndex].SHA] = head
		res.SourceHead = projected[syncIndex].SHA
	}
	current, currentTree := head, headTree
	for i := syncIndex - 1; i >= 0; i-- { // projected is newest first
		entry := projected[i]
		tree, err := g.subtree(ctx, entry.SHA, cfg.Subdir)
		if err != nil {
			return nil, err
		}
		if tree == currentTree || (current == "" && tree == emptyTree) {
			// Nothing changed in the mirrored folder (or it does not exist
			// yet): the commit maps to the mirror commit before it.
			if current != "" {
				exported[entry.SHA] = current
				res.SourceHead = entry.SHA
			}
			continue
		}
		rawCommit, err := g.run(ctx, nil, "log", "-1", "--format=%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd%x00%B", "--date=raw", entry.SHA)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(rawCommit, "\x00", 7)
		if len(parts) != 7 {
			return nil, fmt.Errorf("unexpected commit metadata for %s", short(entry.SHA))
		}
		body := strings.TrimSuffix(parts[6], "\n")
		env := []string{
			"GIT_AUTHOR_NAME=" + parts[0], "GIT_AUTHOR_EMAIL=" + parts[1], "GIT_AUTHOR_DATE=" + parts[2],
			"GIT_COMMITTER_NAME=" + parts[3], "GIT_COMMITTER_EMAIL=" + parts[4], "GIT_COMMITTER_DATE=" + parts[5],
		}
		args := []string{"commit-tree", tree}
		if current != "" {
			args = append(args, "-p", current)
		}
		sha, err := g.runInput(ctx, env, []byte(body), append(args, "-F", "-")...)
		if err != nil {
			return nil, err
		}
		current, currentTree = strings.TrimSpace(sha), tree
		exported[entry.SHA] = current
		res.SourceHead = entry.SHA
		res.Exported = append(res.Exported, current)
	}
	res.Head = current

	tags, err := g.tagTargets(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	type tagPush struct{ name, target string }
	var newTags []tagPush
	for _, name := range names {
		if cfg.Subdir != "" && strings.HasPrefix(name, strings.Trim(cfg.Subdir, "/")+"/") {
			continue // Go's subdirectory copy of a tag; the mirror keeps modules at its root
		}
		if _, ok := remoteTags[name]; ok {
			continue // tags are immutable
		}
		tagSHA := tags[name]
		target, ok := exported[tagSHA]
		if !ok {
			// A tag on an older commit: map it through the trailers, to the
			// mirror commit exported earlier or the original imported commit.
			meta, found := projectedBySHA[tagSHA]
			if !found && g.isAncestor(ctx, tagSHA, SourceBranchRef) {
				if loaded, loadErr := g.commitMetaFor(ctx, tagSHA); loadErr == nil {
					meta, found = loaded, true
					projectedBySHA[tagSHA] = loaded
				}
			}
			if found {
				if mirrored := mirrorByNative[meta.Native]; mirrored != "" {
					target, ok = mirrored, true
				} else if meta.Git != "" && remoteBranch != "" {
					target, ok = meta.Git, true
				}
			}
		}
		if !ok || target == "" {
			continue
		}
		newTags = append(newTags, tagPush{name, target})
	}

	if cfg.Push && (current != head || len(newTags) > 0) && cfg.Hydrate != nil {
		wants := []string{}
		if current != head {
			wants = append(wants, current)
		}
		for _, tag := range newTags {
			wants = append(wants, tag.target)
		}
		missing, err := g.missingObjects(ctx, wants, head)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			if err := cfg.Hydrate(ctx, missing); err != nil {
				return nil, fmt.Errorf("read file contents for the mirror: %w", err)
			}
		}
	}
	if cfg.Push && current != head {
		if _, err := remote.run(ctx, nil, "push", "--quiet", cfg.Remote, "--", current+":refs/heads/"+cfg.Branch); err != nil {
			return nil, fmt.Errorf("push mirror branch: %w", err)
		}
	}
	if cfg.Push && len(newTags) > 0 {
		for start := 0; start < len(newTags); start += tagPushBatchSize {
			end := min(start+tagPushBatchSize, len(newTags))
			args := make([]string, 0, 4+(end-start))
			args = append(args, "push", "--quiet", cfg.Remote, "--")
			for _, tag := range newTags[start:end] {
				args = append(args, tag.target+":refs/tags/"+tag.name)
			}
			if _, err := remote.run(ctx, nil, args...); err != nil {
				return nil, fmt.Errorf("push tags: %w", err)
			}
		}
	}
	for _, tag := range newTags {
		res.NewTags = append(res.NewTags, tag.name)
	}
	return res, nil
}

// trailer returns the value of the last "<key>: <value>" line in a message.
func trailer(body, key string) string {
	value := ""
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			value = strings.TrimSpace(rest)
		}
	}
	return value
}

// Short abbreviates a commit id.
func Short(sha string) string { return short(sha) }

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

type runner struct {
	dir    string
	env    []string
	redact func(string) string
}

func (g runner) run(ctx context.Context, env []string, args ...string) (string, error) {
	return g.runInput(ctx, env, nil, args...)
}

func (g runner) runInput(ctx context.Context, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = append(append(os.Environ(), g.env...), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := fmt.Sprintf("git %s: %v\n%s", strings.Join(redactArgs(args), " "), err, strings.TrimSpace(stderr.String()))
		if g.redact != nil {
			msg = g.redact(msg)
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}

func (g runner) output(ctx context.Context, args ...string) (string, error) {
	out, err := g.run(ctx, nil, args...)
	return strings.TrimSpace(out), err
}

func (g runner) refExists(ctx context.Context, ref string) bool {
	_, err := g.run(ctx, nil, "rev-parse", "--verify", "-q", ref)
	return err == nil
}

// subtree returns the tree id of dir inside commit, or the empty tree.
func (g runner) subtree(ctx context.Context, commit, dir string) (string, error) {
	spec := commit + "^{tree}"
	if dir = strings.Trim(dir, "/"); dir != "" {
		spec = commit + ":" + dir
	}
	out, err := g.run(ctx, nil, "rev-parse", "--verify", "-q", spec)
	if err != nil {
		return emptyTree, nil
	}
	return strings.TrimSpace(out), nil
}

// streamFirstParentLog streams commits newest first along first parents,
// retaining only commit SHAs and trailers rather than full commit bodies, and
// stopping early as soon as stop(entry) returns true.
func (g runner) streamFirstParentLog(ctx context.Context, ref string, limit int, stop func(commitMeta) bool) ([]commitMeta, error) {
	logCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := []string{"log", "--first-parent", "--format=%H%x00%B%x1e"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	args = append(args, ref)

	cmd := exec.CommandContext(logCtx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), g.env...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	reader := bufio.NewReaderSize(stdout, 64<<10)
	var entries []commitMeta
	stoppedEarly := false
	for {
		rawRecord, readErr := reader.ReadBytes('\x1e')
		if len(rawRecord) > 0 {
			record := strings.TrimSuffix(string(rawRecord), "\x1e")
			record = strings.TrimLeft(record, "\n")
			if record != "" {
				sha, body, ok := strings.Cut(record, "\x00")
				if !ok {
					cancel()
					_ = cmd.Wait()
					return nil, fmt.Errorf("malformed git log record")
				}
				meta := commitMeta{
					SHA:    sha,
					Native: trailer(body, "Gitslice-Commit"),
					Git:    trailer(body, "Git-Commit"),
				}
				entries = append(entries, meta)
				if stop != nil && stop(meta) {
					stoppedEarly = true
					cancel()
					break
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			cancel()
			_ = cmd.Wait()
			return nil, readErr
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil && !stoppedEarly {
		msg := fmt.Sprintf("git %s: %v\n%s", strings.Join(redactArgs(args), " "), waitErr, strings.TrimSpace(stderr.String()))
		if g.redact != nil {
			msg = g.redact(msg)
		}
		return nil, errors.New(msg)
	}
	return entries, nil
}

func (g runner) isAncestor(ctx context.Context, ancestor, descendant string) bool {
	_, err := g.run(ctx, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func (g runner) commitMetaFor(ctx context.Context, sha string) (commitMeta, error) {
	body, err := g.run(ctx, nil, "log", "-1", "--format=%B", sha)
	if err != nil {
		return commitMeta{}, err
	}
	return commitMeta{
		SHA:    sha,
		Native: trailer(body, "Gitslice-Commit"),
		Git:    trailer(body, "Git-Commit"),
	}, nil
}

// tagTargets maps each source tag to the commit it names.
func (g runner) tagTargets(ctx context.Context) (map[string]string, error) {
	out, err := g.output(ctx, "for-each-ref", "--format=%(refname) %(*objectname) %(objectname)", SourceTagsRef)
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimPrefix(fields[0], SourceTagsRef)
		tags[name] = fields[len(fields)-1]
		if len(fields) == 3 { // annotated: use the peeled commit
			tags[name] = fields[1]
		}
	}
	return tags, nil
}

// lsRemote returns the remote branch's commit ("" when it has none) and the
// remote's tag names.
func (g runner) lsRemote(ctx context.Context, remote, branch string) (string, map[string]struct{}, error) {
	out, err := g.output(ctx, "ls-remote", "--refs", remote, "refs/heads/"+branch, "refs/tags/*")
	if err != nil {
		return "", nil, fmt.Errorf("read the mirror: %w", err)
	}
	head := ""
	tags := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == "refs/heads/"+branch {
			head = fields[0]
		} else if name, ok := strings.CutPrefix(fields[1], "refs/tags/"); ok {
			tags[name] = struct{}{}
		}
	}
	return head, tags, nil
}

// missingObjects lists the objects reachable from wants, and not from have,
// that the repository cannot read.
func (g runner) missingObjects(ctx context.Context, wants []string, have string) ([]string, error) {
	var stdin strings.Builder
	seen := make(map[string]struct{}, len(wants))
	for _, w := range wants {
		if _, dup := seen[w]; dup || w == "" {
			continue
		}
		seen[w] = struct{}{}
		stdin.WriteString(w)
		stdin.WriteByte('\n')
	}
	if stdin.Len() == 0 {
		return nil, nil
	}
	if have != "" {
		stdin.WriteByte('^')
		stdin.WriteString(have)
		stdin.WriteByte('\n')
	}
	out, err := g.runInput(ctx, nil, []byte(stdin.String()), "rev-list", "--objects", "--missing=print", "--stdin")
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "?") {
			missing = append(missing, strings.TrimSpace(line[1:]))
		}
	}
	return missing, nil
}

func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "http.extraHeader=") {
			arg = "http.extraHeader=<redacted>"
		}
		out[i] = arg
	}
	return out
}

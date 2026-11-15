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
	"bytes"
	"context"
	"errors"
	"fmt"
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

type logEntry struct {
	SHA     string
	Parents []string
	Body    string
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

	projected, err := g.firstParentLog(ctx, SourceBranchRef, 0)
	if err != nil {
		return nil, err
	}
	byNative := map[string]int{}
	byGit := map[string]int{}
	for i, entry := range projected {
		if native := trailer(entry.Body, "Gitslice-Commit"); native != "" {
			byNative[native] = i
		}
		if original := trailer(entry.Body, "Git-Commit"); original != "" {
			byGit[original] = i
		}
	}

	var mirror []logEntry
	head, headTree := "", ""
	syncIndex := len(projected) // an empty mirror: replay everything
	if remoteBranch != "" {
		if mirror, err = g.firstParentLog(ctx, targetRef, maxMirrorWalk); err != nil {
			return nil, err
		}
		syncIndex = -1
		for walked, entry := range mirror {
			index, ok := byNative[trailer(entry.Body, "Gitslice-Commit")]
			if !ok {
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

	mirrorByNative := map[string]string{}
	for _, entry := range mirror {
		if native := trailer(entry.Body, "Gitslice-Commit"); native != "" {
			if _, seen := mirrorByNative[native]; !seen {
				mirrorByNative[native] = entry.SHA
			}
		}
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
		identity, err := g.output(ctx, "log", "-1", "--format=%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd", "--date=raw", entry.SHA)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(identity, "\x00")
		if len(parts) != 6 {
			return nil, fmt.Errorf("unexpected identity for %s", short(entry.SHA))
		}
		env := []string{
			"GIT_AUTHOR_NAME=" + parts[0], "GIT_AUTHOR_EMAIL=" + parts[1], "GIT_AUTHOR_DATE=" + parts[2],
			"GIT_COMMITTER_NAME=" + parts[3], "GIT_COMMITTER_EMAIL=" + parts[4], "GIT_COMMITTER_DATE=" + parts[5],
		}
		args := []string{"commit-tree", tree}
		if current != "" {
			args = append(args, "-p", current)
		}
		sha, err := g.runInput(ctx, env, []byte(entry.Body), append(args, "-F", "-")...)
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
		target, ok := exported[tags[name]]
		if !ok {
			// A tag on an older commit: map it through the trailers, to the
			// mirror commit exported earlier or the original imported commit.
			for _, entry := range projected {
				if entry.SHA != tags[name] {
					continue
				}
				if mirrored := mirrorByNative[trailer(entry.Body, "Gitslice-Commit")]; mirrored != "" {
					target, ok = mirrored, true
				} else if original := trailer(entry.Body, "Git-Commit"); original != "" && remoteBranch != "" {
					target, ok = original, true
				}
				break
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
		if _, err := remote.run(ctx, nil, "push", "--quiet", cfg.Remote, current+":refs/heads/"+cfg.Branch); err != nil {
			return nil, fmt.Errorf("push mirror branch: %w", err)
		}
	}
	for _, tag := range newTags {
		if cfg.Push {
			if _, err := remote.run(ctx, nil, "push", "--quiet", cfg.Remote, tag.target+":refs/tags/"+tag.name); err != nil {
				return nil, fmt.Errorf("push tag %s: %w", tag.name, err)
			}
		}
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

// firstParentLog lists commits newest first along first parents.
func (g runner) firstParentLog(ctx context.Context, ref string, limit int) ([]logEntry, error) {
	args := []string{"log", "--first-parent", "--format=%H%x00%P%x00%B%x1e"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	out, err := g.run(ctx, nil, append(args, ref)...)
	if err != nil {
		return nil, err
	}
	var entries []logEntry
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x00", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed git log record")
		}
		entries = append(entries, logEntry{SHA: parts[0], Parents: strings.Fields(parts[1]), Body: parts[2]})
	}
	return entries, nil
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
	args := append([]string{"rev-list", "--objects", "--missing=print"}, wants...)
	if have != "" {
		args = append(args, "--not", have)
	}
	out, err := g.output(ctx, args...)
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

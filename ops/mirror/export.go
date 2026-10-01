package main

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

// The exporter keeps a Git mirror (the GitHub repository) in step with a
// slice's projected history on Gitslice (design/21_self_hosting.md, Phase 2).
//
// Every projected commit carries a "Gitslice-Commit: <native id>" trailer, and
// projected commits that came from a Git import also carry "Git-Commit: <sha>"
// naming the original commit. The exporter finds the newest mirror commit that
// maps to a projected commit (by either trailer), checks that the mirror has
// not drifted from Gitslice, and then replays every later projected commit:
// the slice subdirectory's tree, with the projected commit's author, committer
// and message. New release tags are pushed the same way.

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// maxMirrorWalk bounds how far back the exporter looks for the sync point.
const maxMirrorWalk = 2000

type config struct {
	Repo   string // clone of the mirror
	Remote string // mirror remote name
	Branch string // mirror branch
	Source string // Gitslice Git URL (or path) of the slice
	Subdir string // slice path inside the projected repository
	Token  string // optional bearer token for the source
	Push   bool
}

type result struct {
	Exported []string // new mirror commits, oldest first
	NewTags  []string
	Head     string
}

type logEntry struct {
	SHA     string
	Parents []string
	Body    string
}

func run(ctx context.Context, cfg config) (*result, error) {
	g := gitRunner{dir: cfg.Repo}
	sourceFetch := []string{"fetch", "--quiet", "--no-tags", cfg.Source,
		"+refs/heads/main:refs/mirror/main", "+refs/tags/*:refs/mirror/tags/*"}
	if cfg.Token != "" {
		sourceFetch = append([]string{"-c", "http.extraHeader=Authorization: Bearer " + cfg.Token}, sourceFetch...)
	}
	if _, err := g.run(ctx, nil, nil, sourceFetch...); err != nil {
		return nil, fmt.Errorf("fetch Gitslice projection: %w", err)
	}
	if _, err := g.run(ctx, nil, nil, "fetch", "--quiet", "--no-tags", cfg.Remote,
		fmt.Sprintf("+refs/heads/%s:refs/mirror/target", cfg.Branch)); err != nil {
		return nil, fmt.Errorf("fetch mirror branch: %w", err)
	}
	if !g.refExists(ctx, "refs/mirror/main") {
		return &result{}, nil // the slice has no history yet
	}

	projected, err := g.firstParentLog(ctx, "refs/mirror/main", 0)
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

	mirror, err := g.firstParentLog(ctx, "refs/mirror/target", maxMirrorWalk)
	if err != nil {
		return nil, err
	}
	syncIndex := -1
	for walked, entry := range mirror {
		index, ok := byNative[trailer(entry.Body, "Gitslice-Commit")]
		if !ok {
			index, ok = byGit[entry.SHA]
		}
		if !ok {
			continue
		}
		if walked > 0 {
			return nil, fmt.Errorf("the mirror has %d commit(s) after %s that did not come from Gitslice; land them in Gitslice and reset the mirror instead of exporting over them", walked, short(entry.SHA))
		}
		syncIndex = index
		break
	}
	if syncIndex < 0 {
		return nil, errors.New("no mirror commit maps to the Gitslice history (no Gitslice-Commit or Git-Commit match)")
	}
	head := mirror[0].SHA
	headTree, err := g.output(ctx, "rev-parse", head+"^{tree}")
	if err != nil {
		return nil, err
	}
	syncTree, err := g.subtree(ctx, projected[syncIndex].SHA, cfg.Subdir)
	if err != nil {
		return nil, err
	}
	if headTree != syncTree {
		return nil, fmt.Errorf("mirror head %s does not match Gitslice commit %s; refusing to export", short(head), short(projected[syncIndex].SHA))
	}

	mirrorByNative := map[string]string{}
	for _, entry := range mirror {
		if native := trailer(entry.Body, "Gitslice-Commit"); native != "" {
			if _, seen := mirrorByNative[native]; !seen {
				mirrorByNative[native] = entry.SHA
			}
		}
	}

	res := &result{Head: head}
	exported := map[string]string{projected[syncIndex].SHA: head}
	current, currentTree := head, headTree
	for i := syncIndex - 1; i >= 0; i-- { // projected is newest first
		entry := projected[i]
		tree, err := g.subtree(ctx, entry.SHA, cfg.Subdir)
		if err != nil {
			return nil, err
		}
		if tree == currentTree {
			exported[entry.SHA] = current
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
		sha, err := g.run(ctx, env, []byte(entry.Body), "commit-tree", tree, "-p", current, "-F", "-")
		if err != nil {
			return nil, err
		}
		current, currentTree = strings.TrimSpace(sha), tree
		exported[entry.SHA] = current
		res.Exported = append(res.Exported, current)
	}
	res.Head = current
	if cfg.Push && current != head {
		if _, err := g.run(ctx, nil, nil, "push", "--quiet", cfg.Remote, current+":refs/heads/"+cfg.Branch); err != nil {
			return nil, fmt.Errorf("push mirror branch: %w", err)
		}
	}

	tags, err := g.tagTargets(ctx)
	if err != nil {
		return nil, err
	}
	remoteTags, err := g.remoteTags(ctx, cfg.Remote)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
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
				} else if original := trailer(entry.Body, "Git-Commit"); original != "" {
					target, ok = original, true
				}
				break
			}
		}
		if !ok {
			continue
		}
		if cfg.Push {
			if _, err := g.run(ctx, nil, nil, "push", "--quiet", cfg.Remote, target+":refs/tags/"+name); err != nil {
				return nil, fmt.Errorf("push tag %s: %w", name, err)
			}
		}
		res.NewTags = append(res.NewTags, name)
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

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

type gitRunner struct {
	dir string
}

func (g gitRunner) run(ctx context.Context, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(redact(args), " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func (g gitRunner) output(ctx context.Context, args ...string) (string, error) {
	out, err := g.run(ctx, nil, nil, args...)
	return strings.TrimSpace(out), err
}

func (g gitRunner) refExists(ctx context.Context, ref string) bool {
	_, err := g.run(ctx, nil, nil, "rev-parse", "--verify", "-q", ref)
	return err == nil
}

// subtree returns the tree id of dir inside commit, or the empty tree.
func (g gitRunner) subtree(ctx context.Context, commit, dir string) (string, error) {
	spec := commit + "^{tree}"
	if dir = strings.Trim(dir, "/"); dir != "" {
		spec = commit + ":" + dir
	}
	out, err := g.run(ctx, nil, nil, "rev-parse", "--verify", "-q", spec)
	if err != nil {
		return emptyTree, nil
	}
	return strings.TrimSpace(out), nil
}

// firstParentLog lists commits newest first along first parents.
func (g gitRunner) firstParentLog(ctx context.Context, ref string, limit int) ([]logEntry, error) {
	args := []string{"log", "--first-parent", "--format=%H%x00%P%x00%B%x1e"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	out, err := g.run(ctx, nil, nil, append(args, ref)...)
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

// tagTargets maps each fetched source tag to the commit it names.
func (g gitRunner) tagTargets(ctx context.Context) (map[string]string, error) {
	out, err := g.output(ctx, "for-each-ref", "--format=%(refname) %(*objectname) %(objectname)", "refs/mirror/tags/")
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "refs/mirror/tags/")
		tags[name] = fields[len(fields)-1]
		if len(fields) == 3 { // annotated: use the peeled commit
			tags[name] = fields[1]
		}
	}
	return tags, nil
}

func (g gitRunner) remoteTags(ctx context.Context, remote string) (map[string]struct{}, error) {
	out, err := g.output(ctx, "ls-remote", "--tags", "--refs", remote)
	if err != nil {
		return nil, err
	}
	tags := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			tags[strings.TrimPrefix(fields[1], "refs/tags/")] = struct{}{}
		}
	}
	return tags, nil
}

func redact(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "http.extraHeader=") {
			arg = "http.extraHeader=<redacted>"
		}
		out[i] = arg
	}
	return out
}

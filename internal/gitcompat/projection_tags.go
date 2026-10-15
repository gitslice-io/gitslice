package gitcompat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// goSemverTag matches the version tags Go understands.
var goSemverTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// syncTags publishes the slice's native tags as lightweight tags on the
// projected commit at or before each tagged native commit. For a slice with a
// single included path, a semver tag is also published under that path (for
// example acme/payment/v1.2.0): that is how Go names versions of a module
// whose root is a repository subdirectory, which the canonical layout puts
// every slice's files in. It reports whether the cached mapping changed.
func (p *Projector) syncTags(ctx context.Context, repoPath string, state *projectionState, slice *corev1.Slice, prefixes []string) (bool, error) {
	if p.slices == nil {
		return false, nil
	}
	tags, err := p.slices.ListTags(ctx, slice.Id)
	if err != nil {
		return false, err
	}
	history := make(map[string]string, len(state.Commits))
	for _, commit := range state.Commits {
		history[commit.Native] = commit.Git
	}
	if state.Tags == nil {
		state.Tags = map[string]projectedCommit{}
	}
	changed := false
	resolved := map[string]projectedCommit{}
	for _, tag := range tags {
		if cached, ok := state.Tags[tag.Name]; ok && cached.Native == tag.CommitID {
			// The cached projected commit must still be part of this history.
			if _, known := projectedIndex(state, cached.Git); known {
				resolved[tag.Name] = cached
				continue
			}
		}
		target, ok, err := p.projectedCommitAt(ctx, state, history, tag.CommitID)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		resolved[tag.Name] = projectedCommit{Native: tag.CommitID, Git: target}
		changed = true
	}
	if len(resolved) != len(state.Tags) {
		changed = true
	}
	state.Tags = resolved

	want := map[string]string{}
	subdir := ""
	if len(prefixes) == 1 && prefixes[0] != "/" {
		subdir = strings.Trim(prefixes[0], "/")
	}
	for name, target := range resolved {
		want["refs/tags/"+name] = target.Git
		if subdir != "" && goSemverTag.MatchString(name) {
			want["refs/tags/"+subdir+"/"+name] = target.Git
		}
	}
	return changed, applyTagRefs(ctx, repoPath, want)
}

// applyTagRefs makes refs/tags/* in the projected repository exactly want.
func applyTagRefs(ctx context.Context, repoPath string, want map[string]string) error {
	raw, err := gitOutput(ctx, repoPath, nil, "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags/")
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if ref, oid, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			have[ref] = oid
		}
	}
	var commands strings.Builder
	refs := make([]string, 0, len(want))
	for ref := range want {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if have[ref] != want[ref] {
			fmt.Fprintf(&commands, "update %s %s\n", ref, want[ref])
		}
	}
	stale := make([]string, 0)
	for ref := range have {
		if _, ok := want[ref]; !ok {
			stale = append(stale, ref)
		}
	}
	sort.Strings(stale)
	for _, ref := range stale {
		fmt.Fprintf(&commands, "delete %s\n", ref)
	}
	if commands.Len() == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "update-ref", "--stdin")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	cmd.Stdin = strings.NewReader(commands.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref failed: %w\n%s", err, string(out))
	}
	return nil
}

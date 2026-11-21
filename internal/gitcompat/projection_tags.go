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

type projectedTagSource struct {
	name     string
	commitID string
	own      bool
}

// syncTags publishes the slice's native tags as lightweight tags on the
// projected commit at or before each tagged native commit. For a slice with a
// single included path, a semver tag is also published under that path (for
// example acme/payment/v1.2.0): that is how Go names versions of a module
// whose root is a repository subdirectory, which the canonical layout puts
// every slice's files in. For an account's home slice, tags from the account's
// other slices are also published under refs/tags/<slice>/<name>. It reports
// whether the cached mapping changed.
func (p *Projector) syncTags(ctx context.Context, repoPath string, state *projectionState, slice *corev1.Slice, prefixes []string) (bool, error) {
	if p.slices == nil {
		return false, nil
	}
	sources, err := p.collectProjectedTags(ctx, slice)
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
	ownResolved := map[string]bool{}
	// Resolve the slice's own tags before aggregated sub-slice tags so a home
	// slice's own tag always takes precedence on name collision.
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].own != sources[j].own {
			return sources[i].own
		}
		return sources[i].name < sources[j].name
	})
	for _, src := range sources {
		if _, exists := resolved[src.name]; exists {
			continue
		}
		if cached, ok := state.Tags[src.name]; ok && cached.Native == src.commitID {
			// The cached projected commit must still be part of this history.
			if _, known := projectedIndex(state, cached.Git); known {
				resolved[src.name] = cached
				ownResolved[src.name] = src.own
				continue
			}
		}
		target, ok, err := p.projectedCommitAt(ctx, state, history, src.commitID)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		resolved[src.name] = projectedCommit{Native: src.commitID, Git: target}
		ownResolved[src.name] = src.own
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
	ownNames := make([]string, 0, len(resolved))
	aggNames := make([]string, 0, len(resolved))
	for name := range resolved {
		if ownResolved[name] {
			ownNames = append(ownNames, name)
		} else {
			aggNames = append(aggNames, name)
		}
	}
	sort.Strings(ownNames)
	sort.Strings(aggNames)
	for _, name := range ownNames {
		target := resolved[name]
		ref := "refs/tags/" + name
		if !tagRefConflicts(want, ref) {
			want[ref] = target.Git
		}
		if subdir != "" && goSemverTag.MatchString(name) {
			subdirRef := "refs/tags/" + subdir + "/" + name
			if !tagRefConflicts(want, subdirRef) {
				want[subdirRef] = target.Git
			}
		}
	}
	for _, name := range aggNames {
		target := resolved[name]
		ref := "refs/tags/" + name
		if !tagRefConflicts(want, ref) {
			want[ref] = target.Git
		}
	}
	return changed, applyTagRefs(ctx, repoPath, want)
}

func (p *Projector) collectProjectedTags(ctx context.Context, slice *corev1.Slice) ([]projectedTagSource, error) {
	if slice.GetRef().GetSlice() != "home" {
		tags, err := p.slices.ListTags(ctx, slice.Id)
		if err != nil {
			return nil, err
		}
		out := make([]projectedTagSource, 0, len(tags))
		for _, tag := range tags {
			out = append(out, projectedTagSource{name: tag.Name, commitID: tag.CommitID, own: true})
		}
		return out, nil
	}
	account := slice.GetRef().GetAccount()
	publicHome := strings.EqualFold(strings.TrimSpace(slice.GetDefinition().GetVisibility()), "public")
	tags, err := p.slices.ListAccountTags(ctx, account, publicHome, "")
	if err != nil {
		return nil, err
	}
	out := make([]projectedTagSource, 0, len(tags))
	for _, tag := range tags {
		if tag.SliceID == slice.Id {
			out = append(out, projectedTagSource{name: tag.Name, commitID: tag.CommitID, own: true})
			continue
		}
		if tag.SliceSlug == "" {
			continue
		}
		out = append(out, projectedTagSource{name: tag.SliceSlug + "/" + tag.Name, commitID: tag.CommitID, own: false})
	}
	return out, nil
}

// tagRefConflicts reports whether adding candidate to want would collide with
// an existing ref name or create a Git directory/file ref conflict (for
// example refs/tags/foo and refs/tags/foo/v1.0.0).
func tagRefConflicts(want map[string]string, candidate string) bool {
	if _, exists := want[candidate]; exists {
		return true
	}
	candPrefix := candidate + "/"
	for existing := range want {
		if strings.HasPrefix(existing, candPrefix) || strings.HasPrefix(candidate, existing+"/") {
			return true
		}
	}
	return false
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
	stale := make([]string, 0)
	for ref := range have {
		if _, ok := want[ref]; !ok {
			stale = append(stale, ref)
		}
	}
	sort.Strings(stale)
	// Delete stale refs before updating so directory/file ref transitions
	// (for example refs/tags/foo -> refs/tags/foo/v1.0.0) succeed cleanly.
	for _, ref := range stale {
		fmt.Fprintf(&commands, "delete %s\n", ref)
	}
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

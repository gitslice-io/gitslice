package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gitslice.io/gitslice/internal/gitmirror"
)

// The exporter keeps a Git mirror (the GitHub repository) in step with a
// slice's projected history on Gitslice (design/21_self_hosting.md, Phase 2).
// It fetches the projection over Gitslice's Git endpoint and hands the rest
// to internal/gitmirror, which the server's own mirrors use too
// (design/35_git_mirrors.md).

type config struct {
	Repo   string // clone of the mirror
	Remote string // mirror remote name
	Branch string // mirror branch
	Source string // Gitslice Git URL (or path) of the slice
	Subdir string // slice path inside the projected repository
	Token  string // optional bearer token for the source
	Push   bool
}

func run(ctx context.Context, cfg config) (*gitmirror.Result, error) {
	args := []string{"fetch", "--quiet", "--no-tags", cfg.Source,
		"+refs/heads/main:" + gitmirror.SourceBranchRef, "+refs/tags/*:" + gitmirror.SourceTagsRef + "*"}
	if cfg.Token != "" {
		args = append([]string{"-c", "http.extraHeader=Authorization: Bearer " + cfg.Token}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cfg.Repo
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fetch Gitslice projection: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return gitmirror.Run(ctx, gitmirror.Config{
		Dir:    cfg.Repo,
		Remote: cfg.Remote,
		Branch: cfg.Branch,
		Subdir: cfg.Subdir,
		Push:   cfg.Push,
	})
}

func short(sha string) string { return gitmirror.Short(sha) }

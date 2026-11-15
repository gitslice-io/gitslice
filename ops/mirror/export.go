package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func buildFetchCommand(ctx context.Context, cfg config) (*exec.Cmd, error) {
	if cfg.Source == "" || strings.HasPrefix(cfg.Source, "-") || strings.ContainsAny(cfg.Source, "\r\n\x00") {
		return nil, errors.New("invalid mirror source")
	}
	if strings.ContainsAny(cfg.Token, "\r\n\x00") {
		return nil, errors.New("invalid mirror token")
	}

	allowProtocol := "https"
	switch {
	case strings.HasPrefix(cfg.Source, "https://"):
		allowProtocol = "https"
	case cfg.Token == "" && !strings.Contains(cfg.Source, "://") && filepath.IsAbs(cfg.Source):
		// Local filesystem sources are permitted only when unauthenticated (e.g. unit tests).
		allowProtocol = "https:file"
	default:
		return nil, errors.New("mirror source must use https://")
	}

	args := []string{
		"fetch",
		"--quiet",
		"--no-tags",
		"--",
		cfg.Source,
		"+refs/heads/main:" + gitmirror.SourceBranchRef,
		"+refs/tags/*:" + gitmirror.SourceTagsRef + "*",
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cfg.Repo

	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_CONFIG_COUNT="),
			strings.HasPrefix(kv, "GIT_CONFIG_KEY_"),
			strings.HasPrefix(kv, "GIT_CONFIG_VALUE_"),
			strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="),
			strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL="):
			continue
		default:
			env = append(env, kv)
		}
	}
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+allowProtocol,
	)
	if cfg.Token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http.followRedirects",
			"GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_KEY_1=http.extraHeader",
			"GIT_CONFIG_VALUE_1=Authorization: Bearer "+cfg.Token,
		)
	} else {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.followRedirects",
			"GIT_CONFIG_VALUE_0=false",
		)
	}
	cmd.Env = env
	return cmd, nil
}

func redactToken(text, token string) string {
	if token == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "[REDACTED]")
}

func run(ctx context.Context, cfg config) (*gitmirror.Result, error) {
	cmd, err := buildFetchCommand(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		safeErr := redactToken(err.Error(), cfg.Token)
		safeOut := redactToken(strings.TrimSpace(string(out)), cfg.Token)
		return nil, fmt.Errorf("fetch Gitslice projection: %s\n%s", safeErr, safeOut)
	}
	res, err := gitmirror.Run(ctx, gitmirror.Config{
		Dir:    cfg.Repo,
		Remote: cfg.Remote,
		Branch: cfg.Branch,
		Subdir: cfg.Subdir,
		Push:   cfg.Push,
	})
	if err != nil {
		return nil, errors.New(redactToken(err.Error(), cfg.Token))
	}
	return res, nil
}

func short(sha string) string { return gitmirror.Short(sha) }

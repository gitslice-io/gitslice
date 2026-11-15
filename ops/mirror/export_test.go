package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, env []string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.invalid",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.invalid",
	}, env...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// projectedCommit writes files under org/proj and commits them on the
// projected repository's main with the given message.
func projectedCommit(t *testing.T, work string, files map[string]string, author, message string) string {
	t.Helper()
	for path, content := range files {
		writeFile(t, filepath.Join(work, "org", "proj", path), content)
	}
	git(t, work, nil, "", "add", "-A")
	env := []string{"GIT_AUTHOR_NAME=" + author, "GIT_AUTHOR_EMAIL=" + author + "@users.noreply.gitslice.io"}
	git(t, work, env, message, "commit", "-q", "-F", "-")
	return git(t, work, nil, "", "rev-parse", "HEAD")
}

func TestExportReplaysNewProjectedCommits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// The mirror (GitHub) holds the original history: two commits at the root.
	github := filepath.Join(root, "github.git")
	git(t, root, nil, "", "init", "-q", "--bare", "-b", "main", github)
	seed := filepath.Join(root, "seed")
	git(t, root, nil, "", "clone", "-q", github, seed)
	writeFile(t, filepath.Join(seed, "a.txt"), "a\n")
	git(t, seed, nil, "", "add", "-A")
	git(t, seed, nil, "", "commit", "-q", "-m", "add a")
	c1 := git(t, seed, nil, "", "rev-parse", "HEAD")
	writeFile(t, filepath.Join(seed, "b.txt"), "b\n")
	git(t, seed, nil, "", "add", "-A")
	git(t, seed, nil, "", "commit", "-q", "-m", "add b")
	c2 := git(t, seed, nil, "", "rev-parse", "HEAD")
	git(t, seed, nil, "", "push", "-q", "origin", "HEAD:main")
	git(t, seed, nil, "", "push", "-q", "origin", c1+":refs/tags/v0.1.0")

	// The Gitslice projection: the imported commits (with Git-Commit
	// trailers) followed by two native commits, under org/proj.
	projectedRepo := filepath.Join(root, "projected")
	git(t, root, nil, "", "init", "-q", "-b", "main", projectedRepo)
	projectedCommit(t, projectedRepo, map[string]string{"a.txt": "a\n"}, "ada", "add a\n\nGit-Commit: "+c1+"\nGitslice-Commit: n1\n")
	p2 := projectedCommit(t, projectedRepo, map[string]string{"b.txt": "b\n"}, "ada", "add b\n\nGit-Commit: "+c2+"\nGitslice-Commit: n2\n")
	p3 := projectedCommit(t, projectedRepo, map[string]string{"c.txt": "c\n"}, "nic", "native change\n\nGitslice-Commit: n3\n")
	p4 := projectedCommit(t, projectedRepo, map[string]string{"c.txt": "c2\n"}, "nic", "second native\n\nGitslice-Commit: n4\n")
	git(t, projectedRepo, nil, "", "tag", "v1.0.0", p3)
	git(t, projectedRepo, nil, "", "tag", "org/proj/v1.0.0", p3)
	git(t, projectedRepo, nil, "", "tag", "imported-b", p2)
	git(t, projectedRepo, nil, "", "tag", "v0.1.0", p2) // the mirror already has v0.1.0

	mirror := filepath.Join(root, "mirror")
	git(t, root, nil, "", "clone", "-q", github, mirror)
	cfg := config{Repo: mirror, Remote: "origin", Branch: "main", Source: projectedRepo, Subdir: "org/proj", Push: true}
	res, err := run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exported) != 2 {
		t.Fatalf("exported %d commits, want 2", len(res.Exported))
	}
	if strings.Join(res.NewTags, ",") != "imported-b,v1.0.0" {
		t.Fatalf("new tags = %v", res.NewTags)
	}

	check := filepath.Join(root, "check")
	git(t, root, nil, "", "clone", "-q", github, check)
	if got := git(t, check, nil, "", "log", "--format=%s|%an", "-4"); got != "second native|nic\nnative change|nic\nadd b|Tester\nadd a|Tester" {
		t.Fatalf("mirror history:\n%s", got)
	}
	if head, want := git(t, check, nil, "", "rev-parse", "HEAD^{tree}"), git(t, projectedRepo, nil, "", "rev-parse", p4+":org/proj"); head != want {
		t.Fatalf("mirror tree %s, want projected subtree %s", head, want)
	}
	if body := git(t, check, nil, "", "log", "-1", "--format=%B"); !strings.Contains(body, "Gitslice-Commit: n4") {
		t.Fatalf("exported commit lost its trailer:\n%s", body)
	}
	git(t, check, nil, "", "fetch", "-q", "--tags")
	if got := git(t, check, nil, "", "log", "-1", "--format=%s", "v1.0.0"); got != "native change" {
		t.Fatalf("v1.0.0 -> %q", got)
	}
	if got := git(t, check, nil, "", "rev-parse", "imported-b^{commit}"); got != c2 {
		t.Fatalf("imported-b -> %s, want original %s", got, c2)
	}
	if got := git(t, check, nil, "", "rev-parse", "v0.1.0^{commit}"); got != c1 {
		t.Fatalf("existing v0.1.0 was moved to %s", got)
	}
	if out := git(t, check, nil, "", "tag", "-l", "org/*"); out != "" {
		t.Fatalf("Go subdirectory tag copies must not reach the mirror: %q", out)
	}

	// A second run is a no-op.
	res, err = run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exported) != 0 || len(res.NewTags) != 0 {
		t.Fatalf("second run exported %d commits and %d tags", len(res.Exported), len(res.NewTags))
	}

	// A later native tag on an already-exported commit maps through the
	// mirror's Gitslice-Commit trailer.
	git(t, projectedRepo, nil, "", "tag", "v1.0.1", p4)
	if res, err = run(ctx, cfg); err != nil || strings.Join(res.NewTags, ",") != "v1.0.1" {
		t.Fatalf("late tag run: %v %v", res, err)
	}

	// A commit pushed straight to the mirror stops the export.
	writeFile(t, filepath.Join(check, "rogue.txt"), "rogue\n")
	git(t, check, nil, "", "add", "-A")
	git(t, check, nil, "", "commit", "-q", "-m", "pushed to GitHub directly")
	git(t, check, nil, "", "push", "-q", "origin", "HEAD:main")
	projectedCommit(t, projectedRepo, map[string]string{"d.txt": "d\n"}, "nic", "third native\n\nGitslice-Commit: n5\n")
	if _, err := run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "did not come from Gitslice") {
		t.Fatalf("export over a foreign mirror commit: %v", err)
	}
}

func TestExportRefusesTreeDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	github := filepath.Join(root, "github.git")
	git(t, root, nil, "", "init", "-q", "--bare", "-b", "main", github)
	seed := filepath.Join(root, "seed")
	git(t, root, nil, "", "clone", "-q", github, seed)
	writeFile(t, filepath.Join(seed, "a.txt"), "different\n")
	git(t, seed, nil, "", "add", "-A")
	git(t, seed, nil, "", "commit", "-q", "-m", "add a")
	c1 := git(t, seed, nil, "", "rev-parse", "HEAD")
	git(t, seed, nil, "", "push", "-q", "origin", "HEAD:main")

	projectedRepo := filepath.Join(root, "projected")
	git(t, root, nil, "", "init", "-q", "-b", "main", projectedRepo)
	projectedCommit(t, projectedRepo, map[string]string{"a.txt": "a\n"}, "ada", "add a\n\nGit-Commit: "+c1+"\nGitslice-Commit: n1\n")

	mirror := filepath.Join(root, "mirror")
	git(t, root, nil, "", "clone", "-q", github, mirror)
	_, err := run(ctx, config{Repo: mirror, Remote: "origin", Branch: "main", Source: projectedRepo, Subdir: "org/proj", Push: true})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected a tree drift error, got %v", err)
	}
}

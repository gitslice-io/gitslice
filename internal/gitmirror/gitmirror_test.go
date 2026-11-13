package gitmirror

import (
	"context"
	"errors"
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

func commitFiles(t *testing.T, work string, files map[string]string, message string) string {
	t.Helper()
	for path, content := range files {
		writeFile(t, filepath.Join(work, path), content)
	}
	git(t, work, nil, "", "add", "-A")
	git(t, work, nil, message, "commit", "-q", "--allow-empty", "-F", "-")
	return git(t, work, nil, "", "rev-parse", "HEAD")
}

// setup returns a projection (a work tree with commits), an empty bare
// remote, and a work repository that borrows the projection's objects.
func setup(t *testing.T) (projection, remote, work string) {
	root := t.TempDir()
	projection = filepath.Join(root, "projection")
	git(t, root, nil, "", "init", "-q", "-b", "main", projection)
	remote = filepath.Join(root, "remote.git")
	git(t, root, nil, "", "init", "-q", "--bare", "-b", "main", remote)
	work = filepath.Join(root, "work.git")
	git(t, root, nil, "", "init", "-q", "--bare", work)
	writeFile(t, filepath.Join(work, "objects", "info", "alternates"), filepath.Join(projection, ".git", "objects")+"\n")
	return projection, remote, work
}

func pointSource(t *testing.T, projection, work string) {
	t.Helper()
	git(t, work, nil, "", "update-ref", SourceBranchRef, git(t, projection, nil, "", "rev-parse", "HEAD"))
	for _, line := range strings.Split(git(t, projection, nil, "", "for-each-ref", "--format=%(refname:strip=2) %(objectname)", "refs/tags/"), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			git(t, work, nil, "", "update-ref", SourceTagsRef+fields[0], fields[1])
		}
	}
}

func TestRunExportsEverythingToAnEmptyMirror(t *testing.T) {
	ctx := context.Background()
	projection, remote, work := setup(t)
	// A commit outside the slice folder comes first: it has no tree in the
	// mirror, so it is skipped rather than exported as an empty root.
	commitFiles(t, projection, map[string]string{"other/x.txt": "x\n"}, "elsewhere\n\nGitslice-Commit: n0\n")
	p1 := commitFiles(t, projection, map[string]string{"acme/app/a.txt": "a\n"}, "add a\n\nGitslice-Commit: n1\n")
	commitFiles(t, projection, map[string]string{"other/y.txt": "y\n"}, "elsewhere again\n\nGitslice-Commit: n2\n")
	p3 := commitFiles(t, projection, map[string]string{"acme/app/b.txt": "b\n"}, "add b\n\nGitslice-Commit: n3\n")
	git(t, projection, nil, "", "tag", "v1.0.0", p1)
	git(t, projection, nil, "", "tag", "acme/app/v1.0.0", p1)
	pointSource(t, projection, work)

	cfg := Config{Dir: work, Remote: remote, Branch: "main", Subdir: "acme/app", Push: true}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exported) != 2 || res.SourceHead != p3 || strings.Join(res.NewTags, ",") != "v1.0.0" {
		t.Fatalf("result = %+v", res)
	}
	if got := git(t, remote, nil, "", "log", "--format=%s", "main"); got != "add b\nadd a" {
		t.Fatalf("mirror history:\n%s", got)
	}
	if got, want := git(t, remote, nil, "", "rev-parse", "main^{tree}"), git(t, projection, nil, "", "rev-parse", p3+":acme/app"); got != want {
		t.Fatalf("mirror tree %s, want %s", got, want)
	}
	if got := git(t, remote, nil, "", "log", "-1", "--format=%s", "v1.0.0"); got != "add a" {
		t.Fatalf("v1.0.0 -> %q", got)
	}

	// Run again: nothing to do. Then a new commit is a fast-forward.
	if res, err = Run(ctx, cfg); err != nil || len(res.Exported) != 0 || len(res.NewTags) != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	commitFiles(t, projection, map[string]string{"acme/app/c.txt": "c\n"}, "add c\n\nGitslice-Commit: n4\n")
	pointSource(t, projection, work)
	if res, err = Run(ctx, cfg); err != nil || len(res.Exported) != 1 {
		t.Fatalf("third run: %+v %v", res, err)
	}
	if got := git(t, remote, nil, "", "log", "-1", "--format=%s", "main"); got != "add c" {
		t.Fatalf("mirror head %q", got)
	}
}

func TestRunHydratesMissingContentsBeforePushing(t *testing.T) {
	ctx := context.Background()
	projection, remote, work := setup(t)
	commitFiles(t, projection, map[string]string{"acme/app/a.txt": "lazy\n"}, "add a\n\nGitslice-Commit: n1\n")
	blob := git(t, projection, nil, "", "rev-parse", "HEAD:acme/app/a.txt")
	// Make the projection lazy: drop the file's contents.
	if err := os.Remove(filepath.Join(projection, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	pointSource(t, projection, work)

	var asked []string
	_, err := Run(ctx, Config{Dir: work, Remote: remote, Subdir: "acme/app", Push: true,
		Hydrate: func(ctx context.Context, ids []string) error {
			asked = append(asked, ids...)
			git(t, projection, nil, "lazy\n", "hash-object", "-w", "--stdin")
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != blob {
		t.Fatalf("hydrated %v, want %s", asked, blob)
	}
	if got := git(t, remote, nil, "", "show", "main:a.txt"); got != "lazy" {
		t.Fatalf("mirror a.txt = %q", got)
	}
}

func TestRunRefusesAnUnrelatedMirror(t *testing.T) {
	ctx := context.Background()
	projection, remote, work := setup(t)
	commitFiles(t, projection, map[string]string{"acme/app/a.txt": "a\n"}, "add a\n\nGitslice-Commit: n1\n")
	pointSource(t, projection, work)
	// The remote was created with its own first commit (a README).
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, filepath.Dir(seed), nil, "", "clone", "-q", remote, seed)
	commitFiles(t, seed, map[string]string{"README.md": "hi\n"}, "Initial commit")
	git(t, seed, nil, "", "push", "-q", "origin", "HEAD:main")

	secret := "s3cr3t"
	_, err := Run(ctx, Config{Dir: work, Remote: remote, Subdir: "acme/app", Push: true,
		Redact: func(s string) string { return strings.ReplaceAll(s, secret, "***") }})
	if !errors.Is(err, ErrDiverged) || !strings.Contains(err.Error(), "empty repository") {
		t.Fatalf("err = %v", err)
	}
}

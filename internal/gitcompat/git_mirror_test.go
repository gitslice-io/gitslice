package gitcompat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushMirrorRepoHydratesAndRootsTheSliceFolder(t *testing.T) {
	f := newLazyFixture(t)
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, "", nil, "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(t.TempDir(), ".mirrors", "acme", "p.git")
	target := MirrorTarget{URL: remote, Branch: "main", Token: "tok3n", AllowLocal: true}

	res, source, err := f.p.pushMirrorRepo(ctx, f.repo, work, "acme/p", target)
	if err != nil {
		t.Fatal(err)
	}
	if source != f.head || res.Exported != 3 || res.Head == "" {
		t.Fatalf("result %+v source %s, want head %s", res, source, f.head)
	}
	if f.missing(t) != 0 {
		t.Fatalf("%d files still missing from the projection after the push", f.missing(t))
	}
	if got := gitIn(t, remote, nil, "show", "main:a.txt"); strings.TrimSpace(got) != "alpha" {
		t.Fatalf("mirror a.txt = %q", got)
	}
	if got := gitIn(t, remote, nil, "ls-tree", "--name-only", "main"); strings.Fields(got)[0] != "a.txt" {
		t.Fatalf("mirror root = %q, want the slice folder's contents", got)
	}
	// The projection does not receive the mirror's refs.
	if refs := gitIn(t, f.repo, nil, "for-each-ref", "refs/mirror/"); strings.TrimSpace(refs) != "" {
		t.Fatalf("projection has mirror refs: %s", refs)
	}
	// Nothing new: a second push does nothing.
	if res, _, err = f.p.pushMirrorRepo(ctx, f.repo, work, "acme/p", target); err != nil || res.Exported != 0 {
		t.Fatalf("second push: %+v %v", res, err)
	}
}

func TestMirrorRemoteEnvKeepsTheTokenOffCommandLines(t *testing.T) {
	env, redact := mirrorRemoteEnv(MirrorTarget{URL: "https://example.com/a.git", Token: "ghp_secret", Username: "bot"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "ghp_secret") {
		t.Fatal("token appears in clear in the environment; it must be inside the Basic header")
	}
	if !strings.Contains(joined, "GIT_ALLOW_PROTOCOL=https\n") || !strings.Contains(joined, "Authorization: Basic ") {
		t.Fatalf("env = %s", joined)
	}
	header := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_VALUE_2=Authorization: Basic "); ok {
			header = v
		}
	}
	if got := redact("fatal: ghp_secret and " + header); strings.Contains(got, "ghp_secret") || strings.Contains(got, header) {
		t.Fatalf("redact left a secret: %s", got)
	}
}

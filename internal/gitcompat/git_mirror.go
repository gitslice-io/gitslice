package gitcompat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/gitmirror"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Git mirrors (design/35_git_mirrors.md): the server pushes a slice's
// projection to a repository elsewhere, such as GitHub.
//
// Each mirrored slice has a work repository beside the projections. It
// borrows the projection's objects (objects/info/alternates), holds the
// projection's head and tags under refs/mirror/, and fetches the remote's
// branch, so the remote's history never enters the projection or its covers.
// It is a cache: removing it costs one fetch of the remote branch.

// MirrorTarget is where, and as whom, a slice is pushed.
type MirrorTarget struct {
	URL    string
	Branch string
	// Username and Token authenticate over HTTPS (Basic). An empty token
	// pushes without credentials.
	Username string
	Token    string
	// Resolve pins the remote's host to an address checked beforehand, as
	// curl's --resolve ("host:port:address"), so DNS cannot change between
	// the check and the connection.
	Resolve string
	// AllowLocal permits non-https remotes, such as a path (tests and local
	// servers only).
	AllowLocal bool
}

// MirrorResult reports a push.
type MirrorResult struct {
	// Head is the mirror branch's head ("" when the slice has nothing to
	// push yet), and NativeCommitID the native commit it reflects.
	Head           string
	NativeCommitID string
	Exported       int
	NewTags        []string
	// Subdir is the folder that became the mirror's root, or "".
	Subdir string
}

// ErrMirrorDiverged is returned when the remote has commits or contents that
// did not come from the slice. Retrying does not help.
var ErrMirrorDiverged = gitmirror.ErrDiverged

// MirrorSubdir returns the folder of a slice that becomes a mirror's root:
// its one root folder, or "" when it has several (they keep full paths).
func MirrorSubdir(slice *corev1.Slice) (string, error) {
	prefixes, err := projectionPrefixes(slice)
	if err != nil {
		return "", err
	}
	if len(prefixes) == 1 && prefixes[0] != "/" {
		return strings.Trim(prefixes[0], "/"), nil
	}
	return "", nil
}

// PushMirror brings the slice's projection up to date and pushes its new
// commits and tags to the target.
func (p *Projector) PushMirror(ctx context.Context, account, sliceSlug string, target MirrorTarget) (*MirrorResult, error) {
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return nil, err
	}
	subdir, err := MirrorSubdir(slice)
	if err != nil {
		return nil, err
	}
	repoPath, projection, err := p.ensureResolved(ctx, account, sliceSlug, slice)
	if err != nil {
		return nil, err
	}
	work := filepath.Join(p.cacheRoot, ".mirrors", account, sliceSlug+".git")
	out, sourceHead, err := p.pushMirrorRepo(ctx, repoPath, work, subdir, target)
	if err != nil {
		return nil, err
	}
	if sourceHead != "" && projection != nil {
		out.NativeCommitID, _ = projection.NativeCommitFor(sourceHead)
	}
	return out, nil
}

// pushMirrorRepo pushes the projection at repoPath through the work
// repository, and returns the projected commit the mirror head reflects.
func (p *Projector) pushMirrorRepo(ctx context.Context, repoPath, work, subdir string, target MirrorTarget) (*MirrorResult, string, error) {
	lock := p.lockFor(work)
	lock.Lock()
	defer lock.Unlock()
	if err := prepareMirrorWork(ctx, work, repoPath); err != nil {
		_ = os.RemoveAll(work)
		return nil, "", err
	}
	env, redact := mirrorRemoteEnv(target)
	res, err := gitmirror.Run(ctx, gitmirror.Config{
		Dir:       work,
		Remote:    target.URL,
		Branch:    target.Branch,
		Subdir:    subdir,
		Push:      true,
		RemoteEnv: env,
		Redact:    redact,
		Hydrate: func(ctx context.Context, ids []string) error {
			if p.blobs == nil {
				return fmt.Errorf("%d file(s) are missing and the server has no blob store", len(ids))
			}
			// Hydration writes into the projection, as a fetch's would.
			hydrate := p.hydrateLock(repoPath)
			hydrate.Lock()
			defer hydrate.Unlock()
			return p.hydrate(ctx, repoPath, ids)
		},
	})
	if err != nil {
		if !errors.Is(err, gitmirror.ErrDiverged) {
			// Start from a clean work repository next time.
			_ = os.RemoveAll(work)
		}
		return nil, "", err
	}
	return &MirrorResult{Head: res.Head, Exported: len(res.Exported), NewTags: res.NewTags, Subdir: subdir}, res.SourceHead, nil
}

// prepareMirrorWork creates the work repository if needed and points its
// source refs at the projection's head and tags.
func prepareMirrorWork(ctx context.Context, work, repoPath string) error {
	if _, err := os.Stat(filepath.Join(work, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
			return err
		}
		if err := runGit(ctx, "", nil, "init", "--quiet", "--bare", work); err != nil {
			return err
		}
	}
	alternates := filepath.Join(work, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
		return err
	}
	objects, err := filepath.Abs(filepath.Join(repoPath, "objects"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(alternates, []byte(objects+"\n"), 0o644); err != nil {
		return err
	}

	source, err := gitOutput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, "for-each-ref", "--format=%(objectname) %(refname)", projectedBranch, "refs/tags/")
	if err != nil {
		return err
	}
	current, err := gitOutput(ctx, work, []string{"GIT_DIR=" + work}, "for-each-ref", "--format=%(refname)", "refs/mirror/main", gitmirror.SourceTagsRef)
	if err != nil {
		return err
	}
	want := map[string]string{}
	for _, line := range strings.Split(source, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == projectedBranch {
			want[gitmirror.SourceBranchRef] = fields[0]
		} else if name, ok := strings.CutPrefix(fields[1], "refs/tags/"); ok {
			want[gitmirror.SourceTagsRef+name] = fields[0]
		}
	}
	var script strings.Builder
	for _, ref := range strings.Fields(current) {
		if _, ok := want[ref]; !ok {
			fmt.Fprintf(&script, "delete %s\n", ref)
		}
	}
	for ref, id := range want {
		fmt.Fprintf(&script, "update %s %s\n", ref, id)
	}
	if script.Len() == 0 {
		return nil
	}
	_, err = gitOutputInput(ctx, work, []string{"GIT_DIR=" + work}, []byte(script.String()), "update-ref", "--stdin")
	return err
}

// mirrorRemoteEnv returns the environment for git commands that talk to the
// mirror, and a function that removes the token from error text. The token
// travels as configuration in the environment, never on a command line.
func mirrorRemoteEnv(target MirrorTarget) ([]string, func(string) string) {
	protocols := "https"
	if target.AllowLocal {
		protocols = "https:http:file"
	}
	config := [][2]string{{"http.followRedirects", "false"}, {"credential.helper", ""}}
	if target.Resolve != "" {
		config = append(config, [2]string{"http.curloptResolve", target.Resolve})
	}
	var secrets []string
	if target.Token != "" {
		user := target.Username
		if user == "" {
			user = "x-access-token"
		}
		basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + target.Token))
		config = append(config, [2]string{"http.extraHeader", "Authorization: Basic " + basic})
		secrets = append(secrets, target.Token, basic)
	}
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ALLOW_PROTOCOL=" + protocols,
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(config)),
	}
	for i, kv := range config {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	redact := func(s string) string {
		for _, secret := range secrets {
			s = strings.ReplaceAll(s, secret, "***")
		}
		return s
	}
	return env, redact
}

package checkexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

// A container check runs as the daemon's user, so it can read and write the
// tree the daemon materialized in a folder only that user may open. As root
// with every capability dropped it could do neither on rootful Docker.
func TestRunContainerReadsAndWritesItsTree(t *testing.T) {
	image := requireLocalImage(t, "busybox")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "marker", "ok")

	result, err := Run(context.Background(), root, checks.CheckSpec{
		Image: image,
		Run:   `test "$(cat marker)" = ok && mkdir -p out && touch out/written && test -w "$HOME" -o ! -e "$HOME"`,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("result = %#v, want passed", result)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "written")); err != nil {
		t.Fatalf("the check's write is not in the tree: %v", err)
	}
	// What the check wrote belongs to the daemon's user, who removes it.
	if err := os.RemoveAll(filepath.Join(root, "out")); err != nil {
		t.Fatalf("remove what the check wrote: %v", err)
	}
}

func TestRunContainerBuildsOnceAndReusesTheImage(t *testing.T) {
	base := requireLocalImage(t, "busybox")
	rt := DetectRuntime(context.Background())
	scope := "test-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { removeFamilyImages(t, rt, scope, "ci/Dockerfile") })

	tree := func(dep string) string {
		root := t.TempDir()
		writeTestFile(t, root, "ci/Dockerfile", "FROM "+base+"\nCOPY deps/ /deps/\n")
		writeTestFile(t, root, "deps/version.txt", dep)
		writeTestFile(t, root, "src/main.txt", "changes every run")
		return root
	}
	spec := checks.CheckSpec{
		Name:  "test",
		Scope: scope,
		Build: &checks.ImageBuild{
			ContextDir: "/",
			Dockerfile: "ci/Dockerfile",
			Inputs:     []string{"deps"},
		},
		Run: `cat /deps/version.txt`,
	}

	first, err := Run(context.Background(), tree("v1"), spec)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.Status != "passed" || first.Cached || !strings.Contains(first.Log, "[gitslice] built image") || !strings.HasSuffix(first.Log, "v1") {
		t.Fatalf("first result = %#v, want a passed run that built its image", first)
	}

	// Files outside the inputs change on every patchset and do not matter.
	second, err := Run(context.Background(), tree("v1"), spec)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.Status != "passed" || !second.Cached || second.SetupMs != 0 || !strings.Contains(second.Log, "[gitslice] using image") {
		t.Fatalf("second result = %#v, want the image reused", second)
	}

	// Changing an input builds a new image; only the newest two are kept.
	for _, version := range []string{"v2", "v3"} {
		result, err := Run(context.Background(), tree(version), spec)
		if err != nil {
			t.Fatalf("Run(%s) error = %v", version, err)
		}
		if result.Status != "passed" || result.Cached || !strings.HasSuffix(result.Log, version) {
			t.Fatalf("Run(%s) = %#v, want a fresh build", version, result)
		}
	}
	if got := familyImages(t, rt, scope, "ci/Dockerfile"); len(got) != keptBuiltImages {
		t.Fatalf("images kept = %v, want the newest %d", got, keptBuiltImages)
	}
}

func TestRunContainerBuildConcurrentChecksShareOneBuild(t *testing.T) {
	base := requireLocalImage(t, "busybox")
	rt := DetectRuntime(context.Background())
	scope := "test-concurrent-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { removeFamilyImages(t, rt, scope, "Dockerfile") })

	spec := checks.CheckSpec{
		Name:  "test",
		Scope: scope,
		Build: &checks.ImageBuild{ContextDir: "/", Dockerfile: "Dockerfile"},
		Run:   "true",
	}
	results := make(chan Result, 3)
	for i := 0; i < 3; i++ {
		root := t.TempDir()
		writeTestFile(t, root, "Dockerfile", "FROM "+base+"\nRUN echo "+scope+" > /built\n")
		go func() {
			result, err := Run(context.Background(), root, spec)
			if err != nil {
				result = Result{Status: "errored", Summary: err.Error()}
			}
			results <- result
		}()
	}
	built := 0
	for i := 0; i < 3; i++ {
		result := <-results
		if result.Status != "passed" {
			t.Fatalf("result = %#v, want passed", result)
		}
		if !result.Cached {
			built++
		}
	}
	if built != 1 {
		t.Fatalf("%d checks built the image, want 1", built)
	}
}

func TestRunContainerBuildMissingInputErrors(t *testing.T) {
	requireLocalImage(t, "busybox")
	root := t.TempDir()
	writeTestFile(t, root, "Dockerfile", "FROM busybox\n")
	result, err := Run(context.Background(), root, checks.CheckSpec{
		Build: &checks.ImageBuild{ContextDir: "/", Dockerfile: "Dockerfile", Inputs: []string{"go.sum"}},
		Run:   "true",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "errored" || !strings.Contains(result.Summary, "build input go.sum is not in the tree") {
		t.Fatalf("result = %#v, want a missing-input error", result)
	}
}

func TestRunContainerBuildFailureShowsBuildLog(t *testing.T) {
	requireLocalImage(t, "busybox")
	root := t.TempDir()
	writeTestFile(t, root, "Dockerfile", "FROM busybox\nRUN echo build-step-output && exit 3\n")
	result, err := Run(context.Background(), root, checks.CheckSpec{
		Scope: "test-fail-" + time.Now().Format("150405.000000"),
		Build: &checks.ImageBuild{ContextDir: "/", Dockerfile: "Dockerfile"},
		Run:   "true",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "errored" || !strings.HasPrefix(result.Summary, "image build failed") || !strings.Contains(result.Log, "build-step-output") {
		t.Fatalf("result = %#v, want a build failure with its log", result)
	}
}

func TestResolveBuiltImageKeysOnInputsOnly(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "web/ci/Dockerfile", "FROM x\n")
	writeTestFile(t, root, "web/package-lock.json", "{}")
	writeTestFile(t, root, "web/src/app.ts", "a")
	b := &checks.ImageBuild{ContextDir: "web", Dockerfile: "ci/Dockerfile", Inputs: []string{"package-lock.json"}}

	first, err := resolveBuiltImage(root, "s", b)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "web/src/app.ts", "b")
	same, err := resolveBuiltImage(root, "s", b)
	if err != nil {
		t.Fatal(err)
	}
	if same.Tag != first.Tag {
		t.Fatalf("tag changed with a file outside the inputs: %s -> %s", first.Tag, same.Tag)
	}
	writeTestFile(t, root, "web/package-lock.json", `{"v":2}`)
	changed, err := resolveBuiltImage(root, "s", b)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Tag == first.Tag || changed.Family != first.Family {
		t.Fatalf("tags %s -> %s, families %s -> %s; want a new tag in the same family", first.Tag, changed.Tag, first.Family, changed.Family)
	}
	other, err := resolveBuiltImage(root, "other-slice", b)
	if err != nil {
		t.Fatal(err)
	}
	if other.Family == changed.Family {
		t.Fatalf("two slices share the image family %s", other.Family)
	}
	if _, err := resolveBuiltImage(root, "s", &checks.ImageBuild{ContextDir: "web", Dockerfile: "../Dockerfile"}); err == nil {
		t.Fatal("a Dockerfile outside the context resolved")
	}
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func familyImages(t *testing.T, rt RuntimeInfo, scope, dockerfile string) []string {
	t.Helper()
	var out []string
	listed, err := exec.Command(rt.Path, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	family := builtImageFamily(scope, "/", dockerfile)
	for _, ref := range strings.Fields(string(listed)) {
		if strings.HasPrefix(ref, builtImageRepoPrefix+family+":") {
			out = append(out, ref)
		}
	}
	return out
}

func removeFamilyImages(t *testing.T, rt RuntimeInfo, scope, dockerfile string) {
	t.Helper()
	for _, ref := range familyImages(t, rt, scope, dockerfile) {
		_ = exec.Command(rt.Path, "image", "rm", "-f", ref).Run()
	}
}

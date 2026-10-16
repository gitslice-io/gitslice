package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func upsertEdits(paths ...string) []*corev1.FileEdit {
	edits := make([]*corev1.FileEdit, 0, len(paths))
	for _, p := range paths {
		edits = append(edits, &corev1.FileEdit{Op: "upsert", Path: p})
	}
	return edits
}

func baseWith(paths ...string) BaseSnapshot {
	files := map[string]BaseSnapshotFile{}
	for _, p := range paths {
		files[p] = BaseSnapshotFile{Path: p}
	}
	return BaseSnapshot{Files: files}
}

func manyFiles(dir string, n int) []string {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		paths = append(paths, fmt.Sprintf("%s/f%03d.txt", dir, i))
	}
	return paths
}

func TestNewDirAdditionsGroupsByShallowestNewDirectory(t *testing.T) {
	base := baseWith("/acme/app/main.go", "/acme/app/lib/a.go")
	edits := upsertEdits(
		"/acme/app/lib/b.go",       // existing directory
		"/acme/app/main.go",        // modified, not added
		"/acme/app/docs/a.md",      // new directory
		"/acme/app/docs/deep/b.md", // under the same new directory
		"/acme/app/tools/run.sh",
	)
	edits = append(edits, &corev1.FileEdit{Op: "delete", Path: "/acme/app/lib/a.go"})

	got := newDirAdditions(edits, base, []string{"/acme/app"})
	if len(got) != 2 {
		t.Fatalf("additions = %+v, want docs and tools", got)
	}
	if got[0].Path != "/acme/app/docs" || got[0].Files != 2 || got[0].RepeatsSlice {
		t.Fatalf("first addition = %+v", got[0])
	}
	if got[1].Path != "/acme/app/tools" || got[1].Files != 1 {
		t.Fatalf("second addition = %+v", got[1])
	}
}

func TestNewDirAdditionsIgnoresAFirstImport(t *testing.T) {
	if got := newDirAdditions(upsertEdits(manyFiles("/acme/app", 500)...), BaseSnapshot{}, []string{"/acme/app"}); got != nil {
		t.Fatalf("a base with no files has no new directories, got %+v", got)
	}
}

func TestNewDirAdditionsFlagsTheSlicePathRepeated(t *testing.T) {
	base := baseWith("/sunny/botparley/README.md")
	got := newDirAdditions(upsertEdits(manyFiles("/sunny/botparley/sunny/botparley/src", 3)...), base, []string{"/sunny/botparley"})
	if len(got) != 1 || got[0].Path != "/sunny/botparley/sunny" || got[0].Files != 3 || !got[0].RepeatsSlice {
		t.Fatalf("additions = %+v, want one repeated-path directory", got)
	}
	if repeatsSlicePath("/sunny/botparley/src/sunny.go", []string{"/sunny/botparley"}) {
		t.Fatal("a file named like the slice is not a repeat")
	}
}

func TestReviewEditsRefusesARepeatedSlicePathWithNoOneToAsk(t *testing.T) {
	var stderr bytes.Buffer
	r := Runner{Stdin: strings.NewReader(""), Stderr: &stderr}
	ws := WorkspaceConfig{IncludedPaths: []string{"/sunny/botparley"}}
	base := baseWith("/sunny/botparley/README.md")
	edits := upsertEdits(manyFiles("/sunny/botparley/sunny/botparley", 3)...)

	err := r.reviewEdits(commandOptions{}, ws, base, reviewOptions{})(edits)
	if !isUserErrorCode(err, "repeated_slice_path") {
		t.Fatalf("err = %v, want repeated_slice_path", err)
	}
	if !strings.Contains(stderr.String(), "/sunny/botparley/sunny: 3 file(s) added (repeats the slice path)") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if err := r.reviewEdits(commandOptions{}, ws, base, reviewOptions{yes: true})(edits); err != nil {
		t.Fatalf("--yes should go ahead: %v", err)
	}
}

func TestReviewEditsListsLargeAdditionsButOnlyAsksWhenSomeoneCanAnswer(t *testing.T) {
	var stderr bytes.Buffer
	r := Runner{Stdin: strings.NewReader(""), Stderr: &stderr}
	ws := WorkspaceConfig{IncludedPaths: []string{"/acme/app"}}
	base := baseWith("/acme/app/main.go")

	if err := r.reviewEdits(commandOptions{}, ws, base, reviewOptions{})(upsertEdits(manyFiles("/acme/app/vendor", 150)...)); err != nil {
		t.Fatalf("a large addition with nobody to ask must go ahead: %v", err)
	}
	if !strings.Contains(stderr.String(), "/acme/app/vendor: 150 file(s) added") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	stderr.Reset()
	if err := r.reviewEdits(commandOptions{}, ws, base, reviewOptions{})(upsertEdits(manyFiles("/acme/app/small", reviewMinNewDirFiles-1)...)); err != nil || stderr.Len() != 0 {
		t.Fatalf("a small addition is silent: err=%v stderr=%q", err, stderr.String())
	}

	stderr.Reset()
	if err := r.reviewEdits(commandOptions{Quiet: true}, ws, base, reviewOptions{})(upsertEdits(manyFiles("/acme/app/vendor", 150)...)); err != nil || stderr.Len() != 0 {
		t.Fatalf("--quiet prints nothing: err=%v stderr=%q", err, stderr.String())
	}
}

func TestAskToAddFiles(t *testing.T) {
	for _, tt := range []struct {
		answer string
		ok     bool
	}{{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}, {"", false}} {
		r := Runner{Stdin: strings.NewReader(tt.answer), Stderr: &bytes.Buffer{}}
		err := r.askToAddFiles()
		if tt.ok != (err == nil) {
			t.Fatalf("answer %q: err = %v, want ok=%t", tt.answer, err, tt.ok)
		}
		if err != nil && !isUserErrorCode(err, "cancelled") {
			t.Fatalf("answer %q: err = %v, want cancelled", tt.answer, err)
		}
	}
}

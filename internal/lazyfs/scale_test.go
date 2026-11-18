package lazyfs

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"
)

// TestRemoveScalesWithTheSubtree: deleting build output one file at a time
// (rm -rf node_modules) costs each file, not every entry in the layer. With a
// scan of the whole layer per removal, 20,000 removals took seconds; walking
// the subtree they take milliseconds.
func TestRemoveScalesWithTheSubtree(t *testing.T) {
	check := checkSubtreeWalk
	checkSubtreeWalk = false
	defer func() { checkSubtreeWalk = check }()
	content := newMemContent()
	base := newBase(t, content, "c1", map[string]baseFile{"/acme/app/main.go": {content: "package main\n"}})
	f := openFS(t, t.TempDir(), base, content)
	const dirs, files = 200, 100
	for d := 0; d < dirs; d++ {
		dir := fmt.Sprintf("/acme/app/node_modules/p%d", d)
		for _, d := range []string{"/acme/app/node_modules", dir} {
			if err := f.Mkdir(d); err != nil && !errors.Is(err, fs.ErrExist) {
				t.Fatal(err)
			}
		}
		for i := 0; i < files; i++ {
			if err := f.WriteFile(fmt.Sprintf("%s/f%d.js", dir, i), []byte("x"), ModeFile); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := time.Now()
	for d := 0; d < dirs; d++ {
		dir := fmt.Sprintf("/acme/app/node_modules/p%d", d)
		for i := 0; i < files; i++ {
			if err := f.Remove(fmt.Sprintf("%s/f%d.js", dir, i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Remove(dir); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("%d removals in %s (%s each)", dirs*files+dirs, elapsed, elapsed/time.Duration(dirs*files+dirs))
	if per := elapsed / time.Duration(dirs*files); per > 200*time.Microsecond {
		t.Fatalf("a removal takes %s; it should not grow with the layer", per)
	}
}

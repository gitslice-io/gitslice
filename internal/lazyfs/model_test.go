//go:build linux

package lazyfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The reference model: a real directory gets the same operations as an FS.
// After every step the two must agree on the error, the tree, and the edits a
// walk of the real directory would produce.

var modelNames = []string{"a", "b", "c"}

type modelOp struct {
	kind   string
	path   string
	path2  string
	data   string
	mode   uint32
	size   int64
	target string
}

func (op modelOp) String() string {
	switch op.kind {
	case "rename":
		return fmt.Sprintf("rename %s %s", op.path, op.path2)
	case "write":
		return fmt.Sprintf("write %s %q %o", op.path, op.data, op.mode)
	case "append":
		return fmt.Sprintf("append %s %q", op.path, op.data)
	case "symlink":
		return fmt.Sprintf("symlink %s -> %s", op.path, op.target)
	case "chmod":
		return fmt.Sprintf("chmod %s %o", op.path, op.mode)
	case "truncate":
		return fmt.Sprintf("truncate %s %d", op.path, op.size)
	}
	return op.kind + " " + op.path
}

func randomPath(rng *rand.Rand) string {
	depth := 1 + rng.Intn(3)
	parts := make([]string, depth)
	for i := range parts {
		parts[i] = modelNames[rng.Intn(len(modelNames))]
	}
	return "/" + strings.Join(parts, "/")
}

func randomOp(rng *rand.Rand) modelOp {
	op := modelOp{path: randomPath(rng)}
	modes := []uint32{0, ModeFile, ModeExecutable}
	switch n := rng.Intn(100); {
	case n < 22:
		op.kind, op.data, op.mode = "write", fmt.Sprintf("w%d", rng.Intn(4)), modes[rng.Intn(3)]
	case n < 32:
		op.kind, op.data = "append", fmt.Sprintf("+%d", rng.Intn(3))
	case n < 47:
		op.kind = "mkdir"
	case n < 62:
		op.kind = "remove"
	case n < 82:
		op.kind, op.path2 = "rename", randomPath(rng)
	case n < 87:
		op.kind, op.target = "symlink", modelNames[rng.Intn(len(modelNames))]
	case n < 93:
		op.kind, op.mode = "chmod", modes[1+rng.Intn(2)]
	default:
		op.kind, op.size = "truncate", int64(rng.Intn(3))
	}
	return op
}

// skip reports ops the model cannot mirror: a path through a symlink (the
// kernel resolves those before a FUSE file system sees them), and content
// operations on a symlink itself (they would follow it).
func skip(root string, op modelOp) bool {
	for _, p := range []string{op.path, op.path2} {
		if p == "" {
			continue
		}
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			if info, err := os.Lstat(filepath.Join(root, dir)); err == nil && info.Mode()&fs.ModeSymlink != 0 {
				return true
			}
		}
	}
	switch op.kind {
	case "write", "append", "chmod", "truncate":
		if info, err := os.Lstat(filepath.Join(root, op.path)); err == nil && (info.Mode()&fs.ModeSymlink != 0 || (op.kind == "chmod" && info.IsDir())) {
			return true
		}
	}
	return false
}

func perm(mode uint32) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func applyModel(root string, op modelOp) string {
	p := filepath.Join(root, op.path)
	var err error
	switch op.kind {
	case "write":
		if err = os.WriteFile(p, []byte(op.data), 0o644); err == nil && op.mode != 0 {
			err = os.Chmod(p, perm(op.mode))
		}
	case "append":
		var file *os.File
		if file, err = os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0); err == nil {
			_, err = file.WriteString(op.data)
			file.Close()
		}
	case "mkdir":
		err = os.Mkdir(p, 0o755)
	case "remove":
		err = os.Remove(p)
	case "rename":
		// rename(2), as the kernel calls it: os.Rename refuses to replace an
		// existing directory, even an empty one or the same path.
		if err = syscall.Rename(p, filepath.Join(root, op.path2)); err != nil {
			err = &os.LinkError{Op: "rename", Old: p, New: op.path2, Err: err}
		}
	case "symlink":
		err = os.Symlink(op.target, p)
	case "chmod":
		err = os.Chmod(p, perm(op.mode))
	case "truncate":
		err = os.Truncate(p, op.size)
	}
	return classify(err)
}

func applyFS(f *FS, op modelOp) string {
	ctx := context.Background()
	var err error
	switch op.kind {
	case "write":
		err = f.WriteFile(op.path, []byte(op.data), op.mode)
	case "append":
		var local string
		if local, err = f.OpenWritable(ctx, op.path, false); err == nil {
			var file *os.File
			if file, err = os.OpenFile(local, os.O_WRONLY|os.O_APPEND, 0); err == nil {
				_, err = file.WriteString(op.data)
				file.Close()
			}
		}
	case "mkdir":
		err = f.Mkdir(op.path)
	case "remove":
		err = f.Remove(op.path)
	case "rename":
		err = f.Rename(op.path, op.path2)
	case "symlink":
		err = f.Symlink(op.target, op.path)
	case "chmod":
		err = f.Chmod(op.path, op.mode)
	case "truncate":
		err = f.Truncate(ctx, op.path, op.size)
	}
	return classify(err)
}

// stressTrials returns LAZYFS_TRIALS when set (for long runs), else n.
func stressTrials(n int) int {
	if v, err := strconv.Atoi(os.Getenv("LAZYFS_TRIALS")); err == nil && v > 0 {
		return v
	}
	return n
}

func classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotEmpty), errors.Is(err, syscall.ENOTEMPTY):
		return "notempty"
	case errors.Is(err, ErrNotDir), errors.Is(err, syscall.ENOTDIR):
		return "notdir"
	case errors.Is(err, ErrIsDir), errors.Is(err, syscall.EISDIR):
		return "isdir"
	case errors.Is(err, fs.ErrNotExist):
		return "notexist"
	case errors.Is(err, fs.ErrExist):
		return "exist"
	case errors.Is(err, fs.ErrInvalid), errors.Is(err, syscall.EINVAL):
		return "invalid"
	}
	return "other: " + err.Error()
}

func modelTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel := "/" + filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "symlink " + target
		case info.IsDir():
			out[rel] = "dir"
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			mode := ModeFile
			if info.Mode()&0o111 != 0 {
				mode = ModeExecutable
			}
			out[rel] = fmt.Sprintf("file %o %s", mode, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// walkEdits is what a walk of the real directory would send: every file and
// symlink compared with the base.
func walkEdits(base *Manifest, model map[string]string) []string {
	type state struct {
		hash string
		mode uint32
	}
	now := map[string]state{}
	for p, desc := range model {
		switch {
		case strings.HasPrefix(desc, "symlink "):
			now[p] = state{hashOf([]byte(strings.TrimPrefix(desc, "symlink "))), ModeSymlink}
		case strings.HasPrefix(desc, "file "):
			var mode uint32
			var data string
			fields := strings.SplitN(desc, " ", 3)
			fmt.Sscanf(fields[1], "%o", &mode)
			if len(fields) == 3 {
				data = fields[2]
			}
			now[p] = state{hashOf([]byte(data)), mode}
		}
	}
	var out []string
	paths := map[string]bool{}
	for p := range now {
		paths[p] = true
	}
	for p, entry := range base.entries {
		if entry.Kind != KindDir {
			paths[p] = true
		}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		entry, inBase := base.Lookup(p)
		inBase = inBase && entry.Kind != KindDir
		cur, exists := now[p]
		switch {
		case !exists && inBase:
			out = append(out, "delete "+p)
		case exists && (!inBase || entry.ContentHash != cur.hash || entry.Mode != cur.mode):
			out = append(out, fmt.Sprintf("upsert %s %o %s", p, cur.mode, cur.hash))
		}
	}
	return out
}

// randomBase makes a base of up to eight files and a symlink, and the same
// tree in a real directory.
func randomBase(t *testing.T, rng *rand.Rand, content *memContent, root string) *Manifest {
	t.Helper()
	files := map[string]baseFile{}
	taken := map[string]string{} // path -> "file" | "dir"
	for i := 0; i < 8; i++ {
		p := randomPath(rng)
		ok := taken[p] == ""
		for dir := parentPath(p); ok && dir != "/"; dir = parentPath(dir) {
			ok = taken[dir] != "file"
		}
		if !ok {
			continue
		}
		file := baseFile{content: fmt.Sprintf("base%d", i), executable: rng.Intn(4) == 0}
		if i == 7 {
			file = baseFile{symlink: modelNames[rng.Intn(len(modelNames))]}
		}
		files[p] = file
		taken[p] = "file"
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			taken[dir] = "dir"
		}
	}
	for p, file := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if file.symlink != "" {
			if err := os.Symlink(file.symlink, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(file.content), perm(map[bool]uint32{true: ModeExecutable, false: ModeFile}[file.executable])); err != nil {
			t.Fatal(err)
		}
	}
	return newBase(t, content, "commit-base", files)
}

func TestFSMatchesARealDirectory(t *testing.T) {
	trials, steps := stressTrials(300), 40
	if testing.Short() {
		trials = 40
	}
	for trial := 0; trial < trials; trial++ {
		rng := rand.New(rand.NewSource(int64(trial)))
		content := newMemContent()
		root := t.TempDir()
		base := randomBase(t, rng, content, root)
		dir := t.TempDir()
		f := openFS(t, dir, base, content)
		var log []string
		for step := 0; step < steps; step++ {
			op := randomOp(rng)
			if skip(root, op) {
				continue
			}
			log = append(log, op.String())
			want := applyModel(root, op)
			got := applyFS(f, op)
			if want != got {
				t.Fatalf("trial %d: %s: real directory %s, lazyfs %s\nops:\n  %s", trial, op, want, got, strings.Join(log, "\n  "))
			}
			model := modelTree(t, root)
			if d := diffTrees(model, tree(t, f)); d != "" {
				t.Fatalf("trial %d: after %s the trees differ:\n%sops:\n  %s", trial, op, d, strings.Join(log, "\n  "))
			}
			edits, err := f.Edits()
			if err != nil {
				t.Fatal(err)
			}
			if want, got := strings.Join(walkEdits(base, model), "\n"), strings.Join(editStrings(edits), "\n"); want != got {
				t.Fatalf("trial %d: after %s the edits differ:\nwalk:\n%s\nlazyfs:\n%s\nops:\n  %s", trial, op, want, got, strings.Join(log, "\n  "))
			}
			if step%10 == 9 {
				// The journal alone must bring the layer back.
				_ = f.Close()
				f = openFS(t, dir, base, content)
				if d := diffTrees(model, tree(t, f)); d != "" {
					t.Fatalf("trial %d: reopened tree differs:\n%sops:\n  %s", trial, d, strings.Join(log, "\n  "))
				}
			}
		}
	}
}

// TestFSSurvivesACrashAtEveryStep stops one operation at each point where a
// process could die, reopens the layer, and expects the state before the
// operation or after it: before, if the journal record was not complete;
// after, once it was.
func TestFSSurvivesACrashAtEveryStep(t *testing.T) {
	points := []string{"content-written", "journal-half", "journal-written", "applied"}
	trials := stressTrials(400)
	if testing.Short() {
		trials = 60
	}
	for trial := 0; trial < trials; trial++ {
		rng := rand.New(rand.NewSource(int64(1000 + trial)))
		content := newMemContent()
		root := t.TempDir()
		base := randomBase(t, rng, content, root)
		dir := t.TempDir()
		f := openFS(t, dir, base, content)
		for step := 0; step < rng.Intn(15); step++ {
			op := randomOp(rng)
			if skip(root, op) {
				continue
			}
			applyModel(root, op)
			applyFS(f, op)
		}
		var op modelOp
		for op = randomOp(rng); skip(root, op); op = randomOp(rng) {
		}
		before := modelTree(t, root)
		applyModel(root, op)
		after := modelTree(t, root)

		point := points[trial%len(points)]
		reached := false
		f.hook = func(at string) error {
			if at == point && !reached {
				reached = true
				return errors.New("crash")
			}
			return nil
		}
		applyFS(f, op)
		_ = f.Close()

		reopened := openFS(t, dir, base, content)
		got := tree(t, reopened)
		want := after
		if reached && (point == "content-written" || point == "journal-half") {
			want = before
		}
		// append and truncate copy the file up first and then write it, so a
		// crash between the two leaves the old content: either state is right.
		twoStep := reached && (op.kind == "append" || op.kind == "truncate")
		if twoStep && diffTrees(before, got) == "" {
			want = before
		}
		if d := diffTrees(want, got); d != "" {
			t.Fatalf("trial %d: crash at %s (reached %t) during %s:\n%s", trial, point, reached, op, d)
		}
		files, err := os.ReadDir(filepath.Join(dir, filesDir))
		if err != nil {
			t.Fatal(err)
		}
		referenced := 0
		for _, entry := range reopened.entries {
			if entry.Kind == entryFile {
				referenced++
			}
		}
		if len(files) != referenced {
			t.Fatalf("trial %d: %d local files, %d referenced: garbage was left", trial, len(files), referenced)
		}
	}
}

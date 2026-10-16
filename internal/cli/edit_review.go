package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// An import adds many files under a directory the base lacks, and that is
// usually what the author meant. A copy of the slice nested inside itself
// looks the same and is a mistake that splits the slice in two. So before a
// changeset uploads anything, gs create says where new directories got files,
// and asks (or, with nobody to ask, refuses) when the additions look wrong.
const (
	// reviewMinNewDirFiles is how many added files a new directory needs
	// before gs create mentions it.
	reviewMinNewDirFiles = 20
	// reviewConfirmNewDirFiles is how many added files under one new
	// directory make gs create ask before it uploads them.
	reviewConfirmNewDirFiles = 100
)

// editReview looks at a workspace's edits before they are uploaded, and
// returns an error to stop the upload.
type editReview func(edits []*corev1.FileEdit) error

type reviewOptions struct {
	// yes skips the question about large or repeated path additions.
	yes bool
}

func (o *reviewOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.yes, "yes", false, "create the changeset without asking about large or repeated path additions")
}

// newDirAddition is a directory that did not exist at the base and now holds
// added files.
type newDirAddition struct {
	Path  string
	Files int
	// RepeatsSlice is set when some of the files sit under their own slice's
	// path twice over (/acme/app/acme/app/...).
	RepeatsSlice bool
}

// newDirAdditions groups the added files by the shallowest directory that is
// new. A base with no files (a first import) has no new directories to report.
func newDirAdditions(edits []*corev1.FileEdit, base BaseSnapshot, includedPaths []string) []newDirAddition {
	if len(base.Files) == 0 {
		return nil
	}
	baseDirs := map[string]bool{}
	for p := range base.Files {
		for dir := parentPath(p); dir != "" && !baseDirs[dir]; dir = parentPath(dir) {
			baseDirs[dir] = true
		}
	}
	byDir := map[string]*newDirAddition{}
	for _, edit := range edits {
		if edit.Op != "upsert" {
			continue
		}
		if _, exists := base.Files[edit.Path]; exists {
			continue
		}
		dir := shallowestNewDir(edit.Path, baseDirs)
		if dir == "" {
			continue
		}
		add := byDir[dir]
		if add == nil {
			add = &newDirAddition{Path: dir}
			byDir[dir] = add
		}
		add.Files++
		if repeatsSlicePath(edit.Path, includedPaths) {
			add.RepeatsSlice = true
		}
	}
	out := make([]newDirAddition, 0, len(byDir))
	for _, add := range byDir {
		out = append(out, *add)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// parentPath returns the directory of a global path, or "" at the top.
func parentPath(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return ""
	}
	return p[:idx]
}

// shallowestNewDir returns the topmost ancestor of p that is not in baseDirs,
// or "" when the file's directory already exists.
func shallowestNewDir(p string, baseDirs map[string]bool) string {
	var dirs []string
	for dir := parentPath(p); dir != ""; dir = parentPath(dir) {
		if baseDirs[dir] {
			break
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return ""
	}
	return dirs[len(dirs)-1]
}

// repeatsSlicePath reports whether p sits under one of the included paths
// twice in a row: /acme/app/acme/app/main.go for the path /acme/app.
func repeatsSlicePath(p string, includedPaths []string) bool {
	for _, inc := range includedPaths {
		inc = strings.TrimSuffix(inc, "/")
		if inc != "" && strings.HasPrefix(p, inc+inc+"/") {
			return true
		}
	}
	return false
}

// reviewEdits returns the review gs create runs before it uploads.
func (r Runner) reviewEdits(opts commandOptions, ws WorkspaceConfig, base BaseSnapshot, review reviewOptions) editReview {
	return func(edits []*corev1.FileEdit) error {
		adds := newDirAdditions(edits, base, ws.IncludedPaths)
		shown, ask, repeats := 0, false, false
		for _, add := range adds {
			if add.Files < reviewMinNewDirFiles && !add.RepeatsSlice {
				continue
			}
			shown++
			if shown == 1 && !opts.Quiet {
				fmt.Fprintln(r.stderr(), "new directories with added files:")
			}
			if !opts.Quiet {
				note := ""
				if add.RepeatsSlice {
					note = " (repeats the slice path)"
				}
				fmt.Fprintf(r.stderr(), "  %s: %d file(s) added%s\n", add.Path, add.Files, note)
			}
			if add.RepeatsSlice {
				repeats = true
			}
			if add.Files >= reviewConfirmNewDirFiles {
				ask = true
			}
		}
		if (!ask && !repeats) || review.yes {
			return nil
		}
		if opts.Quiet || opts.NonInteractive || os.Getenv("TERM") == "dumb" || !isTerminal(r.stdin()) {
			if !repeats {
				return nil
			}
			return userError("repeated_slice_path",
				"files would be added under the slice's own path a second time, which splits the slice in two",
				"Look for a copied or nested workspace directory and remove it. To add these paths anyway, run again with --yes.")
		}
		return r.askToAddFiles()
	}
}

// askToAddFiles asks whether to go ahead with the files listed above.
func (r Runner) askToAddFiles() error {
	fmt.Fprint(r.stderr(), "Add these files? [y/N] ")
	line, err := bufio.NewReader(r.stdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return userError("cancelled", "no changeset was created", "Remove the files you did not mean to add, or run again with --yes.")
}

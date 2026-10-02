package cli

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// workspaceIgnore decides which untracked paths a workspace scan skips. It
// reads .gitignore files the way Git does:
//
//   - A file's patterns apply below its own directory.
//   - Within a file, a later match overrides an earlier one.
//   - A deeper file overrides a shallower one.
//   - Nothing below an ignored directory can be re-included.
//
// Tracked paths, those in the base snapshot, are never skipped. Ignoring a file
// that is already in the slice therefore cannot turn it into a delete. Build
// output such as node_modules stays out of status, changesets and sync.
type workspaceIgnore struct {
	root        string
	rules       map[string][]ignoreRule // by workspace-relative directory, "" for the root
	dirIgnored  map[string]bool         // memoized directory decisions
	tracked     map[string]struct{}     // workspace-relative tracked files
	trackedDirs map[string]struct{}     // workspace-relative ancestors of tracked files
}

type ignoreRule struct {
	pattern string // doublestar pattern relative to the .gitignore's directory
	negate  bool
	dirOnly bool
}

// newWorkspaceIgnore builds the matcher for a workspace rooted at root.
// tracked holds the base snapshot entries, keyed by global path.
func newWorkspaceIgnore(root string, ws WorkspaceConfig, tracked ...map[string]BaseSnapshotFile) *workspaceIgnore {
	ig := &workspaceIgnore{
		root:        root,
		rules:       map[string][]ignoreRule{},
		dirIgnored:  map[string]bool{},
		tracked:     map[string]struct{}{},
		trackedDirs: map[string]struct{}{},
	}
	for _, files := range tracked {
		for globalPath, file := range files {
			rel := file.RelPath
			if rel == "" {
				var err error
				if rel, err = workspaceRelPath(ws, globalPath); err != nil {
					continue
				}
			}
			rel = filepath.ToSlash(rel)
			ig.tracked[rel] = struct{}{}
			for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
				ig.trackedDirs[dir] = struct{}{}
			}
		}
	}
	return ig
}

// skip reports whether the scan should leave out rel, a workspace-relative
// slash path. Directories are skipped whole only when no tracked file lies
// beneath them.
func (ig *workspaceIgnore) skip(rel string, isDir bool) bool {
	if ig == nil {
		return false
	}
	if isDir {
		if _, ok := ig.trackedDirs[rel]; ok {
			return false
		}
		return ig.directoryIgnored(rel)
	}
	if _, ok := ig.tracked[rel]; ok {
		return false
	}
	return ig.ancestorIgnored(rel) || ig.matches(rel, false)
}

func (ig *workspaceIgnore) directoryIgnored(dir string) bool {
	if ignored, ok := ig.dirIgnored[dir]; ok {
		return ignored
	}
	ignored := ig.ancestorIgnored(dir) || ig.matches(dir, true)
	ig.dirIgnored[dir] = ignored
	return ignored
}

func (ig *workspaceIgnore) ancestorIgnored(rel string) bool {
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if ig.directoryIgnored(dir) {
			return true
		}
	}
	return false
}

// matches evaluates the patterns of every .gitignore from the workspace root
// down to rel's parent directory. The last matching pattern decides.
func (ig *workspaceIgnore) matches(rel string, isDir bool) bool {
	ignored := false
	var dirs []string // deepest first; iterated backwards from the root
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	dirs = append(dirs, "")
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		sub := rel
		if dir != "" {
			sub = strings.TrimPrefix(rel, dir+"/")
		}
		for _, rule := range ig.rulesFor(dir) {
			if rule.dirOnly && !isDir {
				continue
			}
			if ok, _ := doublestar.Match(rule.pattern, sub); ok {
				ignored = !rule.negate
			}
		}
	}
	return ignored
}

func (ig *workspaceIgnore) rulesFor(dir string) []ignoreRule {
	if rules, ok := ig.rules[dir]; ok {
		return rules
	}
	// A missing or unreadable .gitignore contributes no rules.
	data, _ := os.ReadFile(filepath.Join(ig.root, filepath.FromSlash(dir), ".gitignore"))
	rules := parseGitignore(data)
	ig.rules[dir] = rules
	return rules
}

// parseGitignore turns .gitignore content into doublestar rules. It covers
// comments, negation, directory-only and anchored patterns, and "**"; Git's
// escapes for a leading "#" or "!" and for trailing spaces are kept literal.
func parseGitignore(data []byte) []ignoreRule {
	var rules []ignoreRule
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !strings.HasSuffix(line, `\ `) {
			line = strings.TrimRight(line, " \t")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		// A slash at the start or in the middle anchors the pattern to the
		// .gitignore's directory; otherwise it matches at any depth.
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		// Git has no brace alternation, so braces stay literal.
		line = strings.NewReplacer("{", `\{`, "}", `\}`).Replace(line)
		if !anchored && !strings.HasPrefix(line, "**/") {
			line = "**/" + line
		}
		if !doublestar.ValidatePattern(line) {
			continue
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules
}

// Package mergerules reads .gitslice/merge.yaml: how a path both a change
// and the new base changed is merged when a line merge would leave
// conflict markers (fb_3fc4; design/07_conflict_resolution.md).
//
//	version: 1
//	rules:
//	  - paths: ["go.sum", "**/go.sum"]
//	    merge: union
//	  - paths: ["**/package-lock.json", "**/*.pb.go"]
//	    merge: newest
//
// Patterns are doublestar globs relative to the folder that holds .gitslice.
// The file nearest the path wins, and in it the first rule that matches.
// Rules are always read from the new base, which has landed, so a change
// cannot set the rules of its own merge.
package mergerules

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Strategy is how a conflicting path is resolved.
type Strategy string

const (
	// Union keeps the lines either side added and drops the lines either
	// side removed (go.sum, other line sets).
	Union Strategy = "union"
	// Newest takes the new base's version: the one that landed last
	// (lockfiles, generated files).
	Newest Strategy = "newest"
	// Ours keeps the change's version.
	Ours Strategy = "ours"
)

// ConfigName is the rules file inside a folder's .gitslice directory.
const ConfigName = ".gitslice/merge.yaml"

// Rule is one entry of a rules file.
type Rule struct {
	Paths    []string
	Strategy Strategy
}

// File is a parsed rules file, for the folder Dir.
type File struct {
	Dir   string
	Rules []Rule
}

type rawFile struct {
	Version int `yaml:"version"`
	Rules   []struct {
		Paths []string `yaml:"paths"`
		Merge string   `yaml:"merge"`
	} `yaml:"rules"`
}

// Parse reads the rules file of folder dir (an absolute slice path such as
// /acme/app).
func Parse(dir string, data []byte) (*File, error) {
	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", strings.TrimSuffix(dir, "/"), ConfigName, err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("%s/%s: version must be 1", strings.TrimSuffix(dir, "/"), ConfigName)
	}
	out := &File{Dir: dir}
	for i, r := range raw.Rules {
		if len(r.Paths) == 0 {
			return nil, fmt.Errorf("rule %d: paths is required", i+1)
		}
		for _, p := range r.Paths {
			if !doublestar.ValidatePattern(p) || strings.HasPrefix(p, "/") {
				return nil, fmt.Errorf("rule %d: invalid pattern %q (relative globs such as **/go.sum)", i+1, p)
			}
		}
		s := Strategy(strings.TrimSpace(r.Merge))
		switch {
		case s == Union || s == Newest || s == Ours:
		case strings.HasPrefix(string(s), "regenerate"):
			return nil, fmt.Errorf("rule %d: merge %q is not supported yet: nothing can write a check's output back to a change; use newest and let the check fail when the file is stale", i+1, r.Merge)
		default:
			return nil, fmt.Errorf("rule %d: merge must be union, newest or ours, not %q", i+1, r.Merge)
		}
		out.Rules = append(out.Rules, Rule{Paths: r.Paths, Strategy: s})
	}
	return out, nil
}

// Match returns the strategy of the first rule matching p (an absolute
// slice path inside f.Dir).
func (f *File) Match(p string) (Strategy, bool) {
	dir := strings.TrimSuffix(f.Dir, "/") + "/"
	if !strings.HasPrefix(p, dir) {
		return "", false
	}
	rel := strings.TrimPrefix(p, dir)
	for _, rule := range f.Rules {
		for _, pattern := range rule.Paths {
			if ok, _ := doublestar.Match(pattern, rel); ok {
				return rule.Strategy, true
			}
		}
	}
	return "", false
}

// ConfigPaths are the rules files that could apply to p, nearest first.
func ConfigPaths(p string) []string {
	var out []string
	dir := path.Dir(path.Clean("/" + strings.TrimPrefix(p, "/")))
	for {
		out = append(out, path.Join(dir, ConfigName))
		if dir == "/" {
			return out
		}
		dir = path.Dir(dir)
	}
}

// Lookup finds the strategy for p. read returns a rules file's content at
// the new base (ok false when there is none). The nearest file that exists
// decides; a path it has no rule for has none.
func Lookup(p string, read func(configPath string) (data []byte, ok bool, err error)) (Strategy, bool, error) {
	for _, cfg := range ConfigPaths(p) {
		data, ok, err := read(cfg)
		if err != nil {
			return "", false, err
		}
		if !ok {
			continue
		}
		f, err := Parse(strings.TrimSuffix(cfg, "/"+ConfigName), data)
		if err != nil {
			return "", false, err
		}
		s, matched := f.Match(p)
		return s, matched, nil
	}
	return "", false, nil
}

// MergeUnion merges three versions of a line set: the new base's lines in
// its order, without the lines the change removed, followed by the lines the
// change added that the new base does not have.
func MergeUnion(base, ours, theirs []byte) []byte {
	baseLines, oursLines, theirsLines := splitLines(base), splitLines(ours), splitLines(theirs)
	inBase := set(baseLines)
	inOurs := set(oursLines)
	inTheirs := set(theirsLines)
	var out []string
	seen := map[string]bool{}
	for _, l := range theirsLines {
		if inBase[l] && !inOurs[l] {
			continue // removed by the change
		}
		if !seen[l] {
			out = append(out, l)
			seen[l] = true
		}
	}
	for _, l := range oursLines {
		if !inBase[l] && !inTheirs[l] && !seen[l] {
			out = append(out, l)
			seen[l] = true
		}
	}
	if len(out) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func splitLines(data []byte) []string {
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func set(lines []string) map[string]bool {
	out := make(map[string]bool, len(lines))
	for _, l := range lines {
		out[l] = true
	}
	return out
}

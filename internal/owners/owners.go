// Package owners reads .gitslice/owners.yaml: path-level review ownership
// inside an account or slice (design/36_multi_agent_collaboration.md section 5).
//
//	version: 1
//	owners:
//	  - paths: ["api/**"]
//	    owners: ["@acme/api-agents", "alice"]
//	  - paths: ["web/**"]
//	    owners: ["web-agent", "bob"]
//	    require: 1
//
// Patterns are doublestar globs relative to the folder that holds .gitslice.
// The nearest file above a path decides, and in it the first rule that matches.
// Rules are always read from the target ref's landed commit tree, so a change
// cannot set the rules of its own review.
package owners

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gopkg.in/yaml.v3"
)

// ConfigName is the owners file inside a folder's .gitslice directory.
const ConfigName = ".gitslice/owners.yaml"

// Rule is one entry of an owners file.
type Rule struct {
	Paths   []string
	Owners  []string
	Require int
}

// File is a parsed owners file, for the folder Dir (an absolute slice folder
// path such as /acme/app or /acme).
type File struct {
	Dir   string
	Rules []Rule
}

// MatchResult identifies the config file and rule within it that matched a path.
type MatchResult struct {
	ConfigPath string
	RuleIndex  int
	Rule       Rule
}

type rawFile struct {
	Version int `yaml:"version"`
	Owners  []struct {
		Paths   []string `yaml:"paths"`
		Owners  []string `yaml:"owners"`
		Require *int     `yaml:"require"`
	} `yaml:"owners"`
}

// Parse reads the owners file of folder dir (an absolute folder path such as
// /acme/app or /acme).
func Parse(dir string, data []byte) (*File, error) {
	cleanDir := normalizeDir(dir)
	cfgLabel := strings.TrimSuffix(cleanDir, "/") + "/" + ConfigName

	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", cfgLabel, err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("%s: version must be 1", cfgLabel)
	}

	out := &File{Dir: cleanDir}
	for i, r := range raw.Owners {
		if len(r.Paths) == 0 {
			return nil, fmt.Errorf("%s: rule %d: paths is required", cfgLabel, i+1)
		}
		cleanPaths := make([]string, 0, len(r.Paths))
		for _, rawPattern := range r.Paths {
			p := strings.TrimSpace(rawPattern)
			if p == "" {
				return nil, fmt.Errorf("%s: rule %d: paths must not contain empty patterns", cfgLabel, i+1)
			}
			if strings.ContainsAny(p, "\r\n\x00") {
				return nil, fmt.Errorf("%s: rule %d: pattern %q contains control characters", cfgLabel, i+1, rawPattern)
			}
			if strings.HasPrefix(p, "/") {
				return nil, fmt.Errorf("%s: rule %d: invalid pattern %q (must be relative, without leading /)", cfgLabel, i+1, rawPattern)
			}
			if strings.Contains(p, "..") {
				return nil, fmt.Errorf("%s: rule %d: invalid pattern %q (must not contain ..)", cfgLabel, i+1, rawPattern)
			}
			if !doublestar.ValidatePattern(p) {
				return nil, fmt.Errorf("%s: rule %d: invalid pattern %q", cfgLabel, i+1, rawPattern)
			}
			cleanPaths = append(cleanPaths, p)
		}

		if len(r.Owners) == 0 {
			return nil, fmt.Errorf("%s: rule %d: owners is required", cfgLabel, i+1)
		}
		cleanOwners := make([]string, 0, len(r.Owners))
		seenOwners := make(map[string]struct{}, len(r.Owners))
		for _, rawOwner := range r.Owners {
			o := strings.ToLower(strings.TrimSpace(rawOwner))
			if o == "" {
				return nil, fmt.Errorf("%s: rule %d: owners must not contain empty entries", cfgLabel, i+1)
			}
			if _, ok := seenOwners[o]; ok {
				continue
			}
			seenOwners[o] = struct{}{}
			cleanOwners = append(cleanOwners, o)
		}

		req := 1
		if r.Require != nil {
			if *r.Require < 0 {
				return nil, fmt.Errorf("%s: rule %d: require must not be negative", cfgLabel, i+1)
			}
			req = *r.Require
		}

		out.Rules = append(out.Rules, Rule{
			Paths:   cleanPaths,
			Owners:  cleanOwners,
			Require: req,
		})
	}
	return out, nil
}

// Match returns the first rule matching p (an absolute path inside f.Dir).
func (f *File) Match(p string) (Rule, bool) {
	rule, _, ok := f.MatchIndex(p)
	return rule, ok
}

// MatchIndex returns the first rule matching p and its zero-based index in
// f.Rules.
func (f *File) MatchIndex(p string) (Rule, int, bool) {
	if f == nil {
		return Rule{}, -1, false
	}
	cleaned, ok := normalizeFilePath(p)
	if !ok {
		return Rule{}, -1, false
	}
	dir := strings.TrimSuffix(normalizeDir(f.Dir), "/") + "/"
	if !strings.HasPrefix(cleaned, dir) {
		return Rule{}, -1, false
	}
	rel := strings.TrimPrefix(cleaned, dir)
	if rel == "" {
		return Rule{}, -1, false
	}
	for idx, rule := range f.Rules {
		for _, pattern := range rule.Paths {
			if matched, _ := doublestar.Match(pattern, rel); matched {
				return rule, idx, true
			}
		}
	}
	return Rule{}, -1, false
}

// ConfigPaths returns the owners config files that could apply to filePath,
// nearest first, walking up to /<account>/.gitslice/owners.yaml.
func ConfigPaths(filePath string) []string {
	return gitslicecfg.ConfigPaths("", filePath, ConfigName)
}

// NewResolver creates a memoized Resolver[*File] for .gitslice/owners.yaml.
func NewResolver(read gitslicecfg.ReadConfigFunc) *gitslicecfg.Resolver[*File] {
	return gitslicecfg.NewResolver(ConfigName, read, Parse)
}

// LookupMatchWithResolver finds the nearest owners file in treeID via resolver
// and returns the matching rule within it (if any).
func LookupMatchWithResolver(ctx context.Context, resolver *gitslicecfg.Resolver[*File], treeID, account, p string) (MatchResult, bool, error) {
	cfgPath, f, exists, err := resolver.Nearest(ctx, treeID, account, p)
	if err != nil || !exists {
		return MatchResult{}, false, err
	}
	rule, idx, matched := f.MatchIndex(p)
	if !matched {
		return MatchResult{}, false, nil
	}
	return MatchResult{
		ConfigPath: cfgPath,
		RuleIndex:  idx,
		Rule:       rule,
	}, true, nil
}

// Lookup finds the owner rule for p. read returns an owners file's content at
// the landed base (ok false when there is none). The nearest file that exists
// decides; a path it has no rule for has none.
func Lookup(p string, read func(configPath string) (data []byte, ok bool, err error)) (Rule, bool, error) {
	m, ok, err := LookupMatch(p, read)
	if err != nil || !ok {
		return Rule{}, false, err
	}
	return m.Rule, true, nil
}

// LookupMatch is like Lookup, returning the config path and rule index of the
// matching rule.
func LookupMatch(p string, read func(configPath string) (data []byte, ok bool, err error)) (MatchResult, bool, error) {
	resolver := NewResolver(func(_ context.Context, _, cfg string) ([]byte, bool, error) {
		return read(cfg)
	})
	return LookupMatchWithResolver(context.Background(), resolver, "", "", p)
}

// ParseGroupRef parses an owner entry of the form "@account/name". It returns
// ("", "", false) when owner is a plain username or malformed.
func ParseGroupRef(owner string) (account, name string, ok bool) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if !strings.HasPrefix(owner, "@") {
		return "", "", false
	}
	rest := strings.TrimPrefix(owner, "@")
	account, name, found := strings.Cut(rest, "/")
	if !found || account == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return account, name, true
}

func normalizeDir(dir string) string {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	if dir == "" || dir == "/" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(dir, "/"))
}

func normalizeFilePath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "", false
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if cleaned == "/" || cleaned == "." {
		return "", false
	}
	return cleaned, true
}

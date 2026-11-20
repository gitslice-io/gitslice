package automations

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var moduleImport = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(?\s*)["'](\.{1,2}/[^"']+)["']`)

// LoadModules reads a worker script and every relative ES module it imports,
// confined inside dotGitslice, enforcing module count and total byte limits.
func LoadModules(ctx context.Context, tree TreeReader, rootTreeID, dotGitslice, entry string, maxModules, maxBytes int) (map[string]string, error) {
	modules := map[string]string{}
	total := 0
	queue := []string{entry}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		if _, done := modules[rel]; done {
			continue
		}
		if len(modules) >= maxModules {
			return nil, fmt.Errorf("the script imports more than %d modules", maxModules)
		}
		data, err := tree.ReadFile(ctx, rootTreeID, dotGitslice+"/"+rel)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("script module %s/%s does not exist", dotGitslice, rel)
		}
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxBytes {
			return nil, fmt.Errorf("the script's modules are larger than %d KiB", maxBytes>>10)
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("script module %s is not UTF-8", rel)
		}
		modules[rel] = string(data)
		for _, m := range moduleImport.FindAllStringSubmatch(StripJSComments(string(data)), -1) {
			target := path.Clean(path.Join(path.Dir(rel), m[1]))
			if target == ".." || strings.HasPrefix(target, "../") {
				return nil, fmt.Errorf("%s imports %s, outside the .gitslice folder", rel, m[1])
			}
			if path.Ext(target) == "" {
				target += ".js"
			}
			queue = append(queue, target)
		}
	}
	return modules, nil
}

// StripJSComments replaces JS line and block comments with whitespace while
// preserving string/template literals and line breaks.
func StripJSComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); {
		c := src[i]
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			b.WriteByte(c)
			i++
			for i < len(src) {
				ch := src[i]
				b.WriteByte(ch)
				i++
				if ch == '\\' && i < len(src) {
					b.WriteByte(src[i])
					i++
					continue
				}
				if ch == quote {
					break
				}
			}
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			b.WriteByte(' ')
			i += 2
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			b.WriteByte(' ')
			i += 2
			for i < len(src) {
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					i += 2
					break
				}
				if src[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

package intents

import (
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// patternVariants returns the segment-list representations of pattern.
// Non-glob patterns represent both the exact path and everything underneath it
// ("<pattern>/**") so directory intents intersect properly with glob intents.
// Simple top-level brace alternations are expanded up to a bounded limit.
func patternVariants(pattern string) [][]string {
	expanded := expandBraces(pattern, 32)
	var out [][]string
	for _, exp := range expanded {
		segs := splitSegs(exp)
		out = append(out, segs)
		if !hasGlob(exp) {
			withWildcard := slices.Concat(segs, []string{"**"})
			out = append(out, withWildcard)
		}
	}
	return out
}

func splitSegs(p string) []string {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func expandBraces(p string, limit int) []string {
	results := []string{p}
	for {
		expandedAny := false
		var next []string
		for _, cur := range results {
			start, end, alts, ok := findInnermostBrace(cur)
			if !ok {
				next = append(next, cur)
				continue
			}
			expandedAny = true
			for _, alt := range alts {
				next = append(next, cur[:start]+alt+cur[end+1:])
				if len(next) >= limit {
					return next
				}
			}
		}
		results = next
		if !expandedAny || len(results) >= limit {
			return results
		}
	}
}

func findInnermostBrace(s string) (int, int, []string, bool) {
	openIdx := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			openIdx = i
		case '}':
			if openIdx >= 0 {
				inner := s[openIdx+1 : i]
				return openIdx, i, strings.Split(inner, ","), true
			}
		}
	}
	return 0, 0, nil, false
}

func segListsOverlap(a, b []string) bool {
	type key struct{ i, j int }
	memo := make(map[key]bool)
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		if i == len(a) && j == len(b) {
			return true
		}
		if i < len(a) && a[i] == "**" && j == len(b) {
			return rec(i+1, j)
		}
		if j < len(b) && b[j] == "**" && i == len(a) {
			return rec(i, j+1)
		}
		if i == len(a) || j == len(b) {
			return false
		}

		k := key{i, j}
		if v, seen := memo[k]; seen {
			return v
		}
		var ok bool
		switch {
		case a[i] == "**" && b[j] == "**":
			ok = rec(i+1, j) || rec(i, j+1)
		case a[i] == "**":
			ok = rec(i+1, j) || rec(i, j+1)
		case b[j] == "**":
			ok = rec(i, j+1) || rec(i+1, j)
		default:
			ok = singleSegOverlap(a[i], b[j]) && rec(i+1, j+1)
		}
		memo[k] = ok
		return ok
	}
	return rec(0, 0)
}

type segTok struct {
	kind byte // '*', '?', 'c' (literal), '[' (char class)
	ch   byte
	raw  string // char class inside [...]
}

func tokenizeSingleSeg(s string) []segTok {
	var toks []segTok
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*':
			if len(toks) > 0 && toks[len(toks)-1].kind == '*' {
				continue
			}
			toks = append(toks, segTok{kind: '*'})
		case '?':
			toks = append(toks, segTok{kind: '?'})
		case '[':
			end := i + 1
			if end < len(s) && (s[end] == '!' || s[end] == '^') {
				end++
			}
			if end < len(s) && s[end] == ']' {
				end++
			}
			for end < len(s) && s[end] != ']' {
				end++
			}
			if end < len(s) {
				toks = append(toks, segTok{kind: '[', raw: s[i : end+1]})
				i = end
			} else {
				toks = append(toks, segTok{kind: 'c', ch: s[i]})
			}
		default:
			toks = append(toks, segTok{kind: 'c', ch: s[i]})
		}
	}
	return toks
}

func tokensOverlap(a, b segTok) bool {
	if a.kind == '?' || b.kind == '?' {
		return true
	}
	if a.kind == 'c' && b.kind == 'c' {
		return a.ch == b.ch
	}
	if a.kind == 'c' && b.kind == '[' {
		matched, err := doublestar.Match(b.raw, string(a.ch))
		return err == nil && matched
	}
	if a.kind == '[' && b.kind == 'c' {
		matched, err := doublestar.Match(a.raw, string(b.ch))
		return err == nil && matched
	}
	if a.kind == '[' && b.kind == '[' {
		for ch := byte(0x20); ch <= 0x7e; ch++ {
			if ch == '/' {
				continue
			}
			s := string(ch)
			m1, err1 := doublestar.Match(a.raw, s)
			m2, err2 := doublestar.Match(b.raw, s)
			if err1 == nil && err2 == nil && m1 && m2 {
				return true
			}
		}
		return false
	}
	return false
}

func singleSegOverlap(sa, sb string) bool {
	if sa == sb {
		return true
	}
	ta := tokenizeSingleSeg(sa)
	tb := tokenizeSingleSeg(sb)

	type key struct{ i, j int }
	memo := make(map[key]bool)
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		if i == len(ta) && j == len(tb) {
			return true
		}
		if i < len(ta) && ta[i].kind == '*' && j == len(tb) {
			return rec(i+1, j)
		}
		if j < len(tb) && tb[j].kind == '*' && i == len(ta) {
			return rec(i, j+1)
		}
		if i == len(ta) || j == len(tb) {
			return false
		}

		k := key{i, j}
		if v, seen := memo[k]; seen {
			return v
		}
		var ok bool
		switch {
		case ta[i].kind == '*' && tb[j].kind == '*':
			ok = rec(i+1, j) || rec(i, j+1)
		case ta[i].kind == '*':
			ok = rec(i+1, j) || rec(i, j+1)
		case tb[j].kind == '*':
			ok = rec(i, j+1) || rec(i+1, j)
		default:
			ok = tokensOverlap(ta[i], tb[j]) && rec(i+1, j+1)
		}
		memo[k] = ok
		return ok
	}
	return rec(0, 0)
}

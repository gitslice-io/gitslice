package paths

import (
	"fmt"
	"path"
	"strings"
)

func Canonical(p string) (string, error) {
	cleaned, segments, err := canonicalize(p)
	if err != nil {
		return "", err
	}
	if len(segments) < 2 {
		return "", fmt.Errorf("path must include account and slice segments: %q", p)
	}
	return cleaned, nil
}

// CanonicalPrefix cleans an included-path prefix like Canonical but permits an
// account-root prefix (a single segment, e.g. "/acme"). Home slices may include
// their whole account root, and the git projector lists files under such
// prefixes, so it must accept them where Canonical (which requires
// account/slice) would not.
func CanonicalPrefix(p string) (string, error) {
	cleaned, _, err := canonicalize(p)
	if err != nil {
		return "", err
	}
	return cleaned, nil
}

// canonicalize normalizes p to a rooted, cleaned path and returns its
// non-empty segments. It rejects empty input, `..` escapes, and empty/dot
// segments, but does not itself require any minimum number of segments.
func canonicalize(p string) (string, []string, error) {
	if p == "" {
		return "", nil, fmt.Errorf("path is empty")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "/" {
		return "", nil, fmt.Errorf("path must include at least an account segment: %q", p)
	}
	if strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
		return "", nil, fmt.Errorf("path escapes root: %q", p)
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", nil, fmt.Errorf("invalid path segment %q in %q", segment, p)
		}
	}
	return cleaned, segments, nil
}

func Contains(prefix, p string) bool {
	prefix = strings.TrimRight(prefix, "/")
	p = strings.TrimRight(p, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func InAnyPrefix(prefixes []string, p string) bool {
	for _, prefix := range prefixes {
		if Contains(prefix, p) {
			return true
		}
	}
	return false
}

func AncestorPrefixes(p string) []string {
	p = strings.TrimRight(path.Clean(strings.ReplaceAll(p, "\\", "/")), "/")
	if p == "." || p == "" || p == "/" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(segments))
	current := ""
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil
		}
		current += "/" + segment
		out = append(out, current)
	}
	return out
}

func FromWorkspacePath(includedPrefix, workspacePath string) (string, error) {
	workspacePath = strings.TrimPrefix(strings.ReplaceAll(workspacePath, "\\", "/"), "./")
	trimmedPrefix := strings.TrimPrefix(includedPrefix, "/")
	account := strings.Split(trimmedPrefix, "/")[0]
	if strings.HasPrefix(workspacePath, account+"/") {
		return Canonical(workspacePath)
	}
	if workspacePath == "" {
		return Canonical(includedPrefix)
	}
	return Canonical(strings.TrimRight(includedPrefix, "/") + "/" + workspacePath)
}

package paths

import (
	"fmt"
	"path"
	"strings"
)

func Canonical(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("path must include account and slice segments")
	}
	if strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
		return "", fmt.Errorf("path escapes root: %q", p)
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) < 2 {
		return "", fmt.Errorf("path must include account and slice segments: %q", p)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid path segment %q in %q", segment, p)
		}
	}
	return cleaned, nil
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

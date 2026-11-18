package checks

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	gspaths "gitslice.io/gitslice/internal/paths"
)

const logicalCanonicalBase = "/__gitslice_logical__/repo"

func cleanLogicalPaths(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		cleaned, err := cleanLogicalPath(value)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", value, err)
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out, nil
}

func cleanLogicalPath(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	value = normalizePathValue(value)
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("path contains .. segment")
		}
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if path.Clean(value) == "/" {
		return "/", nil
	}

	canonical, err := gspaths.Canonical(logicalCanonicalBase + value)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimPrefix(canonical, logicalCanonicalBase)
	if trimmed == "" || trimmed == "/" {
		return "/", nil
	}
	return strings.TrimPrefix(trimmed, "/"), nil
}

func resolveLogicalPath(definingDir, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	if err := validateNoAscent(value); err != nil {
		return "", err
	}
	value = normalizePathValue(value)
	if strings.HasPrefix(value, "/") {
		return cleanLogicalPath(value)
	}
	base := logicalAbs(dirPrefix(definingDir))
	return cleanLogicalPath(path.Join(base, value))
}

func validateNoAscent(value string) error {
	value = normalizePathValue(value)
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("path %q contains .. segment", value)
		}
	}
	return nil
}

func discoverDirs(changedPaths []string) []string {
	dirs := map[string]struct{}{}
	for _, changed := range changedPaths {
		for _, dir := range ancestorDirs(logicalDir(changed)) {
			dirs[dir] = struct{}{}
		}
	}
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Slice(out, func(i, j int) bool {
		leftDepth := logicalDepth(out[i])
		rightDepth := logicalDepth(out[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return out[i] < out[j]
	})
	return out
}

func ancestorDirs(dir string) []string {
	if dir == "" || dir == "/" {
		return []string{""}
	}
	segments := strings.Split(dir, "/")
	out := make([]string, 0, len(segments)+1)
	for i := len(segments); i > 0; i-- {
		out = append(out, strings.Join(segments[:i], "/"))
	}
	out = append(out, "")
	return out
}

func logicalDir(p string) string {
	if p == "/" {
		return ""
	}
	dir := path.Dir(p)
	if dir == "." || dir == "/" {
		return ""
	}
	return dir
}

func checksFilePath(dir string) string {
	if dir == "" || dir == "/" {
		return "/.gitslice/checks.yaml"
	}
	return dir + "/.gitslice/checks.yaml"
}

func qualifiedID(dir, localName string) string {
	if dir == "" || dir == "/" {
		return localName
	}
	return path.Join(dir, localName)
}

func dirPrefix(dir string) string {
	if dir == "" || dir == "/" {
		return "/"
	}
	return dir
}

func collapsePrefixes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cleaned, err := cleanLogicalPaths(values)
	if err != nil {
		return nil
	}
	sort.Slice(cleaned, func(i, j int) bool {
		leftDepth := logicalDepth(cleaned[i])
		rightDepth := logicalDepth(cleaned[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return cleaned[i] < cleaned[j]
	})

	out := make([]string, 0, len(cleaned))
	for _, candidate := range cleaned {
		nested := false
		for _, existing := range out {
			if containsLogicalPath(existing, candidate) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, candidate)
		}
	}
	return out
}

func containsLogicalPath(prefix, p string) bool {
	return gspaths.Contains(logicalAbs(prefix), logicalAbs(p))
}

func logicalAbs(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return "/" + path.Clean(p)
}

func relativeToDefiningDir(definingDir, changedPath string) string {
	if definingDir == "" || definingDir == "/" {
		if changedPath == "/" {
			return "."
		}
		return changedPath
	}
	if changedPath == definingDir {
		return "."
	}
	return strings.TrimPrefix(changedPath, definingDir+"/")
}

func logicalDepth(p string) int {
	if p == "" || p == "/" {
		return 0
	}
	return len(strings.Split(strings.Trim(p, "/"), "/"))
}

func normalizeRequiredChecks(requiredChecks []string) []string {
	out := make([]string, 0, len(requiredChecks))
	seen := map[string]struct{}{}
	for _, required := range requiredChecks {
		normalized := strings.Trim(normalizePathValue(required), "/")
		if normalized == "" {
			normalized = required
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func parseErrorCheckNames(definingDir string, err error) []string {
	var parseErr *parseError
	if errors.As(err, &parseErr) && len(parseErr.checkNames) > 0 {
		out := make([]string, 0, len(parseErr.checkNames))
		for _, name := range parseErr.checkNames {
			out = append(out, qualifiedID(definingDir, name))
		}
		sort.Strings(out)
		return out
	}
	return []string{checksFilePath(definingDir)}
}

func sortedCheckNames(checks map[string]Check) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNotFound)
}

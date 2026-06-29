package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	gspaths "github.com/gitslice-io/gitslice/internal/paths"
)

// ErrNotFound may be returned by TreeReader when a path is absent.
var ErrNotFound = errors.New("not found")

const logicalCanonicalBase = "/__gitslice_logical__/repo"

// TreeReader reads file content from an immutable tree.
type TreeReader interface {
	ReadFile(ctx context.Context, rootTreeID, path string) ([]byte, error)
}

// CheckSpec is a fully-resolved, runnable check.
type CheckSpec struct {
	Name             string // qualified id, "<dir>/<name>"; root dir => "<name>"
	Run              string
	Image            string
	WorkingDir       string // logical path
	MaterializePaths []string
	Env              map[string]string
	Network          bool
	Timeout          time.Duration
	OutOfSlice       bool
}

type SkippedCheck struct{ Name, Reason string }
type ErroredCheck struct{ Name, Reason string }

type Plan struct {
	Runnable []CheckSpec
	Skipped  []SkippedCheck
	Errored  []ErroredCheck
}

type discoveredFile struct {
	dir  string
	file *File
	err  error
}

// ResolvePlan computes the applicable checks for a patchset.
func ResolvePlan(ctx context.Context, tree TreeReader, rootTreeID string, changedPaths, includedPaths, requiredChecks []string) (*Plan, error) {
	if tree == nil {
		return nil, fmt.Errorf("tree reader is required")
	}

	cleanChanged, err := cleanLogicalPaths(changedPaths)
	if err != nil {
		return nil, fmt.Errorf("clean changed paths: %w", err)
	}
	sort.Strings(cleanChanged)

	cleanIncluded, err := cleanLogicalPaths(includedPaths)
	if err != nil {
		return nil, fmt.Errorf("clean included paths: %w", err)
	}
	cleanIncluded = collapsePrefixes(cleanIncluded)

	dirs := discoverDirs(cleanChanged)
	discovered := make([]discoveredFile, 0, len(dirs))
	for _, dir := range dirs {
		checksPath := checksFilePath(dir)
		data, err := tree.ReadFile(ctx, rootTreeID, checksPath)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		file, err := Parse(data)
		discovered = append(discovered, discoveredFile{
			dir:  dir,
			file: file,
			err:  err,
		})
	}

	plan := &Plan{}
	produced := map[string]struct{}{}
	for _, found := range discovered {
		if found.err != nil {
			for _, name := range parseErrorCheckNames(found.dir, found.err) {
				plan.Errored = append(plan.Errored, ErroredCheck{
					Name:   name,
					Reason: found.err.Error(),
				})
				produced[name] = struct{}{}
			}
			continue
		}

		for _, localName := range sortedCheckNames(found.file.Checks) {
			check := found.file.Checks[localName]
			name := qualifiedID(found.dir, localName)
			if _, ok := produced[name]; ok {
				continue
			}
			produced[name] = struct{}{}

			matches, err := checkMatchesChangedPaths(found.dir, check.Paths, cleanChanged)
			if err != nil {
				plan.Errored = append(plan.Errored, ErroredCheck{Name: name, Reason: err.Error()})
				continue
			}
			if !matches {
				plan.Skipped = append(plan.Skipped, SkippedCheck{
					Name:   name,
					Reason: "paths filter did not match changed paths",
				})
				continue
			}

			spec, err := buildCheckSpec(found.dir, localName, check, cleanIncluded)
			if err != nil {
				plan.Errored = append(plan.Errored, ErroredCheck{Name: name, Reason: err.Error()})
				continue
			}
			plan.Runnable = append(plan.Runnable, spec)
		}
	}

	for _, required := range normalizeRequiredChecks(requiredChecks) {
		if _, ok := produced[required]; ok {
			continue
		}
		plan.Errored = append(plan.Errored, ErroredCheck{
			Name:   required,
			Reason: "required check has no definition in this revision",
		})
		produced[required] = struct{}{}
	}

	return plan, nil
}

func buildCheckSpec(definingDir, localName string, check Check, includedPaths []string) (CheckSpec, error) {
	workingDir, err := resolveLogicalPath(definingDir, check.WorkingDir)
	if err != nil {
		return CheckSpec{}, fmt.Errorf("resolve working_dir: %w", err)
	}

	materializePaths := []string{dirPrefix(definingDir)}
	for _, include := range check.Include {
		resolved, err := resolveLogicalPath(definingDir, include)
		if err != nil {
			return CheckSpec{}, fmt.Errorf("resolve include %q: %w", include, err)
		}
		materializePaths = append(materializePaths, resolved)
	}
	materializePaths = collapsePrefixes(materializePaths)

	return CheckSpec{
		Name:             qualifiedID(definingDir, localName),
		Run:              check.Run,
		Image:            check.Image,
		WorkingDir:       workingDir,
		MaterializePaths: materializePaths,
		Env:              copyEnv(check.Env),
		Network:          check.Network,
		Timeout:          check.Timeout,
		OutOfSlice:       materializeOutOfSlice(materializePaths, includedPaths),
	}, nil
}

func checkMatchesChangedPaths(definingDir string, patterns []string, changedPaths []string) (bool, error) {
	if len(patterns) == 0 {
		for _, changed := range changedPaths {
			if containsLogicalPath(dirPrefix(definingDir), changed) {
				return true, nil
			}
		}
		return false, nil
	}

	for _, pattern := range patterns {
		if !doublestar.ValidatePattern(pattern) {
			return false, fmt.Errorf("invalid paths glob %q", pattern)
		}
		absolute := strings.HasPrefix(pattern, "/")
		for _, changed := range changedPaths {
			var target string
			if absolute {
				target = logicalAbs(changed)
			} else {
				if !containsLogicalPath(dirPrefix(definingDir), changed) {
					continue
				}
				target = relativeToDefiningDir(definingDir, changed)
			}
			matches, err := doublestar.Match(pattern, target)
			if err != nil {
				return false, fmt.Errorf("match paths glob %q: %w", pattern, err)
			}
			if matches {
				return true, nil
			}
		}
	}
	return false, nil
}

func materializeOutOfSlice(materializePaths, includedPaths []string) bool {
	for _, materializePath := range materializePaths {
		included := false
		for _, includedPath := range includedPaths {
			if containsLogicalPath(includedPath, materializePath) {
				included = true
				break
			}
		}
		if !included {
			return true
		}
	}
	return false
}

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

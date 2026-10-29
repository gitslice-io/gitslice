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
	gspaths "gitslice.io/gitslice/internal/paths"
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
	Build            *ImageBuild // set instead of Image when the image is built from the tree
	Setup            []string
	Services         []ServiceSpec
	Cache            []string
	WorkingDir       string // logical path
	MaterializePaths []string
	Env              map[string]string
	Network          bool
	Memory           string
	CPUs             string
	Timeout          time.Duration
	OutOfSlice       bool
	// Scope is set by whoever runs the check, not by the checks file: the
	// slice it runs for. Caches and built images of different scopes are
	// kept apart.
	Scope string
}

// ImageBuild is a Build resolved against the tree: ContextDir is a logical
// repo path ("/" for the root), Dockerfile and Inputs are relative to it.
type ImageBuild struct {
	ContextDir string
	Dockerfile string
	Inputs     []string
	Timeout    time.Duration
}

// ServiceSpec is a service a check runs beside, by the name it is reached at.
type ServiceSpec struct {
	Name         string
	Image        string
	Env          map[string]string
	Ready        string
	ReadyTimeout time.Duration
	Memory       string
}

type SkippedCheck struct{ Name, Reason string }
type ErroredCheck struct{ Name, Reason string }

type Plan struct {
	// Runnable checks start with every new patchset.
	Runnable []CheckSpec
	// Triggered checks match the patchset but start only when a trigger
	// asks (started_by: trigger, design/34_triggers.md).
	Triggered []CheckSpec
	Skipped   []SkippedCheck
	Errored   []ErroredCheck
}

// Spec finds a runnable or triggered check by name.
func (plan *Plan) Spec(name string) (CheckSpec, bool) {
	if plan == nil {
		return CheckSpec{}, false
	}
	for _, list := range [][]CheckSpec{plan.Runnable, plan.Triggered} {
		for _, spec := range list {
			if spec.Name == name {
				return spec, true
			}
		}
	}
	return CheckSpec{}, false
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
			name := qualifiedID(found.dir, localName)
			if _, ok := produced[name]; ok {
				continue
			}
			produced[name] = struct{}{}
			plan.addCheck(found.dir, localName, found.file.Checks[localName], found.file.Services, cleanChanged, cleanIncluded)
		}
	}

	// Discovery reads only the checks files on the changed paths' ancestor
	// directories. A required check defined anywhere else in the revision is
	// evaluated from its own file: a change outside its directory skips it
	// rather than leaving it without a result, and an absolute paths filter
	// can still select it.
	for _, required := range normalizeRequiredChecks(requiredChecks) {
		if _, ok := produced[required]; ok {
			continue
		}
		produced[required] = struct{}{}
		dir, localName := splitQualifiedID(required)
		checksPath := checksFilePath(dir)
		data, err := tree.ReadFile(ctx, rootTreeID, checksPath)
		if err != nil && !isNotFound(err) {
			return nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		var check Check
		var services map[string]Service
		defined := false
		if err == nil {
			file, parseErr := Parse(data)
			if parseErr != nil {
				plan.Errored = append(plan.Errored, ErroredCheck{Name: required, Reason: parseErr.Error()})
				continue
			}
			check, defined = file.Checks[localName]
			services = file.Services
		}
		if !defined {
			plan.Errored = append(plan.Errored, ErroredCheck{
				Name:   required,
				Reason: "required check has no definition in this revision",
			})
			continue
		}
		plan.addCheck(dir, localName, check, services, cleanChanged, cleanIncluded)
	}

	return plan, nil
}

// addCheck sorts one defined check into Runnable, Skipped or Errored for the
// changed paths.
func (plan *Plan) addCheck(dir, localName string, check Check, services map[string]Service, changedPaths, includedPaths []string) {
	name := qualifiedID(dir, localName)
	matches, err := checkMatchesChangedPaths(dir, check.Paths, changedPaths)
	if err != nil {
		plan.Errored = append(plan.Errored, ErroredCheck{Name: name, Reason: err.Error()})
		return
	}
	if !matches {
		reason := "paths filter did not match changed paths"
		if len(check.Paths) == 0 {
			reason = "no changed paths under " + dirPrefix(dir)
		}
		plan.Skipped = append(plan.Skipped, SkippedCheck{Name: name, Reason: reason})
		return
	}
	spec, err := buildCheckSpec(dir, localName, check, services, includedPaths)
	if err != nil {
		plan.Errored = append(plan.Errored, ErroredCheck{Name: name, Reason: err.Error()})
		return
	}
	if check.StartedBy == StartedByTrigger {
		plan.Triggered = append(plan.Triggered, spec)
		return
	}
	plan.Runnable = append(plan.Runnable, spec)
}

// splitQualifiedID splits "<dir>/<name>" into the checks file's directory and
// the check's local name. Local names never contain "/".
func splitQualifiedID(id string) (string, string) {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

func buildCheckSpec(definingDir, localName string, check Check, services map[string]Service, includedPaths []string) (CheckSpec, error) {
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
	// The daemon builds the image from the tree, so the Dockerfile and its
	// inputs are materialized like includes.
	var build *ImageBuild
	if check.Build != nil {
		resolved, err := resolveImageBuild(definingDir, check.Build)
		if err != nil {
			return CheckSpec{}, err
		}
		build = resolved
		for _, rel := range append([]string{build.Dockerfile}, build.Inputs...) {
			materializePaths = append(materializePaths, joinLogical(build.ContextDir, rel))
		}
	}
	materializePaths = collapsePrefixes(materializePaths)

	var serviceSpecs []ServiceSpec
	for _, name := range check.Services {
		service, ok := services[name]
		if !ok {
			return CheckSpec{}, fmt.Errorf("service %q is not defined", name)
		}
		serviceSpecs = append(serviceSpecs, ServiceSpec{
			Name:         name,
			Image:        service.Image,
			Env:          copyEnv(service.Env),
			Ready:        service.Ready,
			ReadyTimeout: service.ReadyTimeout,
			Memory:       service.Memory,
		})
	}

	return CheckSpec{
		Name:             qualifiedID(definingDir, localName),
		Run:              check.Run,
		Image:            check.Image,
		Build:            build,
		Setup:            copyStringSlice(check.Setup),
		Services:         serviceSpecs,
		Cache:            copyStringSlice(check.Cache),
		WorkingDir:       workingDir,
		MaterializePaths: materializePaths,
		Env:              copyEnv(check.Env),
		Network:          check.Network,
		Memory:           check.Memory,
		CPUs:             check.CPUs,
		Timeout:          check.Timeout,
		OutOfSlice:       materializeOutOfSlice(materializePaths, includedPaths),
	}, nil
}

func resolveImageBuild(definingDir string, b *Build) (*ImageBuild, error) {
	contextDir, err := resolveLogicalPath(definingDir, b.Context)
	if err != nil {
		return nil, fmt.Errorf("resolve build context: %w", err)
	}
	timeout := b.Timeout
	if timeout == 0 {
		timeout = DefaultBuildTimeout
	}
	return &ImageBuild{
		ContextDir: contextDir,
		Dockerfile: b.Dockerfile,
		Inputs:     copyStringSlice(b.Inputs),
		Timeout:    timeout,
	}, nil
}

// joinLogical joins a context-relative path onto a logical directory.
func joinLogical(dir, rel string) string {
	if dir == "" || dir == "/" {
		return rel
	}
	return path.Join(dir, rel)
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

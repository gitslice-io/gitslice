package checks

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/gitslicecfg"
)

// ConfigName is the filename under .gitslice/ for check definitions.
const ConfigName = "checks.yaml"

// ErrNotFound may be returned by TreeReader when a path is absent.
var ErrNotFound = errors.New("not found")

// TreeReader reads file content from an immutable tree.
type TreeReader interface {
	ReadFile(ctx context.Context, rootTreeID, path string) ([]byte, error)
}

// NewResolver creates a memoized Resolver[*File] for .gitslice/checks.yaml
// backed by tree.
func NewResolver(tree TreeReader) *gitslicecfg.Resolver[*File] {
	var read gitslicecfg.ReadConfigFunc
	if tree != nil {
		read = func(ctx context.Context, rootTreeID, configPath string) ([]byte, bool, error) {
			data, err := tree.ReadFile(ctx, rootTreeID, configPath)
			if isNotFound(err) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return data, true, nil
		}
	}
	return gitslicecfg.NewResolver(ConfigName, read, func(_ string, data []byte) (*File, error) {
		return Parse(data)
	})
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
	Required         bool
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
	return ResolveWithResolver(ctx, NewResolver(tree), rootTreeID, changedPaths, includedPaths, requiredChecks)
}

// ResolveWithResolver computes the applicable checks for a patchset using a
// memoized gitslicecfg.Resolver[*File] so repeated plan lookups on the same
// tree reuse parsed checks.File definitions.
func ResolveWithResolver(ctx context.Context, resolver *gitslicecfg.Resolver[*File], rootTreeID string, changedPaths, includedPaths, requiredChecks []string) (*Plan, error) {
	if resolver == nil {
		return nil, fmt.Errorf("checks resolver is required")
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
		file, exists, err := resolver.Resolve(ctx, rootTreeID, checksPath)
		if err != nil {
			var pe *parseError
			if errors.As(err, &pe) {
				discovered = append(discovered, discoveredFile{
					dir: dir,
					err: err,
				})
				continue
			}
			return nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		if !exists {
			continue
		}
		discovered = append(discovered, discoveredFile{
			dir:  dir,
			file: file,
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
		file, exists, err := resolver.Resolve(ctx, rootTreeID, checksPath)
		if err != nil {
			var pe *parseError
			if errors.As(err, &pe) {
				plan.Errored = append(plan.Errored, ErroredCheck{Name: required, Reason: err.Error()})
				continue
			}
			return nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		var check Check
		var services map[string]Service
		defined := false
		if exists && file != nil {
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
		Required:         check.Required,
	}, nil
}

// FolderCheck is a check defined in a .gitslice/checks.yaml file along a
// folder's ancestor chain.
type FolderCheck struct {
	ID         string
	LocalName  string
	SourceDir  string // logical dir relative to account root ("" for root)
	ConfigPath string // logical path to the defining checks.yaml
	Check      Check
}

// FolderCheckProblem describes a .gitslice/checks.yaml parse failure along a
// folder's ancestor chain.
type FolderCheckProblem struct {
	ConfigPath string
	SourceDir  string
	Message    string
}

// ListForFolder lists all checks defined in .gitslice/checks.yaml files from
// the account root down to logicalFolder (inclusive).
func ListForFolder(ctx context.Context, resolver *gitslicecfg.Resolver[*File], rootTreeID, logicalFolder string) ([]FolderCheck, []FolderCheckProblem, error) {
	if resolver == nil {
		return nil, nil, fmt.Errorf("checks resolver is required")
	}
	dir := ""
	trimmed := strings.TrimSpace(logicalFolder)
	if trimmed != "" && trimmed != "/" {
		cleaned, err := cleanLogicalPath(trimmed)
		if err != nil {
			return nil, nil, fmt.Errorf("clean logical folder %q: %w", logicalFolder, err)
		}
		if cleaned != "/" {
			dir = cleaned
		}
	}
	ancestors := ancestorDirs(dir)
	// Walk from account root down to logicalFolder so shallower configs appear first.
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	var out []FolderCheck
	var problems []FolderCheckProblem
	for _, d := range ancestors {
		checksPath := checksFilePath(d)
		file, exists, err := resolver.Resolve(ctx, rootTreeID, checksPath)
		if err != nil {
			var pe *parseError
			if errors.As(err, &pe) {
				problems = append(problems, FolderCheckProblem{
					ConfigPath: checksPath,
					SourceDir:  d,
					Message:    err.Error(),
				})
				continue
			}
			return nil, nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		if !exists || file == nil {
			continue
		}
		for _, localName := range sortedCheckNames(file.Checks) {
			out = append(out, FolderCheck{
				ID:         qualifiedID(d, localName),
				LocalName:  localName,
				SourceDir:  d,
				ConfigPath: checksPath,
				Check:      file.Checks[localName],
			})
		}
	}
	return out, problems, nil
}

// RequiredForPaths returns the sorted qualified IDs of checks marked
// `required: true` in .gitslice/checks.yaml files on changedPaths' ancestor
// directories whose paths filter matches changedPaths.
//
// Callers enforcing submit gates should pass the target ref's landed root tree
// so unapproved draft edits cannot disable required checks.
func RequiredForPaths(ctx context.Context, resolver *gitslicecfg.Resolver[*File], rootTreeID string, changedPaths []string) ([]string, error) {
	if resolver == nil || len(changedPaths) == 0 {
		return nil, nil
	}
	cleanChanged, err := cleanLogicalPaths(changedPaths)
	if err != nil {
		return nil, fmt.Errorf("clean changed paths: %w", err)
	}
	sort.Strings(cleanChanged)
	dirs := discoverDirs(cleanChanged)
	seen := map[string]struct{}{}
	var required []string
	for _, dir := range dirs {
		checksPath := checksFilePath(dir)
		file, exists, err := resolver.Resolve(ctx, rootTreeID, checksPath)
		if err != nil {
			var pe *parseError
			if errors.As(err, &pe) {
				continue
			}
			return nil, fmt.Errorf("read checks file %s: %w", checksPath, err)
		}
		if !exists || file == nil {
			continue
		}
		for _, localName := range sortedCheckNames(file.Checks) {
			chk := file.Checks[localName]
			if !chk.Required {
				continue
			}
			matches, err := checkMatchesChangedPaths(dir, chk.Paths, cleanChanged)
			if err != nil || !matches {
				continue
			}
			id := qualifiedID(dir, localName)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			required = append(required, id)
		}
	}
	sort.Strings(required)
	return required, nil
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

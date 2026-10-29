package checks

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// File is one parsed .gitslice/checks.yaml.
type File struct {
	Version  int
	Defaults Defaults
	Checks   map[string]Check // keyed by local name
	// Services are the containers a check may start beside itself, keyed by
	// the host name the check reaches each one at.
	Services map[string]Service
}

// Build is an image the daemon builds from a Dockerfile in the tree, and
// reuses for as long as the Dockerfile and its inputs are unchanged.
type Build struct {
	Context    string        // folder-relative or "/"-absolute; default the checks.yaml folder
	Dockerfile string        // relative to Context
	Inputs     []string      // files and folders the build reads, relative to Context
	Timeout    time.Duration // optional
}

// Service is a container started for each run of a check that names it, on a
// network private to that run.
type Service struct {
	Image        string
	Env          map[string]string
	Ready        string        // command run inside the service until it passes
	ReadyTimeout time.Duration // optional
	Memory       string
}

type Defaults struct {
	Image   string
	Build   *Build
	Setup   []string
	Cache   []string      // absolute container paths; check-level cache replaces defaults
	Timeout time.Duration // parsed from a Go duration string, optional
	Env     map[string]string
	Network bool
	Memory  string
	CPUs    string
	// StartedBy is StartedByPatchset or StartedByTrigger.
	StartedBy string
}

// Who starts a check (design/34_triggers.md): every new patchset, or only a
// trigger whose action names it.
const (
	StartedByPatchset = "patchset"
	StartedByTrigger  = "trigger"
)

type Check struct {
	Description string
	Run         string            // required
	Image       string            // optional; overrides Defaults.Image
	Build       *Build            // optional; image built from the tree, instead of Image
	Setup       []string          // optional image preparation commands
	Services    []string          // optional; names in File.Services
	Cache       []string          // optional absolute container paths; replaces Defaults.Cache
	Paths       []string          // optional trigger globs
	Include     []string          // optional extra materialize prefixes
	WorkingDir  string            // optional, default "."
	Env         map[string]string // merged over Defaults.Env
	Network     bool
	Memory      string
	CPUs        string
	Timeout     time.Duration // optional, overrides Defaults.Timeout
	StartedBy   string        // StartedByPatchset (default) or StartedByTrigger
}

type rawFile struct {
	Version  *int                  `yaml:"version"`
	Defaults rawDefaults           `yaml:"defaults"`
	Checks   map[string]rawCheck   `yaml:"checks"`
	Services map[string]rawService `yaml:"services"`
}

type rawBuild struct {
	Context    string   `yaml:"context"`
	Dockerfile string   `yaml:"dockerfile"`
	Inputs     []string `yaml:"inputs"`
	Timeout    string   `yaml:"timeout"`
}

type rawService struct {
	Image        string            `yaml:"image"`
	Env          map[string]string `yaml:"env"`
	Ready        string            `yaml:"ready"`
	ReadyTimeout string            `yaml:"ready_timeout"`
	Memory       string            `yaml:"memory"`
}

type rawDefaults struct {
	Image     string            `yaml:"image"`
	Build     *rawBuild         `yaml:"build"`
	Setup     []string          `yaml:"setup"`
	Cache     []string          `yaml:"cache"`
	Timeout   string            `yaml:"timeout"`
	Env       map[string]string `yaml:"env"`
	Network   bool              `yaml:"network"`
	Memory    string            `yaml:"memory"`
	CPUs      string            `yaml:"cpus"`
	StartedBy string            `yaml:"started_by"`
}

type rawCheck struct {
	Description string            `yaml:"description"`
	Run         string            `yaml:"run"`
	Image       string            `yaml:"image"`
	Build       *rawBuild         `yaml:"build"`
	Setup       []string          `yaml:"setup"`
	Services    []string          `yaml:"services"`
	Cache       []string          `yaml:"cache"`
	Paths       []string          `yaml:"paths"`
	Include     []string          `yaml:"include"`
	WorkingDir  string            `yaml:"working_dir"`
	Env         map[string]string `yaml:"env"`
	Network     *bool             `yaml:"network"`
	Memory      string            `yaml:"memory"`
	CPUs        string            `yaml:"cpus"`
	Timeout     string            `yaml:"timeout"`
	StartedBy   string            `yaml:"started_by"`
}

type parseError struct {
	message    string
	checkNames []string
}

func (e *parseError) Error() string {
	return e.message
}

var localNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// serviceNamePattern keeps service names valid host names, since a check
// reaches each service by its name.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// DefaultBuildTimeout bounds an image build that does not declare a timeout.
const DefaultBuildTimeout = 30 * time.Minute

// Parse parses and validates one .gitslice/checks.yaml file.
func Parse(data []byte) (*File, error) {
	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, &parseError{message: fmt.Sprintf("parse checks yaml: %v", err)}
	}

	names := sortedRawCheckNames(raw.Checks)
	if raw.Version == nil {
		return nil, &parseError{message: "version is required", checkNames: names}
	}
	if *raw.Version != 1 {
		return nil, &parseError{message: fmt.Sprintf("unsupported version %d", *raw.Version), checkNames: names}
	}
	if raw.Checks == nil {
		return nil, &parseError{message: "checks is required"}
	}

	defaults, err := parseDefaults(raw.Defaults)
	if err != nil {
		return nil, &parseError{message: err.Error(), checkNames: names}
	}

	services, err := parseServices(raw.Services)
	if err != nil {
		return nil, &parseError{message: err.Error(), checkNames: names}
	}

	out := &File{
		Version:  *raw.Version,
		Defaults: defaults,
		Checks:   make(map[string]Check, len(raw.Checks)),
		Services: services,
	}
	for _, name := range names {
		if !localNamePattern.MatchString(name) {
			return nil, &parseError{
				message:    fmt.Sprintf("check %q has invalid local name", name),
				checkNames: []string{name},
			}
		}
		check, err := parseCheck(name, raw.Checks[name], defaults, services)
		if err != nil {
			return nil, &parseError{
				message:    err.Error(),
				checkNames: []string{name},
			}
		}
		out.Checks[name] = check
	}
	return out, nil
}

func parseDefaults(raw rawDefaults) (Defaults, error) {
	timeout, err := parseOptionalDuration("defaults.timeout", raw.Timeout)
	if err != nil {
		return Defaults{}, err
	}
	if err := validateCacheList("defaults", raw.Cache); err != nil {
		return Defaults{}, err
	}
	if raw.Image != "" && raw.Build != nil {
		return Defaults{}, fmt.Errorf("defaults: set image or build, not both")
	}
	build, err := parseBuild("defaults", raw.Build)
	if err != nil {
		return Defaults{}, err
	}
	startedBy, err := parseStartedBy("defaults", raw.StartedBy, StartedByPatchset)
	if err != nil {
		return Defaults{}, err
	}
	return Defaults{
		StartedBy: startedBy,
		Image:     raw.Image,
		Build:     build,
		Setup:     copyStringSlice(raw.Setup),
		Cache:     normalizeCacheValues(raw.Cache),
		Timeout:   timeout,
		Env:       copyEnv(raw.Env),
		Network:   raw.Network,
		Memory:    raw.Memory,
		CPUs:      raw.CPUs,
	}, nil
}

func parseCheck(name string, raw rawCheck, defaults Defaults, services map[string]Service) (Check, error) {
	if strings.TrimSpace(raw.Run) == "" {
		return Check{}, fmt.Errorf("check %q run is required", name)
	}
	if err := validatePathList(name, "paths", raw.Paths, true); err != nil {
		return Check{}, err
	}
	if err := validatePathList(name, "include", raw.Include, false); err != nil {
		return Check{}, err
	}
	if err := validateCacheList(fmt.Sprintf("check %q", name), raw.Cache); err != nil {
		return Check{}, err
	}

	workingDir := raw.WorkingDir
	if workingDir == "" {
		workingDir = "."
	}
	if err := validatePathValue(name, "working_dir", workingDir, false); err != nil {
		return Check{}, err
	}

	timeout := defaults.Timeout
	if raw.Timeout != "" {
		parsed, err := parseOptionalDuration(fmt.Sprintf("check %q timeout", name), raw.Timeout)
		if err != nil {
			return Check{}, err
		}
		timeout = parsed
	}

	// A check's own image or build replaces whichever of the two the
	// defaults set.
	if raw.Image != "" && raw.Build != nil {
		return Check{}, fmt.Errorf("check %q: set image or build, not both", name)
	}
	image, build := defaults.Image, defaults.Build
	if raw.Image != "" {
		image, build = raw.Image, nil
	}
	if raw.Build != nil {
		parsed, err := parseBuild(fmt.Sprintf("check %q", name), raw.Build)
		if err != nil {
			return Check{}, err
		}
		image, build = "", parsed
	}

	for _, service := range raw.Services {
		if _, ok := services[service]; !ok {
			return Check{}, fmt.Errorf("check %q names service %q, which this file does not define", name, service)
		}
	}
	if len(raw.Services) > 0 && image == "" && build == nil {
		return Check{}, fmt.Errorf("check %q: services need a containerized check; set image or build", name)
	}

	setup := defaults.Setup
	if raw.Setup != nil {
		setup = raw.Setup
	}

	cache := defaults.Cache
	if raw.Cache != nil {
		cache = raw.Cache
	}

	network := defaults.Network
	if raw.Network != nil {
		network = *raw.Network
	}

	memory := defaults.Memory
	if raw.Memory != "" {
		memory = raw.Memory
	}

	cpus := defaults.CPUs
	if raw.CPUs != "" {
		cpus = raw.CPUs
	}

	startedBy, err := parseStartedBy(fmt.Sprintf("check %q", name), raw.StartedBy, defaults.StartedBy)
	if err != nil {
		return Check{}, err
	}

	env := copyEnv(defaults.Env)
	for key, value := range raw.Env {
		if env == nil {
			env = map[string]string{}
		}
		env[key] = value
	}

	return Check{
		Description: raw.Description,
		Run:         raw.Run,
		Image:       image,
		Build:       build,
		Setup:       copyStringSlice(setup),
		Services:    copyStringSlice(raw.Services),
		Cache:       normalizeCacheValues(cache),
		Paths:       normalizePathValues(raw.Paths),
		Include:     normalizePathValues(raw.Include),
		WorkingDir:  normalizePathValue(workingDir),
		Env:         env,
		Network:     network,
		Memory:      memory,
		CPUs:        cpus,
		Timeout:     timeout,
		StartedBy:   startedBy,
	}, nil
}

func parseStartedBy(owner, value, fallback string) (string, error) {
	switch value {
	case "":
		if fallback == "" {
			return StartedByPatchset, nil
		}
		return fallback, nil
	case StartedByPatchset, StartedByTrigger:
		return value, nil
	}
	return "", fmt.Errorf("%s started_by %q (use patchset or trigger)", owner, value)
}

func parseBuild(owner string, raw *rawBuild) (*Build, error) {
	if raw == nil {
		return nil, nil
	}
	owner += " build"
	if strings.TrimSpace(raw.Dockerfile) == "" {
		return nil, fmt.Errorf("%s: dockerfile is required", owner)
	}
	context := raw.Context
	if context == "" {
		context = "."
	}
	if err := validateBuildPath(owner, "context", context, true); err != nil {
		return nil, err
	}
	if err := validateBuildPath(owner, "dockerfile", raw.Dockerfile, false); err != nil {
		return nil, err
	}
	for _, input := range raw.Inputs {
		if err := validateBuildPath(owner, "input", input, false); err != nil {
			return nil, err
		}
	}
	timeout, err := parseOptionalDuration(owner+" timeout", raw.Timeout)
	if err != nil {
		return nil, err
	}
	return &Build{
		Context:    normalizePathValue(context),
		Dockerfile: path.Clean(normalizePathValue(raw.Dockerfile)),
		Inputs:     cleanRelativePaths(raw.Inputs),
		Timeout:    timeout,
	}, nil
}

// validateBuildPath keeps a build's paths inside its context: relative, with
// no .. segment. Only the context itself may be "/"-absolute.
func validateBuildPath(owner, field, value string, absoluteOK bool) error {
	normalized := normalizePathValue(strings.TrimSpace(value))
	if normalized == "" {
		return fmt.Errorf("%s: %s is empty", owner, field)
	}
	if strings.HasPrefix(normalized, "/") && !absoluteOK {
		return fmt.Errorf("%s: %s %q must be relative to the build context", owner, field, value)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("%s: %s %q contains .. segment", owner, field, value)
		}
	}
	return nil
}

func cleanRelativePaths(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, path.Clean(normalizePathValue(strings.TrimSpace(value))))
	}
	return out
}

func parseServices(raw map[string]rawService) (map[string]Service, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]Service, len(raw))
	for name, service := range raw {
		if !serviceNamePattern.MatchString(name) {
			return nil, fmt.Errorf("service %q: name must be a host name (lowercase letters, digits and -)", name)
		}
		if strings.TrimSpace(service.Image) == "" {
			return nil, fmt.Errorf("service %q: image is required", name)
		}
		readyTimeout, err := parseOptionalDuration(fmt.Sprintf("service %q ready_timeout", name), service.ReadyTimeout)
		if err != nil {
			return nil, err
		}
		out[name] = Service{
			Image:        strings.TrimSpace(service.Image),
			Env:          copyEnv(service.Env),
			Ready:        service.Ready,
			ReadyTimeout: readyTimeout,
			Memory:       service.Memory,
		}
	}
	return out, nil
}

func parseOptionalDuration(field, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: parse duration %q: %w", field, value, err)
	}
	return duration, nil
}

func validatePathList(checkName, field string, values []string, glob bool) error {
	for _, value := range values {
		if err := validatePathValue(checkName, field, value, glob); err != nil {
			return err
		}
	}
	return nil
}

func validateCacheList(owner string, values []string) error {
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%s cache contains empty path", owner)
		}
		normalized := normalizePathValue(value)
		if !strings.HasPrefix(normalized, "/") {
			return fmt.Errorf("%s cache %q must be an absolute container path", owner, value)
		}
		for _, segment := range strings.Split(normalized, "/") {
			if segment == ".." {
				return fmt.Errorf("%s cache %q contains .. segment", owner, value)
			}
		}
		if path.Clean(normalized) == "/" {
			return fmt.Errorf("%s cache %q must not be the container root", owner, value)
		}
	}
	return nil
}

func validatePathValue(checkName, field, value string, glob bool) error {
	if value == "" {
		return fmt.Errorf("check %q %s contains empty path", checkName, field)
	}
	normalized := normalizePathValue(value)
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("check %q %s %q contains .. segment", checkName, field, value)
		}
	}
	if glob && !doublestar.ValidatePattern(normalized) {
		return fmt.Errorf("check %q %s %q is an invalid glob", checkName, field, value)
	}
	return nil
}

func normalizePathValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, normalizePathValue(value))
	}
	return out
}

func normalizeCacheValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, path.Clean(normalizePathValue(value)))
	}
	return out
}

func normalizePathValue(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}

func copyEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func sortedRawCheckNames(checks map[string]rawCheck) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

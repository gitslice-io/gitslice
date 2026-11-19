package checks

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	// Required makes checks in this file gate submit by default when matched.
	Required bool
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
	Required    bool          // when true (on target head), passing result is required to submit
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
	Required  bool              `yaml:"required"`
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
	Required    *bool             `yaml:"required"`
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
	if raw.Image != "" {
		if err := ValidateImageRef(raw.Image); err != nil {
			return Defaults{}, fmt.Errorf("defaults image: %w", err)
		}
	}
	if err := ValidateSetupCommands(raw.Setup); err != nil {
		return Defaults{}, fmt.Errorf("defaults %w", err)
	}
	if err := ValidateEnvMap(raw.Env); err != nil {
		return Defaults{}, fmt.Errorf("defaults env: %w", err)
	}
	if raw.Memory != "" {
		if _, err := ParseMemoryBytes(raw.Memory); err != nil {
			return Defaults{}, fmt.Errorf("defaults memory: %w", err)
		}
	}
	if raw.CPUs != "" {
		if _, err := ParseCPUs(raw.CPUs); err != nil {
			return Defaults{}, fmt.Errorf("defaults cpus: %w", err)
		}
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
		Required:  raw.Required,
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
	if strings.ContainsRune(raw.Run, '\x00') {
		return Check{}, fmt.Errorf("check %q run must not contain NUL", name)
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
		if err := ValidateImageRef(raw.Image); err != nil {
			return Check{}, fmt.Errorf("check %q image: %w", name, err)
		}
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
		if err := ValidateSetupCommands(raw.Setup); err != nil {
			return Check{}, fmt.Errorf("check %q %w", name, err)
		}
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

	required := defaults.Required
	if raw.Required != nil {
		required = *raw.Required
	}

	memory := defaults.Memory
	if raw.Memory != "" {
		if _, err := ParseMemoryBytes(raw.Memory); err != nil {
			return Check{}, fmt.Errorf("check %q memory: %w", name, err)
		}
		memory = raw.Memory
	}

	cpus := defaults.CPUs
	if raw.CPUs != "" {
		if _, err := ParseCPUs(raw.CPUs); err != nil {
			return Check{}, fmt.Errorf("check %q cpus: %w", name, err)
		}
		cpus = raw.CPUs
	}

	if err := ValidateEnvMap(raw.Env); err != nil {
		return Check{}, fmt.Errorf("check %q env: %w", name, err)
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
		Required:    required,
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

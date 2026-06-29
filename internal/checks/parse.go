package checks

import (
	"bytes"
	"fmt"
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
}

type Defaults struct {
	Image   string
	Timeout time.Duration // parsed from a Go duration string, optional
	Env     map[string]string
	Network bool
}

type Check struct {
	Description string
	Run         string            // required
	Image       string            // optional; overrides Defaults.Image
	Paths       []string          // optional trigger globs
	Include     []string          // optional extra materialize prefixes
	WorkingDir  string            // optional, default "."
	Env         map[string]string // merged over Defaults.Env
	Network     bool
	Timeout     time.Duration // optional, overrides Defaults.Timeout
}

type rawFile struct {
	Version  *int                `yaml:"version"`
	Defaults rawDefaults         `yaml:"defaults"`
	Checks   map[string]rawCheck `yaml:"checks"`
}

type rawDefaults struct {
	Image   string            `yaml:"image"`
	Timeout string            `yaml:"timeout"`
	Env     map[string]string `yaml:"env"`
	Network bool              `yaml:"network"`
}

type rawCheck struct {
	Description string            `yaml:"description"`
	Run         string            `yaml:"run"`
	Image       string            `yaml:"image"`
	Paths       []string          `yaml:"paths"`
	Include     []string          `yaml:"include"`
	WorkingDir  string            `yaml:"working_dir"`
	Env         map[string]string `yaml:"env"`
	Network     *bool             `yaml:"network"`
	Timeout     string            `yaml:"timeout"`
}

type parseError struct {
	message    string
	checkNames []string
}

func (e *parseError) Error() string {
	return e.message
}

var localNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

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

	out := &File{
		Version:  *raw.Version,
		Defaults: defaults,
		Checks:   make(map[string]Check, len(raw.Checks)),
	}
	for _, name := range names {
		if !localNamePattern.MatchString(name) {
			return nil, &parseError{
				message:    fmt.Sprintf("check %q has invalid local name", name),
				checkNames: []string{name},
			}
		}
		check, err := parseCheck(name, raw.Checks[name], defaults)
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
	return Defaults{
		Image:   raw.Image,
		Timeout: timeout,
		Env:     copyEnv(raw.Env),
		Network: raw.Network,
	}, nil
}

func parseCheck(name string, raw rawCheck, defaults Defaults) (Check, error) {
	if strings.TrimSpace(raw.Run) == "" {
		return Check{}, fmt.Errorf("check %q run is required", name)
	}
	if err := validatePathList(name, "paths", raw.Paths, true); err != nil {
		return Check{}, err
	}
	if err := validatePathList(name, "include", raw.Include, false); err != nil {
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

	image := defaults.Image
	if raw.Image != "" {
		image = raw.Image
	}

	network := defaults.Network
	if raw.Network != nil {
		network = *raw.Network
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
		Paths:       normalizePathValues(raw.Paths),
		Include:     normalizePathValues(raw.Include),
		WorkingDir:  normalizePathValue(workingDir),
		Env:         env,
		Network:     network,
		Timeout:     timeout,
	}, nil
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

func sortedRawCheckNames(checks map[string]rawCheck) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

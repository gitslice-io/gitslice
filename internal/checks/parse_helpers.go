package checks

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

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
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s: %s %q contains control characters", owner, field, value)
	}
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
		if service.Image == "" {
			return nil, fmt.Errorf("service %q: image is required", name)
		}
		if err := ValidateImageRef(service.Image); err != nil {
			return nil, fmt.Errorf("service %q image: %w", name, err)
		}
		if err := ValidateEnvMap(service.Env); err != nil {
			return nil, fmt.Errorf("service %q env: %w", name, err)
		}
		if strings.ContainsRune(service.Ready, '\x00') {
			return nil, fmt.Errorf("service %q ready must not contain NUL", name)
		}
		if service.Memory != "" {
			if _, err := ParseMemoryBytes(service.Memory); err != nil {
				return nil, fmt.Errorf("service %q memory: %w", name, err)
			}
		}
		readyTimeout, err := parseOptionalDuration(fmt.Sprintf("service %q ready_timeout", name), service.ReadyTimeout)
		if err != nil {
			return nil, err
		}
		out[name] = Service{
			Image:        service.Image,
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
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s cache %q contains control characters", owner, value)
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
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("check %q %s %q contains control characters", checkName, field, value)
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

package checks

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxImageRefLen = 512

var (
	envKeyPattern        = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)
	imageTagPattern      = regexp.MustCompile(`\A[A-Za-z0-9_][A-Za-z0-9._-]{0,127}\z`)
	imageDigestPattern   = regexp.MustCompile(`\Asha256:[0-9a-f]{64}\z`)
	imageHostPattern     = regexp.MustCompile(`\A[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?\z`)
	imagePathCompPattern = regexp.MustCompile(`\A[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?\z`)
	cpuDecimalPattern    = regexp.MustCompile(`\A[0-9]+(?:\.[0-9]+)?\z`)
)

// ValidateImageRef enforces strict OCI image reference syntax and rejects
// leading dashes, path traversal segments, whitespace, and control bytes.
func ValidateImageRef(image string) error {
	if image == "" {
		return fmt.Errorf("image reference is empty")
	}
	if len(image) > maxImageRefLen {
		return fmt.Errorf("image reference exceeds %d characters", maxImageRefLen)
	}
	if strings.ContainsAny(image, " \t\r\n\v\f\x00") {
		return fmt.Errorf("image reference %q contains whitespace or control characters", image)
	}
	if strings.HasPrefix(image, "-") {
		return fmt.Errorf("image reference %q must not start with '-'", image)
	}
	if strings.Contains(image, "..") {
		return fmt.Errorf("image reference %q must not contain '..'", image)
	}

	rest := image
	if before, digest, hasDigest := strings.Cut(rest, "@"); hasDigest {
		if before == "" || !imageDigestPattern.MatchString(digest) {
			return fmt.Errorf("image reference %q has invalid digest", image)
		}
		rest = before
	}

	lastSlash := strings.LastIndexByte(rest, '/')
	tail := rest[lastSlash+1:]
	if colon := strings.IndexByte(tail, ':'); colon >= 0 {
		namePart := tail[:colon]
		tag := tail[colon+1:]
		if namePart == "" || !imageTagPattern.MatchString(tag) {
			return fmt.Errorf("image reference %q has invalid tag", image)
		}
		rest = rest[:lastSlash+1+colon]
	}

	parts := strings.Split(rest, "/")
	if len(parts) == 0 {
		return fmt.Errorf("image reference %q is missing repository name", image)
	}
	startIdx := 0
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		if err := validateImageRegistry(parts[0]); err != nil {
			return fmt.Errorf("image reference %q: %w", image, err)
		}
		startIdx = 1
	}
	for _, comp := range parts[startIdx:] {
		if !imagePathCompPattern.MatchString(comp) {
			return fmt.Errorf("image reference %q has invalid repository component %q", image, comp)
		}
	}
	return nil
}

func validateImageRegistry(authority string) error {
	host := authority
	if h, portStr, hasPort := strings.Cut(authority, ":"); hasPort {
		if h == "" || portStr == "" || strings.Contains(portStr, ":") {
			return fmt.Errorf("invalid registry authority %q", authority)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid registry port in %q", authority)
		}
		host = h
	}
	if !imageHostPattern.MatchString(host) {
		return fmt.Errorf("invalid registry host %q", host)
	}
	return nil
}

// ValidateEnvKey verifies that an environment variable name is a POSIX-safe
// identifier without delimiters or control bytes.
func ValidateEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid env variable name %q", key)
	}
	return nil
}

// ValidateEnvMap validates every environment key and ensures values cannot
// inject extra lines or NUL bytes into --env-file.
func ValidateEnvMap(env map[string]string) error {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := ValidateEnvKey(k); err != nil {
			return err
		}
		if strings.ContainsAny(env[k], "\r\n\x00") {
			return fmt.Errorf("env variable %q value must not contain CR, LF, or NUL", k)
		}
	}
	return nil
}

// ValidateSetupCommands ensures setup steps are non-empty single-line RUN
// commands without CR, LF, or NUL characters that could inject Dockerfile directives.
func ValidateSetupCommands(setup []string) error {
	for i, cmd := range setup {
		if strings.ContainsAny(cmd, "\r\n\x00") {
			return fmt.Errorf("setup[%d] must not contain CR, LF, or NUL", i)
		}
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("setup[%d] is empty", i)
		}
	}
	return nil
}

// ParseMemoryBytes parses a Docker-style memory limit (e.g. "512m", "4g") into bytes.
func ParseMemoryBytes(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("memory limit is empty")
	}
	if strings.ContainsAny(value, " \t\r\n\v\f\x00") || strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	lower := strings.ToLower(value)
	var mult int64 = 1
	numPart := lower
	switch {
	case strings.HasSuffix(lower, "gb"):
		mult = 1 << 30
		numPart = strings.TrimSuffix(lower, "gb")
	case strings.HasSuffix(lower, "g"):
		mult = 1 << 30
		numPart = strings.TrimSuffix(lower, "g")
	case strings.HasSuffix(lower, "mb"):
		mult = 1 << 20
		numPart = strings.TrimSuffix(lower, "mb")
	case strings.HasSuffix(lower, "m"):
		mult = 1 << 20
		numPart = strings.TrimSuffix(lower, "m")
	case strings.HasSuffix(lower, "kb"):
		mult = 1 << 10
		numPart = strings.TrimSuffix(lower, "kb")
	case strings.HasSuffix(lower, "k"):
		mult = 1 << 10
		numPart = strings.TrimSuffix(lower, "k")
	case strings.HasSuffix(lower, "b"):
		mult = 1
		numPart = strings.TrimSuffix(lower, "b")
	}
	if numPart == "" {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	for i := 0; i < len(numPart); i++ {
		if numPart[i] < '0' || numPart[i] > '9' {
			return 0, fmt.Errorf("invalid memory limit %q", value)
		}
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	return n * mult, nil
}

// FormatMemoryBytes formats a byte count using the largest exact binary suffix.
func FormatMemoryBytes(bytes int64) string {
	switch {
	case bytes > 0 && bytes%(1<<30) == 0:
		return strconv.FormatInt(bytes>>30, 10) + "g"
	case bytes > 0 && bytes%(1<<20) == 0:
		return strconv.FormatInt(bytes>>20, 10) + "m"
	case bytes > 0 && bytes%(1<<10) == 0:
		return strconv.FormatInt(bytes>>10, 10) + "k"
	default:
		return strconv.FormatInt(bytes, 10) + "b"
	}
}

// ParseCPUs parses a positive decimal CPU count (e.g. "0.5", "2", "4").
func ParseCPUs(value string) (float64, error) {
	if value == "" {
		return 0, fmt.Errorf("cpus limit is empty")
	}
	if !cpuDecimalPattern.MatchString(value) {
		return 0, fmt.Errorf("invalid cpus limit %q", value)
	}
	cpus, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus <= 0 {
		return 0, fmt.Errorf("invalid cpus limit %q", value)
	}
	return cpus, nil
}

// FormatCPUs formats a positive CPU limit without trailing zeros.
func FormatCPUs(cpus float64) string {
	return strconv.FormatFloat(cpus, 'f', -1, 64)
}

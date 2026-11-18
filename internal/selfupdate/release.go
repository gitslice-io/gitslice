// Package selfupdate implements release discovery, Ed25519/SHA-256 verification,
// archive extraction, and atomic in-place binary replacement for `gs upgrade`.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultReleasesBase = "https://gitslice.io/releases"
	maxReleaseAsset     = 200 << 20
	maxReleaseSigBytes  = 4096
)

var (
	maxReleaseAssetBytes int64 = maxReleaseAsset
	releaseTagPattern          = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	// ReleasePublicKey is an optional hex- or base64-encoded Ed25519 public key
	// compiled into release builds (overridable via GS_RELEASE_PUBLIC_KEY).
	ReleasePublicKey string
)

// Error represents a structured self-update failure carrying a machine-readable
// error code and optional remediation hint.
type Error struct {
	Code    string
	Message string
	Hint    string
}

func (e *Error) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func (e *Error) UserError() (string, string, string) {
	return e.Code, e.Message, e.Hint
}

// ValidateReleaseTag verifies that tag matches vMAJOR.MINOR.PATCH(-prerelease)?.
func ValidateReleaseTag(tag string) error {
	if !releaseTagPattern.MatchString(tag) {
		return fmt.Errorf("tag %q must match vMAJOR.MINOR.PATCH", tag)
	}
	return nil
}

// ValidateReleaseBaseURL checks that raw is a valid HTTPS URL (or HTTP on loopback).
func ValidateReleaseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.User != nil {
		return nil, errors.New("credentials are not allowed in release URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("missing host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return u, nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return u, nil
		}
		return nil, errors.New("https is required for non-loopback release hosts")
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewReleaseHTTPClient creates an HTTP client that forbids protocol downgrades
// and non-loopback plaintext redirects.
func NewReleaseHTTPClient(timeout time.Duration, base *url.URL) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL == nil || req.URL.Host == "" || req.URL.Hostname() == "" {
				return errors.New("redirect target missing host")
			}
			scheme := strings.ToLower(req.URL.Scheme)
			if scheme != "https" && scheme != "http" {
				return fmt.Errorf("unsupported redirect scheme %q", req.URL.Scheme)
			}
			if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && scheme != "https" {
				return errors.New("refusing HTTPS to HTTP redirect downgrade")
			}
			if base != nil && strings.EqualFold(base.Scheme, "https") && scheme != "https" {
				return errors.New("refusing non-HTTPS redirect from HTTPS release base")
			}
			if scheme == "http" && !isLoopbackHost(req.URL.Hostname()) {
				return errors.New("refusing plaintext HTTP redirect to non-loopback host")
			}
			return nil
		},
	}
}

// LatestReleaseTag follows <base>/latest and extracts the target release tag.
func LatestReleaseTag(ctx context.Context, client *http.Client, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, base+"/latest", nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	if res.StatusCode >= 400 {
		return "", fmt.Errorf("%s/latest: %s", base, res.Status)
	}
	final := res.Request.URL.Path
	dir, tag := path.Split(strings.TrimRight(final, "/"))
	if !strings.HasSuffix(dir, "/tag/") || tag == "" {
		return "", fmt.Errorf("%s/latest did not lead to a release (ended at %s)", base, res.Request.URL)
	}
	if err := ValidateReleaseTag(tag); err != nil {
		return "", fmt.Errorf("%s/latest returned invalid release tag %q: %w", base, tag, err)
	}
	return tag, nil
}

// ReleaseAsset returns the expected archive filename for goos/goarch.
func ReleaseAsset(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin", "windows":
	default:
		return "", fmt.Errorf("no gs release for %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("no gs release for %s/%s", goos, goarch)
	}
	if goos == "windows" {
		return "gs_" + goos + "_" + goarch + ".zip", nil
	}
	return "gs_" + goos + "_" + goarch + ".tar.gz", nil
}

// ResolveBinaryTarget resolves the executable path to replace (with symlinks evaluated).
func ResolveBinaryTarget(requested string) (string, error) {
	binary := strings.TrimSpace(requested)
	if binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("find this gs: %w", err)
		}
		binary = exe
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", binary, err)
	}
	return resolved, nil
}

// BinaryVersion queries an existing `gs` binary for its version via `version --json`.
func BinaryVersion(ctx context.Context, binary string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version", "--json").Output()
	if err != nil {
		return "unknown"
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &info); err != nil || strings.TrimSpace(info.Version) == "" {
		return "unknown"
	}
	return info.Version
}

// SameVersion compares release tags, ignoring a leading "v".
func SameVersion(a, b string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	return norm(a) != "" && norm(a) == norm(b)
}

// NewerVersion reports whether a is a later release than b.
func NewerVersion(a, b string) bool {
	pa, okA := ParseVersion(a)
	pb, okB := ParseVersion(b)
	if !okA {
		return false
	}
	if !okB {
		return true
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// ParseVersion parses a semantic version triplet (vMAJOR.MINOR.PATCH).
func ParseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if cut := strings.IndexAny(part, "-+"); cut >= 0 {
			part = part[:cut]
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

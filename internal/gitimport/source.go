package gitimport

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/outbound"
)

var (
	sourceSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// ResolvePublicHost is a test seam for DNS public-IP validation on custom
	// import hosts.
	ResolvePublicHost = outbound.ResolvePublic
)

var defaultAllowedHosts = map[string]struct{}{
	"github.com":    {},
	"gitlab.com":    {},
	"bitbucket.org": {},
}

// ValidateSource normalizes and validates a user-supplied git import source.
// It returns the canonical clone target, whether that target is a local
// filesystem path, and an error describing why the source is rejected.
//
// Remote sources are restricted to owner/repo shorthand or HTTPS URLs on an
// allowlisted host so an authenticated caller cannot make the server clone
// internal hosts (SSRF) or use git's file/ext/ssh transports. Local filesystem
// paths are rejected unless GITSLICE_IMPORT_ALLOW_LOCAL is enabled.
func ValidateSource(source string) (string, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false, fmt.Errorf("git import source is required; use owner/repo or an HTTPS URL on an allowed host")
	}
	if strings.HasPrefix(source, "-") {
		return "", false, fmt.Errorf("git import source must not start with '-'")
	}

	if strings.Count(source, "/") == 1 &&
		!strings.Contains(source, "://") &&
		!strings.Contains(source, "@") &&
		!strings.HasPrefix(source, "/") &&
		!strings.HasPrefix(source, ".") {
		segments := strings.Split(source, "/")
		if sourceSegmentPattern.MatchString(segments[0]) && sourceSegmentPattern.MatchString(segments[1]) {
			return "https://github.com/" + strings.TrimSuffix(source, ".git") + ".git", false, nil
		}
	}

	if localPath, ok := LocalImportPath(source); ok {
		if !localImportAllowed() {
			return "", false, fmt.Errorf("git import from local paths is disabled; use owner/repo or an HTTPS URL on an allowed host")
		}
		return localPath, true, nil
	}

	parsed, err := url.Parse(source)
	if err != nil {
		return "", false, fmt.Errorf("git import source must be owner/repo or a valid HTTPS URL on an allowed host")
	}
	if parsed.Scheme != "https" {
		return "", false, fmt.Errorf("git import source must use HTTPS; owner/repo shorthand is also allowed")
	}
	if parsed.User != nil {
		return "", false, fmt.Errorf("git import source must not include credentials; use an HTTPS URL on an allowed host")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", false, fmt.Errorf("git import source must use port 443 or no explicit port; use an HTTPS URL on an allowed host")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if _, ok := allowedImportHosts()[hostname]; !ok {
		return "", false, fmt.Errorf("git import source host is not allowed; use owner/repo or an HTTPS URL on an allowed host")
	}
	if _, builtin := defaultAllowedHosts[hostname]; !builtin && !localImportAllowed() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addr, err := ResolvePublicHost(ctx, hostname, false)
		if err != nil || !addr.IsValid() {
			return "", false, fmt.Errorf("git import source host %q is not a public host", hostname)
		}
	}

	return parsed.String(), false, nil
}

// LocalImportPath reports whether source names a local filesystem path (a bare
// absolute path or a file:// URL) and returns the cleaned absolute path.
func LocalImportPath(source string) (string, bool) {
	if strings.HasPrefix(source, "file://") {
		parsed, err := url.Parse(source)
		if err != nil || parsed.Path == "" || !filepath.IsAbs(parsed.Path) {
			return "", false
		}
		return filepath.Clean(parsed.Path), true
	}
	if strings.HasPrefix(source, "/") {
		return filepath.Clean(source), true
	}
	return "", false
}

func localImportAllowed() bool {
	value := strings.TrimSpace(os.Getenv("GITSLICE_IMPORT_ALLOW_LOCAL"))
	return value == "1" || strings.EqualFold(value, "true")
}

func allowedImportHosts() map[string]struct{} {
	hosts := make(map[string]struct{}, len(defaultAllowedHosts))
	for host := range defaultAllowedHosts {
		hosts[host] = struct{}{}
	}
	for _, host := range strings.Split(os.Getenv("GITSLICE_IMPORT_ALLOWED_HOSTS"), ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	return hosts
}

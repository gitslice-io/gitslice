package service

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var importSourceSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateImportSource normalizes and validates a user-supplied git import
// source. It returns the canonical clone target, whether that target is a local
// filesystem path, and an error describing why the source is rejected.
//
// Remote sources are restricted to owner/repo shorthand or HTTPS URLs on an
// allowlisted host so an authenticated caller cannot make the server clone
// internal hosts (SSRF) or use git's file/ext/ssh transports. Local filesystem
// paths are rejected unless the operator opts in with GITSLICE_IMPORT_ALLOW_LOCAL,
// which is meant for single-tenant/self-hosted use and stays off in the hosted
// multi-tenant deployment.
func validateImportSource(source string) (string, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false, fmt.Errorf("git import source is required; use owner/repo or an HTTPS URL on an allowed host")
	}

	if strings.Count(source, "/") == 1 &&
		!strings.Contains(source, "://") &&
		!strings.Contains(source, "@") &&
		!strings.HasPrefix(source, "/") &&
		!strings.HasPrefix(source, ".") {
		segments := strings.Split(source, "/")
		if importSourceSegmentPattern.MatchString(segments[0]) && importSourceSegmentPattern.MatchString(segments[1]) {
			return "https://github.com/" + strings.TrimSuffix(source, ".git") + ".git", false, nil
		}
	}

	if localPath, ok := localImportPath(source); ok {
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

	return parsed.String(), false, nil
}

// localImportPath reports whether source names a local filesystem path (a bare
// absolute path or a file:// URL) and returns the cleaned absolute path. It does
// not consult the opt-in flag; callers gate on localImportAllowed separately so
// a disabled local import still reports a clear, path-specific error.
func localImportPath(source string) (string, bool) {
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
	hosts := map[string]struct{}{
		"github.com":    {},
		"gitlab.com":    {},
		"bitbucket.org": {},
	}
	for _, host := range strings.Split(os.Getenv("GITSLICE_IMPORT_ALLOWED_HOSTS"), ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	return hosts
}

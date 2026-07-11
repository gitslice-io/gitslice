package service

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var importSourceSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateImportSource normalizes and validates a user-supplied git import
// source. It returns the canonical clone URL or an error describing why the
// source is rejected.
func validateImportSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("git import source is required; use owner/repo or an HTTPS URL on an allowed host")
	}

	if strings.Count(source, "/") == 1 &&
		!strings.Contains(source, "://") &&
		!strings.Contains(source, "@") &&
		!strings.HasPrefix(source, "/") &&
		!strings.HasPrefix(source, ".") {
		segments := strings.Split(source, "/")
		if importSourceSegmentPattern.MatchString(segments[0]) && importSourceSegmentPattern.MatchString(segments[1]) {
			return "https://github.com/" + strings.TrimSuffix(source, ".git") + ".git", nil
		}
	}

	parsed, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("git import source must be owner/repo or a valid HTTPS URL on an allowed host")
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("git import source must use HTTPS; owner/repo shorthand is also allowed")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("git import source must not include credentials; use an HTTPS URL on an allowed host")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", fmt.Errorf("git import source must use port 443 or no explicit port; use an HTTPS URL on an allowed host")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if _, ok := allowedImportHosts()[hostname]; !ok {
		return "", fmt.Errorf("git import source host is not allowed; use owner/repo or an HTTPS URL on an allowed host")
	}

	return parsed.String(), nil
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

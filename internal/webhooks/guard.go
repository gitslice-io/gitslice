package webhooks

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Webhook URLs are chosen by slice admins, so deliveries must not reach the
// server's own network: loopback, private ranges, link-local (which includes
// the cloud metadata server) and other non-public addresses are refused,
// both when a URL is saved and, after DNS resolution, when it is dialed (so a
// name that later resolves to an internal address is caught too).

const maxURLLength = 2048

// errBlockedAddress marks a delivery that went to a non-public address.
var errBlockedAddress = errors.New("the webhook's address is not a public internet address")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, cidr := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved
		"64:ff9b::/96",    // NAT64, which can reach IPv4 private ranges
		"2001:db8::/32",   // documentation
	} {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}()

// publicAddress reports whether ip is an ordinary public unicast address.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateURL checks a webhook URL when it is saved. allowPrivate permits
// http and non-public hosts (tests and local development).
func ValidateURL(raw string, allowPrivate bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("a URL is required")
	}
	if len(raw) > maxURLLength {
		return "", fmt.Errorf("the URL is longer than %d characters", maxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a URL: %v", err)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate:
	default:
		return "", errors.New("the URL must start with https://")
	}
	if u.User != nil {
		return "", errors.New("put credentials in the secret or the query, not in the URL's user part")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("the URL has no host")
	}
	if !allowPrivate {
		if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") ||
			strings.HasSuffix(strings.ToLower(host), ".internal") {
			return "", errBlockedAddress
		}
		if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) {
			return "", errBlockedAddress
		}
	}
	u.Fragment = ""
	return u.String(), nil
}

// newClient returns the HTTP client deliveries use: no proxy from the
// environment, no redirects, a 10 second limit, and (unless allowPrivate) a
// dialer that refuses non-public addresses.
func newClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddress(ip) {
				return errBlockedAddress
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		// A webhook answers where it was registered; a redirect is a failure.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// isBlocked reports whether err came from refusing a non-public address.
func isBlocked(err error) bool {
	return errors.Is(err, errBlockedAddress)
}

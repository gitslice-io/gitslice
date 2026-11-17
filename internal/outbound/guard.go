// Package outbound protects server-initiated HTTP requests and signs them.
package outbound

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// Outbound URLs must not reach the server's own network: loopback, private
// ranges, link-local (which includes the cloud metadata server) and other
// non-public addresses are refused, both when a URL is saved and, after DNS
// resolution, when it is dialed (so a name that later resolves to an internal
// address is caught too).

const maxURLLength = 2048

// errBlockedAddress marks a request that went to a non-public address.
var errBlockedAddress = errors.New("the target address is not a public internet address")

// SignatureHeader carries a SHA-256 HMAC signature of an outbound body.
const SignatureHeader = "X-Gitslice-Signature-256"

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
		"::/96",           // deprecated IPv4-compatible IPv6
		"64:ff9b::/96",    // NAT64 well-known prefix
		"64:ff9b:1::/48",  // NAT64 local-use translation prefix
		"100::/64",        // discard-only prefix (RFC 6666)
		"2001::/23",       // IETF protocol assignments (includes Teredo 2001::/32)
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4 relay / embedded IPv4
	} {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}()

// publicAddress reports whether ip is an ordinary public unicast address.
func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	unmapped := ip.Unmap()
	if unmapped != ip {
		if !unmapped.IsValid() || unmapped.IsLoopback() || unmapped.IsPrivate() || unmapped.IsLinkLocalUnicast() || unmapped.IsLinkLocalMulticast() ||
			unmapped.IsMulticast() || unmapped.IsUnspecified() || unmapped.IsInterfaceLocalMulticast() {
			return false
		}
		for _, prefix := range blockedPrefixes {
			if prefix.Contains(unmapped) {
				return false
			}
		}
	}
	return true
}

// ValidateURL checks an outbound URL. allowPrivate permits http and
// non-public hosts (tests and local development).
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

// NewClient returns an HTTP client that only dials public addresses unless
// allowPrivate, follows no redirects, and has a 10 second request limit.
func NewClient(allowPrivate bool) *http.Client {
	return newClient(allowPrivate)
}

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
		// The caller's target answers directly; a redirect is a failure.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// isBlocked reports whether err came from refusing a non-public address.
func isBlocked(err error) bool {
	return errors.Is(err, errBlockedAddress)
}

// ResolvePublic resolves host and returns one of its addresses, refusing a
// host with any non-public address unless allowPrivate. Callers that cannot
// use NewClient's dialer (git, for Git mirrors) pin the connection to the
// returned address.
func ResolvePublic(ctx context.Context, host string, allowPrivate bool) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && !publicAddress(ip) {
			return netip.Addr{}, errBlockedAddress
		}
		return ip, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range addrs {
		if !allowPrivate && !publicAddress(ip) {
			return netip.Addr{}, errBlockedAddress
		}
	}
	return addrs[0].Unmap(), nil
}

// Sign returns the signature header value for body: "sha256=" and the hex
// HMAC-SHA256 of the body keyed by secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature (the header value) matches body.
func Verify(secret string, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(strings.TrimSpace(signature)))
}

// DescribeSendError says why a request failed, and whether retrying cannot
// help because the target was refused.
func DescribeSendError(err error) (string, bool) {
	if isBlocked(err) {
		return errBlockedAddress.Error(), true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "the endpoint did not answer within 10 seconds", false
		}
		return urlErr.Err.Error(), false
	}
	return err.Error(), false
}

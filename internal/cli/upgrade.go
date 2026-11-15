package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// gs upgrade replaces the running gs with a release build, the way install.sh
// installs one: the same downloads under https://gitslice.io/releases, the same
// checksum check. GS_DOWNLOAD_BASE points it at another copy of the releases
// (a mirror, or a test server), as it does for install.sh.

const (
	defaultReleasesBase = "https://gitslice.io/releases"
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

type upgradeOutput struct {
	Current  string `json:"current"`
	Latest   string `json:"latest"`
	Target   string `json:"target"`
	Path     string `json:"path,omitempty"`
	Upgraded bool   `json:"upgraded"`
	// UpToDate is true when no newer release exists (or the requested one is
	// already installed).
	UpToDate bool `json:"up_to_date"`
}

func (r Runner) upgradeCommand(opts *commandOptions) *cobra.Command {
	var check, force bool
	var version, target string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade gs to the latest release (or --version)",
		Long: "Download the latest gs release for this platform from https://gitslice.io/releases, " +
			"check it against the release checksums, and replace this gs with it. " +
			"--check only reports whether a newer release exists.",
		Args: noArgs("gs upgrade [--check] [--version <tag>] [--force]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runUpgrade(cmd.Context(), *opts, upgradeRequest{check: check, force: force, version: version, path: target})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report the installed and latest versions")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if the version is already installed")
	cmd.Flags().StringVar(&version, "version", "", "release tag to install, such as v0.4.0 (default: the latest)")
	cmd.Flags().StringVar(&target, "path", "", "gs binary to replace (default: this one)")
	return cmd
}

type upgradeRequest struct {
	check, force  bool
	version, path string
}

func (r Runner) runUpgrade(ctx context.Context, opts commandOptions, req upgradeRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	requestedTag := strings.TrimSpace(req.version)
	if requestedTag != "" {
		if err := validateReleaseTag(requestedTag); err != nil {
			return userError("invalid_version", fmt.Sprintf("invalid release version %q: %v", requestedTag, err), "Pass a release tag such as v0.4.0.")
		}
	}
	base := strings.TrimRight(firstNonEmpty(os.Getenv("GS_DOWNLOAD_BASE"), defaultReleasesBase), "/")
	baseURL, err := validateReleaseBaseURL(base)
	if err != nil {
		return userError("invalid_download_base", fmt.Sprintf("invalid release download base %q: %v", base, err), "Use an https:// release base URL.")
	}
	client := newReleaseHTTPClient(2*time.Minute, baseURL)

	binary, err := r.upgradeTarget(req.path)
	if err != nil {
		return err
	}
	// The version being replaced: this gs's, or with --path that binary's own.
	current := cliVersionInfo().Version
	if strings.TrimSpace(req.path) != "" {
		current = binaryVersion(ctx, binary)
	}
	out := upgradeOutput{Current: current, Path: binary}
	latest, err := latestReleaseTag(ctx, client, base)
	if err != nil {
		return userError("release_lookup_failed", fmt.Sprintf("could not find the latest release: %v", err), "Check your network, or pass --version <tag>.")
	}
	out.Latest = latest
	out.Target = firstNonEmpty(requestedTag, latest)
	if err := validateReleaseTag(out.Target); err != nil {
		return userError("invalid_version", fmt.Sprintf("invalid target release tag %q: %v", out.Target, err), "")
	}
	// A build newer than the latest release (a source build) counts as up to
	// date unless a version was asked for.
	out.UpToDate = sameVersion(out.Current, out.Target) || (requestedTag == "" && newerVersion(out.Current, latest))

	if req.check || (out.UpToDate && !req.force) {
		return r.writeUpgrade(opts, out, req.check)
	}
	// Homebrew owns a gs it installed; replacing it here would leave Homebrew
	// out of step.
	if strings.Contains(filepath.ToSlash(binary), "/Cellar/gitslice/") {
		return userError("installed_by_homebrew", "this gs was installed with Homebrew ("+binary+")", "Run brew upgrade gitslice.")
	}

	if err := installRelease(ctx, client, base, out.Target, binary); err != nil {
		return err
	}
	out.Upgraded = true
	out.UpToDate = sameVersion(out.Target, out.Latest)
	return r.writeUpgrade(opts, out, false)
}

func (r Runner) writeUpgrade(opts commandOptions, out upgradeOutput, check bool) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	switch {
	case out.Upgraded:
		verb := "upgraded"
		if sameVersion(out.Current, out.Target) {
			verb = "reinstalled"
		} else if newerVersion(out.Current, out.Target) {
			verb = "downgraded"
		}
		fmt.Fprintf(r.Stdout, "%s gs %s -> %s (%s)\n", verb, out.Current, out.Target, out.Path)
	case check && out.UpToDate:
		fmt.Fprintf(r.Stdout, "gs %s is the latest release\n", out.Current)
	case check:
		fmt.Fprintf(r.Stdout, "gs %s is installed; %s is available. Run %s.\n", out.Current, out.Latest, upgradeHint())
	default:
		fmt.Fprintf(r.Stdout, "gs %s is already installed\n", out.Current)
	}
	return nil
}

// upgradeTarget is the gs binary to replace: --path, else the running one,
// with symlinks resolved so the link stays and its target is replaced.
func (r Runner) upgradeTarget(requested string) (string, error) {
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

// binaryVersion asks a gs binary for its version, or says it could not.
func binaryVersion(ctx context.Context, binary string) string {
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

func validateReleaseTag(tag string) error {
	if !releaseTagPattern.MatchString(tag) {
		return fmt.Errorf("tag %q must match vMAJOR.MINOR.PATCH", tag)
	}
	return nil
}

func validateReleaseBaseURL(raw string) (*url.URL, error) {
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

func newReleaseHTTPClient(timeout time.Duration, base *url.URL) *http.Client {
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

// latestReleaseTag follows <base>/latest, which redirects to the newest
// release's page (…/releases/tag/<tag>), and reads the tag from where it ends.
func latestReleaseTag(ctx context.Context, client *http.Client, base string) (string, error) {
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
	if err := validateReleaseTag(tag); err != nil {
		return "", fmt.Errorf("%s/latest returned invalid release tag %q: %w", base, tag, err)
	}
	return tag, nil
}

// releaseAsset is the archive install.sh and the release workflow use for this
// platform.
func releaseAsset(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
	case "windows":
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

func installRelease(ctx context.Context, client *http.Client, base, tag, binary string) error {
	if err := validateReleaseTag(tag); err != nil {
		return userError("invalid_version", fmt.Sprintf("invalid release tag %q: %v", tag, err), "")
	}
	pubKey, requireSig, err := configuredReleasePublicKey()
	if err != nil {
		return userError("invalid_release_key", fmt.Sprintf("invalid release public key: %v", err), "")
	}
	asset, err := releaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return userError("unsupported_platform", err.Error(), "Build gs from source: go install gitslice.io/gitslice/cmd/gs@latest")
	}
	dir := base + "/download/" + tag
	sums, err := download(ctx, client, dir+"/checksums.txt")
	if err != nil {
		return userError("download_failed", fmt.Sprintf("download %s checksums: %v", tag, err), "")
	}
	if requireSig {
		rawSig, err := downloadBounded(ctx, client, dir+"/checksums.txt.sig", maxReleaseSigBytes)
		if err != nil {
			return userError("signature_mismatch", fmt.Sprintf("download %s checksums signature: %v", tag, err), "The release checksums signature could not be verified; nothing was installed.")
		}
		if err := verifyReleaseChecksumsSignature(pubKey, sums, rawSig); err != nil {
			return userError("signature_mismatch", err.Error(), "The release checksums signature could not be verified; nothing was installed.")
		}
	}
	archive, err := download(ctx, client, dir+"/"+asset)
	if err != nil {
		return userError("download_failed", fmt.Sprintf("download %s %s: %v", tag, asset, err), "Check the version with gs upgrade --check.")
	}
	if err := verifyChecksum(sums, asset, archive); err != nil {
		return userError("checksum_mismatch", err.Error(), "The download was damaged or tampered with; nothing was installed.")
	}
	exe, err := extractGS(asset, archive)
	if err != nil {
		return userError("bad_release", err.Error(), "")
	}
	return replaceBinary(binary, exe, tag)
}

func configuredReleasePublicKey() (ed25519.PublicKey, bool, error) {
	raw := firstNonEmpty(strings.TrimSpace(os.Getenv("GS_RELEASE_PUBLIC_KEY")), strings.TrimSpace(ReleasePublicKey))
	if raw == "" {
		return nil, false, nil
	}
	decoded, err := decodeHexOrBase64(raw)
	if err != nil {
		return nil, false, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, false, fmt.Errorf("expected %d-byte Ed25519 public key, got %d bytes", ed25519.PublicKeySize, len(decoded))
	}
	return ed25519.PublicKey(decoded), true, nil
}

func verifyReleaseChecksumsSignature(pubKey ed25519.PublicKey, sums, rawSig []byte) error {
	sig, err := parseReleaseSignature(rawSig)
	if err != nil {
		return fmt.Errorf("invalid checksums.txt.sig: %w", err)
	}
	if !ed25519.Verify(pubKey, sums, sig) {
		return errors.New("checksums.txt signature verification failed")
	}
	return nil
}

func parseReleaseSignature(raw []byte) ([]byte, error) {
	if len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty signature")
	}
	decoded, err := decodeHexOrBase64(trimmed)
	if err != nil {
		return nil, err
	}
	if len(decoded) != ed25519.SignatureSize {
		return nil, fmt.Errorf("expected %d-byte Ed25519 signature, got %d bytes", ed25519.SignatureSize, len(decoded))
	}
	return decoded, nil
}

func decodeHexOrBase64(s string) ([]byte, error) {
	if decoded, err := hex.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return nil, errors.New("must be hex or base64 encoded")
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	return downloadBounded(ctx, client, url, maxReleaseAssetBytes)
}

func downloadBounded(ctx context.Context, client *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("download is too large")
	}
	return data, nil
}

// verifyChecksum checks data against its line in a sha256sum-style
// checksums.txt.
func verifyChecksum(sums []byte, asset string, data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			sum := sha256.Sum256(data)
			if !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
				return fmt.Errorf("checksum mismatch for %s", asset)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum for %s", asset)
}

// extractGS returns the gs (or gs.exe) executable from a release archive.
func extractGS(asset string, archive []byte) ([]byte, error) {
	limit := maxReleaseAssetBytes
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == "gs.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				data, err := io.ReadAll(io.LimitReader(rc, limit+1))
				if err != nil {
					return nil, err
				}
				if int64(len(data)) > limit {
					return nil, fmt.Errorf("release binary in %s exceeds maximum size", asset)
				}
				return data, nil
			}
		}
		return nil, fmt.Errorf("%s has no gs.exe", asset)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s has no gs", asset)
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == "gs" {
			data, err := io.ReadAll(io.LimitReader(tr, limit+1))
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > limit {
				return nil, fmt.Errorf("release binary in %s exceeds maximum size", asset)
			}
			return data, nil
		}
	}
}

// replaceBinary writes the new gs next to the old one, checks that it runs and
// reports the expected version, and then renames it into place, so a failure
// at any step leaves the old gs untouched. Windows will not replace a running
// executable, so there the old one is moved aside first.
func replaceBinary(binary string, exe []byte, tag string) error {
	dir := filepath.Dir(binary)
	tmp, err := os.CreateTemp(dir, ".gs-upgrade-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return userError("permission_denied", fmt.Sprintf("cannot write to %s", dir), "Run gs upgrade as the user that installed gs, or reinstall with GS_INSTALL_DIR set (see https://gitslice.io/install.sh).")
		}
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(exe); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := checkNewBinary(tmpPath, tag); err != nil {
		return userError("bad_release", err.Error(), "Nothing was installed.")
	}
	if runtime.GOOS == "windows" {
		old := binary + ".old"
		_ = os.Remove(old)
		if err := os.Rename(binary, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tmpPath, binary)
}

// checkNewBinary runs the downloaded gs once: it must start and report the tag
// it was released as.
func checkNewBinary(binary, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("the downloaded gs does not run: %v", err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("the downloaded gs printed an unexpected version: %v", err)
	}
	if !sameVersion(info.Version, tag) {
		return fmt.Errorf("the downloaded gs is %s, not %s", info.Version, tag)
	}
	return nil
}

// sameVersion compares release tags, ignoring a leading "v".
func sameVersion(a, b string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	return norm(a) != "" && norm(a) == norm(b)
}

// newerVersion reports whether a is a later release than b (vMAJOR.MINOR.PATCH;
// anything else, such as "dev", is older than every release).
func newerVersion(a, b string) bool {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
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

func parseVersion(v string) ([3]int, bool) {
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

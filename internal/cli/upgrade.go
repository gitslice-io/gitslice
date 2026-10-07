package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	base := strings.TrimRight(firstNonEmpty(os.Getenv("GS_DOWNLOAD_BASE"), defaultReleasesBase), "/")
	client := &http.Client{Timeout: 2 * time.Minute}

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
	out.Target = firstNonEmpty(strings.TrimSpace(req.version), latest)
	// A build newer than the latest release (a source build) counts as up to
	// date unless a version was asked for.
	out.UpToDate = sameVersion(out.Current, out.Target) || (req.version == "" && newerVersion(out.Current, latest))

	if req.check || (out.UpToDate && !req.force) {
		return r.writeUpgrade(opts, out, req.check)
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
		fmt.Fprintf(r.Stdout, "gs %s is installed; %s is available. Run gs upgrade.\n", out.Current, out.Latest)
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
	asset, err := releaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return userError("unsupported_platform", err.Error(), "Build gs from source: go install gitslice.io/gitslice/cmd/gs@latest")
	}
	dir := base + "/download/" + tag
	archive, err := download(ctx, client, dir+"/"+asset)
	if err != nil {
		return userError("download_failed", fmt.Sprintf("download %s %s: %v", tag, asset, err), "Check the version with gs upgrade --check.")
	}
	sums, err := download(ctx, client, dir+"/checksums.txt")
	if err != nil {
		return userError("download_failed", fmt.Sprintf("download %s checksums: %v", tag, err), "")
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

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
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
	data, err := io.ReadAll(io.LimitReader(res.Body, maxReleaseAsset+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReleaseAsset {
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
				return io.ReadAll(io.LimitReader(rc, maxReleaseAsset))
			}
		}
		return nil, fmt.Errorf("%s has no gs.exe", asset)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
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
			return io.ReadAll(io.LimitReader(tr, maxReleaseAsset))
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

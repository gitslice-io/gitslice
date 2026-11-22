package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/selfupdate"
)

// ReleasePublicKey is an optional hex- or base64-encoded Ed25519 public key
// compiled into release builds (overridable via GS_RELEASE_PUBLIC_KEY).
var ReleasePublicKey string

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
		if err := selfupdate.ValidateReleaseTag(requestedTag); err != nil {
			return userError("invalid_version", fmt.Sprintf("invalid release version %q: %v", requestedTag, err), "Pass a release tag such as v0.4.0.")
		}
	}
	base := strings.TrimRight(firstNonEmpty(os.Getenv("GS_DOWNLOAD_BASE"), selfupdate.DefaultReleasesBase), "/")
	baseURL, err := selfupdate.ValidateReleaseBaseURL(base)
	if err != nil {
		return userError("invalid_download_base", fmt.Sprintf("invalid release download base %q: %v", base, err), "Use an https:// release base URL.")
	}
	client := selfupdate.NewReleaseHTTPClient(2*time.Minute, baseURL)

	binary, err := selfupdate.ResolveBinaryTarget(req.path)
	if err != nil {
		return err
	}
	current := cliVersionInfo().Version
	if strings.TrimSpace(req.path) != "" {
		current = selfupdate.BinaryVersion(ctx, binary)
	}
	out := upgradeOutput{Current: current, Path: binary}
	latest, err := selfupdate.LatestReleaseTag(ctx, client, base)
	if err != nil {
		return userError("release_lookup_failed", fmt.Sprintf("could not find the latest release: %v", err), "Check your network, or pass --version <tag>.")
	}
	out.Latest = latest
	out.Target = firstNonEmpty(requestedTag, latest)
	if err := selfupdate.ValidateReleaseTag(out.Target); err != nil {
		return userError("invalid_version", fmt.Sprintf("invalid target release tag %q: %v", out.Target, err), "")
	}
	out.UpToDate = selfupdate.SameVersion(out.Current, out.Target) || (requestedTag == "" && selfupdate.NewerVersion(out.Current, latest))

	if req.check || (out.UpToDate && !req.force) {
		return r.writeUpgrade(opts, out, req.check)
	}
	if strings.Contains(filepath.ToSlash(binary), "/Cellar/gitslice/") {
		return userError("installed_by_homebrew", "this gs was installed with Homebrew ("+binary+"), which gs no longer supports", homebrewMoveHint)
	}

	if err := selfupdate.InstallRelease(ctx, client, base, out.Target, binary, ReleasePublicKey); err != nil {
		var uerr *selfupdate.Error
		if errors.As(err, &uerr) {
			return userError(uerr.Code, uerr.Message, uerr.Hint)
		}
		return err
	}
	out.Upgraded = true
	out.UpToDate = selfupdate.SameVersion(out.Target, out.Latest)
	return r.writeUpgrade(opts, out, false)
}

func (r Runner) writeUpgrade(opts commandOptions, out upgradeOutput, check bool) error {
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	switch {
	case out.Upgraded:
		verb := "upgraded"
		if selfupdate.SameVersion(out.Current, out.Target) {
			verb = "reinstalled"
		} else if selfupdate.NewerVersion(out.Current, out.Target) {
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

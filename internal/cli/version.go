package cli

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version     = "dev"
	BuildCommit = ""
	BuildDate   = ""
)

type versionOutput struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	GoVersion string `json:"go_version"`
	Dirty     bool   `json:"dirty"`
}

func (r Runner) runVersion(ctx context.Context, opts commandOptions) error {
	out := cliVersionInfo()
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "gs version %s\n", out.Version)
	if out.Commit != "" {
		fmt.Fprintf(r.Stdout, "commit: %s\n", out.Commit)
	}
	if out.BuildDate != "" {
		fmt.Fprintf(r.Stdout, "built: %s\n", out.BuildDate)
	}
	fmt.Fprintf(r.Stdout, "go: %s\n", out.GoVersion)
	if out.Dirty {
		fmt.Fprintln(r.Stdout, "dirty: true")
	}
	// A release build says when a newer release is out (source builds have
	// no release to compare; --json stays offline).
	if _, ok := parseVersion(out.Version); ok {
		if latest := r.latestKnownRelease(ctx); latest != "" && newerVersion(latest, out.Version) {
			fmt.Fprintf(r.Stdout, "update: %s is available; run %s\n", latest, upgradeHint())
		}
	}
	return nil
}

func cliVersionInfo() versionOutput {
	out := versionOutput{
		Version:   Version,
		Commit:    BuildCommit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if (out.Version == "" || out.Version == "dev") && info.Main.Version != "" && info.Main.Version != "(devel)" {
			out.Version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if out.Commit == "" {
					out.Commit = setting.Value
				}
			case "vcs.time":
				if out.BuildDate == "" {
					out.BuildDate = setting.Value
				}
			case "vcs.modified":
				out.Dirty = setting.Value == "true"
			}
		}
	}
	if out.Version == "" {
		out.Version = "dev"
	}
	return out
}

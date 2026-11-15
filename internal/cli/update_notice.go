package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Once a day, a command also looks up the latest gs release, and when it is
// newer than this gs, says so on stderr after the command's own output. The
// lookup runs alongside the command and is dropped if it is slow, so it never
// holds a command up. Builds without a release version (source builds),
// --json, --quiet and GS_NO_UPDATE_CHECK=1 skip it.

const (
	updateCheckInterval = 24 * time.Hour
	updateCheckTimeout  = 3 * time.Second
	// updateCheckGrace is how long a command that has finished waits for a
	// lookup still in flight.
	updateCheckGrace = 300 * time.Millisecond
	// versionLookupTimeout bounds the lookup gs version makes when the daily
	// check's answer is stale.
	versionLookupTimeout = 2 * time.Second
)

type updateCheckState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

type updateCheck struct {
	current string
	result  chan string
}

func (r Runner) updateCheckPath() string {
	return filepath.Join(r.homeDir(), ".gitslice", "update-check.json")
}

// startUpdateCheck begins the daily release lookup when one is due for the
// command args run. It returns nil when no lookup is due.
func (r Runner) startUpdateCheck(ctx context.Context, args []string) *updateCheck {
	current := cliVersionInfo().Version
	if !updateNoticeWanted(args, current) {
		return nil
	}
	var state updateCheckState
	if err := readJSONFile(r.updateCheckPath(), &state); err == nil && time.Since(state.CheckedAt) < updateCheckInterval {
		return nil
	}
	check := &updateCheck{current: current, result: make(chan string, 1)}
	go func() {
		check.result <- r.lookUpLatestRelease(context.WithoutCancel(ctx), updateCheckTimeout)
	}()
	return check
}

// lookUpLatestRelease asks the release site for the latest release, within
// timeout, and records the answer for the daily check. It returns "" when the
// lookup fails.
func (r Runner) lookUpLatestRelease(ctx context.Context, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := strings.TrimRight(firstNonEmpty(os.Getenv("GS_DOWNLOAD_BASE"), defaultReleasesBase), "/")
	latest, err := latestReleaseTag(ctx, &http.Client{Timeout: timeout}, base)
	if err != nil {
		latest = ""
	}
	// A failed lookup is recorded too, so an offline machine tries once a
	// day rather than on every command.
	if err := os.MkdirAll(filepath.Dir(r.updateCheckPath()), 0o700); err == nil {
		_ = writeJSONFile(r.updateCheckPath(), updateCheckState{CheckedAt: time.Now().UTC(), Latest: latest}, 0o600)
	}
	return latest
}

// latestKnownRelease is the latest release for gs version: the daily
// check's answer while it is fresh, else a quick lookup now. It returns ""
// when the check is off or the release is unknown.
func (r Runner) latestKnownRelease(ctx context.Context) string {
	if updateCheckDisabled() {
		return ""
	}
	var state updateCheckState
	if err := readJSONFile(r.updateCheckPath(), &state); err == nil && time.Since(state.CheckedAt) < updateCheckInterval {
		return state.Latest
	}
	return r.lookUpLatestRelease(ctx, versionLookupTimeout)
}

func updateCheckDisabled() bool {
	v := strings.TrimSpace(os.Getenv("GS_NO_UPDATE_CHECK"))
	return v != "" && v != "0"
}

// upgradeHint is how to install a newer gs: through Homebrew when Homebrew
// installed this one, else gs upgrade.
func upgradeHint() string {
	if homebrewInstalled() {
		return "brew upgrade gitslice"
	}
	return "gs upgrade"
}

// homebrewInstalled reports whether this gs lives in a Homebrew Cellar, which
// Homebrew upgrades and gs upgrade must not overwrite.
func homebrewInstalled() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return strings.Contains(filepath.ToSlash(exe), "/Cellar/gitslice/")
}

// finish prints the notice if the lookup found a newer release.
func (c *updateCheck) finish(r Runner) {
	if c == nil {
		return
	}
	var latest string
	select {
	case latest = <-c.result:
	case <-time.After(updateCheckGrace):
		return
	}
	if latest == "" || !newerVersion(latest, c.current) {
		return
	}
	fmt.Fprintf(r.stderr(), "\ngs %s is available (this is %s). Run %s to install it.\n", latest, c.current, upgradeHint())
}

func updateNoticeWanted(args []string, current string) bool {
	if updateCheckDisabled() {
		return false
	}
	if _, ok := parseVersion(current); !ok {
		return false
	}
	if wantsJSON(args) {
		return false
	}
	for _, arg := range args {
		if arg == "--quiet" {
			return false
		}
	}
	if i := aliasExpansionIndex(args); i >= 0 {
		switch args[i] {
		// upgrade and doctor report versions themselves; the rest print
		// machine-read output or are not commands.
		case "upgrade", "doctor", "version", "completion", "__complete", "__completeNoDesc", "help", "schema":
			return false
		}
	}
	return true
}

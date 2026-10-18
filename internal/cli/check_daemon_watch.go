package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// checkDaemonLookInterval is how often a submit that waits on a required
// check looks at whether the daemon that should run it is online.
var checkDaemonLookInterval = 30 * time.Second

// checkDaemonWatch tells someone waiting on a required check, once, that
// the daemon it is queued on is offline, so the wait is not silent. It says
// so again only after the daemon came back and went away once more.
type checkDaemonWatch struct {
	out      io.Writer
	lastLook time.Time
	told     map[string]bool // daemon id -> said it is offline
	now      func() time.Time
}

func newCheckDaemonWatch(out io.Writer) *checkDaemonWatch {
	return &checkDaemonWatch{out: out, told: map[string]bool{}, now: time.Now}
}

// look checks the runs of check on changesetID, at most once per
// checkDaemonLookInterval unless force is set (a new check to wait for).
// Lookup failures are ignored: this only adds information to a wait.
func (w *checkDaemonWatch) look(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changesetID, check string, force bool) {
	if w == nil || w.out == nil || changesetID == "" || check == "" {
		return
	}
	now := w.now()
	if !force && now.Sub(w.lastLook) < checkDaemonLookInterval {
		return
	}
	w.lastLook = now
	res, err := corev1.NewCheckServiceClient(conn).ListCheckRuns(authContext(ctx, cfg), &corev1.ListCheckRunsRequest{ChangesetId: changesetID})
	if err != nil {
		return
	}
	w.note(res.GetRuns(), check, now)
}

func (w *checkDaemonWatch) note(runs []*corev1.CheckRun, check string, now time.Time) {
	for _, run := range runs {
		daemonID := strings.TrimSpace(run.GetDaemonId())
		// A server that predates daemon status leaves last-seen empty;
		// say nothing rather than call every daemon offline.
		if run.GetCheckName() != check || run.GetStatus() != "queued" || daemonID == "" || run.GetDaemonLastSeenAt() == "" {
			continue
		}
		if run.GetDaemonOnline() {
			w.told[daemonID] = false
			continue
		}
		if w.told[daemonID] {
			continue
		}
		w.told[daemonID] = true
		name := firstNonEmpty(run.GetDaemonName(), daemonID)
		fmt.Fprintf(w.out, "CI daemon %s is offline%s; %q starts when it reconnects (or when the slice names another CI daemon)\n", name, lastSeenPhrase(run.GetDaemonLastSeenAt(), now), check)
	}
}

// lastSeenPhrase renders " (last seen 18:56 UTC, 23m ago)".
func lastSeenPhrase(lastSeen string, now time.Time) string {
	seen, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastSeen))
	if err != nil {
		return ""
	}
	seen = seen.UTC()
	when := seen.Format("15:04 UTC")
	if seen.Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		when = seen.Format("2006-01-02 15:04 UTC")
	}
	ago := now.Sub(seen)
	switch {
	case ago < time.Minute:
		return fmt.Sprintf(" (last seen %s)", when)
	case ago < time.Hour:
		return fmt.Sprintf(" (last seen %s, %dm ago)", when, int(ago.Minutes()))
	case ago < 48*time.Hour:
		return fmt.Sprintf(" (last seen %s, %dh ago)", when, int(ago.Hours()))
	default:
		return fmt.Sprintf(" (last seen %s, %dd ago)", when, int(ago.Hours()/24))
	}
}

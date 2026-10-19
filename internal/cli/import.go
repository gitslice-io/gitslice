package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// importAccountFromMount returns the account segment of an import mount path so
// the authoring slice can default to that account's home slice without requiring
// a workspace or an explicit --slice.
func importAccountFromMount(mountPath string) (string, error) {
	canonical, err := paths.Canonical(mountPath)
	if err != nil {
		return "", userError("invalid_mount", "invalid import mount path", "Use --mount /account/path, for example --mount /nic/gitslice.")
	}
	return strings.Split(strings.Trim(canonical, "/"), "/")[0], nil
}

func (r Runner) runImportGitRepository(ctx context.Context, opts commandOptions, source, mountPath, sliceRef, mode string, maxCommits int, resume bool, retries int) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if mountPath == "" {
		return userError("missing_mount", "missing import mount path", "Use --mount /account/path, for example --mount /nic/gitslice.")
	}
	if sliceRef == "" {
		// Default the authoring slice from the mount path's account: its home
		// slice covers the account root (/<account>), so it always covers the
		// mount. This lets `gs import` run outside a workspace and without an
		// explicit --slice. Pass --slice to author through a narrower slice.
		account, err := importAccountFromMount(mountPath)
		if err != nil {
			return err
		}
		sliceRef = account + ":home"
	}
	if maxCommits < 0 {
		return userError("invalid_max_commits", "max commits must be non-negative", "Use --max-commits 0 for no limit.")
	}
	conn, err := dialStream(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	req := &corev1.ImportGitRepositoryRequest{
		Source:         source,
		MountPath:      mountPath,
		AuthoringSlice: ref,
		Mode:           mode,
		TargetRef:      postgres.DefaultTargetRef,
		MaxCommits:     int32(maxCommits),
		Resume:         resume,
	}
	client := corev1.NewRepositoryServiceClient(conn)
	reporter := importProgressReporter{w: r.Stderr, quiet: opts.Quiet || opts.jsonOutput()}
	var retryOut io.Writer = r.Stderr
	if opts.Quiet {
		retryOut = nil
	}
	res, err := r.importWithRetries(ctx, importRetryPolicy{retries: retries, resume: resume}, retryOut, func(progress *int64) (*corev1.ImportGitRepositoryResponse, error) {
		if opts.jsonOutput() {
			return client.ImportGitRepository(callCtx, req)
		}
		return importAttempt(callCtx, client, req, &reporter, progress)
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, res)
	}
	if opts.Quiet {
		return nil
	}
	printImportSummary(r.Stdout, res)
	return nil
}

// defaultImportRetries is how many times in a row gs import reconnects
// without the import making progress before it gives up.
const defaultImportRetries = 8

var (
	importRetryBaseDelay = 2 * time.Second
	importRetryMaxDelay  = time.Minute
)

type importRetryPolicy struct {
	retries int
	resume  bool
}

// importWithRetries runs attempt until it succeeds, fails for good, or
// fails policy.retries times in a row without progress. A dropped
// connection or a server restart is retried: the server resumes the import
// where it stopped (published commits are skipped) and keeps its clone of
// the repository between attempts. Without --resume a retry would publish
// commits again, so then it retries only while nothing has been published.
func (r Runner) importWithRetries(ctx context.Context, policy importRetryPolicy, w io.Writer, attempt func(progress *int64) (*corev1.ImportGitRepositoryResponse, error)) (*corev1.ImportGitRepositoryResponse, error) {
	best := int64(0)
	failures := 0
	for {
		progress := int64(0)
		res, err := attempt(&progress)
		if err == nil {
			return res, nil
		}
		if progress > best {
			best = progress
			failures = 0
		}
		failures++
		if !importRetryable(err) || failures > policy.retries || ctx.Err() != nil {
			return nil, err
		}
		if !policy.resume && best > 0 {
			return nil, userError("import_interrupted", fmt.Sprintf("the connection dropped after %d commit(s) were published: %v", best, err), "Run the same gs import with --resume to continue where it stopped.")
		}
		delay := importRetryBaseDelay << (failures - 1)
		if delay > importRetryMaxDelay || delay <= 0 {
			delay = importRetryMaxDelay
		}
		if w != nil {
			at := ""
			if best > 0 {
				at = fmt.Sprintf(" at commit %d", best)
			}
			fmt.Fprintf(w, "connection lost%s (%s); reconnecting in %s (retry %d of %d)...\n", at, oneLineSummary(grpcstatus.Convert(err).Message()), delay, failures, policy.retries)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// errImportStalled is an import whose server stopped sending anything, not
// even the heartbeat it sends while it works.
var errImportStalled = errors.New("the server went silent")

// importStallTimeout is how long a server that sends heartbeats may be silent
// before the connection is given up on and the import reconnects.
var importStallTimeout = 60 * time.Second

// importRetryable reports whether an import failed because the connection
// or the server instance went away, rather than because of the import.
func importRetryable(err error) bool {
	if errors.Is(err, errImportStalled) {
		return true
	}
	st, ok := grpcstatus.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.Aborted:
		return true
	case codes.Internal, codes.Unknown, codes.Canceled:
		msg := strings.ToLower(st.Message())
		for _, transient := range []string{"context canceled", "rst_stream", "eof", "connection reset", "transport is closing", "goaway"} {
			if strings.Contains(msg, transient) {
				return true
			}
		}
	}
	return false
}

// importAttempt runs one ImportGitRepositoryStream, reporting progress and
// recording in progress the furthest commit published or skipped.
//
// A server that works on a long step sends a "working" heartbeat every few
// seconds. Once one has arrived, silence for importStallTimeout (read once, when the attempt starts) means the
// connection is dead without having said so, and the attempt ends with
// errImportStalled. A server that sends no heartbeats is never timed out: its
// long steps are silent.
func importAttempt(ctx context.Context, client corev1.RepositoryServiceClient, req *corev1.ImportGitRepositoryRequest, reporter *importProgressReporter, progress *int64) (*corev1.ImportGitRepositoryResponse, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.ImportGitRepositoryStream(streamCtx, req)
	if err != nil {
		return nil, err
	}
	stallTimeout := importStallTimeout
	var lastEvent atomic.Int64 // unix nanoseconds of the last event
	var heartbeats atomic.Bool // the server has sent a heartbeat
	var stalled atomic.Bool
	lastEvent.Store(time.Now().UnixNano())
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		interval := max(stallTimeout/4, 10*time.Millisecond)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ticker.C:
				if heartbeats.Load() && time.Since(time.Unix(0, lastEvent.Load())) > stallTimeout {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	var res *corev1.ImportGitRepositoryResponse
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if stalled.Load() && ctx.Err() == nil {
				return nil, errImportStalled
			}
			return nil, err
		}
		lastEvent.Store(time.Now().UnixNano())
		if event.Phase == "working" {
			heartbeats.Store(true)
		}
		reporter.report(event)
		if (event.Phase == "published" || event.Phase == "skipped") && event.Current > *progress {
			*progress = event.Current
		}
		if event.Result != nil {
			res = event.Result
		}
	}
	if res == nil {
		return nil, fmt.Errorf("import completed without a final result")
	}
	return res, nil
}

func printImportSummary(w io.Writer, res *corev1.ImportGitRepositoryResponse) {
	fmt.Fprintf(w, "imported %d commit(s) from %s\n", len(res.Commits), res.Source)
	fmt.Fprintf(w, "mount: %s\n", res.MountPath)
	fmt.Fprintf(w, "mode: %s\n", res.Mode)
	fmt.Fprintf(w, "final commit: %s\n", res.FinalCommitId)
	for _, commit := range res.Commits {
		fmt.Fprintf(w, "  %s -> %s %s\n", shortID(commit.GitCommitId), commit.NativeCommitId, commit.Message)
	}
}

type importProgressReporter struct {
	w        io.Writer
	quiet    bool
	lastLine time.Time
}

func (r *importProgressReporter) report(event *corev1.ImportGitRepositoryProgress) {
	if r.quiet || r.w == nil || event == nil {
		return
	}
	switch event.Phase {
	case "cloning":
		fmt.Fprintln(r.w, "cloning repository...")
	case "listing_commits":
		fmt.Fprintln(r.w, "listing commits...")
	case "listed_commits":
		fmt.Fprintf(r.w, "found %d commit(s)\n", event.Total)
	case "reading_commit":
		if r.shouldPrintCommitLine(event) {
			fmt.Fprintf(r.w, "reading %d/%d %s %s\n", event.Current, event.Total, shortID(event.GitCommitId), event.Message)
		}
	case "uploading_blobs":
		if r.shouldPrintCommitLine(event) {
			fmt.Fprintf(r.w, "uploading %d/%d %s (%d changed path(s))\n", event.Current, event.Total, shortID(event.GitCommitId), event.ChangedPathCount)
		}
	case "submitting":
		if r.shouldPrintCommitLine(event) {
			fmt.Fprintf(r.w, "publishing %d/%d %s\n", event.Current, event.Total, shortID(event.GitCommitId))
		}
	case "published":
		if r.shouldPrintCommitLine(event) {
			fmt.Fprintf(r.w, "published %d/%d %s -> %s (%d changed path(s))\n", event.Current, event.Total, shortID(event.GitCommitId), event.NativeCommitId, event.ChangedPathCount)
		}
	case "skipped":
		if r.shouldPrintCommitLine(event) {
			fmt.Fprintf(r.w, "skipped %d/%d %s -> %s\n", event.Current, event.Total, shortID(event.GitCommitId), event.NativeCommitId)
		}
	case "waiting":
		fmt.Fprintln(r.w, "waiting for the previous connection of this import to finish its commit...")
	case "done":
		fmt.Fprintln(r.w, "import complete")
	}
}

func (r *importProgressReporter) shouldPrintCommitLine(event *corev1.ImportGitRepositoryProgress) bool {
	now := time.Now()
	if event.Total <= 20 || event.Current <= 1 || event.Current == event.Total || now.Sub(r.lastLine) >= 2*time.Second {
		r.lastLine = now
		return true
	}
	return false
}

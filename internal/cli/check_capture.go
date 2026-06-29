package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/checkexec"
	"github.com/gitslice-io/gitslice/internal/checks"
	gspaths "github.com/gitslice-io/gitslice/internal/paths"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type WorkspaceCheckResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ExitCode   int32  `json:"exit_code"`
	Summary    string `json:"summary,omitempty"`
	Log        string `json:"log,omitempty"`
	SetupMs    int64  `json:"setup_ms,omitempty"`
	RunMs      int64  `json:"run_ms,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
	OutOfSlice bool   `json:"out_of_slice,omitempty"`
}

type ciOutput struct {
	Workspace        string                 `json:"workspace"`
	ChangedPathCount int                    `json:"changed_path_count"`
	ChangedPaths     []string               `json:"changed_paths"`
	Results          []WorkspaceCheckResult `json:"results"`
}

func (r Runner) runCI(ctx context.Context, opts commandOptions) error {
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEdits(ctx, nil, UserConfig{}, ws, false)
	if err != nil {
		return err
	}
	changedPaths := changedPathsFromEdits(edits)
	results, err := runWorkspaceChecks(ctx, root, ws.IncludedPaths, changedPaths)
	if err != nil {
		return err
	}

	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, ciOutput{
			Workspace:        ws.Account + ":" + ws.Slice,
			ChangedPathCount: len(changedPaths),
			ChangedPaths:     changedPaths,
			Results:          results,
		})
	}
	if !opts.Quiet {
		if len(changedPaths) == 0 {
			fmt.Fprintln(r.Stdout, "no changes to check")
		}
		for _, result := range results {
			r.printCICheckSummary(result)
		}
	}
	if workspaceChecksFailed(results) {
		return userError("ci_failed", "one or more checks failed", "Fix failing checks and rerun gs ci.")
	}
	return nil
}

func (r Runner) captureBundledCheckRuns(ctx context.Context, opts commandOptions, ws WorkspaceConfig, changedPaths []string) ([]*corev1.BundledCheckRun, error) {
	if len(changedPaths) == 0 {
		return nil, nil
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, err
	}
	includedPaths := append([]string{}, ws.IncludedPaths...)
	if len(includedPaths) == 0 {
		includedPaths = []string{"/"}
	}
	results, err := runWorkspaceChecks(ctx, root, includedPaths, changedPaths)
	if err != nil {
		return nil, err
	}

	bundled := make([]*corev1.BundledCheckRun, 0, len(results))
	for _, result := range results {
		run := bundledCheckRunFromWorkspaceResult(result)
		bundled = append(bundled, run)
		r.printCaptureCheckSummary(opts, run)
	}
	return bundled, nil
}

func (r Runner) printCICheckSummary(result WorkspaceCheckResult) {
	status := result.Status
	if result.OutOfSlice {
		fmt.Fprintf(r.Stdout, "%s: skipped-locally (requires files outside workspace slice; runs on the slice's CI daemon)\n", result.Name)
		return
	}
	details := ciCheckDetails(result)
	if details == "" {
		fmt.Fprintf(r.Stdout, "%s: %s\n", result.Name, status)
		return
	}
	fmt.Fprintf(r.Stdout, "%s: %s (%s)\n", result.Name, status, details)
}

func ciCheckDetails(result WorkspaceCheckResult) string {
	if result.Status == "skipped" {
		return result.Summary
	}
	parts := []string{}
	if result.Cached {
		parts = append(parts, "setup cached")
	} else if result.SetupMs > 0 {
		parts = append(parts, "setup "+formatCheckDuration(result.SetupMs))
	}
	if result.RunMs > 0 || len(parts) > 0 {
		parts = append(parts, "run "+formatCheckDuration(result.RunMs))
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" && summary != "exit 0" {
		parts = append(parts, summary)
	}
	return strings.Join(parts, ", ")
}

func formatCheckDuration(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func workspaceChecksFailed(results []WorkspaceCheckResult) bool {
	for _, result := range results {
		switch result.Status {
		case "failed", "errored":
			return true
		}
	}
	return false
}

func runWorkspaceChecks(ctx context.Context, root string, includedPaths, changedPaths []string) ([]WorkspaceCheckResult, error) {
	if len(changedPaths) == 0 {
		return nil, nil
	}
	includedPaths = append([]string{}, includedPaths...)
	if len(includedPaths) == 0 {
		includedPaths = []string{"/"}
	}
	plan, err := checks.ResolvePlan(ctx, workspaceDiskTreeReader{root: root, includedPaths: includedPaths}, "", changedPaths, includedPaths, nil)
	if err != nil {
		return nil, err
	}

	var results []WorkspaceCheckResult
	for _, skipped := range plan.Skipped {
		results = append(results, WorkspaceCheckResult{
			Name:    skipped.Name,
			Status:  "skipped",
			Summary: oneLineSummary(skipped.Reason),
		})
	}
	for _, errored := range plan.Errored {
		results = append(results, WorkspaceCheckResult{
			Name:     errored.Name,
			Status:   "errored",
			ExitCode: -1,
			Summary:  oneLineSummary(errored.Reason),
			Log:      errored.Reason,
		})
	}
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
			results = append(results, WorkspaceCheckResult{
				Name:       spec.Name,
				Status:     "skipped",
				Summary:    "requires files outside workspace slice",
				OutOfSlice: true,
			})
			continue
		}
		start := time.Now()
		result, err := checkexec.Run(ctx, root, spec)
		elapsedMs := time.Since(start).Milliseconds()
		workspaceResult := WorkspaceCheckResult{Name: spec.Name}
		if err != nil {
			workspaceResult.Status = "errored"
			workspaceResult.ExitCode = -1
			workspaceResult.Summary = oneLineSummary(err.Error())
			workspaceResult.Log = err.Error()
			workspaceResult.RunMs = elapsedMs
		} else {
			workspaceResult.Status = result.Status
			workspaceResult.ExitCode = result.ExitCode
			workspaceResult.Summary = oneLineSummary(result.Summary)
			workspaceResult.Log = result.Log
			workspaceResult.SetupMs = result.SetupMs
			workspaceResult.Cached = result.Cached
			workspaceResult.RunMs = result.RunMs
			if workspaceResult.RunMs == 0 && elapsedMs > workspaceResult.SetupMs {
				workspaceResult.RunMs = elapsedMs - workspaceResult.SetupMs
			}
		}
		results = append(results, workspaceResult)
	}
	return results, nil
}

func bundledCheckRunFromWorkspaceResult(result WorkspaceCheckResult) *corev1.BundledCheckRun {
	return &corev1.BundledCheckRun{
		Name:     result.Name,
		Status:   result.Status,
		ExitCode: result.ExitCode,
		Summary:  oneLineSummary(result.Summary),
		Log:      result.Log,
	}
}

func (r Runner) printCaptureCheckSummary(opts commandOptions, run *corev1.BundledCheckRun) {
	if opts.Quiet || opts.jsonOutput() || run == nil {
		return
	}
	summary := strings.TrimSpace(run.Summary)
	if summary == "" {
		fmt.Fprintf(r.Stdout, "check %s: %s\n", run.Name, run.Status)
		return
	}
	fmt.Fprintf(r.Stdout, "check %s: %s (%s)\n", run.Name, run.Status, summary)
}

func oneLineSummary(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) <= 200 {
		return value
	}
	return value[:197] + "..."
}

type workspaceDiskTreeReader struct {
	root          string
	includedPaths []string
}

func (r workspaceDiskTreeReader) ReadFile(ctx context.Context, _ string, logicalPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var lastErr error
	for _, rel := range r.candidateRelPaths(logicalPath) {
		target, err := workspaceDiskPath(r.root, rel)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(target)
		if err == nil {
			return data, nil
		}
		if os.IsNotExist(err) {
			lastErr = err
			continue
		}
		return nil, err
	}
	if lastErr != nil {
		return nil, checks.ErrNotFound
	}
	return nil, checks.ErrNotFound
}

func (r workspaceDiskTreeReader) candidateRelPaths(logicalPath string) []string {
	cleaned := cleanDiskLogicalPath(logicalPath)
	var candidates []string
	if len(r.includedPaths) == 0 || gspaths.InAnyPrefix(r.includedPaths, cleaned) {
		candidates = append(candidates, strings.TrimPrefix(cleaned, "/"))
	}
	for i, included := range r.includedPaths {
		included = cleanDiskLogicalPath(included)
		if !gspaths.Contains(included, cleaned) {
			continue
		}
		if i > 0 {
			continue
		}
		rel := strings.TrimPrefix(cleaned, strings.TrimRight(included, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = "."
		}
		candidates = append(candidates, rel)
	}
	return uniqueStrings(candidates)
}

func workspaceDiskPath(root, logicalPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	rel := strings.TrimPrefix(cleanDiskLogicalPath(logicalPath), "/")
	if rel == "." {
		rel = ""
	}
	target := filepath.Join(absRoot, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", fmt.Errorf("resolve checks path: %w", err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return "", fmt.Errorf("checks path escapes workspace: %s", logicalPath)
	}
	return target, nil
}

func cleanDiskLogicalPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

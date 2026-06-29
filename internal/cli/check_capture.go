package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gitslice-io/gitslice/internal/checkexec"
	"github.com/gitslice-io/gitslice/internal/checks"
	gspaths "github.com/gitslice-io/gitslice/internal/paths"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

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
	plan, err := checks.ResolvePlan(ctx, workspaceDiskTreeReader{root: root, includedPaths: includedPaths}, "", changedPaths, includedPaths, nil)
	if err != nil {
		return nil, err
	}

	var bundled []*corev1.BundledCheckRun
	for _, skipped := range plan.Skipped {
		run := &corev1.BundledCheckRun{
			Name:    skipped.Name,
			Status:  "skipped",
			Summary: oneLineSummary(skipped.Reason),
		}
		bundled = append(bundled, run)
		r.printCaptureCheckSummary(opts, run)
	}
	for _, errored := range plan.Errored {
		run := &corev1.BundledCheckRun{
			Name:     errored.Name,
			Status:   "errored",
			ExitCode: -1,
			Summary:  oneLineSummary(errored.Reason),
			Log:      errored.Reason,
		}
		bundled = append(bundled, run)
		r.printCaptureCheckSummary(opts, run)
	}
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
			run := &corev1.BundledCheckRun{
				Name:    spec.Name,
				Status:  "skipped",
				Summary: "requires files outside workspace slice",
			}
			bundled = append(bundled, run)
			r.printCaptureCheckSummary(opts, run)
			continue
		}
		result, err := checkexec.Run(ctx, root, spec)
		run := &corev1.BundledCheckRun{Name: spec.Name}
		if err != nil {
			run.Status = "errored"
			run.ExitCode = -1
			run.Summary = oneLineSummary(err.Error())
			run.Log = err.Error()
		} else {
			run.Status = result.Status
			run.ExitCode = result.ExitCode
			run.Summary = oneLineSummary(result.Summary)
			run.Log = result.Log
		}
		bundled = append(bundled, run)
		r.printCaptureCheckSummary(opts, run)
	}
	return bundled, nil
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

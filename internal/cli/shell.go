package cli

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/remoteshell"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runShell(ctx context.Context, opts commandOptions, commitID, sliceRef string) error {
	if opts.JSONOutput() {
		return userError("unsupported_format", "gs shell only supports text output", "Run gs shell without --json or --format json.")
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	rootPath, scopeLabel, workspaceScoped, syntheticDirs, mutationSlice, err := r.shellScope(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	pinnedCommit := commitID != ""
	if commitID == "" {
		ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: model.DefaultTargetRef})
		if err != nil {
			return err
		}
		commitID = ref.CommitId
	}
	var mutator remoteshell.Mutator
	if mutationSlice != nil && !pinnedCommit {
		mutator = &shellMutatorAdapter{
			runner:  r,
			mutator: &remoteFileMutator{runner: r, cfg: cfg, conn: conn, slice: mutationSlice},
		}
	}
	projectionRoots, err := shellProjectionRoots(sliceRef, mutationSlice, workspaceScoped)
	if err != nil {
		return err
	}
	sh := remoteshell.New(remoteshell.Config{
		Stdout:        r.stdout(),
		Stderr:        r.stderr(),
		Repo:          repo,
		Mutator:       mutator,
		Root:          rootPath,
		Cwd:           rootPath,
		CommitID:      commitID,
		Scope:         scopeLabel,
		Scoped:        workspaceScoped,
		SyntheticDirs: syntheticDirs,
		Projection:    projectionRoots,
		Color:         r.colorEnabled(opts),
		StickyHeader:  r.stickyShellHeaderEnabled(opts),
		OutsideSliceError: func(message, hint string) error {
			return userError("outside_slice", message, hint)
		},
	})
	if !opts.Quiet {
		sh.PrintHeader()
		defer sh.RestoreHeader()
	}
	if r.shellLineEditorEnabled(opts) {
		return sh.RunLineEditor(callCtx)
	}
	return sh.RunScanner(callCtx, r.stdin(), opts.Quiet)
}

type shellMutatorAdapter struct {
	runner  Runner
	mutator *remoteFileMutator
}

func (a *shellMutatorAdapter) WriteFile(ctx context.Context, globalPath string, data []byte) (remoteshell.MutationResult, error) {
	cleaned, err := normalizeMutationPath(a.mutator.slice, globalPath)
	if err != nil {
		return remoteshell.MutationResult{}, err
	}
	edit, err := a.runner.uploadFileEdit(authContext(ctx, a.mutator.cfg), a.mutator.conn, a.mutator.slice.Ref, cleaned, data)
	if err != nil {
		return remoteshell.MutationResult{}, err
	}
	return a.Apply(ctx, "write", []*corev1.FileEdit{edit})
}

func (a *shellMutatorAdapter) Apply(ctx context.Context, operation string, edits []*corev1.FileEdit) (remoteshell.MutationResult, error) {
	var captured strings.Builder
	originalStdout := a.mutator.runner.Stdout
	a.mutator.runner.Stdout = &captured
	err := a.mutator.apply(ctx, commandOptions{Quiet: true}, operation, edits, false)
	a.mutator.runner.Stdout = originalStdout
	if err != nil {
		return remoteshell.MutationResult{}, err
	}
	return remoteshell.MutationResult{
		Changeset:    a.mutator.last.Changeset,
		ChangesetURL: a.mutator.last.ChangesetURL,
	}, nil
}

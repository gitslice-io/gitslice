package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type fileMutationOutput struct {
	Operation string `json:"operation"`
	Slice     string `json:"slice"`
	// The changeset the edit landed through (or, with --no-submit, waits in
	// for review), and its web page.
	Changeset      string   `json:"changeset"`
	ChangesetID    string   `json:"changeset_id"`
	ChangesetURL   string   `json:"changeset_url,omitempty"`
	Submitted      bool     `json:"submitted"`
	CommitID       string   `json:"commit_id,omitempty"`
	NewRefCommitID string   `json:"new_ref_commit_id,omitempty"`
	ChangedPaths   []string `json:"changed_paths"`
}

func (r Runner) runFileMkdir(ctx context.Context, opts commandOptions, p string) error {
	return r.runHomeFileMutation(ctx, opts, "mkdir", []*corev1.FileEdit{{Op: "mkdir", Path: p}})
}

func (r Runner) runFileWrite(ctx context.Context, opts commandOptions, p string, data []byte) error {
	cfg, conn, mutator, err := r.homeFileMutator(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	cleaned, err := normalizeMutationPath(mutator.slice, p)
	if err != nil {
		return err
	}
	edit, err := r.uploadFileEdit(authContext(ctx, cfg), conn, mutator.slice.Ref, cleaned, data)
	if err != nil {
		return err
	}
	return mutator.apply(ctx, opts, "write", []*corev1.FileEdit{edit})
}

func (r Runner) runFileMove(ctx context.Context, opts commandOptions, oldPath, newPath string) error {
	return r.runHomeFileMutation(ctx, opts, "mv", []*corev1.FileEdit{{Op: "rename", OldPath: oldPath, Path: newPath}})
}

func (r Runner) runFileRemove(ctx context.Context, opts commandOptions, p string) error {
	return r.runHomeFileMutation(ctx, opts, "rm", []*corev1.FileEdit{{Op: "delete", Path: p}})
}

func (r Runner) runHomeFileMutation(ctx context.Context, opts commandOptions, operation string, edits []*corev1.FileEdit) error {
	_, conn, mutator, err := r.homeFileMutator(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return mutator.apply(ctx, opts, operation, edits)
}

func (r Runner) homeFileMutator(ctx context.Context) (UserConfig, *grpc.ClientConn, *remoteFileMutator, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	slice, err := r.personalHomeSlice(authContext(ctx, cfg), cfg, conn)
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, err
	}
	return cfg, conn, &remoteFileMutator{runner: r, cfg: cfg, conn: conn, slice: slice}, nil
}

// personalAccountSlug returns the signed-in subject's personal account slug (the
// first account from auth status). The account is the user's chosen username and
// is NOT derivable from the subject id.
func (r Runner) personalAccountSlug(ctx context.Context, conn *grpc.ClientConn) (string, error) {
	status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		return "", err
	}
	// needs_username means no personal account even when Accounts is non-empty:
	// a claimed agent account is a membership, not the caller's own account.
	if len(status.Accounts) == 0 || status.NeedsUsername {
		return "", userError("no_account", "signed-in subject has no personal account yet", "Choose a username in the web app to create your account.")
	}
	return status.Accounts[0], nil
}

func (r Runner) personalHomeSlice(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (*corev1.Slice, error) {
	accountSlug, err := r.personalAccountSlug(ctx, conn)
	if err != nil {
		return nil, err
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: accountSlug, Slice: "home"},
	})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound || grpcstatus.Code(err) == codes.PermissionDenied {
			return nil, userError("no_home_slice", "personal home slice was not found for "+accountSlug, "Choose a username in the web app to create your account.")
		}
		return nil, err
	}
	return slice, nil
}

func (r Runner) uploadFileEdit(ctx context.Context, conn *grpc.ClientConn, sliceRef *corev1.SliceRef, p string, data []byte) (*corev1.FileEdit, error) {
	upload, err := uploadBlobBytes(ctx, corev1.NewBlobServiceClient(conn), sliceRef, "", data)
	if err != nil {
		return nil, err
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}, nil
}

type remoteFileMutator struct {
	runner Runner
	cfg    UserConfig
	conn   *grpc.ClientConn
	slice  *corev1.Slice
	// last is what the latest apply landed, for gs shell to report.
	last fileMutationOutput
}

func (m *remoteFileMutator) apply(ctx context.Context, opts commandOptions, operation string, edits []*corev1.FileEdit) error {
	if len(edits) == 0 {
		return userError("empty_mutation", "no file edits to apply", "Pass at least one file path.")
	}
	changed, err := normalizeMutationEdits(m.slice, edits)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, m.cfg)
	repoClient := corev1.NewRepositoryServiceClient(m.conn)
	ref, err := repoClient.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	changesetClient := corev1.NewChangesetServiceClient(m.conn)
	title := mutationTitle(operation, changed)
	if len(title) > 180 {
		title = title[:180]
	}
	cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: m.slice.Ref,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		return err
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.Id)
	if opts.NoSubmit {
		output := fileMutationOutput{
			Operation:    operation,
			Slice:        m.slice.Ref.Account + ":" + m.slice.Ref.Slice,
			Changeset:    handle,
			ChangesetID:  cs.Id,
			ChangesetURL: webResourceURL("/cs/" + handle),
			ChangedPaths: changed,
		}
		m.last = output
		if opts.jsonOutput() {
			return writeJSON(m.runner.stdout(), output)
		}
		if !opts.Quiet {
			stdout := m.runner.stdout()
			fmt.Fprintf(stdout, "%s %s in %s: changeset %s is open for review, not submitted\n", operationPastTense(operation), changedPathsSummary(changed), output.Slice, handle)
			if output.ChangesetURL != "" {
				fmt.Fprintf(stdout, "view: %s\n", output.ChangesetURL)
			}
			fmt.Fprintf(stdout, "submit it with: gs cs submit %s\n", handle)
		}
		return nil
	}
	res, err := changesetClient.SubmitChangeset(callCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	})
	if err != nil {
		return err
	}
	commitID := res.CommitId
	refCommitID := res.NewRefCommitId
	if refCommitID == "" || res.Status == "pending_publish" {
		commitID, refCommitID, err = m.runner.waitForChangesetPublished(ctx, m.conn, m.cfg, cs.Id, displayChangesetID(cs), mutationPublishTimeout(len(edits)), false)
		if err != nil {
			return err
		}
	}
	output := fileMutationOutput{
		Operation:      operation,
		Slice:          m.slice.Ref.Account + ":" + m.slice.Ref.Slice,
		Changeset:      handle,
		ChangesetID:    cs.Id,
		ChangesetURL:   webResourceURL("/cs/" + handle),
		Submitted:      true,
		CommitID:       commitID,
		NewRefCommitID: refCommitID,
		ChangedPaths:   changed,
	}
	m.last = output
	if opts.jsonOutput() {
		return writeJSON(m.runner.stdout(), output)
	}
	if opts.Quiet {
		return nil
	}
	// Name the changeset and its link first: a commit id is not a changeset
	// id, and agents share whatever id the output offers.
	stdout := m.runner.stdout()
	fmt.Fprintf(stdout, "%s %s in %s through changeset %s\n", operationPastTense(operation), changedPathsSummary(changed), output.Slice, handle)
	if output.ChangesetURL != "" {
		fmt.Fprintf(stdout, "view: %s\n", output.ChangesetURL)
	}
	if refCommitID != "" {
		fmt.Fprintf(stdout, "landed as commit %s (a commit id, not a changeset id; share the view link)\n", refCommitID)
	}
	return nil
}

func mutationPublishTimeout(editCount int) time.Duration {
	timeout := 2 * time.Minute
	if editCount > 0 {
		timeout += time.Duration(editCount/100) * time.Second
	}
	if timeout > 15*time.Minute {
		return 15 * time.Minute
	}
	return timeout
}

func normalizeMutationEdits(slice *corev1.Slice, edits []*corev1.FileEdit) ([]string, error) {
	if slice == nil || slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return nil, fmt.Errorf("authoring slice has no included paths")
	}
	changedSet := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil {
			return nil, fmt.Errorf("file edit is nil")
		}
		var err error
		if edit.Path != "" {
			edit.Path, err = normalizeMutationPath(slice, edit.Path)
			if err != nil {
				return nil, err
			}
			changedSet[edit.Path] = struct{}{}
		}
		if edit.OldPath != "" {
			edit.OldPath, err = normalizeMutationPath(slice, edit.OldPath)
			if err != nil {
				return nil, err
			}
			changedSet[edit.OldPath] = struct{}{}
		}
	}
	changed := make([]string, 0, len(changedSet))
	for p := range changedSet {
		changed = append(changed, p)
	}
	sort.Strings(changed)
	return changed, nil
}

func normalizeMutationPath(slice *corev1.Slice, value string) (string, error) {
	return normalizeHomePath(slice, value, false)
}

func normalizeHomePath(slice *corev1.Slice, value string, allowRoot bool) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(value), "/") {
		account := "account"
		if slice != nil && slice.Ref != nil && slice.Ref.Account != "" {
			account = slice.Ref.Account
		}
		return "", userError("invalid_path", "gs fs paths must be absolute: "+value, "Use an absolute path such as /"+account+"/notes/readme.md.")
	}
	cleaned, err := cleanShellGlobalPath(value)
	if err != nil {
		return "", err
	}
	if slice == nil || slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return "", fmt.Errorf("home slice has no included paths")
	}
	for _, prefix := range slice.Definition.IncludedPaths {
		root, err := cleanShellGlobalPath(prefix)
		if err != nil {
			return "", err
		}
		if !paths.Contains(root, cleaned) {
			continue
		}
		if cleaned == root && !allowRoot {
			return "", userError("invalid_path", "cannot mutate the home root: "+cleaned, "Choose a path under "+root+".")
		}
		return cleaned, nil
	}
	return "", userError("outside_home", "path is outside the home slice: "+cleaned, "Use a path under "+strings.TrimRight(slice.Definition.IncludedPaths[0], "/")+".")
}

func homeSliceRoot(slice *corev1.Slice) (string, error) {
	if slice == nil || slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return "", fmt.Errorf("home slice has no included paths")
	}
	return cleanShellGlobalPath(slice.Definition.IncludedPaths[0])
}

func operationPastTense(operation string) string {
	switch operation {
	case "mkdir":
		return "created"
	case "write":
		return "wrote"
	case "upload":
		return "uploaded"
	case "mv":
		return "moved"
	case "rm":
		return "removed"
	default:
		return operation
	}
}

func mutationTitle(operation string, changed []string) string {
	if len(changed) == 0 {
		return "file " + operation
	}
	if len(changed) == 1 {
		return "file " + operation + " " + changed[0]
	}
	return fmt.Sprintf("file %s %s and %d more paths", operation, changed[0], len(changed)-1)
}

func changedPathsSummary(changed []string) string {
	if len(changed) == 0 {
		return "0 paths"
	}
	if len(changed) <= 5 {
		return strings.Join(changed, ", ")
	}
	return fmt.Sprintf("%d paths", len(changed))
}

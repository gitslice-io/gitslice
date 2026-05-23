package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Dir    string
	Home   string
}

type UserConfig struct {
	ServerAddr string `json:"server_addr"`
	Token      string `json:"token"`
	SubjectID  string `json:"subject_id"`
}

type WorkspaceConfig struct {
	Account        string   `json:"account"`
	Slice          string   `json:"slice"`
	SliceID        string   `json:"slice_id"`
	DefinitionHash string   `json:"definition_hash"`
	IncludedPaths  []string `json:"included_paths"`
	BaseCommitID   string   `json:"base_commit_id"`
}

type WorkspaceState struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
	BaseCommitID       string `json:"base_commit_id"`
}

type BaseSnapshot struct {
	CommitID string                      `json:"commit_id"`
	Files    map[string]BaseSnapshotFile `json:"files"`
}

type BaseSnapshotFile struct {
	Path        string `json:"path"`
	RelPath     string `json:"rel_path"`
	ContentHash string `json:"content_hash"`
	Mode        uint32 `json:"mode"`
	Size        int64  `json:"size"`
}

type workingFile struct {
	BaseSnapshotFile
	AbsPath string
}

type commandOptions struct {
	Format         string
	Quiet          bool
	NonInteractive bool
	NoColor        bool
	Verbose        bool
	Debug          bool
	Trace          bool
}

func (o commandOptions) jsonOutput() bool {
	return o.Format == "json"
}

type commandError struct {
	Code      string
	Message   string
	Hint      string
	Retriable bool
	Cause     error
}

func (e commandError) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func (e commandError) Unwrap() error {
	return e.Cause
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retriable bool   `json:"retriable"`
}

type statusOutput struct {
	Workspace        string   `json:"workspace"`
	ChangedPathCount int      `json:"changed_path_count"`
	ChangedPaths     []string `json:"changed_paths"`
	ChangesetID      string   `json:"changeset_id,omitempty"`
	PatchsetID       string   `json:"patchset_id,omitempty"`
}

type changesetOutput struct {
	ChangesetID string `json:"changeset_id"`
	PatchsetID  string `json:"patchset_id,omitempty"`
	Status      string `json:"status,omitempty"`
}

func Main(args []string, stdout, stderr io.Writer) int {
	r := Runner{Stdout: stdout, Stderr: stderr}
	if err := r.Run(context.Background(), args); err != nil {
		if wantsJSON(args) {
			if writeErr := writeJSON(stderr, classifyError(err)); writeErr != nil {
				fmt.Fprintln(stderr, err)
			}
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}

func (r Runner) Run(ctx context.Context, args []string) error {
	root := r.rootCommand()
	root.SetArgs(args)
	root.SetOut(r.Stdout)
	root.SetErr(r.Stderr)
	return root.ExecuteContext(ctx)
}

func (r Runner) rootCommand() *cobra.Command {
	opts := &commandOptions{Format: "text"}
	jsonFlag := false

	root := &cobra.Command{
		Use:           "gs",
		Short:         "Gitslice native CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return userError("missing_command", "missing command", "Run gs --help to list available commands.")
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if jsonFlag {
				opts.Format = "json"
			}
			if opts.Format != "text" && opts.Format != "json" {
				return userError("invalid_format", "invalid output format "+opts.Format, "Use --format text, --format json, or --json.")
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&opts.Format, "format", "text", "output format: text or json")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "emit JSON output")
	root.PersistentFlags().BoolVar(&opts.Quiet, "quiet", false, "suppress non-essential text output")
	root.PersistentFlags().BoolVar(&opts.NonInteractive, "non-interactive", false, "fail instead of prompting for input")
	root.PersistentFlags().BoolVar(&opts.NoColor, "no-color", false, "disable colorized output")
	root.PersistentFlags().BoolVar(&opts.Verbose, "verbose", false, "emit additional diagnostic output")
	root.PersistentFlags().BoolVar(&opts.Debug, "debug", false, "emit debug diagnostics")
	root.PersistentFlags().BoolVar(&opts.Trace, "trace", false, "emit trace diagnostics")

	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with a Gitslice server",
		RunE:  requireSubcommand("auth"),
	}
	loginServer := defaultServerAddr()
	loginDevUser := "alice"
	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Log in through the development account service",
		Args:  noArgs("gs auth login [--server addr] [--dev-user alice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthLogin(cmd.Context(), *opts, loginServer, loginDevUser)
		},
	}
	loginCmd.Flags().StringVar(&loginServer, "server", loginServer, "server gRPC address")
	loginCmd.Flags().StringVar(&loginDevUser, "dev-user", loginDevUser, "development user")
	authCmd.AddCommand(loginCmd)

	workspaceCmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage the current single-slice workspace",
		RunE:  requireSubcommand("workspace"),
	}
	workspaceInitCmd := &cobra.Command{
		Use:   "init <account>/<slice>",
		Short: "Bind the current directory to a slice",
		Args:  exactArgs(1, "gs workspace init <account>/<slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceInit(cmd.Context(), *opts, args[0])
		},
	}
	workspaceCmd.AddCommand(workspaceInitCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show workspace changes against the local base snapshot",
		Args:  noArgs("gs status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStatus(cmd.Context(), *opts)
		},
	}

	csCmd := &cobra.Command{
		Use:     "cs",
		Aliases: []string{"changeset"},
		Short:   "Manage changesets",
		RunE:    requireSubcommand("cs"),
	}
	createTitle := "CLI changeset"
	csCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a changeset from workspace edits",
		Args:  noArgs("gs cs create [--title title]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetCreate(cmd.Context(), *opts, createTitle)
		},
	}
	csCreateCmd.Flags().StringVar(&createTitle, "title", createTitle, "changeset title")
	csUpdateCmd := &cobra.Command{
		Use:   "update",
		Short: "Create a new patchset for the current changeset",
		Args:  noArgs("gs cs update"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetUpdate(cmd.Context(), *opts)
		},
	}
	csSubmitCmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit the current changeset",
		Args:  noArgs("gs cs submit"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetSubmit(cmd.Context(), *opts)
		},
	}
	csStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current changeset status",
		Args:  noArgs("gs cs status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetStatus(cmd.Context(), *opts)
		},
	}
	csCmd.AddCommand(csCreateCmd, csUpdateCmd, csSubmitCmd, csStatusCmd)

	repoCmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage imported repositories",
		RunE:  requireSubcommand("repo"),
	}
	repoImportCmd := &cobra.Command{
		Use:   "import",
		Short: "Import external repositories",
		RunE:  requireSubcommand("repo import"),
	}
	importMountPath := ""
	importSlice := ""
	importMode := "shallow"
	importDeep := false
	importMaxCommits := 0
	importResume := true
	importGithubCmd := &cobra.Command{
		Use:   "github <owner/repo-or-url>",
		Short: "Import a GitHub repository under a mounted path",
		Args:  exactArgs(1, "gs repo import github <owner/repo-or-url> --mount /acme/payment/vendor/repo [--slice acme/payment] [--mode shallow|deep]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := importMode
			if importDeep {
				mode = "deep"
			}
			return r.runRepoImportGithub(cmd.Context(), *opts, args[0], importMountPath, importSlice, mode, importMaxCommits, importResume)
		},
	}
	importGithubCmd.Flags().StringVar(&importMountPath, "mount", importMountPath, "absolute Gitslice path where the repository should be mounted")
	importGithubCmd.Flags().StringVar(&importSlice, "slice", importSlice, "authoring slice, defaults to current workspace slice")
	importGithubCmd.Flags().StringVar(&importMode, "mode", importMode, "import mode: shallow or deep")
	importGithubCmd.Flags().BoolVar(&importDeep, "deep", importDeep, "import every reachable Git commit")
	importGithubCmd.Flags().IntVar(&importMaxCommits, "max-commits", importMaxCommits, "maximum recent commits to import in deep mode")
	importGithubCmd.Flags().BoolVar(&importResume, "resume", importResume, "resume previously completed commits for the same import")
	repoImportCmd.AddCommand(importGithubCmd)
	repoCmd.AddCommand(repoImportCmd)

	commitCmd := &cobra.Command{
		Use:   "commit",
		Short: "Inspect native commits",
		RunE:  requireSubcommand("commit"),
	}
	commitLimit := 20
	commitListCmd := &cobra.Command{
		Use:   "list",
		Short: "List native commits from a ref",
		Args:  noArgs("gs commit list [--limit 20]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCommitList(cmd.Context(), *opts, commitLimit)
		},
	}
	commitListCmd.Flags().IntVar(&commitLimit, "limit", commitLimit, "maximum commits to list")
	commitInspectCmd := &cobra.Command{
		Use:   "inspect <commit-id>",
		Short: "Inspect a native commit",
		Args:  exactArgs(1, "gs commit inspect <commit-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCommitInspect(cmd.Context(), *opts, args[0])
		},
	}
	commitCmd.AddCommand(commitListCmd, commitInspectCmd)

	schemaCmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the machine-readable CLI schema",
		Args:  noArgs("gs schema"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSchema()
		},
	}

	sliceCmd := &cobra.Command{
		Use:   "slice",
		Short: "Slice commands are not available in the MVP CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			return userError("unsupported_command", "multi-slice workspace commands are not supported", "Use gs workspace init <account>/<slice>.")
		},
	}

	root.AddCommand(authCmd, workspaceCmd, statusCmd, csCmd, repoCmd, commitCmd, schemaCmd, sliceCmd)
	return root
}

func (r Runner) runAuthLogin(ctx context.Context, opts commandOptions, serverAddr, devUser string) error {
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewFakeAccountServiceClient(conn).Login(ctx, &corev1.LoginRequest{DevUser: devUser})
	if err != nil {
		return err
	}
	cfg := UserConfig{ServerAddr: serverAddr, Token: res.Token, SubjectID: res.SubjectId}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"server_addr": serverAddr,
			"subject_id":  res.SubjectId,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "logged in as %s\n", res.SubjectId)
	return nil
}

func (r Runner) runWorkspaceInit(ctx context.Context, opts commandOptions, sliceRef string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	refRecord, err := corev1.NewRepositoryServiceClient(conn).GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	workspace := WorkspaceConfig{
		Account:        ref.Account,
		Slice:          ref.Slice,
		SliceID:        slice.Id,
		DefinitionHash: slice.DefinitionHash,
		IncludedPaths:  slice.Definition.IncludedPaths,
		BaseCommitID:   refRecord.CommitId,
	}
	if err := r.writeWorkspaceConfig(workspace); err != nil {
		return err
	}
	if err := r.writeWorkspaceState(WorkspaceState{BaseCommitID: refRecord.CommitId}); err != nil {
		return err
	}
	if err := r.writeBaseSnapshot(BaseSnapshot{CommitID: refRecord.CommitId, Files: map[string]BaseSnapshotFile{}}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"workspace":      ref.Account + "/" + ref.Slice,
			"slice_id":       slice.Id,
			"base_commit_id": refRecord.CommitId,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "initialized workspace for %s/%s\n", ref.Account, ref.Slice)
	return nil
}

func (r Runner) runStatus(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEdits(ctx, nil, cfg, ws, false)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	validation, err := corev1.NewWorkspaceServiceClient(conn).ValidateWorkspaceDiff(authContext(ctx, cfg), &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    workspaceRef(ws),
		BaseCommitId: state.BaseCommitID,
		FileEdits:    edits,
	})
	if err != nil {
		return err
	}
	output := statusOutput{
		Workspace:        ws.Account + "/" + ws.Slice,
		ChangedPathCount: len(validation.AffectedPaths),
		ChangedPaths:     validation.AffectedPaths,
		ChangesetID:      state.CurrentChangesetID,
		PatchsetID:       state.CurrentPatchsetID,
	}
	if opts.jsonOutput() {
		if output.ChangedPaths == nil {
			output.ChangedPaths = []string{}
		}
		return writeJSON(r.Stdout, output)
	}
	if opts.Quiet {
		if output.ChangedPathCount == 0 {
			return nil
		}
		return userError("workspace_dirty", "workspace has local changes", "Run gs status without --quiet to list changed paths.")
	}
	fmt.Fprintf(r.Stdout, "workspace: %s\n", output.Workspace)
	if output.ChangedPathCount == 0 {
		fmt.Fprintln(r.Stdout, "status: clean")
		return nil
	}
	fmt.Fprintf(r.Stdout, "status: %d changed path(s)\n", output.ChangedPathCount)
	for _, p := range output.ChangedPaths {
		fmt.Fprintf(r.Stdout, "  %s\n", p)
	}
	return nil
}

func (r Runner) runChangesetCreate(ctx context.Context, opts commandOptions, title string) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	edits, _, err := r.snapshotEdits(ctx, conn, cfg, ws, true)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, cfg)
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   state.BaseCommitID,
		Title:          title,
	})
	if err != nil {
		return err
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: state.BaseCommitID,
		FileEdits:    edits,
	})
	if err != nil {
		return err
	}
	state.CurrentChangesetID = cs.Id
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, changesetOutput{
			ChangesetID: cs.Id,
			PatchsetID:  patchset.Id,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "created changeset %s patchset %s\n", cs.Id, patchset.Id)
	return nil
}

func (r Runner) runChangesetUpdate(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	edits, _, err := r.snapshotEdits(ctx, conn, cfg, ws, true)
	if err != nil {
		return err
	}
	patchset, err := corev1.NewChangesetServiceClient(conn).UpdateChangeset(authContext(ctx, cfg), &corev1.UpdateChangesetRequest{
		ChangesetId:               state.CurrentChangesetID,
		ExpectedCurrentPatchsetId: state.CurrentPatchsetID,
		BaseCommitId:              state.BaseCommitID,
		FileEdits:                 edits,
	})
	if err != nil {
		return err
	}
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, changesetOutput{
			ChangesetID: state.CurrentChangesetID,
			PatchsetID:  patchset.Id,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated changeset %s patchset %s\n", state.CurrentChangesetID, patchset.Id)
	return nil
}

func (r Runner) runChangesetSubmit(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	res, err := changesetClient.SubmitChangeset(authContext(ctx, cfg), &corev1.SubmitChangesetRequest{
		ChangesetId:               state.CurrentChangesetID,
		ExpectedCurrentPatchsetId: state.CurrentPatchsetID,
	})
	if err != nil {
		return err
	}
	commitID := res.CommitId
	refCommitID := res.NewRefCommitId
	if refCommitID == "" || res.Status == "pending_publish" {
		var err error
		commitID, refCommitID, err = r.waitForChangesetPublished(ctx, conn, cfg, state.CurrentChangesetID)
		if err != nil {
			return err
		}
	}
	state.BaseCommitID = refCommitID
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	current, err := r.scanWorkspaceFiles(ws)
	if err != nil {
		return err
	}
	if err := r.writeBaseSnapshot(BaseSnapshot{CommitID: refCommitID, Files: snapshotFiles(current)}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"commit_id":         commitID,
			"target_ref":        res.TargetRef,
			"new_ref_commit_id": refCommitID,
			"status":            "submitted",
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "submitted %s to %s\n", commitID, res.TargetRef)
	return nil
}

func (r Runner) waitForChangesetPublished(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changesetID string) (string, string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	repoClient := corev1.NewRepositoryServiceClient(conn)
	for {
		cs, err := changesetClient.GetChangeset(authContext(waitCtx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			return "", "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			ref, err := repoClient.GetRef(authContext(waitCtx, cfg), &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
			if err != nil {
				return "", "", err
			}
			return cs.CommitId, ref.CommitId, nil
		}
		if cs.Status != "pending_publish" && cs.Status != "draft" {
			return "", "", userError("publish_failed", "changeset publish failed with status "+cs.Status, "Run gs cs status for details.")
		}
		select {
		case <-waitCtx.Done():
			return "", "", userError("publish_timeout", "changeset accepted but not published before timeout", "Run gs cs status to check publish progress.")
		case <-ticker.C:
		}
	}
}

func (r Runner) runChangesetStatus(ctx context.Context, opts commandOptions) error {
	cfg, _, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	cs, err := corev1.NewChangesetServiceClient(conn).GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: state.CurrentChangesetID})
	if err != nil {
		return err
	}
	output := changesetOutput{
		ChangesetID: cs.Id,
		PatchsetID:  cs.CurrentPatchsetId,
		Status:      cs.Status,
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, output)
	}
	if opts.Quiet {
		if cs.Status == "submitted" {
			return nil
		}
		return userError("changeset_not_submitted", "changeset is not submitted", "Run gs cs status without --quiet for details.")
	}
	fmt.Fprintf(r.Stdout, "changeset: %s\nstatus: %s\npatchset: %s\n", cs.Id, cs.Status, cs.CurrentPatchsetId)
	return nil
}

func (r Runner) runRepoImportGithub(ctx context.Context, opts commandOptions, source, mountPath, sliceRef, mode string, maxCommits int, resume bool) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if mountPath == "" {
		return userError("missing_mount", "missing import mount path", "Use --mount /account/slice/path.")
	}
	if sliceRef == "" {
		ws, err := r.readWorkspaceConfig()
		if err != nil {
			return err
		}
		sliceRef = ws.Account + "/" + ws.Slice
	}
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	if maxCommits < 0 {
		return userError("invalid_max_commits", "max commits must be non-negative", "Use --max-commits 0 for no limit.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
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
	if opts.jsonOutput() {
		res, err := client.ImportGitRepository(authContext(ctx, cfg), req)
		if err != nil {
			return err
		}
		return writeJSON(r.Stdout, res)
	}
	stream, err := client.ImportGitRepositoryStream(authContext(ctx, cfg), req)
	if err != nil {
		return err
	}
	var res *corev1.ImportGitRepositoryResponse
	reporter := importProgressReporter{w: r.Stderr, quiet: opts.Quiet}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		reporter.report(event)
		if event.Result != nil {
			res = event.Result
		}
	}
	if res == nil {
		return fmt.Errorf("import completed without a final result")
	}
	if opts.Quiet {
		return nil
	}
	printImportSummary(r.Stdout, res)
	return nil
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

func (r Runner) runCommitList(ctx context.Context, opts commandOptions, limit int) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewRepositoryServiceClient(conn).ListCommits(authContext(ctx, cfg), &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Limit:   int32(limit),
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, res)
	}
	if opts.Quiet {
		return nil
	}
	for _, commit := range res.Commits {
		fmt.Fprintf(r.Stdout, "%s %s\n", commit.Id, commit.Message)
	}
	return nil
}

func (r Runner) runCommitInspect(ctx context.Context, opts commandOptions, commitID string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	commit, err := corev1.NewRepositoryServiceClient(conn).GetCommit(authContext(ctx, cfg), &corev1.GetCommitRequest{CommitId: commitID})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, commit)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "commit: %s\n", commit.Id)
	fmt.Fprintf(r.Stdout, "root_tree: %s\n", commit.RootTreeId)
	fmt.Fprintf(r.Stdout, "author: %s\n", commit.Author)
	fmt.Fprintf(r.Stdout, "created_at: %s\n", commit.CreatedAt)
	fmt.Fprintf(r.Stdout, "message: %s\n", commit.Message)
	if len(commit.ParentIds) > 0 {
		fmt.Fprintf(r.Stdout, "parents: %s\n", strings.Join(commit.ParentIds, " "))
	}
	if len(commit.ChangedPaths) > 0 {
		fmt.Fprintln(r.Stdout, "changed_paths:")
		for _, p := range commit.ChangedPaths {
			fmt.Fprintf(r.Stdout, "  %s\n", p)
		}
	}
	return nil
}

func (r Runner) snapshotEdits(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, upload bool) ([]*corev1.FileEdit, map[string]workingFile, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return nil, nil, err
	}
	current, err := r.scanWorkspaceFiles(ws)
	if err != nil {
		return nil, nil, err
	}
	var blobClient corev1.BlobServiceClient
	callCtx := ctx
	if upload {
		if conn == nil {
			return nil, nil, fmt.Errorf("connection is required for blob upload")
		}
		blobClient = corev1.NewBlobServiceClient(conn)
		callCtx = authContext(ctx, cfg)
	}
	var edits []*corev1.FileEdit
	for p, file := range current {
		baseFile, ok := base.Files[p]
		if ok && baseFile.ContentHash == file.ContentHash && baseFile.Mode == file.Mode {
			continue
		}
		edit := &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: file.ContentHash, Mode: file.Mode}
		if upload {
			data, err := os.ReadFile(file.AbsPath)
			if err != nil {
				return nil, nil, err
			}
			uploadRes, err := blobClient.UploadBlob(callCtx, &corev1.UploadBlobRequest{ContentHash: file.ContentHash, Data: data})
			if err != nil {
				return nil, nil, err
			}
			edit.BlobId = uploadRes.BlobId
		}
		edits = append(edits, edit)
	}
	for p := range base.Files {
		if _, ok := current[p]; !ok {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
		}
	}
	sortFileEdits(edits)
	return edits, current, nil
}

func (r Runner) scanWorkspaceFiles(ws WorkspaceConfig) (map[string]workingFile, error) {
	root := r.cwd()
	if len(ws.IncludedPaths) == 0 {
		return nil, fmt.Errorf("workspace has no included paths")
	}
	files := map[string]workingFile{}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkip(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		globalPath, err := paths.FromWorkspacePath(ws.IncludedPaths[0], rel)
		if err != nil {
			return err
		}
		contentHash := objectid.RawContentHash(data)
		mode := uint32(0o100644)
		if info.Mode()&0o111 != 0 {
			mode = 0o100755
		}
		files[globalPath] = workingFile{
			BaseSnapshotFile: BaseSnapshotFile{
				Path:        globalPath,
				RelPath:     rel,
				ContentHash: contentHash,
				Mode:        mode,
				Size:        info.Size(),
			},
			AbsPath: p,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r Runner) loadLocalState() (UserConfig, WorkspaceConfig, WorkspaceState, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	return cfg, ws, state, nil
}

func (r Runner) readUserConfig() (UserConfig, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, userError("not_logged_in", "not logged in", "Run gs auth login.")
		}
		return cfg, err
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return cfg, userError("invalid_user_config", "invalid user config", "Run gs auth login again.")
	}
	return cfg, nil
}

func (r Runner) writeUserConfig(cfg UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(r.userConfigPath()), 0o700); err != nil {
		return err
	}
	return writeJSONFile(r.userConfigPath(), cfg, 0o600)
}

func (r Runner) readWorkspaceConfig() (WorkspaceConfig, error) {
	var cfg WorkspaceConfig
	if err := readJSONFile(filepath.Join(r.cwd(), ".gs", "slice.json"), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, userError("not_in_workspace", "not in a gitslice workspace", "Run gs workspace init <account>/<slice>.")
		}
		return cfg, err
	}
	return cfg, nil
}

func (r Runner) writeWorkspaceConfig(cfg WorkspaceConfig) error {
	dir := filepath.Join(r.cwd(), ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "slice.json"), cfg, 0o644)
}

func (r Runner) readWorkspaceState() (WorkspaceState, error) {
	var state WorkspaceState
	err := readJSONFile(filepath.Join(r.cwd(), ".gs", "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	return state, err
}

func (r Runner) readBaseSnapshot() (BaseSnapshot, error) {
	var snapshot BaseSnapshot
	err := readJSONFile(filepath.Join(r.cwd(), ".gs", "base_snapshot.json"), &snapshot)
	if errors.Is(err, os.ErrNotExist) {
		snapshot.Files = map[string]BaseSnapshotFile{}
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if snapshot.Files == nil {
		snapshot.Files = map[string]BaseSnapshotFile{}
	}
	return snapshot, nil
}

func (r Runner) writeBaseSnapshot(snapshot BaseSnapshot) error {
	if snapshot.Files == nil {
		snapshot.Files = map[string]BaseSnapshotFile{}
	}
	dir := filepath.Join(r.cwd(), ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "base_snapshot.json"), snapshot, 0o644)
}

func (r Runner) writeWorkspaceState(state WorkspaceState) error {
	dir := filepath.Join(r.cwd(), ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "state.json"), state, 0o644)
}

func (r Runner) userConfigPath() string {
	home := r.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".gitslice", "config.json")
}

func (r Runner) cwd() string {
	if r.Dir != "" {
		return r.Dir
	}
	dir, _ := os.Getwd()
	return dir
}

func (r Runner) runSchema() error {
	return writeJSON(r.Stdout, map[string]any{
		"schema_version": "v1",
		"global_flags": []map[string]any{
			{"name": "--format", "values": []string{"text", "json"}, "default": "text", "description": "output format"},
			{"name": "--json", "description": "alias for --format json"},
			{"name": "--quiet", "description": "suppress non-essential text output"},
			{"name": "--non-interactive", "description": "fail instead of prompting for input"},
			{"name": "--no-color", "description": "disable colorized output"},
			{"name": "--verbose", "description": "emit additional diagnostics"},
			{"name": "--debug", "description": "emit debug diagnostics"},
			{"name": "--trace", "description": "emit trace diagnostics"},
		},
		"commands": []map[string]any{
			{
				"use":            "gs auth login",
				"summary":        "log in through the development account service",
				"flags":          []string{"--server", "--dev-user"},
				"writes_stdout":  true,
				"machine_output": []string{"server_addr", "subject_id"},
			},
			{
				"use":            "gs workspace init <account>/<slice>",
				"summary":        "bind the current directory to one slice",
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "slice_id", "base_commit_id"},
			},
			{
				"use":            "gs status",
				"summary":        "show workspace changes against the local base snapshot",
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "changed_path_count", "changed_paths", "changeset_id", "patchset_id"},
			},
			{
				"use":            "gs cs create",
				"summary":        "create a changeset and first patchset from workspace edits",
				"flags":          []string{"--title"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id"},
			},
			{
				"use":            "gs cs update",
				"summary":        "create a new patchset for the current changeset",
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id"},
			},
			{
				"use":            "gs cs submit",
				"summary":        "submit the current changeset through server-side validation",
				"writes_stdout":  true,
				"machine_output": []string{"commit_id", "target_ref", "new_ref_commit_id"},
			},
			{
				"use":            "gs cs status",
				"summary":        "show the current changeset status",
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id", "status"},
			},
			{
				"use":            "gs repo import github <owner/repo-or-url>",
				"summary":        "import a GitHub repository under a mounted path",
				"flags":          []string{"--mount", "--slice", "--mode", "--deep", "--max-commits", "--resume"},
				"writes_stdout":  true,
				"machine_output": []string{"source", "mount_path", "mode", "target_ref", "final_commit_id", "commits"},
			},
			{
				"use":            "gs commit list",
				"summary":        "list native commits from the main ref",
				"flags":          []string{"--limit"},
				"writes_stdout":  true,
				"machine_output": []string{"commits"},
			},
			{
				"use":            "gs commit inspect <commit-id>",
				"summary":        "inspect a native commit",
				"args":           []string{"commit-id"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "parent_ids", "root_tree_id", "author", "message", "created_at", "changed_paths"},
			},
			{
				"use":           "gs schema",
				"summary":       "print this machine-readable CLI schema",
				"writes_stdout": true,
			},
		},
		"error_output": map[string]any{
			"stream": "stderr",
			"shape": map[string]any{
				"error": map[string]string{
					"code":      "stable snake_case error code",
					"message":   "human-readable message",
					"hint":      "optional next action",
					"retriable": "boolean",
				},
			},
		},
	})
}

func parseSliceRef(value string) (*corev1.SliceRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, userError("invalid_slice_ref", "slice must be account/slice", "Pass a slice reference such as acme/payment.")
	}
	return &corev1.SliceRef{Account: parts[0], Slice: parts[1]}, nil
}

func workspaceRef(ws WorkspaceConfig) *corev1.WorkspaceRef {
	return &corev1.WorkspaceRef{Id: ws.Account + "/" + ws.Slice}
}

func authContext(ctx context.Context, cfg UserConfig) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+cfg.Token)
}

func dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
}

func defaultServerAddr() string {
	if value := os.Getenv("GITSLICE_GRPC_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_SERVER_ADDR"); value != "" {
		return value
	}
	return "127.0.0.1:50051"
}

func requireSubcommand(name string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		_ = cmd.Help()
		return userError("missing_subcommand", "missing "+name+" subcommand", "Run gs "+name+" --help to list available subcommands.")
	}
}

func noArgs(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return userError("unexpected_args", "unexpected arguments: "+strings.Join(args, " "), "Usage: "+usage)
	}
}

func exactArgs(want int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == want {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected %d argument(s), got %d", want, len(args)), "Usage: "+usage)
	}
}

func userError(code, message, hint string) error {
	return commandError{Code: code, Message: message, Hint: hint}
}

func wantsJSON(args []string) bool {
	for i, arg := range args {
		if arg == "--json" {
			return true
		}
		if arg == "--format=json" {
			return true
		}
		if arg == "--format" && i+1 < len(args) && args[i+1] == "json" {
			return true
		}
	}
	return false
}

func classifyError(err error) errorResponse {
	body := errorBody{
		Code:      "command_failed",
		Message:   err.Error(),
		Retriable: false,
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		body.Code = cmdErr.Code
		body.Message = cmdErr.Message
		body.Hint = cmdErr.Hint
		body.Retriable = cmdErr.Retriable
		return errorResponse{Error: body}
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "not logged in"):
		body.Code = "not_logged_in"
		body.Hint = "Run gs auth login."
	case strings.Contains(msg, "not in a gitslice workspace"):
		body.Code = "not_in_workspace"
		body.Hint = "Run gs workspace init <account>/<slice>."
	case strings.Contains(msg, "outside slice"):
		body.Code = "outside_slice"
	case strings.Contains(msg, "FailedPrecondition"), strings.Contains(strings.ToLower(msg), "conflict"):
		body.Code = "conflict"
	case strings.Contains(msg, "Unavailable"), strings.Contains(msg, "DeadlineExceeded"), strings.Contains(msg, "connection refused"):
		body.Code = "server_unavailable"
		body.Retriable = true
	}
	return errorResponse{Error: body}
}

func snapshotFiles(current map[string]workingFile) map[string]BaseSnapshotFile {
	out := make(map[string]BaseSnapshotFile, len(current))
	for p, file := range current {
		out[p] = file.BaseSnapshotFile
	}
	return out
}

func sortFileEdits(edits []*corev1.FileEdit) {
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Path == edits[j].Path {
			return edits[i].Op < edits[j].Op
		}
		return edits[i].Path < edits[j].Path
	})
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func shouldSkip(rel string, entry fs.DirEntry) bool {
	first := rel
	if idx := strings.Index(first, "/"); idx >= 0 {
		first = first[:idx]
	}
	switch first {
	case ".git", ".gs", ".gitslice":
		return true
	default:
		return strings.HasPrefix(entry.Name(), ".")
	}
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSONFile(path string, v any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

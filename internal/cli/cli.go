package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/clientcache"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
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

type authStatusOutput struct {
	SignedIn   bool   `json:"signed_in"`
	ServerAddr string `json:"server_addr,omitempty"`
	SubjectID  string `json:"subject_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type hydrateResult struct {
	FileCount   int `json:"file_count"`
	CacheHits   int `json:"cache_hits"`
	CacheMisses int `json:"cache_misses"`
}

type sliceOutput struct {
	ID             string   `json:"id"`
	Ref            string   `json:"ref"`
	Account        string   `json:"account"`
	Slice          string   `json:"slice"`
	Version        int64    `json:"version"`
	Visibility     string   `json:"visibility"`
	IncludedPaths  []string `json:"included_paths"`
	DefinitionHash string   `json:"definition_hash"`
}

type fileMutationOutput struct {
	Operation      string   `json:"operation"`
	Slice          string   `json:"slice"`
	CommitID       string   `json:"commit_id"`
	NewRefCommitID string   `json:"new_ref_commit_id"`
	ChangedPaths   []string `json:"changed_paths"`
}

type fsListOutput struct {
	Path     string          `json:"path"`
	Slice    string          `json:"slice"`
	CommitID string          `json:"commit_id"`
	Entries  []fsEntryOutput `json:"entries"`
}

type fsEntryOutput struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Mode        uint32 `json:"mode,omitempty"`
	Size        int64  `json:"size,omitempty"`
	TreeID      string `json:"tree_id,omitempty"`
	BlobID      string `json:"blob_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

type fsCatOutput struct {
	Path        string `json:"path"`
	Slice       string `json:"slice"`
	CommitID    string `json:"commit_id"`
	ContentHash string `json:"content_hash,omitempty"`
	DataBase64  string `json:"data_base64"`
}

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBlue  = "\x1b[34m"
	ansiCyan  = "\x1b[36m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
)

func Main(args []string, stdout, stderr io.Writer) int {
	r := Runner{Stdout: stdout, Stderr: stderr, Stdin: os.Stdin}
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
	signupServer := defaultServerAddr()
	signupWebURL := defaultWebURL()
	signupUsername := ""
	signupNoBrowser := false
	signupCmd := &cobra.Command{
		Use:   "signup",
		Short: "Sign up through the browser approval flow",
		Args:  noArgs("gs auth signup --username name [--server addr] [--web-url url]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthSignup(cmd.Context(), *opts, signupServer, signupWebURL, signupUsername, !signupNoBrowser)
		},
	}
	signupCmd.Flags().StringVar(&signupUsername, "username", signupUsername, "username to create or sign in")
	signupCmd.Flags().StringVar(&signupServer, "server", signupServer, "server gRPC address to store after signup")
	signupCmd.Flags().StringVar(&signupWebURL, "web-url", signupWebURL, "server web signup base URL")
	signupCmd.Flags().BoolVar(&signupNoBrowser, "no-browser", signupNoBrowser, "print the approval URL without opening a browser")
	authStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show current authentication status",
		Args:  noArgs("gs auth status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthStatus(cmd.Context(), *opts)
		},
	}
	authCmd.AddCommand(loginCmd, signupCmd, authStatusCmd)

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
	workspaceHydrateCmd := &cobra.Command{
		Use:   "hydrate <path> [path...]",
		Short: "Hydrate workspace files through the client object cache",
		Args:  minArgs(1, "gs workspace hydrate <path> [path...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceHydrate(cmd.Context(), *opts, args)
		},
	}
	workspaceCmd.AddCommand(workspaceInitCmd, workspaceHydrateCmd)

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

	fsCmd := &cobra.Command{
		Use:     "fs",
		Aliases: []string{"file"},
		Short:   "Read and mutate files in the signed-in home slice",
		RunE:    requireSubcommand("fs"),
	}
	fsLsCmd := &cobra.Command{
		Use:   "ls [absolute-path]",
		Short: "List a remote directory or file",
		Args:  maxArgs(1, "gs fs ls [/account/path]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ""
			if len(args) > 0 {
				p = args[0]
			}
			return r.runFSList(cmd.Context(), *opts, p)
		},
	}
	fsCatCmd := &cobra.Command{
		Use:   "cat <absolute-path>",
		Short: "Print a remote file",
		Args:  exactArgs(1, "gs fs cat /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFSCat(cmd.Context(), *opts, args[0])
		},
	}
	fsMkdirCmd := &cobra.Command{
		Use:   "mkdir <absolute-path>",
		Short: "Create a remote directory",
		Args:  exactArgs(1, "gs fs mkdir /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileMkdir(cmd.Context(), *opts, args[0])
		},
	}
	fsTouchCmd := &cobra.Command{
		Use:   "touch <absolute-path>",
		Short: "Create an empty remote file",
		Args:  exactArgs(1, "gs fs touch /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileWrite(cmd.Context(), *opts, args[0], nil)
		},
	}
	fsWriteText := ""
	fsWriteStdin := false
	fsWriteCmd := &cobra.Command{
		Use:   "write <absolute-path> (--text text|--stdin)",
		Short: "Create or replace a remote file",
		Args:  exactArgs(1, "gs fs write /account/path --text text"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fsWriteStdin && fsWriteText != "" {
				return userError("invalid_args", "use --text or --stdin, not both", "Run gs fs write /account/path --text text.")
			}
			if !fsWriteStdin && fsWriteText == "" {
				return userError("invalid_args", "file content is required", "Pass --text text or --stdin.")
			}
			var data []byte
			if fsWriteStdin {
				var err error
				data, err = io.ReadAll(r.stdin())
				if err != nil {
					return err
				}
			} else {
				data = []byte(fsWriteText)
			}
			return r.runFileWrite(cmd.Context(), *opts, args[0], data)
		},
	}
	fsWriteCmd.Flags().StringVar(&fsWriteText, "text", fsWriteText, "file content")
	fsWriteCmd.Flags().BoolVar(&fsWriteStdin, "stdin", fsWriteStdin, "read file content from stdin")
	fsMvCmd := &cobra.Command{
		Use:   "mv <absolute-old-path> <absolute-new-path>",
		Short: "Rename or move a remote file or directory",
		Args:  exactArgs(2, "gs fs mv /account/old /account/new"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileMove(cmd.Context(), *opts, args[0], args[1])
		},
	}
	fsRmCmd := &cobra.Command{
		Use:   "rm <absolute-path>",
		Short: "Delete a remote file or directory",
		Args:  exactArgs(1, "gs fs rm /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileRemove(cmd.Context(), *opts, args[0])
		},
	}
	fsCmd.AddCommand(fsLsCmd, fsCatCmd, fsMkdirCmd, fsTouchCmd, fsWriteCmd, fsMvCmd, fsRmCmd)

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

	shellCommit := ""
	shellSlice := ""
	shellCmd := &cobra.Command{
		Use:   "shell",
		Short: "Browse server-side files",
		Args:  noArgs("gs shell [--slice account/slice] [--commit commit-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runShell(cmd.Context(), *opts, shellCommit, shellSlice)
		},
	}
	shellCmd.Flags().StringVar(&shellCommit, "commit", shellCommit, "native commit id to inspect; defaults to refs/global/main")
	shellCmd.Flags().StringVar(&shellSlice, "slice", shellSlice, "slice to attach, defaults to workspace slice or personal home slice")

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
		Short: "Manage slices",
		RunE:  requireSubcommand("slice"),
	}
	sliceCreateVisibility := "account"
	var sliceCreateIncludes []string
	sliceCreateCmd := &cobra.Command{
		Use:   "create <account>/<slice>",
		Short: "Create a slice",
		Args:  exactArgs(1, "gs slice create <account>/<slice> [--include /account/path] [--visibility account|public]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceCreate(cmd.Context(), *opts, args[0], sliceCreateIncludes, sliceCreateVisibility)
		},
	}
	sliceCreateCmd.Flags().StringArrayVar(&sliceCreateIncludes, "include", nil, "included global path; repeat for multiple paths")
	sliceCreateCmd.Flags().StringVar(&sliceCreateVisibility, "visibility", sliceCreateVisibility, "slice visibility: account or public")
	sliceListCmd := &cobra.Command{
		Use:   "list [account]",
		Short: "List slices in an account",
		Args:  maxArgs(1, "gs slice list [account]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) > 0 {
				account = args[0]
			}
			return r.runSliceList(cmd.Context(), *opts, account)
		},
	}
	sliceInfoCmd := &cobra.Command{
		Use:   "info <account>/<slice>",
		Short: "Show slice metadata",
		Args:  exactArgs(1, "gs slice info <account>/<slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceInfo(cmd.Context(), *opts, args[0])
		},
	}
	slicePathsCmd := &cobra.Command{
		Use:   "paths <account>/<slice>",
		Short: "Show slice included paths",
		Args:  exactArgs(1, "gs slice paths <account>/<slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSlicePaths(cmd.Context(), *opts, args[0])
		},
	}
	sliceUpdateVisibility := ""
	var sliceUpdateIncludes []string
	sliceUpdateCmd := &cobra.Command{
		Use:   "update <account>/<slice>",
		Short: "Update slice included paths or visibility",
		Args:  exactArgs(1, "gs slice update <account>/<slice> [--include /account/path] [--visibility account|public]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			visibilityChanged := cmd.Flags().Changed("visibility")
			includesChanged := cmd.Flags().Changed("include")
			return r.runSliceUpdate(cmd.Context(), *opts, args[0], sliceUpdateIncludes, includesChanged, sliceUpdateVisibility, visibilityChanged)
		},
	}
	sliceUpdateCmd.Flags().StringArrayVar(&sliceUpdateIncludes, "include", nil, "replacement included global path; repeat for multiple paths")
	sliceUpdateCmd.Flags().StringVar(&sliceUpdateVisibility, "visibility", sliceUpdateVisibility, "slice visibility: account or public")
	sliceDeleteYes := false
	sliceDeleteCmd := &cobra.Command{
		Use:   "delete <account>/<slice>",
		Short: "Delete a slice",
		Args:  exactArgs(1, "gs slice delete <account>/<slice> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceDelete(cmd.Context(), *opts, args[0], sliceDeleteYes)
		},
	}
	sliceDeleteCmd.Flags().BoolVar(&sliceDeleteYes, "yes", sliceDeleteYes, "confirm slice deletion")
	sliceCmd.AddCommand(sliceCreateCmd, sliceListCmd, sliceInfoCmd, slicePathsCmd, sliceUpdateCmd, sliceDeleteCmd)

	root.AddCommand(authCmd, workspaceCmd, statusCmd, csCmd, fsCmd, repoCmd, commitCmd, shellCmd, schemaCmd, sliceCmd)
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

func (r Runner) runAuthSignup(ctx context.Context, opts commandOptions, serverAddr, webURL, username string, openBrowser bool) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return userError("invalid_args", "username is required", "Run gs auth signup --username <name>.")
	}
	callbackLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer callbackLis.Close()

	state, err := objectid.RandomID("signupstate")
	if err != nil {
		return err
	}
	callbackURL := "http://" + callbackLis.Addr().String() + "/callback"
	approvalURL, err := signupApprovalURL(webURL, username, callbackURL, state)
	if err != nil {
		return err
	}

	resultCh := make(chan signupCallbackResult, 1)
	callbackServer := &http.Server{Handler: signupCallbackHandler(state, resultCh)}
	go func() {
		if err := callbackServer.Serve(callbackLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case resultCh <- signupCallbackResult{Error: err.Error()}:
			default:
			}
		}
	}()
	defer callbackServer.Shutdown(context.Background())

	promptWriter := r.stdout()
	if opts.jsonOutput() {
		promptWriter = r.stderr()
	}
	if !opts.Quiet || !openBrowser {
		fmt.Fprintln(promptWriter, "Open this URL to approve signup:")
		fmt.Fprintln(promptWriter, approvalURL)
	}
	if openBrowser {
		if err := openBrowserURL(approvalURL); err != nil && !opts.Quiet {
			fmt.Fprintf(r.stderr(), "could not open browser automatically: %v\n", err)
		}
	}
	if !opts.Quiet {
		fmt.Fprintln(promptWriter, "Waiting for browser approval...")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var result signupCallbackResult
	select {
	case result = <-resultCh:
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
	if result.Error != "" {
		return userError("signup_failed", result.Error, "Try gs auth signup again.")
	}
	cfg := UserConfig{ServerAddr: serverAddr, Token: result.Token, SubjectID: result.SubjectID}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"server_addr": serverAddr,
			"subject_id":  result.SubjectID,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "signed up as %s\n", result.SubjectID)
	return nil
}

type signupCallbackResult struct {
	Token     string
	SubjectID string
	Error     string
}

func signupCallbackHandler(expectedState string, resultCh chan<- signupCallbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/callback" {
			http.NotFound(w, req)
			return
		}
		values := req.URL.Query()
		if values.Get("state") != expectedState {
			http.Error(w, "invalid signup state", http.StatusBadRequest)
			return
		}
		result := signupCallbackResult{
			Token:     values.Get("token"),
			SubjectID: values.Get("subject_id"),
			Error:     values.Get("error"),
		}
		if result.Error == "" && (result.Token == "" || result.SubjectID == "") {
			result.Error = "signup callback did not include token and subject_id"
		}
		if result.Error == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintln(w, "<!doctype html><title>Gitslice Signup Complete</title><p>Signup complete. Return to your terminal.</p>")
		} else {
			http.Error(w, result.Error, http.StatusBadRequest)
		}
		select {
		case resultCh <- result:
		default:
		}
	})
}

func signupApprovalURL(webURL, username, callbackURL, state string) (string, error) {
	webURL = strings.TrimSpace(webURL)
	if webURL == "" {
		webURL = defaultWebURL()
	}
	if !strings.Contains(webURL, "://") {
		webURL = "http://" + webURL
	}
	parsed, err := url.Parse(webURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("web-url must use http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/signup"
	query := parsed.Query()
	query.Set("username", username)
	query.Set("callback_url", callbackURL)
	query.Set("gateway_url", defaultGatewayURL())
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func openBrowserURL(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

func (r Runner) runAuthStatus(ctx context.Context, opts commandOptions) error {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return r.writeAuthStatus(opts, authStatusOutput{Reason: "not_logged_in"})
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return r.writeAuthStatus(opts, authStatusOutput{Reason: "invalid_config"})
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return r.writeAuthStatus(opts, authStatusOutput{
				ServerAddr: cfg.ServerAddr,
				Reason:     "invalid_token",
			})
		}
		return err
	}
	status := authStatusOutput{
		SignedIn:   true,
		ServerAddr: cfg.ServerAddr,
		SubjectID:  res.SubjectId,
	}
	return r.writeAuthStatus(opts, status)
}

func (r Runner) writeAuthStatus(opts commandOptions, status authStatusOutput) error {
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, status)
	}
	if opts.Quiet {
		return nil
	}
	if !status.SignedIn {
		fmt.Fprintln(r.Stdout, "not logged in")
		if status.ServerAddr != "" {
			fmt.Fprintf(r.Stdout, "server: %s\n", status.ServerAddr)
		}
		if status.Reason != "" && status.Reason != "not_logged_in" {
			fmt.Fprintf(r.Stdout, "reason: %s\n", strings.ReplaceAll(status.Reason, "_", " "))
		}
		return nil
	}
	if status.SubjectID != "" {
		fmt.Fprintf(r.Stdout, "signed in as %s\n", status.SubjectID)
	} else {
		fmt.Fprintln(r.Stdout, "signed in")
	}
	fmt.Fprintf(r.Stdout, "server: %s\n", status.ServerAddr)
	return nil
}

func (r Runner) runSliceCreate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, visibility string) error {
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	if len(includedPaths) == 0 {
		includedPaths = defaultSliceIncludedPaths(ref)
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	slice, err := corev1.NewSliceServiceClient(conn).CreateSlice(callCtx, &corev1.CreateSliceRequest{
		Ref:           ref,
		IncludedPaths: includedPaths,
		Visibility:    visibility,
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, sliceToOutput(slice))
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "created slice %s\n", sliceRefLabel(slice.Ref))
	return writeSliceText(r.Stdout, slice)
}

func (r Runner) runSliceList(ctx context.Context, opts commandOptions, account string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if strings.TrimSpace(account) == "" {
		account, err = r.defaultSliceAccount(callCtx, cfg, conn)
		if err != nil {
			return err
		}
	} else {
		account, err = normalizeCLISlug(account, "account")
		if err != nil {
			return err
		}
	}
	res, err := corev1.NewSliceServiceClient(conn).ListSlices(callCtx, &corev1.ListSlicesRequest{
		Account:  account,
		PageSize: 1000,
	})
	if err != nil {
		return err
	}
	out := make([]sliceOutput, 0, len(res.Slices))
	for _, slice := range res.Slices {
		out = append(out, sliceToOutput(slice))
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"account": account,
			"slices":  out,
		})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintf(r.Stdout, "no slices found for account %s\n", account)
		return nil
	}
	fmt.Fprintf(r.Stdout, "slices for account %s:\n", account)
	for _, slice := range out {
		fmt.Fprintf(r.Stdout, "  %s\n", slice.Ref)
		fmt.Fprintf(r.Stdout, "    visibility: %s\n", slice.Visibility)
		fmt.Fprintf(r.Stdout, "    included paths: %s\n", strings.Join(slice.IncludedPaths, ", "))
	}
	return nil
}

func (r Runner) runSliceInfo(ctx context.Context, opts commandOptions, sliceRef string) error {
	slice, err := r.resolveSlice(ctx, sliceRef)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, sliceToOutput(slice))
	}
	if opts.Quiet {
		return nil
	}
	return writeSliceText(r.Stdout, slice)
}

func (r Runner) runSlicePaths(ctx context.Context, opts commandOptions, sliceRef string) error {
	slice, err := r.resolveSlice(ctx, sliceRef)
	if err != nil {
		return err
	}
	out := sliceToOutput(slice)
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"ref":            out.Ref,
			"included_paths": out.IncludedPaths,
		})
	}
	if opts.Quiet {
		return nil
	}
	for _, p := range out.IncludedPaths {
		fmt.Fprintln(r.Stdout, p)
	}
	return nil
}

func (r Runner) runSliceUpdate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, includesChanged bool, visibility string, visibilityChanged bool) error {
	if !includesChanged && !visibilityChanged {
		return userError("invalid_args", "no slice updates requested", "Pass --include, --visibility, or both.")
	}
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewSliceServiceClient(conn)
	current, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	nextIncluded := append([]string{}, current.Definition.IncludedPaths...)
	if includesChanged {
		nextIncluded = includedPaths
	}
	nextVisibility := current.Definition.Visibility
	if visibilityChanged {
		nextVisibility = visibility
	}
	_, err = client.UpdateSliceDefinition(callCtx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                current.Id,
		ExpectedDefinitionHash: current.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: nextIncluded,
			Visibility:    nextVisibility,
		},
	})
	if err != nil {
		return err
	}
	updated, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, sliceToOutput(updated))
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated slice %s\n", sliceRefLabel(updated.Ref))
	return writeSliceText(r.Stdout, updated)
}

func (r Runner) runSliceDelete(ctx context.Context, opts commandOptions, sliceRef string, yes bool) error {
	if !yes {
		return userError("confirmation_required", "slice deletion requires --yes", "Run gs slice delete "+sliceRef+" --yes to confirm.")
	}
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewSliceServiceClient(conn)
	slice, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	_, err = client.DeleteSlice(callCtx, &corev1.DeleteSliceRequest{SliceId: slice.Id})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"slice_id": slice.Id,
			"ref":      sliceRefLabel(slice.Ref),
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "deleted slice %s\n", sliceRefLabel(slice.Ref))
	return nil
}

func (r Runner) resolveSlice(ctx context.Context, sliceRef string) (*corev1.Slice, error) {
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return nil, err
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return corev1.NewSliceServiceClient(conn).ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
}

func (r Runner) authenticatedConn(ctx context.Context) (UserConfig, *grpc.ClientConn, context.Context, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	return cfg, conn, authContext(ctx, cfg), nil
}

func (r Runner) defaultSliceAccount(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (string, error) {
	subjectID := cfg.SubjectID
	if subjectID == "" {
		status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
		if err != nil {
			return "", err
		}
		subjectID = status.SubjectId
	}
	account, ok := personalAccountSlugFromSubjectID(subjectID)
	if !ok {
		return "", userError("account_required", "account is required", "Run gs slice list <account>.")
	}
	return account, nil
}

func defaultSliceIncludedPaths(ref *corev1.SliceRef) []string {
	if ref != nil && ref.Slice == "home" {
		return []string{"/" + ref.Account}
	}
	return []string{"/" + ref.Account + "/" + ref.Slice}
}

func writeSliceText(w io.Writer, slice *corev1.Slice) error {
	out := sliceToOutput(slice)
	fmt.Fprintf(w, "ref: %s\n", out.Ref)
	fmt.Fprintf(w, "id: %s\n", out.ID)
	fmt.Fprintf(w, "version: %d\n", out.Version)
	fmt.Fprintf(w, "visibility: %s\n", out.Visibility)
	fmt.Fprintf(w, "definition_hash: %s\n", out.DefinitionHash)
	fmt.Fprintln(w, "included_paths:")
	for _, p := range out.IncludedPaths {
		fmt.Fprintf(w, "  %s\n", p)
	}
	return nil
}

func sliceToOutput(slice *corev1.Slice) sliceOutput {
	if slice == nil {
		return sliceOutput{}
	}
	out := sliceOutput{
		ID:             slice.Id,
		DefinitionHash: slice.DefinitionHash,
	}
	if slice.Ref != nil {
		out.Account = slice.Ref.Account
		out.Slice = slice.Ref.Slice
		out.Ref = sliceRefLabel(slice.Ref)
	}
	if slice.Definition != nil {
		out.Version = slice.Definition.Version
		out.Visibility = slice.Definition.Visibility
		out.IncludedPaths = append([]string{}, slice.Definition.IncludedPaths...)
	}
	return out
}

func sliceRefLabel(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return ref.Account + "/" + ref.Slice
}

func (r Runner) runWorkspaceInit(ctx context.Context, opts commandOptions, sliceRef string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	cache, err := r.objectCache()
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
	hydrated, err := r.hydrateWorkspacePaths(callCtx, conn, workspace, refRecord.CommitId, workspace.IncludedPaths, cache)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"workspace":           ref.Account + "/" + ref.Slice,
			"slice_id":            slice.Id,
			"base_commit_id":      refRecord.CommitId,
			"client_object_cache": cache.Root(),
			"hydrated":            hydrated,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "initialized workspace for %s/%s\n", ref.Account, ref.Slice)
	fmt.Fprintf(r.Stdout, "hydrated %d file(s) through cache (%d hit(s), %d miss(es))\n", hydrated.FileCount, hydrated.CacheHits, hydrated.CacheMisses)
	return nil
}

func (r Runner) runWorkspaceHydrate(ctx context.Context, opts commandOptions, requested []string) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	baseCommitID := state.BaseCommitID
	if baseCommitID == "" {
		baseCommitID = ws.BaseCommitID
	}
	if baseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs workspace init <account>/<slice> again.")
	}
	hydrated, err := r.hydrateWorkspacePaths(authContext(ctx, cfg), conn, ws, baseCommitID, requested, cache)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, map[string]any{
			"workspace":           ws.Account + "/" + ws.Slice,
			"base_commit_id":      baseCommitID,
			"client_object_cache": cache.Root(),
			"hydrated":            hydrated,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "hydrated %d file(s) through cache (%d hit(s), %d miss(es))\n", hydrated.FileCount, hydrated.CacheHits, hydrated.CacheMisses)
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

func (r Runner) runFSList(ctx context.Context, opts commandOptions, requestedPath string) error {
	cfg, conn, slice, repo, commitID, err := r.homeFSReadScope(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	root, err := homeSliceRoot(slice)
	if err != nil {
		return err
	}
	p := strings.TrimSpace(requestedPath)
	if p == "" {
		p = root
	}
	p, err = normalizeHomePath(slice, p, true)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, cfg)
	var entries []*corev1.TreeEntry
	resolved, err := repo.ResolvePath(callCtx, &corev1.ResolvePathRequest{CommitId: commitID, Path: p})
	if err != nil {
		if grpcstatus.Code(err) != codes.NotFound || p != root {
			return err
		}
		entries = []*corev1.TreeEntry{}
	} else if resolved.Entry != nil && resolved.Entry.Kind == corev1.EntryKind_ENTRY_KIND_FILE {
		entries = []*corev1.TreeEntry{resolved.Entry}
	} else {
		list, err := repo.ListDirectory(callCtx, &corev1.ListDirectoryRequest{CommitId: commitID, Path: p, PageSize: 1000})
		if err != nil {
			return err
		}
		entries = append(entries, list.Entries...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return fsEntryName(entries[i]) < fsEntryName(entries[j])
	})
	if opts.jsonOutput() {
		out := fsListOutput{
			Path:     p,
			Slice:    slice.Ref.Account + "/" + slice.Ref.Slice,
			CommitID: commitID,
			Entries:  make([]fsEntryOutput, 0, len(entries)),
		}
		for _, entry := range entries {
			out.Entries = append(out.Entries, fsEntryOutputFromProto(entry))
		}
		return writeJSON(r.Stdout, out)
	}
	if opts.Quiet {
		return nil
	}
	color := r.colorEnabled(opts)
	for _, entry := range entries {
		name := fsEntryName(entry)
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			name += "/"
			name = colorize(color, ansiBlue, name)
		}
		fmt.Fprintln(r.Stdout, name)
	}
	return nil
}

func (r Runner) runFSCat(ctx context.Context, opts commandOptions, requestedPath string) error {
	cfg, conn, slice, repo, commitID, err := r.homeFSReadScope(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	p, err := normalizeHomePath(slice, requestedPath, true)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, cfg)
	resolved, err := repo.ResolvePath(callCtx, &corev1.ResolvePathRequest{CommitId: commitID, Path: p})
	if err != nil {
		return err
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		return fmt.Errorf("%s is not a file", p)
	}
	read, err := repo.ReadFile(callCtx, &corev1.ReadFileRequest{CommitId: commitID, Path: p})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return writeJSON(r.Stdout, fsCatOutput{
			Path:        p,
			Slice:       slice.Ref.Account + "/" + slice.Ref.Slice,
			CommitID:    commitID,
			ContentHash: read.ContentHash,
			DataBase64:  base64.StdEncoding.EncodeToString(read.Data),
		})
	}
	if opts.Quiet {
		return nil
	}
	_, err = r.stdout().Write(read.Data)
	return err
}

func (r Runner) homeFSReadScope(ctx context.Context) (UserConfig, *grpc.ClientConn, *corev1.Slice, corev1.RepositoryServiceClient, string, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, nil, nil, nil, "", err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, nil, nil, nil, "", err
	}
	callCtx := authContext(ctx, cfg)
	slice, err := r.personalHomeSlice(callCtx, cfg, conn)
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, nil, "", err
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, nil, "", err
	}
	return cfg, conn, slice, repo, ref.CommitId, nil
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
	edit, err := r.uploadFileEdit(authContext(ctx, cfg), conn, cleaned, data)
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

func (r Runner) personalHomeSlice(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (*corev1.Slice, error) {
	subjectID := cfg.SubjectID
	if subjectID == "" {
		status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
		if err != nil {
			return nil, err
		}
		subjectID = status.SubjectId
	}
	accountSlug, ok := personalAccountSlugFromSubjectID(subjectID)
	if !ok {
		return nil, userError("no_home_slice", "signed-in subject does not have a personal home slice", "Run gs auth signup --username <name>.")
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: accountSlug, Slice: "home"},
	})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound || grpcstatus.Code(err) == codes.PermissionDenied {
			return nil, userError("no_home_slice", "personal home slice was not found for "+accountSlug, "Run gs auth signup --username "+accountSlug+".")
		}
		return nil, err
	}
	return slice, nil
}

func (r Runner) uploadFileEdit(ctx context.Context, conn *grpc.ClientConn, p string, data []byte) (*corev1.FileEdit, error) {
	upload, err := corev1.NewBlobServiceClient(conn).UploadBlob(ctx, &corev1.UploadBlobRequest{Data: data})
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
	title := "file " + operation + " " + strings.Join(changed, " ")
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
		commitID, refCommitID, err = m.runner.waitForChangesetPublished(ctx, m.conn, m.cfg, cs.Id)
		if err != nil {
			return err
		}
	}
	output := fileMutationOutput{
		Operation:      operation,
		Slice:          m.slice.Ref.Account + "/" + m.slice.Ref.Slice,
		CommitID:       commitID,
		NewRefCommitID: refCommitID,
		ChangedPaths:   changed,
	}
	if opts.jsonOutput() {
		return writeJSON(m.runner.stdout(), output)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(m.runner.stdout(), "%s %s in %s at %s\n", operationPastTense(operation), strings.Join(changed, ", "), output.Slice, shortID(refCommitID))
	return nil
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

func fsEntryName(entry *corev1.TreeEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Name != "" {
		return entry.Name
	}
	return path.Base(entry.Path)
}

func fsEntryOutputFromProto(entry *corev1.TreeEntry) fsEntryOutput {
	if entry == nil {
		return fsEntryOutput{}
	}
	return fsEntryOutput{
		Path:        entry.Path,
		Name:        fsEntryName(entry),
		Kind:        entryKindName(entry.Kind),
		Mode:        entry.Mode,
		Size:        entry.Size,
		TreeID:      entry.TreeId,
		BlobID:      entry.BlobId,
		ContentHash: entry.ContentHash,
	}
}

func operationPastTense(operation string) string {
	switch operation {
	case "mkdir":
		return "created"
	case "write":
		return "wrote"
	case "mv":
		return "moved"
	case "rm":
		return "removed"
	default:
		return operation
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

func (r Runner) runShell(ctx context.Context, opts commandOptions, commitID, sliceRef string) error {
	if opts.jsonOutput() {
		return userError("unsupported_format", "gs shell only supports text output", "Run gs shell without --json or --format json.")
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
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
		ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
		if err != nil {
			return err
		}
		commitID = ref.CommitId
	}
	var mutator *remoteFileMutator
	if mutationSlice != nil && !pinnedCommit {
		mutator = &remoteFileMutator{runner: r, cfg: cfg, conn: conn, slice: mutationSlice}
	}
	projectionRoots, err := explicitShellProjectionRoots(sliceRef, mutationSlice)
	if err != nil {
		return err
	}
	sh := &serverShell{
		runner:        r,
		stdout:        r.stdout(),
		stderr:        r.stderr(),
		repo:          repo,
		mutator:       mutator,
		root:          rootPath,
		cwd:           rootPath,
		commitID:      commitID,
		scope:         scopeLabel,
		scoped:        workspaceScoped,
		syntheticDirs: syntheticDirs,
		projection:    projectionRoots,
		color:         r.colorEnabled(opts),
		stickyHeader:  r.stickyShellHeaderEnabled(opts),
	}
	if !opts.Quiet {
		sh.printHeader()
		defer sh.restoreHeader()
	}
	scanner := bufio.NewScanner(r.stdin())
	for {
		if !opts.Quiet {
			sh.refreshHeader()
			fmt.Fprint(sh.stdout, sh.prompt())
		}
		if !scanner.Scan() {
			break
		}
		done, err := sh.exec(callCtx, scanner.Text())
		if err != nil {
			fmt.Fprintf(sh.stderr, "%s: %v\n", sh.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
	return scanner.Err()
}

func (r Runner) shellScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceRef string) (rootPath, scopeLabel string, workspaceScoped bool, syntheticDirs map[string]*corev1.TreeEntry, mutationSlice *corev1.Slice, err error) {
	if strings.TrimSpace(sliceRef) != "" {
		return r.explicitShellScope(ctx, conn, sliceRef)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		var cmdErr commandError
		if errors.As(err, &cmdErr) && cmdErr.Code == "not_in_workspace" {
			scopeLabel, syntheticDirs, err := r.personalHomeShellScope(ctx, cfg, conn)
			if err != nil {
				return "", "", false, nil, nil, err
			}
			if scopeLabel != "" {
				slice, err := r.personalHomeSlice(ctx, cfg, conn)
				if err != nil {
					return "", "", false, nil, nil, err
				}
				return "/", scopeLabel, false, syntheticDirs, slice, nil
			}
			return "/", "/", false, nil, nil, nil
		}
		return "", "", false, nil, nil, err
	}
	if len(ws.IncludedPaths) == 0 {
		return "", "", false, nil, nil, fmt.Errorf("workspace has no included paths")
	}
	rootPath, err = canonicalIncludedRoot(ws.IncludedPaths[0])
	if err != nil {
		return "", "", false, nil, nil, err
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
	})
	if err != nil {
		return "", "", false, nil, nil, err
	}
	return rootPath, ws.Account + "/" + ws.Slice, true, nil, slice, nil
}

func (r Runner) explicitShellScope(ctx context.Context, conn *grpc.ClientConn, sliceRef string) (rootPath, scopeLabel string, workspaceScoped bool, syntheticDirs map[string]*corev1.TreeEntry, mutationSlice *corev1.Slice, err error) {
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return "", "", false, nil, nil, err
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return "", "", false, nil, nil, err
	}
	if slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return "", "", false, nil, nil, fmt.Errorf("slice %s/%s has no included paths", ref.Account, ref.Slice)
	}
	rootPath = "/"
	return rootPath, ref.Account + "/" + ref.Slice, true, nil, slice, nil
}

func explicitShellProjectionRoots(sliceRef string, slice *corev1.Slice) ([]string, error) {
	if strings.TrimSpace(sliceRef) == "" {
		return nil, nil
	}
	if slice == nil || slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, len(slice.Definition.IncludedPaths))
	for _, included := range slice.Definition.IncludedPaths {
		root, err := canonicalIncludedRoot(included)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func (r Runner) personalHomeShellScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (string, map[string]*corev1.TreeEntry, error) {
	slice, err := r.personalHomeSlice(ctx, cfg, conn)
	if err != nil {
		var cmdErr commandError
		if errors.As(err, &cmdErr) && cmdErr.Code == "no_home_slice" {
			return "", nil, nil
		}
		return "", nil, err
	}
	accountSlug := slice.Ref.Account
	homeRoot := "/" + accountSlug
	if slice.Definition != nil && len(slice.Definition.IncludedPaths) > 0 {
		root, err := cleanShellGlobalPath(slice.Definition.IncludedPaths[0])
		if err != nil {
			return "", nil, err
		}
		homeRoot = root
	}
	return accountSlug + "/home", map[string]*corev1.TreeEntry{
		homeRoot: &corev1.TreeEntry{
			Path: homeRoot,
			Name: path.Base(homeRoot),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		},
	}, nil
}

func personalAccountSlugFromSubjectID(subjectID string) (string, bool) {
	if !strings.HasPrefix(subjectID, "user_") {
		return "", false
	}
	slug := strings.TrimPrefix(subjectID, "user_")
	if slug == "" {
		return "", false
	}
	return strings.ReplaceAll(slug, "_", "-"), true
}

func canonicalIncludedRoot(value string) (string, error) {
	cleaned, err := cleanShellGlobalPath(value)
	if err != nil {
		return "", err
	}
	if cleaned == "/" {
		return "", fmt.Errorf("workspace included path must not be repository root")
	}
	return cleaned, nil
}

type serverShell struct {
	runner        Runner
	stdout        io.Writer
	stderr        io.Writer
	repo          corev1.RepositoryServiceClient
	mutator       *remoteFileMutator
	root          string
	cwd           string
	commitID      string
	scope         string
	scoped        bool
	syntheticDirs map[string]*corev1.TreeEntry
	projection    []string
	color         bool
	stickyHeader  bool
	headerActive  bool
}

func (s *serverShell) exec(ctx context.Context, line string) (bool, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, nil
	}
	switch fields[0] {
	case "exit", "quit":
		return true, nil
	case "help", "?":
		s.printHelp()
	case "pwd":
		if len(fields) != 1 {
			return false, fmt.Errorf("usage: pwd")
		}
		fmt.Fprintln(s.stdout, s.shellPath(s.cwd))
	case "ref":
		if len(fields) != 1 {
			return false, fmt.Errorf("usage: ref")
		}
		fmt.Fprintln(s.stdout, s.commitID)
	case "cd":
		target := "."
		if len(fields) > 2 {
			return false, fmt.Errorf("usage: cd [path]")
		}
		if len(fields) == 2 {
			target = fields[1]
		}
		return false, s.cd(ctx, target)
	case "ls":
		target := "."
		if len(fields) > 2 {
			return false, fmt.Errorf("usage: ls [path]")
		}
		if len(fields) == 2 {
			target = fields[1]
		}
		return false, s.ls(ctx, target)
	case "cat":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: cat <file>")
		}
		return false, s.cat(ctx, fields[1])
	case "stat":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: stat <path>")
		}
		return false, s.stat(ctx, fields[1])
	case "mkdir":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: mkdir <path>")
		}
		p, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "mkdir", []*corev1.FileEdit{{Op: "mkdir", Path: p}})
	case "touch":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: touch <file>")
		}
		return false, s.write(ctx, fields[1], nil)
	case "write":
		if len(fields) < 3 {
			return false, fmt.Errorf("usage: write <file> <text>")
		}
		return false, s.write(ctx, fields[1], []byte(strings.Join(fields[2:], " ")))
	case "mv":
		if len(fields) != 3 {
			return false, fmt.Errorf("usage: mv <old-path> <new-path>")
		}
		oldPath, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		newPath, err := s.resolve(fields[2])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "mv", []*corev1.FileEdit{{Op: "rename", OldPath: oldPath, Path: newPath}})
	case "rm":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: rm <path>")
		}
		p, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "rm", []*corev1.FileEdit{{Op: "delete", Path: p}})
	default:
		return false, fmt.Errorf("unknown command %q", fields[0])
	}
	return false, nil
}

func (s *serverShell) printHeader() {
	if s.stickyHeader {
		s.headerActive = true
		fmt.Fprint(s.stdout, "\x1b[2J\x1b[H")
		s.refreshHeader()
		fmt.Fprint(s.stdout, "\x1b[4;r\x1b[4;1H")
		return
	}
	fmt.Fprintf(s.stdout, "%s: %s @ %s\n", s.colorize(ansiGreen, "server shell"), s.scope, shortID(s.commitID))
	fmt.Fprintf(s.stdout, "%s: %s\n", s.colorize(ansiDim, "cwd"), s.shellPath(s.cwd))
	fmt.Fprintln(s.stdout, s.colorize(ansiDim, "type help for commands"))
}

func (s *serverShell) refreshHeader() {
	if !s.stickyHeader || !s.headerActive {
		return
	}
	mode := "read-only"
	if s.mutator != nil {
		mode = "mutable"
	}
	scopeLabel := s.scope
	if scopeLabel == "/" {
		scopeLabel = "global root"
	}
	fmt.Fprint(s.stdout, "\x1b[s")
	fmt.Fprint(s.stdout, "\x1b[r")
	fmt.Fprint(s.stdout, "\x1b[1;1H\x1b[2K")
	fmt.Fprintf(s.stdout, "%s  %s %s  %s %s  %s %s\n",
		s.colorize(ansiGreen, "Gitslice shell"),
		s.colorize(ansiDim, "slice:"), scopeLabel,
		s.colorize(ansiDim, "commit:"), shortID(s.commitID),
		s.colorize(ansiDim, "mode:"), mode)
	fmt.Fprint(s.stdout, "\x1b[2K")
	fmt.Fprintf(s.stdout, "%s %s  %s %s  %s\n",
		s.colorize(ansiDim, "cwd:"), s.shellPath(s.cwd),
		s.colorize(ansiDim, "root:"), s.shellPath(s.root),
		s.colorize(ansiDim, "help: type help, exit to quit"))
	fmt.Fprint(s.stdout, "\x1b[2K")
	fmt.Fprintln(s.stdout, s.colorize(ansiDim, strings.Repeat("-", 72)))
	fmt.Fprint(s.stdout, "\x1b[4;r")
	fmt.Fprint(s.stdout, "\x1b[u")
}

func (s *serverShell) restoreHeader() {
	if !s.stickyHeader || !s.headerActive {
		return
	}
	fmt.Fprint(s.stdout, "\x1b[r\n")
	s.headerActive = false
}

func (s *serverShell) printHelp() {
	fmt.Fprintln(s.stdout, "commands:")
	fmt.Fprintln(s.stdout, "  pwd              print current server path")
	fmt.Fprintln(s.stdout, "  ls [path]        list a server directory")
	fmt.Fprintln(s.stdout, "  cd [path]        change server directory")
	fmt.Fprintln(s.stdout, "  cat <file>       print a server file")
	fmt.Fprintln(s.stdout, "  stat <path>      inspect server file or directory metadata")
	fmt.Fprintln(s.stdout, "  mkdir <path>     create a server directory")
	fmt.Fprintln(s.stdout, "  write <file> <text>")
	fmt.Fprintln(s.stdout, "                   create or replace a server file")
	fmt.Fprintln(s.stdout, "  touch <file>     create an empty server file")
	fmt.Fprintln(s.stdout, "  mv <old> <new>   rename or move a server file or directory")
	fmt.Fprintln(s.stdout, "  rm <path>        delete a server file or directory")
	fmt.Fprintln(s.stdout, "  ref              print inspected commit id")
	fmt.Fprintln(s.stdout, "  help             show this help")
	fmt.Fprintln(s.stdout, "  exit, quit       leave the shell")
}

func (s *serverShell) cd(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	if globalPath == s.root {
		s.cwd = globalPath
		return nil
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	if entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		return fmt.Errorf("%s is not a directory", s.shellPath(globalPath))
	}
	s.cwd = globalPath
	return nil
}

func (s *serverShell) ls(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err == nil && entry.Kind == corev1.EntryKind_ENTRY_KIND_FILE {
		fmt.Fprintln(s.stdout, s.entryName(entry))
		return nil
	}
	if err != nil && (grpcstatus.Code(err) != codes.NotFound || globalPath != s.root) {
		return s.lookupError(err, globalPath)
	}
	list, err := s.repo.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: s.commitID, Path: globalPath, PageSize: 1000})
	var entries []*corev1.TreeEntry
	if err != nil {
		if grpcstatus.Code(err) != codes.NotFound {
			return err
		}
		if globalPath != s.root && s.projectionDirectoryEntry(globalPath) == nil {
			return err
		}
	} else {
		entries = append([]*corev1.TreeEntry(nil), list.Entries...)
	}
	entries = s.projectDirectoryEntries(globalPath, entries)
	entries = s.withSyntheticDirectoryEntries(globalPath, entries)
	sort.Slice(entries, func(i, j int) bool {
		return s.entryName(entries[i]) < s.entryName(entries[j])
	})
	for _, entry := range entries {
		name := s.entryName(entry)
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			name = s.colorize(ansiBlue, name+"/")
		}
		fmt.Fprintln(s.stdout, name)
	}
	return nil
}

func (s *serverShell) cat(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	if entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		return fmt.Errorf("%s is not a file", s.shellPath(globalPath))
	}
	read, err := s.repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: s.commitID, Path: globalPath})
	if err != nil {
		return err
	}
	if _, err := s.stdout.Write(read.Data); err != nil {
		return err
	}
	if len(read.Data) == 0 || read.Data[len(read.Data)-1] != '\n' {
		fmt.Fprintln(s.stdout)
	}
	return nil
}

func (s *serverShell) write(ctx context.Context, target string, data []byte) error {
	p, err := s.resolve(target)
	if err != nil {
		return err
	}
	if s.mutator == nil {
		return fmt.Errorf("mutations are unavailable in this shell scope")
	}
	cleaned, err := normalizeMutationPath(s.mutator.slice, p)
	if err != nil {
		return err
	}
	edit, err := s.runner.uploadFileEdit(authContext(ctx, s.mutator.cfg), s.mutator.conn, cleaned, data)
	if err != nil {
		return err
	}
	return s.mutate(ctx, "write", []*corev1.FileEdit{edit})
}

func (s *serverShell) mutate(ctx context.Context, operation string, edits []*corev1.FileEdit) error {
	if s.mutator == nil {
		return fmt.Errorf("mutations are unavailable in this shell scope")
	}
	var captured strings.Builder
	originalStdout := s.mutator.runner.Stdout
	s.mutator.runner.Stdout = &captured
	err := s.mutator.apply(ctx, commandOptions{Quiet: true}, operation, edits)
	s.mutator.runner.Stdout = originalStdout
	if err != nil {
		return err
	}
	ref, err := s.repo.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	s.commitID = ref.CommitId
	fmt.Fprintf(s.stdout, "%s %s @ %s\n", s.colorize(ansiGreen, "ok"), operationPastTense(operation), shortID(s.commitID))
	return nil
}

func (s *serverShell) stat(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	if globalPath == s.root {
		if _, err := s.resolveEntry(ctx, globalPath); err != nil && grpcstatus.Code(err) != codes.NotFound {
			return err
		}
		fmt.Fprintf(s.stdout, "path: %s\n", globalPath)
		fmt.Fprintf(s.stdout, "shell_path: %s\n", s.shellPath(globalPath))
		fmt.Fprintln(s.stdout, "kind: directory")
		return nil
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	fmt.Fprintf(s.stdout, "path: %s\n", entry.Path)
	fmt.Fprintf(s.stdout, "shell_path: %s\n", s.shellPath(entry.Path))
	fmt.Fprintf(s.stdout, "kind: %s\n", entryKindName(entry.Kind))
	if entry.Mode != 0 {
		fmt.Fprintf(s.stdout, "mode: %o\n", entry.Mode)
	}
	if entry.Size != 0 {
		fmt.Fprintf(s.stdout, "size: %d\n", entry.Size)
	}
	if entry.ContentHash != "" {
		fmt.Fprintf(s.stdout, "content_hash: %s\n", entry.ContentHash)
	}
	if entry.TreeId != "" {
		fmt.Fprintf(s.stdout, "tree_id: %s\n", entry.TreeId)
	}
	if entry.BlobId != "" {
		fmt.Fprintf(s.stdout, "blob_id: %s\n", entry.BlobId)
	}
	return nil
}

func (s *serverShell) lookupError(err error, globalPath string) error {
	if grpcstatus.Code(err) == codes.NotFound {
		return fmt.Errorf("path not found: %s", s.shellPath(globalPath))
	}
	return err
}

func (s *serverShell) resolveEntry(ctx context.Context, globalPath string) (*corev1.TreeEntry, error) {
	resolved, err := s.repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: s.commitID, Path: globalPath})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			if entry, ok := s.syntheticDirs[globalPath]; ok {
				return entry, nil
			}
			if entry := s.projectionDirectoryEntry(globalPath); entry != nil {
				return entry, nil
			}
		}
		return nil, err
	}
	if resolved.Entry == nil {
		return nil, fmt.Errorf("path not found: %s", s.shellPath(globalPath))
	}
	return resolved.Entry, nil
}

func (s *serverShell) withSyntheticDirectoryEntries(globalPath string, entries []*corev1.TreeEntry) []*corev1.TreeEntry {
	if len(s.syntheticDirs) == 0 {
		return entries
	}
	byPath := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry != nil {
			byPath[entry.Path] = struct{}{}
		}
	}
	for p, entry := range s.syntheticDirs {
		if entry == nil || path.Dir(p) != globalPath {
			continue
		}
		if _, ok := byPath[p]; ok {
			continue
		}
		entries = append(entries, entry)
		byPath[p] = struct{}{}
	}
	return entries
}

func (s *serverShell) projectDirectoryEntries(globalPath string, entries []*corev1.TreeEntry) []*corev1.TreeEntry {
	if len(s.projection) == 0 {
		return entries
	}
	byPath := make(map[string]struct{}, len(entries)+len(s.projection))
	out := make([]*corev1.TreeEntry, 0, len(entries)+len(s.projection))
	for _, entry := range entries {
		if entry == nil || !s.pathInProjection(entry.Path) {
			continue
		}
		out = append(out, entry)
		byPath[entry.Path] = struct{}{}
	}
	for _, root := range s.projection {
		childPath := projectionChildPath(globalPath, root)
		if childPath == "" {
			continue
		}
		if _, ok := byPath[childPath]; ok {
			continue
		}
		out = append(out, &corev1.TreeEntry{
			Path: childPath,
			Name: path.Base(childPath),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		})
		byPath[childPath] = struct{}{}
	}
	return out
}

func projectionChildPath(parent, includedRoot string) string {
	parent = strings.TrimRight(parent, "/")
	if parent == "" {
		parent = "/"
	}
	if parent == includedRoot {
		return ""
	}
	if !paths.Contains(parent, includedRoot) {
		return ""
	}
	rel := strings.TrimPrefix(includedRoot, strings.TrimRight(parent, "/")+"/")
	if parent == "/" {
		rel = strings.TrimPrefix(includedRoot, "/")
	}
	if rel == "" {
		return ""
	}
	child := strings.Split(rel, "/")[0]
	if parent == "/" {
		return "/" + child
	}
	return strings.TrimRight(parent, "/") + "/" + child
}

func (s *serverShell) projectionDirectoryEntry(globalPath string) *corev1.TreeEntry {
	if len(s.projection) == 0 {
		return nil
	}
	if globalPath == s.root {
		return &corev1.TreeEntry{Path: globalPath, Name: path.Base(globalPath), Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
	}
	for _, root := range s.projection {
		if paths.Contains(globalPath, root) {
			return &corev1.TreeEntry{Path: globalPath, Name: path.Base(globalPath), Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
		}
	}
	return nil
}

func (s *serverShell) pathInProjection(globalPath string) bool {
	if len(s.projection) == 0 {
		return true
	}
	if globalPath == s.root {
		return true
	}
	for _, root := range s.projection {
		if paths.Contains(root, globalPath) || paths.Contains(globalPath, root) {
			return true
		}
	}
	return false
}

func (s *serverShell) resolve(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return s.cwd, nil
	}
	if !s.scoped {
		var candidate string
		if strings.HasPrefix(value, "/") {
			candidate = value
		} else {
			candidate = strings.TrimRight(s.cwd, "/") + "/" + value
		}
		return cleanShellGlobalPath(candidate)
	}
	var candidate string
	scopeRoot := "/" + s.scope
	switch {
	case strings.HasPrefix(value, "/") && (value == s.root || strings.HasPrefix(value, strings.TrimRight(s.root, "/")+"/")):
		candidate = value
	case value == scopeRoot || strings.HasPrefix(value, scopeRoot+"/"):
		candidate = value
	case strings.HasPrefix(value, "/"):
		segments := strings.Split(strings.Trim(value, "/"), "/")
		scopeSegments := strings.Split(s.scope, "/")
		if len(segments) >= 2 && len(scopeSegments) > 0 && segments[0] == scopeSegments[0] {
			candidate = value
		} else {
			candidate = strings.TrimRight(s.root, "/") + "/" + strings.TrimPrefix(value, "/")
		}
	default:
		candidate = strings.TrimRight(s.cwd, "/") + "/" + value
	}
	cleaned, err := cleanShellGlobalPath(candidate)
	if err != nil {
		return "", err
	}
	if !paths.Contains(s.root, cleaned) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+cleaned, "Use paths under "+s.shellPath(s.root)+".")
	}
	if !s.pathInProjection(cleaned) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+cleaned, "Use paths included by "+s.scope+".")
	}
	return cleaned, nil
}

func (s *serverShell) prompt() string {
	if !s.scoped {
		return fmt.Sprintf("%s %s> ", s.colorize(ansiDim, "gs"), s.colorize(ansiCyan, s.shellPath(s.cwd)))
	}
	return fmt.Sprintf("%s %s:%s> ", s.colorize(ansiDim, "gs"), s.colorize(ansiGreen, s.scope), s.colorize(ansiCyan, s.shellPath(s.cwd)))
}

func (s *serverShell) shellPath(globalPath string) string {
	if !s.scoped {
		cleaned, err := cleanShellGlobalPath(globalPath)
		if err != nil {
			return globalPath
		}
		return cleaned
	}
	if globalPath == s.root {
		return "/"
	}
	rel := strings.TrimPrefix(globalPath, strings.TrimRight(s.root, "/")+"/")
	return "/" + rel
}

func cleanShellGlobalPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "/", nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/", nil
	}
	return cleaned, nil
}

func (s *serverShell) entryName(entry *corev1.TreeEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Name != "" {
		return entry.Name
	}
	return path.Base(entry.Path)
}

func (s *serverShell) colorize(code, value string) string {
	return colorize(s.color, code, value)
}

func colorize(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}

func entryKindName(kind corev1.EntryKind) string {
	switch kind {
	case corev1.EntryKind_ENTRY_KIND_FILE:
		return "file"
	case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
		return "directory"
	case corev1.EntryKind_ENTRY_KIND_SYMLINK:
		return "symlink"
	default:
		return "unspecified"
	}
}

func (r Runner) hydrateWorkspacePaths(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, commitID string, requested []string, cache *clientcache.ObjectCache) (hydrateResult, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return hydrateResult{}, err
	}
	if base.CommitID == "" {
		base.CommitID = commitID
	}
	if base.Files == nil {
		base.Files = map[string]BaseSnapshotFile{}
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	hydrator := workspaceHydrator{
		runner:   r,
		repo:     repo,
		cache:    cache,
		ws:       ws,
		commitID: commitID,
		base:     base,
	}
	for _, requestedPath := range requested {
		globalPath, err := workspaceInputToGlobalPath(ws, requestedPath)
		if err != nil {
			return hydrateResult{}, err
		}
		if err := hydrator.hydratePath(ctx, globalPath); err != nil {
			return hydrateResult{}, err
		}
	}
	if err := r.writeBaseSnapshot(hydrator.base); err != nil {
		return hydrateResult{}, err
	}
	return hydrator.result, nil
}

type workspaceHydrator struct {
	runner   Runner
	repo     corev1.RepositoryServiceClient
	cache    *clientcache.ObjectCache
	ws       WorkspaceConfig
	commitID string
	base     BaseSnapshot
	result   hydrateResult
}

func (h *workspaceHydrator) hydratePath(ctx context.Context, globalPath string) error {
	resolved, err := h.repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: h.commitID, Path: globalPath})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	entry := resolved.Entry
	if entry == nil {
		return nil
	}
	switch entry.Kind {
	case corev1.EntryKind_ENTRY_KIND_FILE:
		return h.hydrateFile(ctx, entry)
	case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
		return h.hydrateDirectory(ctx, entry.Path)
	default:
		return fmt.Errorf("unsupported entry kind for %s", globalPath)
	}
}

func (h *workspaceHydrator) hydrateDirectory(ctx context.Context, globalPath string) error {
	list, err := h.repo.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: h.commitID, Path: globalPath, PageSize: 1000})
	if err != nil {
		return err
	}
	for _, entry := range list.Entries {
		if entry == nil {
			continue
		}
		switch entry.Kind {
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := h.hydrateFile(ctx, entry); err != nil {
				return err
			}
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			if err := h.hydrateDirectory(ctx, entry.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *workspaceHydrator) hydrateFile(ctx context.Context, entry *corev1.TreeEntry) error {
	data, err := h.cachedFileBytes(ctx, entry)
	if err != nil {
		return err
	}
	rel, err := workspaceRelPath(h.ws, entry.Path)
	if err != nil {
		return err
	}
	target := filepath.Join(h.runner.cwd(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := uint32(entry.Mode)
	if mode == 0 {
		mode = 0o100644
	}
	fileMode := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		fileMode = 0o755
	}
	if err := os.WriteFile(target, data, fileMode); err != nil {
		return err
	}
	h.base.Files[entry.Path] = BaseSnapshotFile{
		Path:        entry.Path,
		RelPath:     rel,
		ContentHash: entry.ContentHash,
		Mode:        mode,
		Size:        int64(len(data)),
	}
	h.result.FileCount++
	return nil
}

func (h *workspaceHydrator) cachedFileBytes(ctx context.Context, entry *corev1.TreeEntry) ([]byte, error) {
	if entry.ContentHash == "" {
		return nil, fmt.Errorf("file %s has no content hash", entry.Path)
	}
	if h.cache.Exists(entry.ContentHash) {
		h.result.CacheHits++
		return h.cache.Read(entry.ContentHash)
	}
	read, err := h.repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: h.commitID, Path: entry.Path})
	if err != nil {
		return nil, err
	}
	cached, err := h.cache.PutBytes(read.Data)
	if err != nil {
		return nil, err
	}
	if cached.ContentHash != entry.ContentHash {
		return nil, fmt.Errorf("hydrated content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
	}
	h.result.CacheMisses++
	return read.Data, nil
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
	cache, err := r.objectCache()
	if err != nil {
		return nil, nil, err
	}
	var edits []*corev1.FileEdit
	for p, file := range current {
		baseFile, ok := base.Files[p]
		if ok && baseFile.ContentHash == file.ContentHash && baseFile.Mode == file.Mode {
			continue
		}
		edit := &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: file.ContentHash, Mode: file.Mode}
		edits = append(edits, edit)
	}
	for p := range base.Files {
		if _, ok := current[p]; !ok {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
		}
	}
	if upload {
		if err := attachBlobIDs(callCtx, blobClient, cache, edits); err != nil {
			return nil, nil, err
		}
	}
	sortFileEdits(edits)
	return edits, current, nil
}

func (r Runner) scanWorkspaceFiles(ws WorkspaceConfig) (map[string]workingFile, error) {
	cache, err := r.objectCache()
	if err != nil {
		return nil, err
	}
	root := r.cwd()
	if len(ws.IncludedPaths) == 0 {
		return nil, fmt.Errorf("workspace has no included paths")
	}
	files := map[string]workingFile{}
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
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
		cached, err := cache.PutFile(p)
		if err != nil {
			return err
		}
		globalPath, err := paths.FromWorkspacePath(ws.IncludedPaths[0], rel)
		if err != nil {
			return err
		}
		mode := uint32(0o100644)
		if info.Mode()&0o111 != 0 {
			mode = 0o100755
		}
		files[globalPath] = workingFile{
			BaseSnapshotFile: BaseSnapshotFile{
				Path:        globalPath,
				RelPath:     rel,
				ContentHash: cached.ContentHash,
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

func attachBlobIDs(ctx context.Context, blobClient corev1.BlobServiceClient, cache *clientcache.ObjectCache, edits []*corev1.FileEdit) error {
	hashSet := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.BlobId != "" || edit.ContentHash == "" {
			continue
		}
		hashSet[edit.ContentHash] = struct{}{}
	}
	if len(hashSet) == 0 {
		return nil
	}

	hashes := make([]string, 0, len(hashSet))
	for hash := range hashSet {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)

	status, err := blobClient.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: hashes})
	if err != nil {
		return err
	}
	blobIDs := map[string]string{}
	for _, record := range status.Blobs {
		if record.Id != "" && record.State == "available" {
			blobIDs[record.ContentHash] = record.Id
		}
	}

	for _, hash := range hashes {
		if blobIDs[hash] != "" {
			continue
		}
		data, err := cache.Read(hash)
		if err != nil {
			return fmt.Errorf("read cached object %s: %w", hash, err)
		}
		uploaded, err := blobClient.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: hash, Data: data})
		if err != nil {
			return err
		}
		blobIDs[hash] = uploaded.BlobId
	}

	for _, edit := range edits {
		if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.BlobId != "" {
			continue
		}
		edit.BlobId = blobIDs[edit.ContentHash]
	}
	return nil
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
	return filepath.Join(r.homeDir(), ".gitslice", "config.json")
}

func (r Runner) stdin() io.Reader {
	if r.Stdin != nil {
		return r.Stdin
	}
	return os.Stdin
}

func (r Runner) stdout() io.Writer {
	if r.Stdout != nil {
		return r.Stdout
	}
	return os.Stdout
}

func (r Runner) stderr() io.Writer {
	if r.Stderr != nil {
		return r.Stderr
	}
	return os.Stderr
}

func (r Runner) colorEnabled(opts commandOptions) bool {
	if opts.NoColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(r.stdout())
}

func (r Runner) stickyShellHeaderEnabled(opts commandOptions) bool {
	if opts.NoColor || opts.Quiet || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(r.stdout()) && isTerminal(r.stdin())
}

func isTerminal(w any) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (r Runner) objectCache() (*clientcache.ObjectCache, error) {
	root := os.Getenv("GITSLICE_CLIENT_CACHE_DIR")
	if root == "" {
		root = r.defaultObjectCacheRoot()
	}
	return clientcache.New(root)
}

func (r Runner) defaultObjectCacheRoot() string {
	if r.Home != "" {
		return filepath.Join(r.Home, ".cache", "gitslice")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "gitslice")
	}
	return filepath.Join(r.homeDir(), ".cache", "gitslice")
}

func (r Runner) homeDir() string {
	if r.Home != "" {
		return r.Home
	}
	home, _ := os.UserHomeDir()
	return home
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
				"use":            "gs auth signup --username <name>",
				"summary":        "sign up through the browser approval flow",
				"flags":          []string{"--username", "--server", "--web-url", "--no-browser"},
				"writes_stdout":  true,
				"machine_output": []string{"server_addr", "subject_id"},
			},
			{
				"use":            "gs auth status",
				"summary":        "show current authentication status after validating the local token",
				"writes_stdout":  true,
				"machine_output": []string{"signed_in", "server_addr", "subject_id", "reason"},
			},
			{
				"use":            "gs workspace init <account>/<slice>",
				"summary":        "bind the current directory to one slice and hydrate its files",
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "slice_id", "base_commit_id", "client_object_cache", "hydrated"},
			},
			{
				"use":            "gs workspace hydrate <path> [path...]",
				"summary":        "hydrate workspace files through the client object cache",
				"args":           []string{"path"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "base_commit_id", "client_object_cache", "hydrated"},
			},
			{
				"use":            "gs status",
				"summary":        "show workspace changes against the local base snapshot",
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "changed_path_count", "changed_paths", "changeset_id", "patchset_id"},
			},
			{
				"use":            "gs slice create <account>/<slice>",
				"summary":        "create a slice",
				"args":           []string{"account/slice"},
				"flags":          []string{"--include", "--visibility"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice list [account]",
				"summary":        "list slices in an account",
				"args":           []string{"account"},
				"writes_stdout":  true,
				"machine_output": []string{"account", "slices"},
			},
			{
				"use":            "gs slice info <account>/<slice>",
				"summary":        "show slice metadata",
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice paths <account>/<slice>",
				"summary":        "show slice included paths",
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"ref", "included_paths"},
			},
			{
				"use":            "gs slice update <account>/<slice>",
				"summary":        "update slice included paths or visibility",
				"args":           []string{"account/slice"},
				"flags":          []string{"--include", "--visibility"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice delete <account>/<slice>",
				"summary":        "delete a slice",
				"args":           []string{"account/slice"},
				"flags":          []string{"--yes"},
				"writes_stdout":  true,
				"machine_output": []string{"slice_id", "ref"},
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
				"use":            "gs fs ls [absolute-path]",
				"summary":        "list a remote directory or file under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"writes_stdout":  true,
				"machine_output": []string{"path", "slice", "commit_id", "entries"},
			},
			{
				"use":            "gs fs cat <absolute-path>",
				"summary":        "print a remote file under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"writes_stdout":  true,
				"machine_output": []string{"path", "slice", "commit_id", "content_hash", "data_base64"},
			},
			{
				"use":            "gs fs mkdir <absolute-path>",
				"summary":        "create a remote directory under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"writes_stdout":  true,
				"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
			},
			{
				"use":            "gs fs write <absolute-path> (--text text|--stdin)",
				"summary":        "create or replace a remote file under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"flags":          []string{"--text", "--stdin"},
				"writes_stdout":  true,
				"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
			},
			{
				"use":            "gs fs touch <absolute-path>",
				"summary":        "create an empty remote file under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"writes_stdout":  true,
				"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
			},
			{
				"use":            "gs fs mv <absolute-old-path> <absolute-new-path>",
				"summary":        "rename or move a remote file or directory under the signed-in home slice",
				"args":           []string{"absolute-old-path", "absolute-new-path"},
				"writes_stdout":  true,
				"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
			},
			{
				"use":            "gs fs rm <absolute-path>",
				"summary":        "delete a remote file or directory under the signed-in home slice",
				"args":           []string{"absolute-path"},
				"writes_stdout":  true,
				"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
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
				"use":           "gs shell",
				"summary":       "browse server-side files",
				"flags":         []string{"--commit", "--slice"},
				"writes_stdout": true,
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
	account, err := normalizeCLISlug(parts[0], "account")
	if err != nil {
		return nil, err
	}
	slug, err := normalizeCLISlug(parts[1], "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func normalizeCLISlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", userError("invalid_slug", name+" is required", "Use lower-case letters, numbers, '-' or '_'.")
	}
	if len(value) > 63 {
		return "", userError("invalid_slug", name+" must be 63 characters or fewer", "Use a shorter "+name+".")
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", userError("invalid_slug", name+" must not start or end with '-'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", userError("invalid_slug", name+" may contain only letters, numbers, '-' or '_'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	return value, nil
}

func workspaceRef(ws WorkspaceConfig) *corev1.WorkspaceRef {
	return &corev1.WorkspaceRef{Id: ws.Account + "/" + ws.Slice}
}

func workspaceInputToGlobalPath(ws WorkspaceConfig, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(ws.IncludedPaths) == 0 {
		return "", fmt.Errorf("workspace has no included paths")
	}
	if value == "" || value == "." {
		return canonicalIncludedRoot(ws.IncludedPaths[0])
	}
	var globalPath string
	var err error
	if strings.HasPrefix(value, "/") {
		if path.Clean(value) == path.Clean(ws.IncludedPaths[0]) {
			globalPath, err = cleanShellGlobalPath(value)
		} else {
			globalPath, err = paths.Canonical(value)
		}
	} else {
		globalPath, err = paths.FromWorkspacePath(ws.IncludedPaths[0], value)
	}
	if err != nil {
		return "", err
	}
	if !paths.InAnyPrefix(ws.IncludedPaths, globalPath) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
	}
	return globalPath, nil
}

func workspaceRelPath(ws WorkspaceConfig, globalPath string) (string, error) {
	for _, prefix := range ws.IncludedPaths {
		if !paths.Contains(prefix, globalPath) {
			continue
		}
		trimmedPrefix := strings.TrimRight(prefix, "/")
		rel := strings.TrimPrefix(globalPath, trimmedPrefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			parts := strings.Split(strings.Trim(globalPath, "/"), "/")
			rel = parts[len(parts)-1]
		}
		return rel, nil
	}
	return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
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

func defaultWebURL() string {
	if value := os.Getenv("GITSLICE_WEB_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:5173"
}

func defaultGatewayURL() string {
	if value := os.Getenv("GITSLICE_GATEWAY_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_HTTP_ADDR"); value != "" {
		if strings.Contains(value, "://") {
			return value
		}
		return "http://" + value
	}
	return "http://127.0.0.1:8082"
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

func maxArgs(max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= max {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected at most %d argument(s), got %d", max, len(args)), "Usage: "+usage)
	}
}

func minArgs(want int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= want {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected at least %d argument(s), got %d", want, len(args)), "Usage: "+usage)
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

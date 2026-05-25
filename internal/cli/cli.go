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
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/gitslice-io/gitslice/internal/clientcache"
	"github.com/gitslice-io/gitslice/internal/diffutil"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/itchyny/gojq"
	"github.com/peterh/liner"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Dir    string
	Home   string
}

var (
	Version     = "dev"
	BuildCommit = ""
	BuildDate   = ""
)

type UserConfig struct {
	ServerAddr string            `json:"server_addr"`
	Token      string            `json:"token"`
	SubjectID  string            `json:"subject_id"`
	Aliases    map[string]string `json:"aliases,omitempty"`
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
	JSONFields     []string
	JQ             string
	Template       string
	Quiet          bool
	NonInteractive bool
	NoColor        bool
	Verbose        bool
	Debug          bool
	Trace          bool
}

func (o commandOptions) jsonOutput() bool {
	return o.Format == "json" || len(o.JSONFields) > 0 || o.JQ != "" || o.Template != ""
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

type versionOutput struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	GoVersion string `json:"go_version"`
	Dirty     bool   `json:"dirty"`
}

type workspaceDiffOutput struct {
	Workspace        string   `json:"workspace"`
	BaseCommitID     string   `json:"base_commit_id"`
	ChangedPathCount int      `json:"changed_path_count"`
	ChangedPaths     []string `json:"changed_paths"`
	Diff             string   `json:"diff,omitempty"`
}

type authStatusOutput struct {
	SignedIn   bool   `json:"signed_in"`
	ServerAddr string `json:"server_addr,omitempty"`
	SubjectID  string `json:"subject_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type authTokenOutput struct {
	Token      string `json:"token"`
	ServerAddr string `json:"server_addr"`
	SubjectID  string `json:"subject_id,omitempty"`
}

type contextOutput struct {
	CWD               string                  `json:"cwd"`
	ConfigPath        string                  `json:"config_path"`
	ServerAddr        string                  `json:"server_addr,omitempty"`
	SignedIn          bool                    `json:"signed_in"`
	SubjectID         string                  `json:"subject_id,omitempty"`
	AuthReason        string                  `json:"auth_reason,omitempty"`
	AuthError         string                  `json:"auth_error,omitempty"`
	Workspace         *contextWorkspaceOutput `json:"workspace,omitempty"`
	ActiveSlice       string                  `json:"active_slice,omitempty"`
	ActiveSliceSource string                  `json:"active_slice_source,omitempty"`
}

type contextWorkspaceOutput struct {
	Root               string   `json:"root"`
	Ref                string   `json:"ref"`
	SliceID            string   `json:"slice_id"`
	DefinitionHash     string   `json:"definition_hash,omitempty"`
	IncludedPaths      []string `json:"included_paths,omitempty"`
	BaseCommitID       string   `json:"base_commit_id,omitempty"`
	CurrentChangesetID string   `json:"current_changeset_id,omitempty"`
	CurrentPatchsetID  string   `json:"current_patchset_id,omitempty"`
}

type configOutput struct {
	ConfigPath   string `json:"config_path"`
	ServerAddr   string `json:"server_addr,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	TokenPresent bool   `json:"token_present"`
}

type aliasEntryOutput struct {
	Name      string `json:"name"`
	Expansion string `json:"expansion"`
}

type rpcMethodOutput struct {
	Service         string `json:"service"`
	Method          string `json:"method"`
	FullMethod      string `json:"full_method"`
	InputType       string `json:"input_type"`
	OutputType      string `json:"output_type"`
	ClientStreaming bool   `json:"client_streaming"`
	ServerStreaming bool   `json:"server_streaming"`
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

type fsUploadOptions struct {
	Recursive   bool
	Concurrency int
}

type localUploadPlan struct {
	Files           []localUploadFile
	EmptyRemoteDirs []string
}

type localUploadFile struct {
	LocalPath   string
	RemotePath  string
	Mode        uint32
	Size        int64
	ContentHash string
	BlobID      string
}

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBlue  = "\x1b[34m"
	ansiCyan  = "\x1b[36m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
)

type helpTopic struct {
	Name    string
	Summary string
	Body    string
}

var cliHelpTopics = []helpTopic{
	{
		Name:    "environment",
		Summary: "Environment variables used by gs",
		Body: `Environment

GS_SERVER_ADDR
  Default gRPC server address for commands that accept --server.

GITSLICE_GRPC_ADDR, GITSLICE_SERVER_ADDR
  Compatibility aliases for GS_SERVER_ADDR when GS_SERVER_ADDR is unset.

GS_WEB_URL
  Default browser UI base URL for gs auth signup and gs browse.

GITSLICE_WEB_URL
  Compatibility alias for GS_WEB_URL when GS_WEB_URL is unset.

GS_GATEWAY_URL
  Default HTTP JSON gateway URL used by the signup approval page.

GITSLICE_GATEWAY_URL
  Compatibility alias for GS_GATEWAY_URL when GS_GATEWAY_URL is unset.

GS_HTTP_ADDR, GITSLICE_HTTP_ADDR
  Compatibility sources for the default gateway URL when no gateway URL is set.

GS_CLIENT_CACHE_DIR
  Override the shared local content-addressed object cache root.

GITSLICE_CLIENT_CACHE_DIR
  Compatibility alias for GS_CLIENT_CACHE_DIR when GS_CLIENT_CACHE_DIR is unset.

NO_COLOR
  Disable ANSI color output.

TERM=dumb
  Disable color and sticky interactive shell headers.
`,
	},
	{
		Name:    "formatting",
		Summary: "Text, JSON, and error output conventions",
		Body: `Formatting

By default, gs prints human-readable text to stdout.

Use --json or --format json for stable machine-readable output:

  gs status --json
  gs context --format json

Use --json=field,field to select top-level fields:

  gs auth status --json=signed_in,server_addr
  gs context --json=server_addr,active_slice

Use --jq to filter structured command output with jq syntax:

  gs auth status --jq .reason
  gs context --jq '{slice: .active_slice, source: .active_slice_source}'

Use --template to format structured command output with Go text/template over
the same JSON-shaped fields:

  gs auth status --template '{{.signed_in}}'
  gs context --template '{{.active_slice}} {{.active_slice_source}}'

Diagnostics, progress, and errors are written to stderr. When a command is run
with --json, --format json, --jq, or --template, error output has this shape:

  {
    "error": {
      "code": "stable_snake_case_code",
      "message": "human-readable message",
      "hint": "optional next action",
      "retriable": false
    }
  }

Use --quiet to suppress non-essential text output and --no-color or NO_COLOR to
disable ANSI color. Scripts should consume documented JSON fields from gs schema.
`,
	},
	{
		Name:    "exit-codes",
		Summary: "Exit codes returned by gs",
		Body: `Exit Codes

0
  Success.

1
  General command failure.

2
  Command canceled.

4
  Authentication is missing, invalid, or rejected by the server.

Commands may print additional structured error details on stderr when --json,
--format json, --jq, or --template is set.
`,
	},
	{
		Name:    "paths",
		Summary: "Account-rooted path rules",
		Body: `Paths

Server paths are canonical account-rooted paths:

  /nic/notes/readme.md
  /acme/payment/app.go

gs fs commands always require absolute server paths and are scoped to the
signed-in user's home slice. For user nic, mutations must stay under /nic.

Workspaces materialize canonical account-rooted paths below the workspace root.
A server file at /nic/hello/readme.md appears locally as:

  nic/hello/readme.md

Inside gs shell, relative paths are resolved from the shell's current server
directory. Absolute paths are still canonical server paths.
`,
	},
	{
		Name:    "slices",
		Summary: "Home slices, custom slices, and slice slugs",
		Body: `Slices

A slice is a named view over one or more account-rooted server path prefixes.
Slice references use:

  <account>/<slice>

The reserved home slice is created by signup:

  nic/home

The home slice covers the account root /nic, not /nic/home. Custom personal
slices use other slugs and must cover existing paths under the same account,
for example:

  nic/dotfiles -> /nic/dotfiles
  nic/tools    -> /nic/tools

Workspaces bind to exactly one slice. Changesets created from a workspace use
that bound slice as the authoring slice.
`,
	},
}

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
		return exitCodeForError(err)
	}
	return 0
}

func exitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 2
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Code {
		case "not_logged_in", "invalid_user_config", "unauthenticated":
			return 4
		}
	}
	if grpcstatus.Code(err) == codes.Unauthenticated {
		return 4
	}
	return 1
}

func (r Runner) Run(ctx context.Context, args []string) error {
	expanded, err := r.expandAliasArgs(args)
	if err != nil {
		return r.enhanceCommandError(err)
	}
	root := r.rootCommand()
	root.SetArgs(expanded)
	root.SetOut(r.Stdout)
	root.SetErr(r.Stderr)
	return r.enhanceCommandError(root.ExecuteContext(ctx))
}

func (r Runner) expandAliasArgs(args []string) ([]string, error) {
	idx := aliasExpansionIndex(args)
	if idx < 0 {
		return args, nil
	}
	name := args[idx]
	if name == "alias" || name == "help" || name == "completion" {
		return args, nil
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return nil, err
	}
	expansion, ok := cfg.Aliases[name]
	if !ok {
		return args, nil
	}
	parts, err := splitAliasExpansion(expansion)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, userError("invalid_alias", "alias "+name+" has an empty expansion", "Run gs alias set "+name+" '<command>' to replace it.")
	}
	expanded := make([]string, 0, len(args)-1+len(parts))
	expanded = append(expanded, args[:idx]...)
	expanded = append(expanded, parts...)
	expanded = append(expanded, args[idx+1:]...)
	return expanded, nil
}

func aliasExpansionIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if !strings.HasPrefix(arg, "-") {
			return i
		}
		switch {
		case arg == "--format" || arg == "--json" || arg == "--jq" || arg == "--template":
			i++
		case strings.HasPrefix(arg, "--format=") || strings.HasPrefix(arg, "--json=") || strings.HasPrefix(arg, "--jq=") || strings.HasPrefix(arg, "--template="):
		case arg == "--quiet" || arg == "--non-interactive" || arg == "--no-color" || arg == "--verbose" || arg == "--debug" || arg == "--trace":
		default:
			return i
		}
	}
	return -1
}

func (r Runner) enhanceCommandError(err error) error {
	if err == nil {
		return nil
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		return err
	}
	if grpcstatus.Code(err) != codes.Unauthenticated {
		return err
	}
	message := grpcstatus.Convert(err).Message()
	if message == "" {
		message = "unauthenticated"
	}
	return commandError{
		Code:    "unauthenticated",
		Message: "authentication failed: " + message,
		Hint:    r.authRecoveryHint(),
		Cause:   err,
	}
}

func (r Runner) authRecoveryHint() string {
	hint := "Run gs auth status to inspect the saved token."
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err == nil {
		if account, ok := personalAccountSlugFromSubjectID(cfg.SubjectID); ok {
			return hint + " If it is invalid, run gs auth signup --username " + account + "."
		}
	}
	return hint + " If it is invalid, run gs auth signup --username <name>."
}

func (r Runner) rootCommand() *cobra.Command {
	opts := &commandOptions{Format: "text"}
	jsonFlagValue := ""

	root := &cobra.Command{
		Use:           "gs",
		Short:         "Gitslice native CLI",
		Example:       "  gs auth signup --username nic\n  gs shell\n  gs fs upload ./notes /nic/notes --recursive\n  gs workspace init nic/home\n  gs status\n  gs cs create --title \"update notes\"\n  gs cs diff\n  gs cs submit",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return userError("missing_command", "missing command", "Run gs --help to list available commands.")
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Root().PersistentFlags().Changed("json") {
				opts.Format = "json"
				fields, err := parseJSONFields(jsonFlagValue)
				if err != nil {
					return err
				}
				opts.JSONFields = fields
			}
			if opts.Format != "text" && opts.Format != "json" {
				return userError("invalid_format", "invalid output format "+opts.Format, "Use --format text, --format json, or --json.")
			}
			if opts.JQ != "" && opts.Template != "" {
				return userError("invalid_format", "cannot use --jq and --template together", "Use --jq to filter JSON or --template to format output.")
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&opts.Format, "format", "text", "output format: text or json")
	root.PersistentFlags().StringVar(&jsonFlagValue, "json", "", "emit JSON output; optionally select comma-separated fields with --json=field,field")
	root.PersistentFlags().Lookup("json").NoOptDefVal = "*"
	root.PersistentFlags().StringVar(&opts.JQ, "jq", "", "filter structured output with a jq expression")
	root.PersistentFlags().StringVar(&opts.Template, "template", "", "format structured output with a Go template")
	root.PersistentFlags().BoolVar(&opts.Quiet, "quiet", false, "suppress non-essential text output")
	root.PersistentFlags().BoolVar(&opts.NonInteractive, "non-interactive", false, "fail instead of prompting for input")
	root.PersistentFlags().BoolVar(&opts.NoColor, "no-color", false, "disable colorized output")
	root.PersistentFlags().BoolVar(&opts.Verbose, "verbose", false, "emit additional diagnostic output")
	root.PersistentFlags().BoolVar(&opts.Debug, "debug", false, "emit debug diagnostics")
	root.PersistentFlags().BoolVar(&opts.Trace, "trace", false, "emit trace diagnostics")
	root.SetHelpCommand(r.helpCommand(root))

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
	authTokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Print the validated bearer token",
		Args:  noArgs("gs auth token"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthToken(cmd.Context(), *opts)
		},
	}
	authLogoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "Clear saved authentication credentials",
		Args:  noArgs("gs auth logout [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthLogout(*opts)
		},
	}
	authCmd.AddCommand(loginCmd, signupCmd, authStatusCmd, authTokenCmd, authLogoutCmd)

	workspaceCmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage the current single-slice workspace",
		RunE:    requireSubcommand("workspace"),
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
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show workspace changes against the local base snapshot",
		Args:    noArgs("gs status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStatus(cmd.Context(), *opts)
		},
	}
	contextCmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "Show resolved CLI context",
		Args:    noArgs("gs context [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runContext(cmd.Context(), *opts)
		},
	}
	configCmd := &cobra.Command{
		Use:     "config",
		Aliases: []string{"cfg"},
		Short:   "Manage local CLI configuration",
		RunE:    requireSubcommand("config"),
	}
	configListCmd := &cobra.Command{
		Use:   "list",
		Short: "List local CLI configuration",
		Args:  noArgs("gs config list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigList(*opts)
		},
	}
	configGetCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get one local CLI configuration value",
		Args:  exactArgs(1, "gs config get <key>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigGet(*opts, args[0])
		},
	}
	configSetCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one local CLI configuration value",
		Args:  exactArgs(2, "gs config set <key> <value>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigSet(*opts, args[0], args[1])
		},
	}
	configCmd.AddCommand(configListCmd, configGetCmd, configSetCmd)

	aliasCmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage local command aliases",
		RunE:  requireSubcommand("alias"),
	}
	aliasListCmd := &cobra.Command{
		Use:   "list",
		Short: "List local command aliases",
		Args:  noArgs("gs alias list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasList(*opts)
		},
	}
	aliasSetCmd := &cobra.Command{
		Use:   "set <name> <command>",
		Short: "Create or update a local command alias",
		Args:  exactArgs(2, "gs alias set <name> '<command>'"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasSet(*opts, args[0], args[1])
		},
	}
	aliasDeleteCmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a local command alias",
		Args:    exactArgs(1, "gs alias delete <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasDelete(*opts, args[0])
		},
	}
	aliasCmd.AddCommand(aliasListCmd, aliasSetCmd, aliasDeleteCmd)

	rpcCmd := &cobra.Command{
		Use:   "rpc",
		Short: "Call generated core RPCs for diagnostics",
		RunE:  requireSubcommand("rpc"),
	}
	rpcListCmd := &cobra.Command{
		Use:   "list",
		Short: "List generated core RPC methods",
		Args:  noArgs("gs rpc list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runRPCList(*opts)
		},
	}
	rpcCallServer := ""
	rpcCallRequest := "{}"
	rpcCallUnauthenticated := false
	rpcCallCmd := &cobra.Command{
		Use:   "call <service>/<method>",
		Short: "Call a generated unary core RPC with a JSON request",
		Args:  exactArgs(1, "gs rpc call <service>/<method> --request '{}'"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runRPCCall(cmd.Context(), *opts, args[0], rpcCallRequest, rpcCallServer, rpcCallUnauthenticated)
		},
	}
	rpcCallCmd.Flags().StringVar(&rpcCallRequest, "request", rpcCallRequest, "JSON request body")
	rpcCallCmd.Flags().StringVar(&rpcCallServer, "server", rpcCallServer, "server gRPC address override")
	rpcCallCmd.Flags().BoolVar(&rpcCallUnauthenticated, "unauthenticated", rpcCallUnauthenticated, "call without adding the saved bearer token")
	rpcCmd.AddCommand(rpcListCmd, rpcCallCmd)

	browseWebURL := defaultWebURL()
	browsePrint := false
	browseCmd := &cobra.Command{
		Use:   "browse [web-path]",
		Short: "Open the Gitslice web UI",
		Args:  maxArgs(1, "gs browse [web-path] [--web-url url] [--print]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			route := ""
			if len(args) > 0 {
				route = args[0]
			}
			return r.runBrowse(*opts, browseWebURL, route, browsePrint)
		},
	}
	browseCmd.Flags().StringVar(&browseWebURL, "web-url", browseWebURL, "web UI base URL")
	browseCmd.Flags().BoolVar(&browsePrint, "print", browsePrint, "print the URL instead of opening a browser")

	diffNameOnly := false
	diffStat := false
	diffFrom := ""
	diffTo := ""
	diffCmd := &cobra.Command{
		Use:   "diff",
		Short: "Show workspace or current changeset diffs",
		Args:  noArgs("gs diff [--from patchset --to patchset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runDiff(cmd.Context(), *opts, diffFrom, diffTo, diffNameOnly, diffStat)
		},
	}
	diffCmd.Flags().StringVar(&diffFrom, "from", diffFrom, "patchset id or number to diff from")
	diffCmd.Flags().StringVar(&diffTo, "to", diffTo, "patchset id or number to diff to")
	diffCmd.Flags().BoolVar(&diffNameOnly, "name-only", diffNameOnly, "show only changed path names")
	diffCmd.Flags().BoolVar(&diffStat, "stat", diffStat, "show a compact changed-path summary")

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
	csSubmitNoWatch := false
	csSubmitWatchTimeout := "10s"
	csSubmitCmd := &cobra.Command{
		Use:   "submit [changeset-id]",
		Short: "Submit the current changeset",
		Args:  maxArgs(1, "gs cs submit [changeset-id] [--no-watch] [--watch-timeout 10s]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			watchTimeout, err := parsePositiveDurationFlag("watch-timeout", csSubmitWatchTimeout)
			if err != nil {
				return err
			}
			return r.runChangesetSubmit(cmd.Context(), *opts, id, !csSubmitNoWatch, watchTimeout)
		},
	}
	csSubmitCmd.Flags().BoolVar(&csSubmitNoWatch, "no-watch", csSubmitNoWatch, "return after submit is accepted without waiting for publish")
	csSubmitCmd.Flags().StringVar(&csSubmitWatchTimeout, "watch-timeout", csSubmitWatchTimeout, "maximum time to wait for publish")
	csStatusWatch := false
	csStatusWatchTimeout := "10s"
	csStatusCmd := &cobra.Command{
		Use:   "status [changeset-id]",
		Short: "Show the current changeset status",
		Args:  maxArgs(1, "gs cs status [changeset-id] [--watch] [--watch-timeout 10s] [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			watchTimeout, err := parsePositiveDurationFlag("watch-timeout", csStatusWatchTimeout)
			if err != nil {
				return err
			}
			return r.runChangesetStatus(cmd.Context(), *opts, id, csStatusWatch, watchTimeout)
		},
	}
	csStatusCmd.Flags().BoolVar(&csStatusWatch, "watch", csStatusWatch, "wait until the changeset reaches a terminal submitted state")
	csStatusCmd.Flags().StringVar(&csStatusWatchTimeout, "watch-timeout", csStatusWatchTimeout, "maximum time to wait for status changes")
	csShowCmd := &cobra.Command{
		Use:   "show [changeset-id]",
		Short: "Show changeset details",
		Args:  maxArgs(1, "gs cs show [changeset-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetShow(cmd.Context(), *opts, id)
		},
	}
	csExplainCmd := &cobra.Command{
		Use:   "explain [changeset-id]",
		Short: "Explain changeset validation inputs",
		Args:  maxArgs(1, "gs cs explain [changeset-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetExplain(cmd.Context(), *opts, id)
		},
	}
	csVersionsCmd := &cobra.Command{
		Use:     "versions [changeset-id]",
		Aliases: []string{"patchsets"},
		Short:   "List patchsets for a changeset",
		Args:    maxArgs(1, "gs cs versions [changeset-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetVersions(cmd.Context(), *opts, id)
		},
	}
	csDiffPatchset := ""
	csDiffFrom := ""
	csDiffTo := ""
	csDiffNameOnly := false
	csDiffStat := false
	csDiffCmd := &cobra.Command{
		Use:   "diff [changeset-id]",
		Short: "Show a server-side changeset diff",
		Args:  maxArgs(1, "gs cs diff [changeset-id] [--patchset n|id] [--from n|id --to n|id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetDiff(cmd.Context(), *opts, id, csDiffPatchset, csDiffFrom, csDiffTo, csDiffNameOnly, csDiffStat)
		},
	}
	csDiffCmd.Flags().StringVar(&csDiffPatchset, "patchset", csDiffPatchset, "patchset id or number to diff against its base")
	csDiffCmd.Flags().StringVar(&csDiffFrom, "from", csDiffFrom, "patchset id or number to diff from")
	csDiffCmd.Flags().StringVar(&csDiffTo, "to", csDiffTo, "patchset id or number to diff to")
	csDiffCmd.Flags().BoolVar(&csDiffNameOnly, "name-only", csDiffNameOnly, "show only changed path names")
	csDiffCmd.Flags().BoolVar(&csDiffStat, "stat", csDiffStat, "show a compact changed-path summary")
	csAbandonReason := ""
	csAbandonCmd := &cobra.Command{
		Use:   "abandon [changeset-id]",
		Short: "Abandon a changeset",
		Args:  maxArgs(1, "gs cs abandon [changeset-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetAbandon(cmd.Context(), *opts, id, csAbandonReason)
		},
	}
	csAbandonCmd.Flags().StringVar(&csAbandonReason, "reason", csAbandonReason, "abandon reason")
	csListSlice := ""
	csListStatus := ""
	csListLimit := 20
	csListCmd := &cobra.Command{
		Use:   "list",
		Short: "List changesets for a slice",
		Args:  noArgs("gs cs list [--slice account/slice] [--status status]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetList(cmd.Context(), *opts, csListSlice, csListStatus, csListLimit)
		},
	}
	csListCmd.Flags().StringVar(&csListSlice, "slice", csListSlice, "authoring slice, defaults to current workspace slice")
	csListCmd.Flags().StringVar(&csListStatus, "status", csListStatus, "status filter")
	csListCmd.Flags().IntVar(&csListLimit, "limit", csListLimit, "maximum changesets to list")
	csCmd.AddCommand(csCreateCmd, csUpdateCmd, csSubmitCmd, csStatusCmd, csShowCmd, csExplainCmd, csVersionsCmd, csDiffCmd, csAbandonCmd, csListCmd)

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
	fsUploadRecursive := false
	fsUploadConcurrency := defaultUploadConcurrency()
	fsUploadCmd := &cobra.Command{
		Use:   "upload <local-path> <absolute-remote-path>",
		Short: "Upload a local file or directory into the home slice",
		Args:  exactArgs(2, "gs fs upload ./local /account/path [--recursive]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFSUpload(cmd.Context(), *opts, args[0], args[1], fsUploadOptions{
				Recursive:   fsUploadRecursive,
				Concurrency: fsUploadConcurrency,
			})
		},
	}
	fsUploadCmd.Flags().BoolVarP(&fsUploadRecursive, "recursive", "r", fsUploadRecursive, "upload a directory recursively")
	fsUploadCmd.Flags().IntVar(&fsUploadConcurrency, "concurrency", fsUploadConcurrency, "maximum concurrent file uploads")
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
	fsCmd.AddCommand(fsLsCmd, fsCatCmd, fsMkdirCmd, fsTouchCmd, fsWriteCmd, fsUploadCmd, fsMvCmd, fsRmCmd)

	repoCmd := &cobra.Command{
		Use:     "repo",
		Aliases: []string{"repository"},
		Short:   "Manage imported repositories",
		RunE:    requireSubcommand("repo"),
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
		Use:     "commit",
		Aliases: []string{"commits"},
		Short:   "Inspect native commits",
		RunE:    requireSubcommand("commit"),
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

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print CLI version information",
		Args:  noArgs("gs version [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runVersion(*opts)
		},
	}

	schemaCmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the machine-readable CLI schema",
		Args:  noArgs("gs schema"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSchema(*opts)
		},
	}

	sliceCmd := &cobra.Command{
		Use:     "slice",
		Aliases: []string{"slices"},
		Short:   "Manage slices",
		RunE:    requireSubcommand("slice"),
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

	root.AddCommand(authCmd, workspaceCmd, statusCmd, contextCmd, configCmd, aliasCmd, rpcCmd, browseCmd, diffCmd, csCmd, fsCmd, repoCmd, commitCmd, shellCmd, versionCmd, schemaCmd, sliceCmd)
	return root
}

func (r Runner) helpCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "help [command|topic]",
		Short: "Help about any command or topic",
		Long:  "Help provides command help and Gitslice topic guides.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if err := root.Help(); err != nil {
					return err
				}
				r.writeHelpTopicList()
				return nil
			}
			if len(args) == 1 {
				if topic, ok := findHelpTopic(args[0]); ok {
					return writeHelpTopic(r.Stdout, topic)
				}
			}
			target, _, err := root.Find(args)
			if err == nil && target != nil && target != root && target != cmd {
				return target.Help()
			}
			return userError("unknown_help_topic", "unknown help topic or command: "+strings.Join(args, " "), "Run gs help to list available help topics.")
		},
	}
	for _, topic := range cliHelpTopics {
		topic := topic
		cmd.AddCommand(&cobra.Command{
			Use:   topic.Name,
			Short: topic.Summary,
			Args:  noArgs("gs help " + topic.Name),
			RunE: func(cmd *cobra.Command, args []string) error {
				return writeHelpTopic(r.Stdout, topic)
			},
		})
	}
	return cmd
}

func (r Runner) writeHelpTopicList() {
	fmt.Fprintln(r.Stdout)
	fmt.Fprintln(r.Stdout, "HELP TOPICS")
	for _, topic := range cliHelpTopics {
		fmt.Fprintf(r.Stdout, "  %-13s %s\n", topic.Name+":", topic.Summary)
	}
}

func findHelpTopic(name string) (helpTopic, bool) {
	for _, topic := range cliHelpTopics {
		if topic.Name == name {
			return topic, true
		}
	}
	return helpTopic{}, false
}

func writeHelpTopic(w io.Writer, topic helpTopic) error {
	_, err := fmt.Fprint(w, strings.TrimRight(topic.Body, "\n")+"\n")
	return err
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
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
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
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
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

func (r Runner) runBrowse(opts commandOptions, webURL, route string, printOnly bool) error {
	target, err := webRouteURL(webURL, route)
	if err != nil {
		return err
	}
	if printOnly {
		fmt.Fprintln(r.Stdout, target)
		return nil
	}
	if err := openBrowserURL(target); err != nil {
		hint := "Run gs browse --print"
		if strings.TrimSpace(route) != "" {
			hint += " " + route
		}
		hint += " to print the URL."
		return userError("browser_open_failed", "could not open browser: "+err.Error(), hint)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.stderr(), "opened %s\n", target)
	return nil
}

func webRouteURL(webURL, route string) (string, error) {
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
		return "", userError("invalid_web_url", "web-url must use http or https", "Pass --web-url http://127.0.0.1:8082.")
	}
	route = strings.TrimSpace(route)
	if route == "" {
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		return parsed.String(), nil
	}
	routePath, routeQuery, hasQuery := strings.Cut(route, "?")
	routePath = strings.TrimSpace(routePath)
	basePath := strings.TrimRight(parsed.Path, "/")
	if routePath == "" || routePath == "/" {
		if basePath == "" {
			parsed.Path = "/"
		} else {
			parsed.Path = basePath + "/"
		}
	} else {
		if !strings.HasPrefix(routePath, "/") {
			routePath = "/" + routePath
		}
		cleaned := path.Clean(routePath)
		if strings.HasSuffix(routePath, "/") && cleaned != "/" {
			cleaned += "/"
		}
		parsed.Path = path.Join(basePath, cleaned)
	}
	if hasQuery {
		parsed.RawQuery = routeQuery
	}
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
	status, err := r.probeAuthStatus(ctx)
	if err != nil {
		return err
	}
	return r.writeAuthStatus(opts, status)
}

func (r Runner) runAuthToken(ctx context.Context, opts commandOptions) error {
	cfg, subjectID, err := r.validatedAuthToken(ctx)
	if err != nil {
		return err
	}
	out := authTokenOutput{
		Token:      cfg.Token,
		ServerAddr: cfg.ServerAddr,
		SubjectID:  subjectID,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	fmt.Fprintln(r.Stdout, cfg.Token)
	return nil
}

func (r Runner) runAuthLogout(opts commandOptions) error {
	path := r.userConfigPath()
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	existed := true
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		existed = false
	}
	wasSignedIn := cfg.Token != "" || cfg.SubjectID != ""
	if existed {
		cfg.Token = ""
		cfg.SubjectID = ""
		if err := r.writeUserConfig(cfg); err != nil {
			return err
		}
	}
	out := map[string]any{
		"signed_in":     false,
		"was_signed_in": wasSignedIn,
		"config_path":   path,
	}
	if cfg.ServerAddr != "" {
		out["server_addr"] = cfg.ServerAddr
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if wasSignedIn {
		fmt.Fprintln(r.Stdout, "logged out")
	} else {
		fmt.Fprintln(r.Stdout, "already logged out")
	}
	if cfg.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server: %s\n", cfg.ServerAddr)
	}
	return nil
}

func (r Runner) validatedAuthToken(ctx context.Context) (UserConfig, string, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, "", err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, "", err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return UserConfig{}, "", userError("invalid_token", "saved auth token is invalid", r.authRecoveryHint())
		}
		return UserConfig{}, "", err
	}
	return cfg, res.SubjectId, nil
}

func (r Runner) probeAuthStatus(ctx context.Context) (authStatusOutput, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return authStatusOutput{}, err
		}
		return authStatusOutput{Reason: "not_logged_in"}, nil
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "invalid_config"}, nil
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "server_unavailable"}, err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return authStatusOutput{
				ServerAddr: cfg.ServerAddr,
				Reason:     "invalid_token",
			}, nil
		}
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "auth_check_failed"}, err
	}
	return authStatusOutput{
		SignedIn:   true,
		ServerAddr: cfg.ServerAddr,
		SubjectID:  res.SubjectId,
	}, nil
}

func (r Runner) writeAuthStatus(opts commandOptions, status authStatusOutput) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, status)
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

func (r Runner) runContext(ctx context.Context, opts commandOptions) error {
	authStatus, authErr := r.probeAuthStatus(ctx)
	out := contextOutput{
		CWD:        r.cwd(),
		ConfigPath: r.userConfigPath(),
		ServerAddr: authStatus.ServerAddr,
		SignedIn:   authStatus.SignedIn,
		SubjectID:  authStatus.SubjectID,
		AuthReason: authStatus.Reason,
	}
	if authErr != nil {
		out.AuthError = authErr.Error()
	}
	if root, err := r.workspaceRoot(); err == nil {
		ws, wsErr := r.readWorkspaceConfig()
		if wsErr != nil {
			return wsErr
		}
		state, stateErr := r.readWorkspaceState()
		if stateErr != nil {
			return stateErr
		}
		ref := ws.Account + "/" + ws.Slice
		out.Workspace = &contextWorkspaceOutput{
			Root:               root,
			Ref:                ref,
			SliceID:            ws.SliceID,
			DefinitionHash:     ws.DefinitionHash,
			IncludedPaths:      ws.IncludedPaths,
			BaseCommitID:       state.BaseCommitID,
			CurrentChangesetID: state.CurrentChangesetID,
			CurrentPatchsetID:  state.CurrentPatchsetID,
		}
		if out.Workspace.BaseCommitID == "" {
			out.Workspace.BaseCommitID = ws.BaseCommitID
		}
		out.ActiveSlice = ref
		out.ActiveSliceSource = "workspace"
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	} else if authStatus.SignedIn {
		if account, ok := personalAccountSlugFromSubjectID(authStatus.SubjectID); ok {
			out.ActiveSlice = account + "/home"
			out.ActiveSliceSource = "signed_in_home"
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	return r.writeContextText(out)
}

func (r Runner) writeContextText(out contextOutput) error {
	fmt.Fprintf(r.Stdout, "cwd: %s\n", out.CWD)
	fmt.Fprintf(r.Stdout, "config: %s\n", out.ConfigPath)
	if out.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server: %s\n", out.ServerAddr)
	}
	if out.SignedIn {
		if out.SubjectID != "" {
			fmt.Fprintf(r.Stdout, "auth: signed in as %s\n", out.SubjectID)
		} else {
			fmt.Fprintln(r.Stdout, "auth: signed in")
		}
	} else {
		fmt.Fprintln(r.Stdout, "auth: not signed in")
		if out.AuthReason != "" {
			fmt.Fprintf(r.Stdout, "auth_reason: %s\n", strings.ReplaceAll(out.AuthReason, "_", " "))
		}
		if out.AuthError != "" {
			fmt.Fprintf(r.Stdout, "auth_error: %s\n", out.AuthError)
		}
	}
	if out.Workspace != nil {
		fmt.Fprintf(r.Stdout, "workspace: %s\n", out.Workspace.Root)
		fmt.Fprintf(r.Stdout, "workspace_slice: %s\n", out.Workspace.Ref)
		if out.Workspace.BaseCommitID != "" {
			fmt.Fprintf(r.Stdout, "base_commit: %s\n", out.Workspace.BaseCommitID)
		}
		if out.Workspace.CurrentChangesetID != "" {
			fmt.Fprintf(r.Stdout, "current_changeset: %s\n", out.Workspace.CurrentChangesetID)
		}
	} else {
		fmt.Fprintln(r.Stdout, "workspace: none")
	}
	if out.ActiveSlice != "" {
		fmt.Fprintf(r.Stdout, "active_slice: %s (%s)\n", out.ActiveSlice, out.ActiveSliceSource)
	}
	return nil
}

func (r Runner) runConfigList(opts commandOptions) error {
	out, err := r.configOutput()
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "config_path: %s\n", out.ConfigPath)
	if out.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server_addr: %s\n", out.ServerAddr)
	}
	if out.SubjectID != "" {
		fmt.Fprintf(r.Stdout, "subject_id: %s\n", out.SubjectID)
	}
	fmt.Fprintf(r.Stdout, "token_present: %t\n", out.TokenPresent)
	return nil
}

func (r Runner) runConfigGet(opts commandOptions, key string) error {
	out, err := r.configOutput()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	var value any
	switch key {
	case "config_path":
		value = out.ConfigPath
	case "server_addr":
		value = out.ServerAddr
	case "subject_id":
		value = out.SubjectID
	case "token_present":
		value = out.TokenPresent
	case "token":
		return userError("secret_config_key", "token is secret and cannot be printed by config", "Run gs auth token only when a script needs the bearer token, or gs auth status for non-secret auth state.")
	default:
		return userError("unknown_config_key", "unknown config key "+key, "Supported keys: config_path, server_addr, subject_id, token_present.")
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{key: value})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "%v\n", value)
	return nil
}

func (r Runner) runConfigSet(opts commandOptions, key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if value == "" {
		return userError("invalid_config_value", "config value cannot be empty", "Pass a non-empty value.")
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	switch key {
	case "server_addr":
		cfg.ServerAddr = value
	case "subject_id", "token", "token_present", "config_path":
		return userError("readonly_config_key", "config key "+key+" is read-only", "Use gs auth login or gs auth signup to update authentication state.")
	default:
		return userError("unknown_config_key", "unknown config key "+key, "Only server_addr can be set directly.")
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	out := configOutputFromUserConfig(r.userConfigPath(), cfg)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "set %s\n", key)
	return nil
}

func (r Runner) configOutput() (configOutput, error) {
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return configOutput{}, err
	}
	return configOutputFromUserConfig(r.userConfigPath(), cfg), nil
}

func configOutputFromUserConfig(configPath string, cfg UserConfig) configOutput {
	return configOutput{
		ConfigPath:   configPath,
		ServerAddr:   cfg.ServerAddr,
		SubjectID:    cfg.SubjectID,
		TokenPresent: cfg.Token != "",
	}
}

func (r Runner) runAliasList(opts commandOptions) error {
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	entries := aliasEntries(cfg.Aliases)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"aliases": entries})
	}
	if opts.Quiet {
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintln(r.Stdout, "no aliases configured")
		return nil
	}
	for _, entry := range entries {
		fmt.Fprintf(r.Stdout, "%s: %s\n", entry.Name, entry.Expansion)
	}
	return nil
}

func (r Runner) runAliasSet(opts commandOptions, name, expansion string) error {
	name = strings.TrimSpace(name)
	expansion = strings.TrimSpace(expansion)
	if err := validateAliasName(name); err != nil {
		return err
	}
	if reservedAliasNames()[name] {
		return userError("reserved_alias", "alias "+name+" conflicts with a built-in command", "Choose a different alias name.")
	}
	if parts, err := splitAliasExpansion(expansion); err != nil {
		return err
	} else if len(parts) == 0 {
		return userError("invalid_alias", "alias expansion cannot be empty", "Pass a command such as 'status --json'.")
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	cfg.Aliases[name] = expansion
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	entry := aliasEntryOutput{Name: name, Expansion: expansion}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, entry)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "set alias %s: %s\n", name, expansion)
	return nil
}

func (r Runner) runAliasDelete(opts commandOptions, name string) error {
	name = strings.TrimSpace(name)
	if err := validateAliasName(name); err != nil {
		return err
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Aliases[name]; !ok {
		return userError("unknown_alias", "unknown alias "+name, "Run gs alias list to inspect configured aliases.")
	}
	delete(cfg.Aliases, name)
	if len(cfg.Aliases) == 0 {
		cfg.Aliases = nil
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"name": name, "deleted": true})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "deleted alias %s\n", name)
	return nil
}

func aliasEntries(aliases map[string]string) []aliasEntryOutput {
	entries := make([]aliasEntryOutput, 0, len(aliases))
	for name, expansion := range aliases {
		entries = append(entries, aliasEntryOutput{Name: name, Expansion: expansion})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func validateAliasName(name string) error {
	if name == "" {
		return userError("invalid_alias", "alias name cannot be empty", "Use letters, numbers, dash, or underscore.")
	}
	if strings.HasPrefix(name, "-") {
		return userError("invalid_alias", "alias name cannot start with '-'", "Use letters, numbers, dash, or underscore.")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return userError("invalid_alias", "alias name contains unsupported characters: "+name, "Use letters, numbers, dash, or underscore.")
	}
	return nil
}

func reservedAliasNames() map[string]bool {
	names := []string{
		"alias", "auth", "browse", "commit", "commits", "completion", "config", "cfg", "context", "ctx",
		"cs", "changeset", "diff", "file", "fs", "help", "repo", "repository", "rpc", "schema",
		"shell", "slice", "slices", "st", "status", "version", "workspace", "ws",
	}
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		reserved[name] = true
	}
	return reserved
}

func (r Runner) runRPCList(opts commandOptions) error {
	methods := generatedRPCMethods()
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"methods": methods})
	}
	if opts.Quiet {
		return nil
	}
	for _, method := range methods {
		streaming := ""
		if method.ClientStreaming || method.ServerStreaming {
			streaming = " streaming"
		}
		fmt.Fprintf(r.Stdout, "%s%s\n", strings.TrimPrefix(method.FullMethod, "/"), streaming)
	}
	return nil
}

func (r Runner) runRPCCall(ctx context.Context, opts commandOptions, selector, rawRequest, serverAddr string, unauthenticated bool) error {
	method, err := findGeneratedRPCMethod(selector)
	if err != nil {
		return err
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return userError("unsupported_rpc", "streaming RPCs are not supported by gs rpc call", "Use a dedicated CLI command for streaming workflows.")
	}
	cfg := UserConfig{}
	if unauthenticated {
		if serverAddr == "" {
			partial, err := r.readPartialUserConfig()
			if err != nil {
				return err
			}
			serverAddr = partial.ServerAddr
		}
		if serverAddr == "" {
			serverAddr = defaultServerAddr()
		}
	} else {
		cfg, err = r.readUserConfig()
		if err != nil {
			return err
		}
		if serverAddr == "" {
			serverAddr = cfg.ServerAddr
		}
	}
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := dynamicpb.NewMessage(method.Input())
	unmarshaler := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := unmarshaler.Unmarshal([]byte(rawRequest), req); err != nil {
		return userError("invalid_rpc_request", "invalid RPC request JSON: "+err.Error(), "Use gs rpc list to find the method, then pass request JSON with --request.")
	}
	res := dynamicpb.NewMessage(method.Output())
	callCtx := ctx
	if !unauthenticated {
		callCtx = authContext(ctx, cfg)
	}
	fullMethod := "/" + string(method.Parent().FullName()) + "/" + string(method.Name())
	if err := conn.Invoke(callCtx, fullMethod, req, res); err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{Indent: "  ", UseProtoNames: true}.Marshal(res)
	if err != nil {
		return err
	}
	if len(opts.JSONFields) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		return r.writeJSONOutput(opts, obj)
	}
	_, err = fmt.Fprintln(r.Stdout, string(data))
	return err
}

func generatedRPCMethods() []rpcMethodOutput {
	methods := []rpcMethodOutput{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "gitslice.core.v1" {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			serviceName := string(service.FullName())
			serviceMethods := service.Methods()
			for j := 0; j < serviceMethods.Len(); j++ {
				method := serviceMethods.Get(j)
				methods = append(methods, rpcMethodOutput{
					Service:         serviceName,
					Method:          string(method.Name()),
					FullMethod:      "/" + serviceName + "/" + string(method.Name()),
					InputType:       string(method.Input().FullName()),
					OutputType:      string(method.Output().FullName()),
					ClientStreaming: method.IsStreamingClient(),
					ServerStreaming: method.IsStreamingServer(),
				})
			}
		}
		return true
	})
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].FullMethod < methods[j].FullMethod
	})
	return methods
}

func findGeneratedRPCMethod(selector string) (protoreflect.MethodDescriptor, error) {
	selector = strings.TrimSpace(strings.TrimPrefix(selector, "/"))
	if selector == "" {
		return nil, userError("invalid_args", "RPC method is required", "Use gs rpc call <service>/<method>.")
	}
	parts := strings.Split(selector, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, userError("invalid_args", "RPC method must be <service>/<method>", "Example: gs rpc call AuthService/GetAuthStatus --request '{}'.")
	}
	serviceName := parts[0]
	if !strings.Contains(serviceName, ".") {
		serviceName = "gitslice.core.v1." + serviceName
	}
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(serviceName))
	if err != nil {
		return nil, userError("unknown_rpc", "unknown RPC service "+parts[0], "Run gs rpc list to show generated core RPC methods.")
	}
	service, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, userError("unknown_rpc", "descriptor is not an RPC service "+parts[0], "Run gs rpc list to show generated core RPC methods.")
	}
	method := service.Methods().ByName(protoreflect.Name(parts[1]))
	if method == nil {
		return nil, userError("unknown_rpc", "unknown RPC method "+selector, "Run gs rpc list to show generated core RPC methods.")
	}
	return method, nil
}

func (r Runner) runSliceCreate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, visibility string) error {
	ref, err := parseSliceRef(sliceRef)
	if err != nil {
		return err
	}
	includedPaths, err = expandSliceIncludedPaths(includedPaths)
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
		return r.writeJSONOutput(opts, sliceToOutput(slice))
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
		return r.writeJSONOutput(opts, map[string]any{
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
		return r.writeJSONOutput(opts, sliceToOutput(slice))
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
		return r.writeJSONOutput(opts, map[string]any{
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
		includedPaths, err = expandSliceIncludedPaths(includedPaths)
		if err != nil {
			return err
		}
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
		return r.writeJSONOutput(opts, sliceToOutput(updated))
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
		return r.writeJSONOutput(opts, map[string]any{
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

func expandSliceIncludedPaths(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, userError("invalid_include", "included path is required", "Pass each included path as --include /account/path, or separate multiple paths with commas.")
			}
			out = append(out, part)
		}
	}
	return out, nil
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
	if err := r.requireEmptyWorkspaceInitDir(); err != nil {
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
		return r.writeJSONOutput(opts, map[string]any{
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

func (r Runner) requireEmptyWorkspaceInitDir() error {
	if root, err := r.workspaceRoot(); err == nil {
		return userError("already_in_workspace", "already inside a gitslice workspace: "+root, "Create a new empty directory outside the existing workspace.")
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	}
	entries, err := os.ReadDir(r.cwd())
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return userError("workspace_not_empty", "workspace init requires an empty directory", "Create a new empty directory and run gs workspace init <account>/<slice> there.")
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
		return r.writeJSONOutput(opts, map[string]any{
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
		return r.writeJSONOutput(opts, output)
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

func (r Runner) runDiff(ctx context.Context, opts commandOptions, from, to string, nameOnly, stat bool) error {
	if strings.TrimSpace(from) != "" || strings.TrimSpace(to) != "" {
		return r.runChangesetDiff(ctx, opts, "", "", from, to, nameOnly, stat)
	}
	return r.runWorkspaceDiff(ctx, opts, nameOnly, stat)
}

func (r Runner) runWorkspaceDiff(ctx context.Context, opts commandOptions, nameOnly, stat bool) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		return err
	}
	baseCommitID := state.BaseCommitID
	if baseCommitID == "" {
		baseCommitID = base.CommitID
	}
	if baseCommitID == "" {
		baseCommitID = ws.BaseCommitID
	}
	if baseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs workspace init <account>/<slice> again.")
	}
	edits, current, err := r.snapshotEdits(ctx, nil, cfg, ws, false)
	if err != nil {
		return err
	}
	changed := changedPathsFromEdits(edits)
	if opts.jsonOutput() {
		diffText, err := r.workspaceDiffText(ctx, cfg, base, baseCommitID, current, changed)
		if err != nil {
			return err
		}
		return r.writeJSONOutput(opts, workspaceDiffOutput{
			Workspace:        ws.Account + "/" + ws.Slice,
			BaseCommitID:     baseCommitID,
			ChangedPathCount: len(changed),
			ChangedPaths:     changed,
			Diff:             diffText,
		})
	}
	if opts.Quiet {
		return nil
	}
	if nameOnly {
		printPathList(r.Stdout, changed)
		return nil
	}
	if stat {
		printPathStat(r.Stdout, changed)
		return nil
	}
	diffText, err := r.workspaceDiffText(ctx, cfg, base, baseCommitID, current, changed)
	if err != nil {
		return err
	}
	_, err = io.WriteString(r.Stdout, diffText)
	return err
}

func (r Runner) workspaceDiffText(ctx context.Context, cfg UserConfig, base BaseSnapshot, baseCommitID string, current map[string]workingFile, changed []string) (string, error) {
	if len(changed) == 0 {
		return "", nil
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	repo := corev1.NewRepositoryServiceClient(conn)
	cache, err := r.objectCache()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range changed {
		oldFile, err := r.workspaceBaseDiffFile(authContext(ctx, cfg), repo, cache, base, baseCommitID, p)
		if err != nil {
			return "", err
		}
		newFile := diffutil.File{Path: p}
		if file, ok := current[p]; ok {
			data, err := os.ReadFile(file.AbsPath)
			if err != nil {
				return "", err
			}
			newFile = diffutil.File{Path: p, Exists: true, Data: data}
		}
		b.WriteString(diffutil.UnifiedFileDiff(oldFile, newFile))
	}
	return b.String(), nil
}

func (r Runner) workspaceBaseDiffFile(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, base BaseSnapshot, baseCommitID, p string) (diffutil.File, error) {
	if file, ok := base.Files[p]; ok && file.ContentHash != "" {
		if cache.Exists(file.ContentHash) {
			data, err := cache.Read(file.ContentHash)
			if err != nil {
				return diffutil.File{}, err
			}
			return diffutil.File{Path: p, Exists: true, Data: data}, nil
		}
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: baseCommitID, Path: p})
	if grpcstatus.Code(err) == codes.NotFound {
		return diffutil.File{Path: p}, nil
	}
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: read.Data}, nil
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
		return r.writeJSONOutput(opts, changesetOutput{
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
		return r.writeJSONOutput(opts, changesetOutput{
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

func (r Runner) runChangesetSubmit(ctx context.Context, opts commandOptions, requestedID string, watch bool, watchTimeout time.Duration) error {
	cfg, ws, state, hasWorkspace, changesetID, usingWorkspaceCurrent, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return err
	}
	expectedPatchsetID := cs.CurrentPatchsetId
	if usingWorkspaceCurrent && state.CurrentPatchsetID != "" {
		expectedPatchsetID = state.CurrentPatchsetID
	}
	res, err := changesetClient.SubmitChangeset(authContext(ctx, cfg), &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: expectedPatchsetID,
	})
	if err != nil {
		return err
	}
	commitID := res.CommitId
	refCommitID := res.NewRefCommitId
	status := res.Status
	if watch && (refCommitID == "" || res.Status == "pending_publish") {
		var err error
		commitID, refCommitID, err = r.waitForChangesetPublished(ctx, conn, cfg, changesetID, watchTimeout, !opts.Quiet && !opts.jsonOutput())
		if err != nil {
			return err
		}
		status = "submitted"
	}
	if status == "" {
		status = "submitted"
	}
	if status == "submitted" && usingWorkspaceCurrent && hasWorkspace {
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
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset_id":      changesetID,
			"commit_id":         commitID,
			"target_ref":        res.TargetRef,
			"new_ref_commit_id": refCommitID,
			"status":            status,
		})
	}
	if opts.Quiet {
		return nil
	}
	if status != "submitted" {
		fmt.Fprintf(r.Stdout, "submit accepted for %s; status: %s\n", changesetID, status)
		fmt.Fprintf(r.Stdout, "Run gs cs status --watch %s to wait for publish.\n", changesetID)
		return nil
	}
	fmt.Fprintf(r.Stdout, "submitted %s to %s\n", commitID, res.TargetRef)
	return nil
}

func (r Runner) waitForChangesetPublished(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changesetID string, timeout time.Duration, progress bool) (string, string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	repoClient := corev1.NewRepositoryServiceClient(conn)
	lastStatus := ""
	for {
		cs, err := changesetClient.GetChangeset(authContext(waitCtx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			return "", "", err
		}
		if progress && cs.Status != lastStatus {
			fmt.Fprintf(r.stderr(), "changeset %s status: %s\n", changesetID, cs.Status)
			lastStatus = cs.Status
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
		return r.writeJSONOutput(opts, out)
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
		return r.writeJSONOutput(opts, fsCatOutput{
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

func (r Runner) runFSUpload(ctx context.Context, opts commandOptions, localPath, remotePath string, uploadOpts fsUploadOptions) error {
	if uploadOpts.Concurrency <= 0 {
		return userError("invalid_args", "upload concurrency must be positive", "Pass --concurrency 1 or higher.")
	}
	cfg, conn, mutator, err := r.homeFileMutator(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	source := localUploadSourcePath(r.cwd(), localPath)
	plan, err := buildLocalUploadPlan(mutator.slice, source, remotePath, uploadOpts.Recursive)
	if err != nil {
		return err
	}
	edits := make([]*corev1.FileEdit, 0, len(plan.Files)+len(plan.EmptyRemoteDirs))
	if len(plan.Files) > 0 {
		fileEdits, err := r.uploadLocalFiles(ctx, cfg, conn, plan.Files, uploadOpts.Concurrency)
		if err != nil {
			return err
		}
		edits = append(edits, fileEdits...)
	}
	for _, dir := range plan.EmptyRemoteDirs {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: dir})
	}
	if len(edits) == 0 {
		return userError("empty_upload", "no files or directories to upload", "Choose a non-empty source or a destination below the home root.")
	}
	return mutator.apply(ctx, opts, "upload", edits)
}

func localUploadSourcePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func buildLocalUploadPlan(slice *corev1.Slice, source, remotePath string, recursive bool) (localUploadPlan, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return localUploadPlan{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return localUploadPlan{}, userError("unsupported_local_file", "local source is a symlink: "+source, "Upload regular files or directories.")
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return localUploadPlan{}, userError("unsupported_local_file", "local source is not a regular file: "+source, "Upload regular files or directories.")
		}
		p, err := normalizeMutationPath(slice, remotePath)
		if err != nil {
			return localUploadPlan{}, err
		}
		return localUploadPlan{Files: []localUploadFile{{
			LocalPath:  source,
			RemotePath: p,
			Mode:       uploadFileMode(info.Mode()),
			Size:       info.Size(),
		}}}, nil
	}
	if !recursive {
		return localUploadPlan{}, userError("recursive_required", "local source is a directory: "+source, "Pass --recursive to upload directories.")
	}
	remoteRoot, err := normalizeHomePath(slice, remotePath, true)
	if err != nil {
		return localUploadPlan{}, err
	}
	root, err := homeSliceRoot(slice)
	if err != nil {
		return localUploadPlan{}, err
	}
	source = filepath.Clean(source)
	plan := localUploadPlan{}
	dirs := map[string]struct{}{}
	nonEmptyDirs := map[string]struct{}{}
	err = filepath.WalkDir(source, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		nonEmptyDirs[parent] = struct{}{}
		if entry.Type()&fs.ModeSymlink != 0 {
			return userError("unsupported_local_file", "local source contains a symlink: "+p, "Upload regular files or directories.")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs[rel] = struct{}{}
			return nil
		}
		if !info.Mode().IsRegular() {
			return userError("unsupported_local_file", "local source contains a non-regular file: "+p, "Upload regular files or directories.")
		}
		remoteFile, err := normalizeMutationPath(slice, joinRemoteUploadPath(remoteRoot, rel))
		if err != nil {
			return err
		}
		plan.Files = append(plan.Files, localUploadFile{
			LocalPath:  p,
			RemotePath: remoteFile,
			Mode:       uploadFileMode(info.Mode()),
			Size:       info.Size(),
		})
		return nil
	})
	if err != nil {
		return localUploadPlan{}, err
	}
	if len(plan.Files) == 0 && len(dirs) == 0 {
		if remoteRoot == root {
			return plan, nil
		}
		dir, err := normalizeMutationPath(slice, remoteRoot)
		if err != nil {
			return localUploadPlan{}, err
		}
		plan.EmptyRemoteDirs = append(plan.EmptyRemoteDirs, dir)
		return plan, nil
	}
	emptyDirs := make([]string, 0, len(dirs))
	for dir := range dirs {
		if _, ok := nonEmptyDirs[dir]; ok {
			continue
		}
		emptyDirs = append(emptyDirs, dir)
	}
	sort.Strings(emptyDirs)
	for _, dir := range emptyDirs {
		remoteDir, err := normalizeMutationPath(slice, joinRemoteUploadPath(remoteRoot, dir))
		if err != nil {
			return localUploadPlan{}, err
		}
		plan.EmptyRemoteDirs = append(plan.EmptyRemoteDirs, remoteDir)
	}
	sort.Slice(plan.Files, func(i, j int) bool {
		return plan.Files[i].RemotePath < plan.Files[j].RemotePath
	})
	return plan, nil
}

func joinRemoteUploadPath(root, rel string) string {
	if rel == "" || rel == "." {
		return root
	}
	return path.Join(root, rel)
}

func uploadFileMode(mode fs.FileMode) uint32 {
	if mode&0o111 != 0 {
		return 0o100755
	}
	return 0o100644
}

func defaultUploadConcurrency() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		return 4
	}
	if n > 16 {
		return 16
	}
	return n
}

func boundedUploadConcurrency(value, itemCount int) int {
	if itemCount <= 0 {
		return 1
	}
	if value <= 0 {
		value = defaultUploadConcurrency()
	}
	if value > itemCount {
		return itemCount
	}
	return value
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

func (r Runner) uploadLocalFiles(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, files []localUploadFile, concurrency int) ([]*corev1.FileEdit, error) {
	files = append([]localUploadFile(nil), files...)
	if err := hashLocalUploadFiles(ctx, files, boundedUploadConcurrency(concurrency, len(files))); err != nil {
		return nil, err
	}
	blobClient := corev1.NewBlobServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	known, err := remoteBlobRecords(callCtx, blobClient, files)
	if err != nil {
		return nil, err
	}
	missingByHash := map[string]localUploadFile{}
	for _, file := range files {
		if record, ok := known[file.ContentHash]; ok && record.Id != "" && record.State != "missing" {
			continue
		}
		if _, ok := missingByHash[file.ContentHash]; !ok {
			missingByHash[file.ContentHash] = file
		}
	}
	if len(missingByHash) > 0 {
		uploaded, err := uploadMissingLocalBlobs(callCtx, blobClient, missingByHash, boundedUploadConcurrency(concurrency, len(missingByHash)))
		if err != nil {
			return nil, err
		}
		for hash, record := range uploaded {
			known[hash] = record
		}
	}
	edits := make([]*corev1.FileEdit, 0, len(files))
	for _, file := range files {
		record, ok := known[file.ContentHash]
		if !ok || record.Id == "" {
			return nil, fmt.Errorf("blob upload did not return blob id for %s", file.LocalPath)
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.RemotePath,
			BlobId:      record.Id,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		})
	}
	return edits, nil
}

func hashLocalUploadFiles(ctx context.Context, files []localUploadFile, concurrency int) error {
	type result struct {
		index       int
		contentHash string
		size        int64
		err         error
	}
	jobs := make(chan int)
	results := make(chan result, len(files))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				select {
				case <-ctx.Done():
					results <- result{index: index, err: ctx.Err()}
					continue
				default:
				}
				f, err := os.Open(files[index].LocalPath)
				if err != nil {
					results <- result{index: index, err: err}
					continue
				}
				hash, size, hashErr := objectid.RawContentHashReader(f)
				closeErr := f.Close()
				if hashErr != nil {
					results <- result{index: index, err: hashErr}
					continue
				}
				if closeErr != nil {
					results <- result{index: index, err: closeErr}
					continue
				}
				results <- result{index: index, contentHash: hash, size: size}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range files {
			select {
			case <-ctx.Done():
				return
			case jobs <- i:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for result := range results {
		if result.err != nil {
			return result.err
		}
		files[result.index].ContentHash = result.contentHash
		files[result.index].Size = result.size
	}
	return ctx.Err()
}

func remoteBlobRecords(ctx context.Context, blobClient corev1.BlobServiceClient, files []localUploadFile) (map[string]*corev1.BlobRecord, error) {
	seen := map[string]struct{}{}
	hashes := make([]string, 0, len(files))
	for _, file := range files {
		if file.ContentHash == "" {
			return nil, fmt.Errorf("missing content hash for %s", file.LocalPath)
		}
		if _, ok := seen[file.ContentHash]; ok {
			continue
		}
		seen[file.ContentHash] = struct{}{}
		hashes = append(hashes, file.ContentHash)
	}
	records := map[string]*corev1.BlobRecord{}
	const batchSize = 512
	for start := 0; start < len(hashes); start += batchSize {
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		res, err := blobClient.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: hashes[start:end]})
		if err != nil {
			return nil, err
		}
		for _, record := range res.Blobs {
			records[record.ContentHash] = record
		}
	}
	return records, nil
}

func uploadMissingLocalBlobs(ctx context.Context, blobClient corev1.BlobServiceClient, missing map[string]localUploadFile, concurrency int) (map[string]*corev1.BlobRecord, error) {
	hashes := make([]string, 0, len(missing))
	for hash := range missing {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	type result struct {
		hash   string
		record *corev1.BlobRecord
		err    error
	}
	jobs := make(chan string)
	results := make(chan result, len(hashes))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for hash := range jobs {
				file := missing[hash]
				data, err := os.ReadFile(file.LocalPath)
				if err != nil {
					results <- result{hash: hash, err: err}
					continue
				}
				upload, err := blobClient.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: hash, Data: data})
				if err != nil {
					results <- result{hash: hash, err: err}
					continue
				}
				results <- result{
					hash: hash,
					record: &corev1.BlobRecord{
						Id:          upload.BlobId,
						ContentHash: upload.ContentHash,
						Size:        upload.Size,
						State:       "present",
					},
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, hash := range hashes {
			select {
			case <-ctx.Done():
				return
			case jobs <- hash:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	records := map[string]*corev1.BlobRecord{}
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		records[result.hash] = result.record
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return records, nil
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
		commitID, refCommitID, err = m.runner.waitForChangesetPublished(ctx, m.conn, m.cfg, cs.Id, 10*time.Second, false)
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
	fmt.Fprintf(m.runner.stdout(), "%s %s in %s at %s\n", operationPastTense(operation), changedPathsSummary(changed), output.Slice, shortID(refCommitID))
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

func (r Runner) runChangesetStatus(ctx context.Context, opts commandOptions, requestedID string, watch bool, watchTimeout time.Duration) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return err
	}
	if watch && cs.Status != "submitted" {
		if _, _, err := r.waitForChangesetPublished(ctx, conn, cfg, changesetID, watchTimeout, !opts.Quiet && !opts.jsonOutput()); err != nil {
			return err
		}
		cs, err = changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	}
	if err != nil {
		return err
	}
	output := changesetOutput{
		ChangesetID: cs.Id,
		PatchsetID:  cs.CurrentPatchsetId,
		Status:      cs.Status,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, output)
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

func (r Runner) runChangesetShow(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, cs)
	}
	if opts.Quiet {
		return nil
	}
	printChangesetDetails(r.Stdout, cs, false)
	return nil
}

func (r Runner) runChangesetExplain(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, cs)
	}
	if opts.Quiet {
		return nil
	}
	printChangesetDetails(r.Stdout, cs, true)
	return nil
}

func (r Runner) runChangesetVersions(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset_id": cs.Id,
			"patchsets":    cs.Patchsets,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "changeset: %s\n", cs.Id)
	printPatchsets(r.Stdout, cs)
	return nil
}

func (r Runner) runChangesetDiff(ctx context.Context, opts commandOptions, requestedID, patchset, from, to string, nameOnly, stat bool) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).DiffChangeset(authContext(ctx, cfg), &corev1.DiffChangesetRequest{
		ChangesetId:  changesetID,
		Patchset:     strings.TrimSpace(patchset),
		FromPatchset: strings.TrimSpace(from),
		ToPatchset:   strings.TrimSpace(to),
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
	if nameOnly {
		printPathList(r.Stdout, res.ChangedPaths)
		return nil
	}
	if stat {
		printPathStat(r.Stdout, res.ChangedPaths)
		return nil
	}
	_, err = io.WriteString(r.Stdout, res.Diff)
	return err
}

func (r Runner) runChangesetAbandon(ctx context.Context, opts commandOptions, requestedID, reason string) error {
	cfg, _, state, hasWorkspace, changesetID, usingWorkspaceCurrent, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = corev1.NewChangesetServiceClient(conn).AbandonChangeset(authContext(ctx, cfg), &corev1.AbandonChangesetRequest{
		ChangesetId: changesetID,
		Reason:      reason,
	})
	if err != nil {
		return err
	}
	if usingWorkspaceCurrent && hasWorkspace {
		state.CurrentChangesetID = ""
		state.CurrentPatchsetID = ""
		if err := r.writeWorkspaceState(state); err != nil {
			return err
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{ChangesetID: changesetID, Status: "abandoned"})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "abandoned changeset %s\n", changesetID)
	return nil
}

func (r Runner) runChangesetList(ctx context.Context, opts commandOptions, sliceRef, statusFilter string, limit int) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	var ref *corev1.SliceRef
	if strings.TrimSpace(sliceRef) != "" {
		ref, err = parseSliceRef(sliceRef)
		if err != nil {
			return err
		}
	} else {
		ws, err := r.readWorkspaceConfig()
		if err != nil {
			if isUserErrorCode(err, "not_in_workspace") {
				return userError("missing_slice", "changeset list needs a slice outside a workspace", "Run gs cs list --slice <account>/<slice>.")
			}
			return err
		}
		ref = &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).ListChangesets(authContext(ctx, cfg), &corev1.ListChangesetsRequest{
		AuthoringSlice: ref,
		Status:         strings.TrimSpace(statusFilter),
		Limit:          int32(limit),
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
	label := ref.Account + "/" + ref.Slice
	if statusFilter != "" {
		fmt.Fprintf(r.Stdout, "changesets for %s status=%s:\n", label, statusFilter)
	} else {
		fmt.Fprintf(r.Stdout, "changesets for %s:\n", label)
	}
	if len(res.Changesets) == 0 {
		fmt.Fprintln(r.Stdout, "  none")
		return nil
	}
	for _, cs := range res.Changesets {
		printChangesetOneLine(r.Stdout, cs)
	}
	return nil
}

func (r Runner) resolveChangesetCommandState(requestedID string) (UserConfig, WorkspaceConfig, WorkspaceState, bool, string, bool, error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" {
		cfg, ws, state, err := r.loadLocalState()
		if err != nil {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
		}
		if state.CurrentChangesetID == "" {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first or pass a changeset id.")
		}
		return cfg, ws, state, true, state.CurrentChangesetID, true, nil
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
	}
	var ws WorkspaceConfig
	var state WorkspaceState
	hasWorkspace := false
	usingWorkspaceCurrent := false
	if workspaceCfg, err := r.readWorkspaceConfig(); err == nil {
		ws = workspaceCfg
		state, err = r.readWorkspaceState()
		if err != nil {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
		}
		hasWorkspace = true
		usingWorkspaceCurrent = state.CurrentChangesetID == requestedID
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
	}
	return cfg, ws, state, hasWorkspace, requestedID, usingWorkspaceCurrent, nil
}

func (r Runner) getChangeset(ctx context.Context, cfg UserConfig, changesetID string) (*corev1.Changeset, error) {
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return corev1.NewChangesetServiceClient(conn).GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
}

func printChangesetDetails(w io.Writer, cs *corev1.Changeset, explain bool) {
	fmt.Fprintf(w, "changeset: %s\n", cs.Id)
	fmt.Fprintf(w, "status: %s\n", cs.Status)
	if cs.Title != "" {
		fmt.Fprintf(w, "title: %s\n", cs.Title)
	}
	if cs.Author != "" {
		fmt.Fprintf(w, "author: %s\n", cs.Author)
	}
	fmt.Fprintf(w, "authoring_slice: %s\n", changesetSliceLabel(cs.AuthoringSlice))
	if cs.TargetRef != "" {
		fmt.Fprintf(w, "target_ref: %s\n", cs.TargetRef)
	}
	if cs.BaseCommitId != "" {
		fmt.Fprintf(w, "base_commit: %s\n", cs.BaseCommitId)
	}
	if cs.CurrentPatchsetId != "" {
		fmt.Fprintf(w, "current_patchset: %d %s\n", cs.CurrentPatchsetNumber, cs.CurrentPatchsetId)
	}
	if cs.CommitId != "" {
		fmt.Fprintf(w, "commit: %s\n", cs.CommitId)
	}
	if len(cs.AffectedPaths) > 0 {
		fmt.Fprintln(w, "affected_paths:")
		printIndentedPaths(w, cs.AffectedPaths, "  ")
	}
	printPatchsets(w, cs)
	if explain {
		printChangesetExplain(w, cs)
	}
}

func printChangesetOneLine(w io.Writer, cs *corev1.Changeset) {
	title := strings.TrimSpace(cs.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(w, "  %s %s patchset=%d %s\n", cs.Id, cs.Status, cs.CurrentPatchsetNumber, title)
}

func printPatchsets(w io.Writer, cs *corev1.Changeset) {
	fmt.Fprintln(w, "patchsets:")
	if len(cs.Patchsets) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, patchset := range cs.Patchsets {
		current := ""
		if patchset.Id == cs.CurrentPatchsetId {
			current = " current"
		}
		changed := patchsetChangedPaths(patchset)
		fmt.Fprintf(w, "  %d %s%s changed=%d\n", patchset.Number, patchset.Id, current, len(changed))
		if len(changed) > 0 {
			printIndentedPaths(w, changed, "    ")
		}
	}
}

func printChangesetExplain(w io.Writer, cs *corev1.Changeset) {
	patchset := currentPatchset(cs)
	fmt.Fprintln(w, "validation:")
	if patchset == nil {
		fmt.Fprintln(w, "  no current patchset")
		return
	}
	printSubmitRequirements(w, patchset.SubmitRequirements)
	fmt.Fprintln(w, "read_set:")
	printIndentedPaths(w, pathSetEntryPaths(patchset.ReadSet), "  ")
	fmt.Fprintln(w, "write_set:")
	printIndentedPaths(w, pathSetEntryPaths(patchset.WriteSet), "  ")
	if len(patchset.PathBases) > 0 {
		fmt.Fprintln(w, "path_bases:")
		for _, base := range patchset.PathBases {
			state := "missing"
			if base.Exists {
				state = base.EntryKind
			}
			fmt.Fprintf(w, "  %s %s %s\n", base.Path, state, shortID(base.EntryFingerprint))
		}
	}
}

func printSubmitRequirements(w io.Writer, req *corev1.SubmitRequirements) {
	fmt.Fprintln(w, "submit_requirements:")
	if req == nil {
		fmt.Fprintln(w, "  none")
		return
	}
	printRequirementField(w, "required_approvals", req.RequiredApprovals)
	printRequirementField(w, "required_checks", req.RequiredChecks)
	printRequirementField(w, "path_lock_ids", req.PathLockIds)
	if req.SourceSliceDefinitionHash != "" {
		fmt.Fprintf(w, "  source_slice_definition_hash: %s\n", req.SourceSliceDefinitionHash)
	}
	if req.SourcePathLockSetHash != "" {
		fmt.Fprintf(w, "  source_path_lock_set_hash: %s\n", req.SourcePathLockSetHash)
	}
	if len(req.RequiredApprovals) == 0 && len(req.RequiredChecks) == 0 && len(req.PathLockIds) == 0 && req.SourceSliceDefinitionHash == "" && req.SourcePathLockSetHash == "" {
		fmt.Fprintln(w, "  none")
	}
}

func printRequirementField(w io.Writer, name string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s: %s\n", name, strings.Join(values, ", "))
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil || len(cs.Patchsets) == 0 {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func patchsetChangedPaths(patchset *corev1.Patchset) []string {
	if patchset == nil {
		return nil
	}
	if len(patchset.ChangedPaths) > 0 {
		return append([]string{}, patchset.ChangedPaths...)
	}
	return changedPathsFromEdits(patchset.FileEdits)
}

func pathSetEntryPaths(entries []*corev1.PathSetEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Path == "" {
			continue
		}
		p := entry.Path
		if entry.Recursive {
			p += "/**"
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func changesetSliceLabel(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return ref.Account + "/" + ref.Slice
}

func changedPathsFromEdits(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "rename":
			if edit.OldPath != "" {
				seen[edit.OldPath] = struct{}{}
			}
			if edit.Path != "" {
				seen[edit.Path] = struct{}{}
			}
		default:
			if edit.Path != "" {
				seen[edit.Path] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func printPathList(w io.Writer, paths []string) {
	for _, p := range paths {
		fmt.Fprintln(w, p)
	}
}

func printPathStat(w io.Writer, paths []string) {
	fmt.Fprintf(w, "%d changed path(s)\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(w, "  %s\n", p)
	}
}

func printIndentedPaths(w io.Writer, paths []string, indent string) {
	if len(paths) == 0 {
		fmt.Fprintln(w, indent+"none")
		return
	}
	for _, p := range paths {
		fmt.Fprintln(w, indent+p)
	}
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
		return r.writeJSONOutput(opts, res)
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
		return r.writeJSONOutput(opts, res)
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
		return r.writeJSONOutput(opts, commit)
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
	projectionRoots, err := shellProjectionRoots(sliceRef, mutationSlice, workspaceScoped)
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
	if r.shellLineEditorEnabled(opts) {
		return sh.runLineEditor(callCtx)
	}
	return sh.runScanner(callCtx, r.stdin(), opts.Quiet)
}

func (s *serverShell) runScanner(ctx context.Context, input io.Reader, quiet bool) error {
	scanner := bufio.NewScanner(input)
	for {
		if !quiet {
			s.refreshHeader()
			fmt.Fprint(s.stdout, s.prompt())
		}
		if !scanner.Scan() {
			break
		}
		done, err := s.exec(ctx, scanner.Text())
		if err != nil {
			fmt.Fprintf(s.stderr, "%s: %v\n", s.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
	return scanner.Err()
}

func (s *serverShell) runLineEditor(ctx context.Context) error {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCompleter(func(input string) []string {
		return s.completeLine(ctx, input)
	})
	for {
		s.refreshHeader()
		input, err := line.Prompt(s.plainPrompt())
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(input) != "" {
			line.AppendHistory(input)
		}
		done, err := s.exec(ctx, input)
		if err != nil {
			fmt.Fprintf(s.stderr, "%s: %v\n", s.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
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

func shellProjectionRoots(sliceRef string, slice *corev1.Slice, workspaceScoped bool) ([]string, error) {
	if strings.TrimSpace(sliceRef) == "" && workspaceScoped {
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

var serverShellCommands = []string{
	"?",
	"cat",
	"cd",
	"exit",
	"help",
	"ls",
	"mkdir",
	"mv",
	"pwd",
	"ref",
	"rm",
	"stat",
	"touch",
	"quit",
	"write",
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
	fmt.Fprintln(s.stdout, "  <tab>            complete commands and server paths")
}

func (s *serverShell) completeLine(ctx context.Context, line string) []string {
	tokens := shellLineTokens(line)
	if len(tokens) == 0 {
		return completeShellCommand(line, "")
	}
	if len(tokens) == 1 && !shellLineEndsWithSpace(line) {
		return completeShellCommand(line[:tokens[0].start], tokens[0].value)
	}
	command := tokens[0].value
	argIndex := 1
	token := ""
	prefix := line
	if !shellLineEndsWithSpace(line) {
		current := tokens[len(tokens)-1]
		argIndex = len(tokens) - 1
		token = current.value
		prefix = line[:current.start]
	} else {
		argIndex = len(tokens)
	}
	dirsOnly, ok := shellPathCompletion(command, argIndex)
	if !ok {
		return nil
	}
	completed := s.completePath(ctx, token, dirsOnly)
	out := make([]string, 0, len(completed))
	for _, value := range completed {
		out = append(out, prefix+value)
	}
	return out
}

func completeShellCommand(prefix, token string) []string {
	var out []string
	for _, command := range serverShellCommands {
		if !strings.HasPrefix(command, token) {
			continue
		}
		value := command
		if shellCommandAcceptsPath(command) {
			value += " "
		}
		out = append(out, prefix+value)
	}
	sort.Strings(out)
	return out
}

func shellPathCompletion(command string, argIndex int) (dirsOnly bool, ok bool) {
	switch command {
	case "cd":
		return true, argIndex == 1
	case "ls", "cat", "stat", "mkdir", "touch", "rm":
		return false, argIndex == 1
	case "mv":
		return false, argIndex == 1 || argIndex == 2
	case "write":
		return false, argIndex == 1
	default:
		return false, false
	}
}

func shellCommandAcceptsPath(command string) bool {
	_, ok := shellPathCompletion(command, 1)
	return ok
}

func (s *serverShell) completePath(ctx context.Context, token string, dirsOnly bool) []string {
	dirToken, namePrefix, replacementPrefix := shellCompletionPathParts(token)
	globalDir, err := s.resolve(dirToken)
	if err != nil {
		return nil
	}
	entries, err := s.directoryEntries(ctx, globalDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if dirsOnly && entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			continue
		}
		name := s.entryName(entry)
		if name == "" || !strings.HasPrefix(name, namePrefix) {
			continue
		}
		suffix := ""
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			suffix = "/"
		}
		out = append(out, replacementPrefix+name+suffix)
	}
	sort.Strings(out)
	return compactSortedStrings(out)
}

func shellCompletionPathParts(token string) (dirToken, namePrefix, replacementPrefix string) {
	if token == "" {
		return ".", "", ""
	}
	if strings.HasSuffix(token, "/") {
		return token, "", token
	}
	slash := strings.LastIndex(token, "/")
	if slash < 0 {
		return ".", token, ""
	}
	if slash == 0 {
		return "/", token[1:], "/"
	}
	dirToken = token[:slash]
	replacementPrefix = token[:slash+1]
	return dirToken, token[slash+1:], replacementPrefix
}

type shellLineToken struct {
	value string
	start int
	end   int
}

func shellLineTokens(line string) []shellLineToken {
	var tokens []shellLineToken
	for i := 0; i < len(line); {
		for i < len(line) && isShellLineSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && !isShellLineSpace(line[i]) {
			i++
		}
		tokens = append(tokens, shellLineToken{value: line[start:i], start: start, end: i})
	}
	return tokens
}

func shellLineEndsWithSpace(line string) bool {
	return len(line) > 0 && isShellLineSpace(line[len(line)-1])
}

func isShellLineSpace(b byte) bool {
	return b == ' ' || b == '\t'
}

func compactSortedStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value == out[len(out)-1] {
			continue
		}
		out = append(out, value)
	}
	return out
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
	entries, err := s.directoryEntries(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	for _, entry := range entries {
		name := s.entryName(entry)
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			name = s.colorize(ansiBlue, name+"/")
		}
		fmt.Fprintln(s.stdout, name)
	}
	return nil
}

func (s *serverShell) directoryEntries(ctx context.Context, globalPath string) ([]*corev1.TreeEntry, error) {
	list, err := s.repo.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: s.commitID, Path: globalPath, PageSize: 1000})
	var entries []*corev1.TreeEntry
	if err != nil {
		if grpcstatus.Code(err) != codes.NotFound {
			return nil, err
		}
		if globalPath != s.root && s.projectionDirectoryEntry(globalPath) == nil {
			return nil, err
		}
	} else {
		entries = append([]*corev1.TreeEntry(nil), list.Entries...)
	}
	entries = s.projectDirectoryEntries(globalPath, entries)
	entries = s.withSyntheticDirectoryEntries(globalPath, entries)
	sort.Slice(entries, func(i, j int) bool {
		return s.entryName(entries[i]) < s.entryName(entries[j])
	})
	return entries, nil
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
	var candidate string
	if !s.scoped {
		if strings.HasPrefix(value, "/") {
			candidate = value
		} else {
			candidate = strings.TrimRight(s.cwd, "/") + "/" + value
		}
	} else {
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
	}
	cleaned, err := cleanShellGlobalPath(candidate)
	if err != nil {
		return "", err
	}
	if !paths.Contains(s.root, cleaned) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+cleaned, "Use paths under "+s.shellPath(s.root)+".")
	}
	if !s.pathInProjection(cleaned) {
		scopeKind := "workspace slice"
		if s.root == "/" {
			scopeKind = "attached slice"
		}
		return "", userError("outside_slice", "path is outside the "+scopeKind+": "+cleaned, "Use paths included by "+s.scope+".")
	}
	return cleaned, nil
}

func (s *serverShell) prompt() string {
	return s.promptWithColor(s.color)
}

func (s *serverShell) plainPrompt() string {
	return s.promptWithColor(false)
}

func (s *serverShell) promptWithColor(enabled bool) string {
	if !s.scoped {
		return fmt.Sprintf("%s %s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
	}
	return fmt.Sprintf("%s %s:%s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiGreen, s.scope), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
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
	root, err := r.workspaceRoot()
	if err != nil {
		return hydrateResult{}, err
	}
	hydrator := workspaceHydrator{
		runner:   r,
		root:     root,
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
	root     string
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
	rel, err := workspaceRelPath(h.ws, globalPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(h.root, filepath.FromSlash(rel)), 0o755); err != nil {
		return err
	}
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
	target := filepath.Join(h.root, filepath.FromSlash(rel))
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
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, err
	}
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
		globalPath, err := workspaceRelativePathToGlobalPath(ws, rel)
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
	if _, err := os.Stat(r.userConfigPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UserConfig{}, userError("not_logged_in", "not logged in", "Run gs auth login.")
		}
		return UserConfig{}, err
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return cfg, err
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return cfg, userError("invalid_user_config", "invalid user config", "Run gs auth login again.")
	}
	return cfg, nil
}

func (r Runner) readPartialUserConfig() (UserConfig, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
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
	root, err := r.workspaceRoot()
	if err != nil {
		return cfg, err
	}
	if err := readJSONFile(filepath.Join(root, ".gs", "slice.json"), &cfg); err != nil {
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
	root, err := r.workspaceRoot()
	if err != nil {
		return state, err
	}
	err = readJSONFile(filepath.Join(root, ".gs", "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	return state, err
}

func (r Runner) readBaseSnapshot() (BaseSnapshot, error) {
	var snapshot BaseSnapshot
	root, err := r.workspaceRoot()
	if err != nil {
		return snapshot, err
	}
	err = readJSONFile(filepath.Join(root, ".gs", "base_snapshot.json"), &snapshot)
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
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "base_snapshot.json"), snapshot, 0o644)
}

func (r Runner) writeWorkspaceState(state WorkspaceState) error {
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gs")
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

func (r Runner) shellLineEditorEnabled(opts commandOptions) bool {
	if opts.Quiet || opts.NonInteractive || os.Getenv("TERM") == "dumb" {
		return false
	}
	return r.stdin() == os.Stdin && r.stdout() == os.Stdout && isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(w any) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func (r Runner) objectCache() (*clientcache.ObjectCache, error) {
	root := os.Getenv("GS_CLIENT_CACHE_DIR")
	if root == "" {
		root = os.Getenv("GITSLICE_CLIENT_CACHE_DIR")
	}
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

func (r Runner) workspaceRoot() (string, error) {
	dir := r.cwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".gs", "slice.json")); err == nil {
			return dir, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", userError("not_in_workspace", "not in a gitslice workspace", "Run gs workspace init <account>/<slice>.")
		}
		dir = parent
	}
}

func (r Runner) runVersion(opts commandOptions) error {
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

func (r Runner) runSchema(opts commandOptions) error {
	return r.writeJSONOutput(opts, map[string]any{
		"schema_version": "v1",
		"global_flags": []map[string]any{
			{"name": "--format", "values": []string{"text", "json"}, "default": "text", "description": "output format"},
			{"name": "--json", "description": "emit JSON output; optional comma-separated fields use --json=field,field"},
			{"name": "--jq", "description": "filter structured output with a jq expression"},
			{"name": "--template", "description": "format structured output with a Go template over JSON fields"},
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
				"use":            "gs auth token",
				"summary":        "print the validated bearer token for scripts and Git-compatible flows",
				"writes_stdout":  true,
				"machine_output": []string{"token", "server_addr", "subject_id"},
			},
			{
				"use":            "gs auth logout",
				"summary":        "clear saved authentication credentials",
				"writes_stdout":  true,
				"machine_output": []string{"signed_in", "was_signed_in", "server_addr", "config_path"},
			},
			{
				"use":            "gs context",
				"summary":        "show resolved server, auth, workspace, and active slice context",
				"aliases":        []string{"gs ctx"},
				"writes_stdout":  true,
				"machine_output": []string{"cwd", "config_path", "server_addr", "signed_in", "subject_id", "auth_reason", "workspace", "active_slice", "active_slice_source"},
			},
			{
				"use":            "gs config list",
				"summary":        "list local CLI configuration without exposing secrets",
				"aliases":        []string{"gs cfg list"},
				"writes_stdout":  true,
				"machine_output": []string{"config_path", "server_addr", "subject_id", "token_present"},
			},
			{
				"use":            "gs config get <key>",
				"summary":        "get one local CLI configuration value",
				"aliases":        []string{"gs cfg get <key>"},
				"args":           []string{"key"},
				"writes_stdout":  true,
				"machine_output": []string{"<key>"},
			},
			{
				"use":            "gs config set <key> <value>",
				"summary":        "set one local CLI configuration value",
				"aliases":        []string{"gs cfg set <key> <value>"},
				"args":           []string{"key", "value"},
				"writes_stdout":  true,
				"machine_output": []string{"config_path", "server_addr", "subject_id", "token_present"},
			},
			{
				"use":            "gs alias list",
				"summary":        "list local command aliases",
				"writes_stdout":  true,
				"machine_output": []string{"aliases"},
			},
			{
				"use":            "gs alias set <name> <command>",
				"summary":        "create or update a local command alias",
				"args":           []string{"name", "command"},
				"writes_stdout":  true,
				"machine_output": []string{"name", "expansion"},
			},
			{
				"use":            "gs alias delete <name>",
				"summary":        "delete a local command alias",
				"aliases":        []string{"gs alias remove <name>", "gs alias rm <name>"},
				"args":           []string{"name"},
				"writes_stdout":  true,
				"machine_output": []string{"name", "deleted"},
			},
			{
				"use":            "gs rpc list",
				"summary":        "list generated core RPC methods",
				"writes_stdout":  true,
				"machine_output": []string{"methods"},
			},
			{
				"use":            "gs rpc call <service>/<method>",
				"summary":        "call a generated unary core RPC with a JSON request",
				"args":           []string{"service/method"},
				"flags":          []string{"--request", "--server", "--unauthenticated"},
				"writes_stdout":  true,
				"machine_output": []string{"RPC response fields"},
			},
			{
				"use":            "gs browse [web-path]",
				"summary":        "open or print a Gitslice web UI URL",
				"args":           []string{"web-path"},
				"flags":          []string{"--web-url", "--print"},
				"writes_stdout":  true,
				"machine_output": []string{"url"},
			},
			{
				"use":            "gs workspace init <account>/<slice>",
				"summary":        "bind the current directory to one slice and hydrate its files",
				"aliases":        []string{"gs ws init <account>/<slice>"},
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "slice_id", "base_commit_id", "client_object_cache", "hydrated"},
			},
			{
				"use":            "gs workspace hydrate <path> [path...]",
				"summary":        "hydrate workspace files through the client object cache",
				"aliases":        []string{"gs ws hydrate <path> [path...]"},
				"args":           []string{"path"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "base_commit_id", "client_object_cache", "hydrated"},
			},
			{
				"use":            "gs status",
				"summary":        "show workspace changes against the local base snapshot",
				"aliases":        []string{"gs st"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "changed_path_count", "changed_paths", "changeset_id", "patchset_id"},
			},
			{
				"use":            "gs diff",
				"summary":        "show workspace diff against the local base snapshot, or server-side current changeset diff with --from/--to",
				"flags":          []string{"--from", "--to", "--name-only", "--stat"},
				"writes_stdout":  true,
				"machine_output": []string{"workspace", "base_commit_id", "changed_path_count", "changed_paths", "diff"},
			},
			{
				"use":            "gs slice create <account>/<slice>",
				"summary":        "create a slice",
				"aliases":        []string{"gs slices create <account>/<slice>"},
				"args":           []string{"account/slice"},
				"flags":          []string{"--include", "--visibility"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice list [account]",
				"summary":        "list slices in an account",
				"aliases":        []string{"gs slices list [account]"},
				"args":           []string{"account"},
				"writes_stdout":  true,
				"machine_output": []string{"account", "slices"},
			},
			{
				"use":            "gs slice info <account>/<slice>",
				"summary":        "show slice metadata",
				"aliases":        []string{"gs slices info <account>/<slice>"},
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice paths <account>/<slice>",
				"summary":        "show slice included paths",
				"aliases":        []string{"gs slices paths <account>/<slice>"},
				"args":           []string{"account/slice"},
				"writes_stdout":  true,
				"machine_output": []string{"ref", "included_paths"},
			},
			{
				"use":            "gs slice update <account>/<slice>",
				"summary":        "update slice included paths or visibility",
				"aliases":        []string{"gs slices update <account>/<slice>"},
				"args":           []string{"account/slice"},
				"flags":          []string{"--include", "--visibility"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "ref", "account", "slice", "version", "visibility", "included_paths", "definition_hash"},
			},
			{
				"use":            "gs slice delete <account>/<slice>",
				"summary":        "delete a slice",
				"aliases":        []string{"gs slices delete <account>/<slice>"},
				"args":           []string{"account/slice"},
				"flags":          []string{"--yes"},
				"writes_stdout":  true,
				"machine_output": []string{"slice_id", "ref"},
			},
			{
				"use":            "gs cs create",
				"summary":        "create a changeset and first patchset from workspace edits",
				"aliases":        []string{"gs changeset create"},
				"flags":          []string{"--title"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id"},
			},
			{
				"use":            "gs cs update",
				"summary":        "create a new patchset for the current changeset",
				"aliases":        []string{"gs changeset update"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id"},
			},
			{
				"use":            "gs cs submit [changeset-id]",
				"summary":        "submit the current or named changeset through server-side validation",
				"aliases":        []string{"gs changeset submit [changeset-id]"},
				"flags":          []string{"--no-watch", "--watch-timeout"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "commit_id", "target_ref", "new_ref_commit_id", "status"},
			},
			{
				"use":            "gs cs status [changeset-id]",
				"summary":        "show the current or named changeset status",
				"aliases":        []string{"gs changeset status [changeset-id]"},
				"flags":          []string{"--watch", "--watch-timeout"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchset_id", "status"},
			},
			{
				"use":            "gs cs show [changeset-id]",
				"summary":        "show changeset details and patchsets",
				"aliases":        []string{"gs changeset show [changeset-id]"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "authoring_slice", "status", "patchsets", "current_patchset_id"},
			},
			{
				"use":            "gs cs explain [changeset-id]",
				"summary":        "show changeset validation inputs, requirements, read set, and write set",
				"aliases":        []string{"gs changeset explain [changeset-id]"},
				"writes_stdout":  true,
				"machine_output": []string{"id", "submit_requirements", "patchsets"},
			},
			{
				"use":            "gs cs versions [changeset-id]",
				"summary":        "list patchset versions for a changeset",
				"aliases":        []string{"gs changeset versions [changeset-id]", "gs cs patchsets", "gs changeset patchsets"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "patchsets"},
			},
			{
				"use":            "gs cs diff [changeset-id]",
				"summary":        "show a server-side diff for one patchset or between two patchsets",
				"aliases":        []string{"gs changeset diff [changeset-id]"},
				"flags":          []string{"--patchset", "--from", "--to", "--name-only", "--stat"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "from_patchset_id", "to_patchset_id", "changed_paths", "diff"},
			},
			{
				"use":            "gs cs list",
				"summary":        "list changesets for a slice",
				"aliases":        []string{"gs changeset list"},
				"flags":          []string{"--slice", "--status", "--limit"},
				"writes_stdout":  true,
				"machine_output": []string{"changesets"},
			},
			{
				"use":            "gs cs abandon [changeset-id]",
				"summary":        "abandon the current or named draft changeset",
				"aliases":        []string{"gs changeset abandon [changeset-id]"},
				"flags":          []string{"--reason"},
				"writes_stdout":  true,
				"machine_output": []string{"changeset_id", "status"},
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
				"use":            "gs fs upload <local-path> <absolute-remote-path>",
				"summary":        "upload a local file or directory under the signed-in home slice",
				"args":           []string{"local-path", "absolute-remote-path"},
				"flags":          []string{"--recursive", "--concurrency"},
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
				"aliases":        []string{"gs repository import github <owner/repo-or-url>"},
				"flags":          []string{"--mount", "--slice", "--mode", "--deep", "--max-commits", "--resume"},
				"writes_stdout":  true,
				"machine_output": []string{"source", "mount_path", "mode", "target_ref", "final_commit_id", "commits"},
			},
			{
				"use":            "gs commit list",
				"summary":        "list native commits from the main ref",
				"aliases":        []string{"gs commits list"},
				"flags":          []string{"--limit"},
				"writes_stdout":  true,
				"machine_output": []string{"commits"},
			},
			{
				"use":            "gs commit inspect <commit-id>",
				"summary":        "inspect a native commit",
				"aliases":        []string{"gs commits inspect <commit-id>"},
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
				"use":            "gs version",
				"summary":        "print CLI version and build information",
				"writes_stdout":  true,
				"machine_output": []string{"version", "commit", "build_date", "go_version", "dirty"},
			},
			{
				"use":           "gs schema",
				"summary":       "print this machine-readable CLI schema",
				"writes_stdout": true,
			},
			{
				"use":           "gs completion <shell>",
				"summary":       "generate shell completion scripts",
				"args":          []string{"shell"},
				"writes_stdout": true,
			},
			{
				"use":            "gs help <topic>",
				"summary":        "show help for a command or CLI topic",
				"args":           []string{"command", "topic"},
				"writes_stdout":  true,
				"machine_output": []string{},
			},
		},
		"help_topics": cliHelpTopicSchema(),
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

func cliHelpTopicSchema() []map[string]string {
	out := make([]map[string]string, 0, len(cliHelpTopics))
	for _, topic := range cliHelpTopics {
		out = append(out, map[string]string{
			"name":    topic.Name,
			"summary": topic.Summary,
		})
	}
	return out
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
	if strings.HasPrefix(value, "/") {
		globalPath, err := cleanShellGlobalPath(value)
		if err != nil {
			return "", err
		}
		if !paths.InAnyPrefix(ws.IncludedPaths, globalPath) {
			return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
		}
		return globalPath, nil
	}
	return workspaceRelativePathToGlobalPath(ws, value)
}

func workspaceRelativePathToGlobalPath(ws WorkspaceConfig, value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if len(ws.IncludedPaths) == 0 {
		return "", fmt.Errorf("workspace has no included paths")
	}
	canonicalRel, err := cleanShellGlobalPath(value)
	if err == nil && paths.InAnyPrefix(ws.IncludedPaths, canonicalRel) {
		return canonicalRel, nil
	}
	globalPath, err := paths.FromWorkspacePath(ws.IncludedPaths[0], value)
	if err != nil {
		return "", err
	}
	if !paths.InAnyPrefix(ws.IncludedPaths, globalPath) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
	}
	return globalPath, nil
}

func workspaceRelPath(ws WorkspaceConfig, globalPath string) (string, error) {
	cleaned, err := cleanShellGlobalPath(globalPath)
	if err != nil {
		return "", err
	}
	if paths.InAnyPrefix(ws.IncludedPaths, cleaned) {
		return strings.TrimPrefix(cleaned, "/"), nil
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
	if value := os.Getenv("GS_SERVER_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_GRPC_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_SERVER_ADDR"); value != "" {
		return value
	}
	return "127.0.0.1:50051"
}

func defaultWebURL() string {
	if value := os.Getenv("GS_WEB_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_WEB_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:5173"
}

func defaultGatewayURL() string {
	if value := os.Getenv("GS_GATEWAY_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_GATEWAY_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GS_HTTP_ADDR"); value != "" {
		if strings.Contains(value, "://") {
			return value
		}
		return "http://" + value
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

func parsePositiveDurationFlag(name, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	duration, err := time.ParseDuration(trimmed)
	if err != nil || duration <= 0 {
		return 0, userError("invalid_duration", "invalid --"+name+" duration "+value, "Use a positive duration such as 10s, 2m, or 500ms.")
	}
	return duration, nil
}

func splitAliasExpansion(value string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		parts = append(parts, current.String())
		current.Reset()
	}
	for _, r := range strings.TrimSpace(value) {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, userError("invalid_alias", "alias expansion has an unterminated quote", "Close the quote or remove it.")
	}
	flush()
	return parts, nil
}

func userError(code, message, hint string) error {
	return commandError{Code: code, Message: message, Hint: hint}
}

func isUserErrorCode(err error, code string) bool {
	var cmdErr commandError
	return errors.As(err, &cmdErr) && cmdErr.Code == code
}

func wantsJSON(args []string) bool {
	for i, arg := range args {
		if arg == "--json" {
			return true
		}
		if strings.HasPrefix(arg, "--json=") {
			return true
		}
		if arg == "--jq" {
			return true
		}
		if strings.HasPrefix(arg, "--jq=") {
			return true
		}
		if arg == "--template" {
			return true
		}
		if strings.HasPrefix(arg, "--template=") {
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

func parseJSONFields(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	fields := strings.Split(raw, ",")
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, userError("invalid_json_fields", "empty JSON field selector", "Use --json=field or --json=field,field.")
		}
		for _, ch := range field {
			if !(ch == '_' || ch == '-' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
				return nil, userError("invalid_json_fields", "invalid JSON field selector "+field, "Use field names from gs schema.")
			}
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out, nil
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

func (r Runner) writeJSONOutput(opts commandOptions, v any) error {
	output := v
	if len(opts.JSONFields) > 0 {
		projected, err := selectJSONFields(v, opts.JSONFields)
		if err != nil {
			return err
		}
		output = projected
	}
	if opts.JQ != "" {
		return r.writeJQOutput(opts.JQ, output)
	}
	if opts.Template != "" {
		return r.writeTemplateOutput(opts.Template, output)
	}
	return writeJSON(r.Stdout, output)
}

func (r Runner) writeJQOutput(rawQuery string, v any) error {
	data, err := templateData(v)
	if err != nil {
		return err
	}
	query, err := gojq.Parse(rawQuery)
	if err != nil {
		return userError("invalid_jq", "invalid jq expression: "+err.Error(), "Use jq syntax over fields from gs schema.")
	}
	iter := query.Run(data)
	for {
		value, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, ok := value.(error); ok {
			return userError("jq_failed", "jq execution failed: "+err.Error(), "Check field names and value types, or run the command with --json.")
		}
		if err := writeJQValue(r.Stdout, value); err != nil {
			return err
		}
	}
}

func writeJQValue(w io.Writer, value any) error {
	switch v := value.(type) {
	case string:
		_, err := fmt.Fprintln(w, v)
		return err
	case nil:
		_, err := fmt.Fprintln(w, "null")
		return err
	case bool:
		_, err := fmt.Fprintln(w, v)
		return err
	case float64:
		_, err := fmt.Fprintln(w, v)
		return err
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
}

func (r Runner) writeTemplateOutput(rawTemplate string, v any) error {
	data, err := templateData(v)
	if err != nil {
		return err
	}
	tpl, err := template.New("gs").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(v any) (string, error) {
			data, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}).Parse(rawTemplate)
	if err != nil {
		return userError("invalid_template", "invalid template: "+err.Error(), "Use Go text/template syntax over fields from gs schema.")
	}
	if err := tpl.Execute(r.Stdout, data); err != nil {
		return userError("template_failed", "template execution failed: "+err.Error(), "Check field names with gs schema or run the command with --json.")
	}
	return nil
}

func templateData(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func selectJSONFields(v any, fields []string) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, userError("invalid_json_fields", "JSON field selection requires an object output", "Run without field selection to inspect the full JSON output.")
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		value, ok := obj[field]
		if !ok {
			return nil, userError("unknown_json_field", "unknown JSON field "+field, "Run the command with --json to inspect available fields or use gs schema.")
		}
		out[field] = value
	}
	return out, nil
}

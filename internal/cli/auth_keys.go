package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// authKeysCommands builds gs auth keys (list, create, revoke) and gs auth
// use-key. A lost agent key is replaced by an owner of the agent's account:
// create a key, hand it over, install it with use-key, revoke the lost one.
func (r Runner) authKeysCommands(opts *commandOptions) []*cobra.Command {
	keysCmd := &cobra.Command{
		Use:   "keys",
		Short: "List, create and revoke an agent's API keys",
		Long: `Manage the API keys of an agent. An agent's key is shown once, when it is
created, and the server keeps only a hash of it, so a lost key cannot be read
back. Make a new one instead:

  1. the human who claimed the agent runs
       gs auth keys create --agent <username>
     (or the agent itself, while it still has a working key, without --agent);
  2. the key goes to the agent through a channel you trust, never a chat log;
  3. the agent runs  gs auth use-key  and pastes it (or pipes it in);
  4. revoke the lost key:  gs auth keys revoke <key-id> --agent <username>.

--agent is the agent's account name. You may manage an agent's keys when you are
signed in as that agent, or you own its account (accept its claim at
<web-url>/claims or with gs claims accept).`,
		RunE: requireSubcommand("auth keys"),
	}
	listAgent := ""
	keysListCmd := &cobra.Command{
		Use:   "list",
		Short: "List an agent's API keys (never their secrets)",
		Args:  noArgs("gs auth keys list [--agent username]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthKeysList(cmd.Context(), *opts, listAgent)
		},
	}
	keysListCmd.Flags().StringVar(&listAgent, "agent", "", "the agent's account name; omit to list your own keys")
	createAgent, createName := "", ""
	var createSlices []string
	createRole, createExpires, createPurpose := "", "", ""
	keysCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API key for yourself or an agent and print it once",
		Long: `Create a new API key for yourself (without --agent) or for a managed agent.
The key is printed on stdout and never again, so capture it:
  KEY=$(gs auth keys create --slice acme/app --role contributor --expires 7d)
Everything else goes to stderr. With --json the object carries "secret".`,
		Args: noArgs("gs auth keys create [--agent username] [--name label] [--slice account/slice]... [--role reader|contributor|writer] [--expires duration] [--purpose text]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthKeysCreate(cmd.Context(), *opts, createAPIKeyOptions{
				Agent:   createAgent,
				Name:    createName,
				Slices:  createSlices,
				MaxRole: createRole,
				Expires: createExpires,
				Purpose: createPurpose,
			})
		},
	}
	keysCreateCmd.Flags().StringVar(&createAgent, "agent", "", "the agent's account name; omit to create a key for yourself")
	keysCreateCmd.Flags().StringVar(&createName, "name", "", "a label for the key, up to 64 characters (default \"key\")")
	keysCreateCmd.Flags().StringArrayVar(&createSlices, "slice", nil, "limit key access to a slice (<account>/<slice>); repeatable")
	keysCreateCmd.Flags().StringVar(&createRole, "role", "", "cap maximum role to reader, contributor, or writer")
	keysCreateCmd.Flags().StringVar(&createExpires, "expires", "", "key lifetime as a duration (e.g. 24h or 7d)")
	keysCreateCmd.Flags().StringVar(&createPurpose, "purpose", "", "optional description recorded on the key for auditability")
	revokeAgent := ""
	keysRevokeCmd := &cobra.Command{
		Use:   "revoke <key-id>",
		Short: "Revoke an agent's API key at once",
		Args:  exactArgs(1, "gs auth keys revoke <key-id> [--agent username]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthKeysRevoke(cmd.Context(), *opts, revokeAgent, args[0])
		},
	}
	keysRevokeCmd.Flags().StringVar(&revokeAgent, "agent", "", "the agent's account name; omit to revoke one of your own keys")
	keysCmd.AddCommand(keysListCmd, keysCreateCmd, keysRevokeCmd)

	useServer := defaultServerAddr()
	useKeyFile := ""
	useKeyCmd := &cobra.Command{
		Use:   "use-key",
		Short: "Sign in with an API key an owner created for this agent",
		Long: `Install an API key as this CLI's credential, after checking that the server
accepts it. Read the key from stdin (hidden when you type it), or from --key-file;
it is never taken from a flag, which would put it in your shell history and the
process list.

  printf %s "$KEY" | gs auth use-key
  gs auth use-key --key-file ./new-key`,
		Args: noArgs("gs auth use-key [--server addr] [--key-file path]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthUseKey(cmd.Context(), *opts, useServer, useKeyFile)
		},
	}
	useKeyCmd.Flags().StringVar(&useServer, "server", useServer, "server gRPC address")
	useKeyCmd.Flags().StringVar(&useKeyFile, "key-file", "", "read the key from this file instead of stdin")
	return []*cobra.Command{keysCmd, useKeyCmd}
}

type createAPIKeyOptions struct {
	Agent   string
	Name    string
	Slices  []string
	MaxRole string
	Expires string
	Purpose string
}

type apiKeyOutput struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CreatedAt      string   `json:"created_at"`
	LastUsedAt     string   `json:"last_used_at,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	RevokedAt      string   `json:"revoked_at,omitempty"`
	Active         bool     `json:"active"`
	SliceIDs       []string `json:"slice_ids,omitempty"`
	Slices         []string `json:"slices,omitempty"`
	MaxRole        string   `json:"max_role,omitempty"`
	ParentKeyID    string   `json:"parent_key_id,omitempty"`
	Purpose        string   `json:"purpose,omitempty"`
	DaemonID       string   `json:"daemon_id,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
}

func apiKeyToOutput(key *corev1.ApiKey) apiKeyOutput {
	out := apiKeyOutput{
		ID:             key.GetId(),
		Name:           key.GetName(),
		CreatedAt:      key.GetCreatedAt(),
		LastUsedAt:     key.GetLastUsedAt(),
		ExpiresAt:      key.GetExpiresAt(),
		RevokedAt:      key.GetRevokedAt(),
		SliceIDs:       slices.Clone(key.GetSliceIds()),
		Slices:         slices.Clone(key.GetSlices()),
		MaxRole:        key.GetMaxRole(),
		ParentKeyID:    key.GetParentKeyId(),
		Purpose:        key.GetPurpose(),
		DaemonID:       key.GetDaemonId(),
		ConversationID: key.GetConversationId(),
	}
	out.Active = out.RevokedAt == ""
	if out.Active && out.ExpiresAt != "" {
		if expires, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil && !time.Now().Before(expires) {
			out.Active = false
		}
	}
	return out
}

func parseAPIKeyDuration(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	const maxAPIKeyDays = math.MaxInt64 / int64(24*time.Hour)
	var dur time.Duration
	if strings.HasSuffix(strings.ToLower(raw), "d") {
		numStr := strings.TrimSpace(raw[:len(raw)-1])
		days, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil || days <= 0 {
			return 0, userError("invalid_duration", fmt.Sprintf("invalid --expires duration %q", raw),
				"Use a positive Go duration or day count, such as 1h, 24h, or 7d.")
		}
		if days > maxAPIKeyDays {
			return 0, userError("invalid_duration", fmt.Sprintf("--expires duration %q exceeds maximum allowed days (%d)", raw, maxAPIKeyDays),
				"Use a shorter duration, such as 24h, 7d, or 365d.")
		}
		dur = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		dur, err = time.ParseDuration(raw)
		if err != nil {
			return 0, userError("invalid_duration", fmt.Sprintf("invalid --expires duration %q: %v", raw, err),
				"Use a positive Go duration or day count, such as 1h, 24h, or 7d.")
		}
	}
	if dur < time.Second {
		return 0, userError("invalid_duration", "key expiry duration must be at least 1s",
			"Use a duration of at least 1s, such as 30m, 24h, or 7d.")
	}
	return int64(dur.Seconds()), nil
}

func apiKeyDay(rfc3339 string) string {
	if t, err := time.Parse(time.RFC3339, rfc3339); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return rfc3339
}

// authKeysError explains the one failure a person can fix: the account named
// is not an agent they may manage.
func authKeysError(err error) error {
	if grpcstatus.Code(err) == codes.NotFound {
		return userError("agent_not_found", grpcstatus.Convert(err).Message(),
			"Pass --agent with the account name of an agent you own. Accept its claim first (gs claims list, gs claims accept), or sign in as the agent itself.")
	}
	return err
}

func (r Runner) runAuthKeysList(ctx context.Context, opts commandOptions, agent string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).ListApiKeys(callCtx, &corev1.ListApiKeysRequest{Agent: agent})
	if err != nil {
		return authKeysError(err)
	}
	keys := make([]apiKeyOutput, 0, len(resp.Keys))
	for _, key := range resp.Keys {
		keys = append(keys, apiKeyToOutput(key))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"account": resp.Account, "agent_subject_id": resp.AgentSubjectId, "keys": keys})
	}
	if opts.Quiet {
		return nil
	}
	owner := resp.Account
	if owner == "" {
		owner = resp.AgentSubjectId
	}
	if len(keys) == 0 {
		fmt.Fprintf(r.Stdout, "no API keys found for %s\n", owner)
		return nil
	}
	fmt.Fprintf(r.Stdout, "API keys of %s:\n", owner)
	w := tabwriter.NewWriter(r.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tNAME\tSCOPE\tROLE\tPARENT\tCREATED\tLAST USED\tSTATE\tPURPOSE")
	for _, key := range keys {
		scope := "unscoped"
		if len(key.Slices) > 0 {
			scope = strings.Join(key.Slices, ",")
		} else if len(key.SliceIDs) > 0 {
			scope = strings.Join(key.SliceIDs, ",")
		}
		role := key.MaxRole
		if role == "" {
			role = "uncapped"
		}
		parent := key.ParentKeyID
		if parent == "" {
			parent = "-"
		}
		purpose := key.Purpose
		if purpose == "" {
			purpose = "-"
		}
		lastUsed, state := "never", "active"
		if key.LastUsedAt != "" {
			lastUsed = apiKeyDay(key.LastUsedAt)
		}
		switch {
		case key.RevokedAt != "":
			state = "revoked " + apiKeyDay(key.RevokedAt)
		case !key.Active:
			state = "expired " + apiKeyDay(key.ExpiresAt)
		case key.ExpiresAt != "":
			state = "expires " + apiKeyDay(key.ExpiresAt)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			key.ID, key.Name, scope, role, parent, apiKeyDay(key.CreatedAt), lastUsed, state, purpose)
	}
	return w.Flush()
}

func (r Runner) runAuthKeysCreate(ctx context.Context, opts commandOptions, createOpts createAPIKeyOptions) error {
	expiresInSeconds, err := parseAPIKeyDuration(createOpts.Expires)
	if err != nil {
		return err
	}
	maxRole, err := storage.NormalizeAPIKeyMaxRole(createOpts.MaxRole)
	if err != nil {
		return userError("invalid_role", err.Error(), "Use reader, contributor, or writer.")
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).CreateApiKey(callCtx, &corev1.CreateApiKeyRequest{
		Agent:            createOpts.Agent,
		Name:             createOpts.Name,
		Slices:           createOpts.Slices,
		MaxRole:          maxRole,
		ExpiresInSeconds: expiresInSeconds,
		Purpose:          createOpts.Purpose,
	})
	if err != nil {
		return authKeysError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"account": resp.Account, "agent_subject_id": resp.AgentSubjectId,
			"key": apiKeyToOutput(resp.Key), "secret": resp.Secret,
		})
	}
	if !opts.Quiet {
		owner := resp.Account
		if owner == "" {
			owner = resp.AgentSubjectId
		}
		fmt.Fprintf(r.stderr(), "created API key %s (%s) for %s. It is shown once; the server keeps only a hash.\n", resp.Key.GetId(), resp.Key.GetName(), owner)
		if createOpts.Agent != "" {
			fmt.Fprintf(r.stderr(), "Give it to the agent privately, then on the agent: gs auth use-key\n")
			fmt.Fprintf(r.stderr(), "Revoke the key: gs auth keys revoke %s --agent %s\n", resp.Key.GetId(), resp.Account)
		} else {
			fmt.Fprintf(r.stderr(), "Use it on a runner or child tool with: gs auth use-key\n")
			fmt.Fprintf(r.stderr(), "Revoke the key: gs auth keys revoke %s\n", resp.Key.GetId())
		}
	}
	fmt.Fprintln(r.Stdout, resp.Secret)
	return nil
}

func (r Runner) runAuthKeysRevoke(ctx context.Context, opts commandOptions, agent, keyID string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).RevokeApiKey(callCtx, &corev1.RevokeApiKeyRequest{Agent: agent, KeyId: strings.TrimSpace(keyID)})
	if err != nil {
		return authKeysError(err)
	}
	key := apiKeyToOutput(resp.Key)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"key": key})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "revoked API key %s (%s); it no longer signs anything in\n", key.ID, key.Name)
	return nil
}

func (r Runner) runAuthUseKey(ctx context.Context, opts commandOptions, serverAddr, keyFile string) error {
	key, err := r.readAPIKeyInput(keyFile)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(key, storage.APIKeyPrefix) {
		return userError("invalid_api_key", "that is not an API key (they start with "+storage.APIKeyPrefix+")",
			"Use the key printed by gs auth keys create. A browser login token is not an agent key.")
	}
	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		serverAddr = defaultServerAddr()
	}
	cfg := UserConfig{ServerAddr: serverAddr, Token: key}
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Check the key before it replaces the working credential.
	status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return userError("invalid_api_key", "the server does not accept that key",
				"It may be mistyped, revoked, or from another server (check --server). Ask the agent's owner for a new one: gs auth keys create --agent <username>.")
		}
		return err
	}
	cfg.SubjectID = status.GetSubjectId()
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	account := ""
	if accounts := status.GetAccounts(); len(accounts) > 0 {
		account = accounts[0]
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"server_addr": serverAddr, "subject_id": cfg.SubjectID, "account": account})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "signed in as %s (account %s); key saved to %s\n", cfg.SubjectID, account, r.userConfigPath())
	return nil
}

// readAPIKeyInput reads the key from a file, or from stdin: hidden when stdin
// is a terminal, else the whole stream.
func (r Runner) readAPIKeyInput(keyFile string) (string, error) {
	var raw []byte
	var err error
	switch {
	case strings.TrimSpace(keyFile) != "":
		raw, err = os.ReadFile(keyFile)
	case isTerminal(r.stdin()):
		fmt.Fprint(r.stderr(), "API key (input is hidden): ")
		raw, err = term.ReadPassword(int(r.stdin().(*os.File).Fd()))
		fmt.Fprintln(r.stderr())
	default:
		raw, err = io.ReadAll(io.LimitReader(r.stdin(), 4096))
	}
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", userError("api_key_required", "no API key was given",
			`Pipe it in (printf %s "$KEY" | gs auth use-key), pass --key-file, or run gs auth use-key in a terminal and paste it.`)
	}
	return key, nil
}

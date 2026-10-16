package cli

import (
	"context"
	"fmt"
	"io"
	"os"
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
	keysCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API key for an agent and print it once",
		Long: `Create a new API key for an agent. The key is printed on stdout and never
again, so capture it:  KEY=$(gs auth keys create --agent bot)  Everything else
goes to stderr. With --json the object carries the key as "secret".`,
		Args: noArgs("gs auth keys create [--agent username] [--name label]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthKeysCreate(cmd.Context(), *opts, createAgent, createName)
		},
	}
	keysCreateCmd.Flags().StringVar(&createAgent, "agent", "", "the agent's account name; omit to create a key for yourself")
	keysCreateCmd.Flags().StringVar(&createName, "name", "", "a label for the key, up to 64 characters (default \"key\")")
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

type apiKeyOutput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	Active     bool   `json:"active"`
}

func apiKeyToOutput(key *corev1.ApiKey) apiKeyOutput {
	out := apiKeyOutput{
		ID: key.GetId(), Name: key.GetName(), CreatedAt: key.GetCreatedAt(),
		LastUsedAt: key.GetLastUsedAt(), ExpiresAt: key.GetExpiresAt(), RevokedAt: key.GetRevokedAt(),
	}
	out.Active = out.RevokedAt == ""
	if out.Active && out.ExpiresAt != "" {
		if expires, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil && !time.Now().Before(expires) {
			out.Active = false
		}
	}
	return out
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
	if len(keys) == 0 {
		fmt.Fprintf(r.Stdout, "agent %s has no API keys\n", resp.Account)
		return nil
	}
	fmt.Fprintf(r.Stdout, "API keys of agent %s:\n", resp.Account)
	w := tabwriter.NewWriter(r.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tNAME\tCREATED\tLAST USED\tSTATE")
	for _, key := range keys {
		lastUsed, state := "never", "active"
		if key.LastUsedAt != "" {
			lastUsed = apiKeyDay(key.LastUsedAt)
		}
		switch {
		case key.RevokedAt != "":
			state = "revoked " + apiKeyDay(key.RevokedAt)
		case !key.Active:
			state = "expired " + apiKeyDay(key.ExpiresAt)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", key.ID, key.Name, apiKeyDay(key.CreatedAt), lastUsed, state)
	}
	return w.Flush()
}

func (r Runner) runAuthKeysCreate(ctx context.Context, opts commandOptions, agent, name string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).CreateApiKey(callCtx, &corev1.CreateApiKeyRequest{Agent: agent, Name: name})
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
		fmt.Fprintf(r.stderr(), "created API key %s (%s) for agent %s. It is shown once; the server keeps only a hash.\n", resp.Key.GetId(), resp.Key.GetName(), resp.Account)
		fmt.Fprintf(r.stderr(), "Give it to the agent privately, then on the agent: gs auth use-key\n")
		fmt.Fprintf(r.stderr(), "Revoke the key you are replacing: gs auth keys revoke <key-id> --agent %s\n", resp.Account)
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

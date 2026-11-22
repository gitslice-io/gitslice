package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type createAgentInviteOutput struct {
	InviteID          string `json:"invite_id"`
	Token             string `json:"token"`
	Slice             string `json:"slice,omitempty"`
	Role              string `json:"role,omitempty"`
	SuggestedUsername string `json:"suggested_username,omitempty"`
	ExpiresAt         string `json:"expires_at"`
	JoinCommand       string `json:"join_command"`
}

type joinAgentInviteOutput struct {
	ServerAddr     string `json:"server_addr"`
	AgentSubjectID string `json:"agent_subject_id"`
	Account        string `json:"account"`
	Slice          string `json:"slice,omitempty"`
	Role           string `json:"role,omitempty"`
}

func (r Runner) authInviteCommands(opts *commandOptions) []*cobra.Command {
	var (
		createSlice    string
		createRole     string
		createUsername string
		createExpires  time.Duration
		createWebURL   string
	)
	createExpires = 24 * time.Hour
	createWebURL = defaultWebURL()
	createCmd := &cobra.Command{
		Use:   "create-agent-invite",
		Short: "Mint a single-use join token (gsi_...) to onboard an agent in one command",
		Long: `Create a short-lived, single-use join token ("gsi_...") that a coding
agent can pass to 'gs auth join' or 'gs onboard --join' to register itself,
link ownership to your account, and automatically receive access to a slice.`,
		Args: noArgs("gs auth create-agent-invite [--slice account/slice] [--role contributor|writer|reader|admin] [--username NAME] [--expires 24h]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthCreateAgentInvite(cmd.Context(), *opts, createSlice, createRole, createUsername, createExpires, createWebURL)
		},
	}
	createCmd.Flags().StringVar(&createSlice, "slice", "", "slice to grant the joining agent access to (account/slice)")
	createCmd.Flags().StringVar(&createRole, "role", "", "role on --slice: reader, contributor (default), writer, or admin")
	createCmd.Flags().StringVar(&createUsername, "username", "", "optional suggested username for the joining agent")
	createCmd.Flags().DurationVar(&createExpires, "expires", createExpires, "invite token lifetime (default 24h, max 168h)")
	createCmd.Flags().StringVar(&createWebURL, "web-url", createWebURL, "web app base URL")
	withSchemaOutput[createAgentInviteOutput](createCmd)

	listCmd := &cobra.Command{
		Use:   "list-agent-invites",
		Short: "List agent join invites you created",
		Args:  noArgs("gs auth list-agent-invites [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthListAgentInvites(cmd.Context(), *opts)
		},
	}

	revokeCmd := &cobra.Command{
		Use:   "revoke-agent-invite <invite-id>",
		Short: "Revoke an unused agent join invite",
		Args:  exactArgs(1, "gs auth revoke-agent-invite <invite-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthRevokeAgentInvite(cmd.Context(), *opts, args[0])
		},
	}

	joinServer := defaultServerAddr()
	joinTokenFlag := ""
	joinUsername := ""
	joinDisplayName := ""
	joinCmd := &cobra.Command{
		Use:   "join [token]",
		Short: "Join Gitslice as an agent using a single-use invite token (gsi_...)",
		Long: `Redeem a single-use human-minted join token ("gsi_...") to register this
agent, link co-ownership to the inviting human, grant any invited slice access,
and save the resulting API key as the CLI credential.

Pass the token as an argument, via --token - (reads from stdin), or in the
GS_JOIN_TOKEN environment variable.`,
		Args: maxArgs(1, "gs auth join [token] [--token -] [--username NAME] [--display-name NAME] [--server addr]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := joinTokenFlag
			if len(args) > 0 {
				if raw != "" {
					return userError("invalid_args", "pass the invite token either as an argument or with --token, not both", "")
				}
				raw = args[0]
			}
			return r.runAuthJoinInvite(cmd.Context(), *opts, joinAgentInviteOptions{
				serverAddr:  joinServer,
				token:       raw,
				username:    joinUsername,
				displayName: joinDisplayName,
			})
		},
	}
	joinCmd.Flags().StringVar(&joinServer, "server", joinServer, "server gRPC address")
	joinCmd.Flags().StringVar(&joinTokenFlag, "token", "", "invite token (or '-' to read from stdin; also honors GS_JOIN_TOKEN)")
	joinCmd.Flags().StringVar(&joinUsername, "username", "", "desired agent username (optional; uses invite suggestion or auto-generates)")
	joinCmd.Flags().StringVar(&joinDisplayName, "display-name", "", "human-readable display name")
	withSchemaOutput[joinAgentInviteOutput](joinCmd)

	return []*cobra.Command{createCmd, listCmd, revokeCmd, joinCmd}
}

type joinAgentInviteOptions struct {
	serverAddr  string
	token       string
	username    string
	displayName string
}

func (r Runner) runAuthCreateAgentInvite(ctx context.Context, opts commandOptions, sliceInput, role, username string, expires time.Duration, _ string) error {
	if expires <= 0 {
		return userError("invalid_args", "--expires must be positive", "Pass a duration such as --expires 24h.")
	}
	var resp *corev1.CreateAgentInviteResponse
	if err := r.withSession(ctx, func(s *Session) error {
		req := &corev1.CreateAgentInviteRequest{
			Role:              strings.TrimSpace(role),
			SuggestedUsername: strings.TrimSpace(username),
			ExpiresInSeconds:  int64(expires.Seconds()),
		}
		if strings.TrimSpace(sliceInput) != "" {
			ref, err := r.resolveSliceRefInput(s.Ctx(), s.Config(), s.Conn(), sliceInput)
			if err != nil {
				return err
			}
			req.Slice = ref
		}
		var err error
		resp, err = s.Auth().CreateAgentInvite(s.Ctx(), req)
		return err
	}); err != nil {
		return err
	}
	inv := resp.GetInvite()
	sliceStr := ""
	if inv.GetSlice().GetAccount() != "" && inv.GetSlice().GetSlice() != "" {
		sliceStr = inv.GetSlice().GetAccount() + "/" + inv.GetSlice().GetSlice()
	}
	joinCmd := "gs auth join " + resp.GetToken()
	if inv.GetSuggestedUsername() != "" {
		joinCmd += " --username " + inv.GetSuggestedUsername()
	}
	out := createAgentInviteOutput{
		InviteID:          inv.GetId(),
		Token:             resp.GetToken(),
		Slice:             sliceStr,
		Role:              inv.GetRole(),
		SuggestedUsername: inv.GetSuggestedUsername(),
		ExpiresAt:         inv.GetExpiresAt(),
		JoinCommand:       joinCmd,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		fmt.Fprintln(r.Stdout, out.Token)
		return nil
	}
	fmt.Fprintf(r.Stdout, "created single-use agent invite %s (expires %s)\n", out.InviteID, out.ExpiresAt)
	if sliceStr != "" {
		fmt.Fprintf(r.Stdout, "slice access: %s (%s)\n", sliceStr, out.Role)
	}
	fmt.Fprintf(r.Stdout, "\nGive this command to your agent (token shown once):\n  %s\n", out.JoinCommand)
	return nil
}

func (r Runner) runAuthListAgentInvites(ctx context.Context, opts commandOptions) error {
	var resp *corev1.ListAgentInvitesResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Auth().ListAgentInvites(s.Ctx(), &corev1.ListAgentInvitesRequest{})
		return err
	}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, resp.GetInvites())
	}
	if len(resp.GetInvites()) == 0 {
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no agent invites")
		}
		return nil
	}
	for _, inv := range resp.GetInvites() {
		state := "active"
		if inv.GetConsumedAt() != "" {
			state = "used:" + inv.GetConsumedByAccount()
		} else if inv.GetRevokedAt() != "" {
			state = "revoked"
		}
		sliceStr := "-"
		if inv.GetSlice().GetAccount() != "" {
			sliceStr = inv.GetSlice().GetAccount() + "/" + inv.GetSlice().GetSlice() + " (" + inv.GetRole() + ")"
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\texpires %s\n", inv.GetId(), state, sliceStr, inv.GetExpiresAt())
	}
	return nil
}

func (r Runner) runAuthRevokeAgentInvite(ctx context.Context, opts commandOptions, inviteID string) error {
	inviteID = strings.TrimSpace(inviteID)
	if err := r.withSession(ctx, func(s *Session) error {
		_, err := s.Auth().RevokeAgentInvite(s.Ctx(), &corev1.RevokeAgentInviteRequest{InviteId: inviteID})
		return err
	}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"invite_id": inviteID, "revoked": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "revoked agent invite %s\n", inviteID)
	}
	return nil
}

func (r Runner) runAuthJoinInvite(ctx context.Context, opts commandOptions, in joinAgentInviteOptions) error {
	out, err := r.joinAgentInvite(ctx, in)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "joined Gitslice as agent %s (account %s); API key saved to %s\n", out.AgentSubjectID, out.Account, r.userConfigPath())
	if out.Slice != "" {
		fmt.Fprintf(r.Stdout, "granted %s access to %s\n\nStart working:\n  gs init %s\n", out.Role, out.Slice, out.Slice)
	} else {
		fmt.Fprintf(r.Stdout, "\nStart working in your home slice:\n  gs init %s/home\n", out.Account)
	}
	return nil
}

func (r Runner) joinAgentInvite(ctx context.Context, in joinAgentInviteOptions) (joinAgentInviteOutput, error) {
	token := strings.TrimSpace(in.token)
	if token == "-" {
		if r.Stdin == nil {
			return joinAgentInviteOutput{}, userError("invalid_args", "no stdin available to read invite token", "Pass the token via GS_JOIN_TOKEN or as an argument.")
		}
		raw, err := io.ReadAll(io.LimitReader(r.Stdin, 4096))
		if err != nil {
			return joinAgentInviteOutput{}, err
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		token = strings.TrimSpace(r.Getenv("GS_JOIN_TOKEN"))
	}
	if token == "" {
		return joinAgentInviteOutput{}, userError("invalid_args", "an invite token (gsi_...) is required", "Ask your human owner for a join token via 'gs auth create-agent-invite' or the Gitslice web UI (Connect an Agent).")
	}
	serverAddr := strings.TrimSpace(in.serverAddr)
	if serverAddr == "" {
		serverAddr = defaultServerAddr()
	}
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return joinAgentInviteOutput{}, err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).JoinAgentInvite(ctx, &corev1.JoinAgentInviteRequest{
		Token:       token,
		Username:    strings.TrimSpace(in.username),
		DisplayName: strings.TrimSpace(in.displayName),
	})
	if err != nil {
		switch grpcstatus.Code(err) {
		case codes.Unauthenticated:
			return joinAgentInviteOutput{}, userError("invalid_invite", grpcstatus.Convert(err).Message(), "Ask your human owner to generate a fresh join token (tokens are single-use and expire).")
		case codes.AlreadyExists, codes.FailedPrecondition:
			return joinAgentInviteOutput{}, userError("join_failed", grpcstatus.Convert(err).Message(), "Try again with a different --username.")
		case codes.ResourceExhausted:
			return joinAgentInviteOutput{}, userError("rate_limited", grpcstatus.Convert(err).Message(), "Too many agent join attempts from this address; try again later.")
		}
		return joinAgentInviteOutput{}, err
	}
	if strings.TrimSpace(resp.GetApiKey()) == "" || strings.TrimSpace(resp.GetAgentSubjectId()) == "" {
		return joinAgentInviteOutput{}, userError("join_failed", "server completed join without returning an API key", "")
	}
	cfg := UserConfig{ServerAddr: serverAddr, Token: resp.GetApiKey(), SubjectID: resp.GetAgentSubjectId()}
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return joinAgentInviteOutput{}, err
	}
	sliceStr := ""
	if resp.GetSlice().GetAccount() != "" && resp.GetSlice().GetSlice() != "" {
		sliceStr = resp.GetSlice().GetAccount() + "/" + resp.GetSlice().GetSlice()
	}
	return joinAgentInviteOutput{
		ServerAddr:     serverAddr,
		AgentSubjectID: resp.GetAgentSubjectId(),
		Account:        resp.GetAccount(),
		Slice:          sliceStr,
		Role:           resp.GetRole(),
	}, nil
}

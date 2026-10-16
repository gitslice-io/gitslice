package cli

import (
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/auth/servicetoken"
)

// authCommand builds gs auth and its subcommands: login, status, token, logout, register-agent and the service-key tools.
func (r Runner) authCommand(opts *commandOptions) *cobra.Command {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with a Gitslice server",
		RunE:  requireSubcommand("auth"),
	}
	loginServer := defaultServerAddr()
	loginClerkToken := ""
	loginClerkBrowser := false
	loginWebURL := defaultWebURL()
	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a Gitslice server (Clerk browser flow by default)",
		Args:  noArgs("gs auth login [--server addr] [--clerk | --clerk-token JWT]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthLogin(cmd.Context(), *opts, authLoginOptions{
				serverAddr:   loginServer,
				clerkToken:   loginClerkToken,
				clerkBrowser: loginClerkBrowser,
				webURL:       loginWebURL,
			})
		},
	}
	loginCmd.Flags().StringVar(&loginServer, "server", loginServer, "server gRPC address")
	loginCmd.Flags().StringVar(&loginClerkToken, "clerk-token", loginClerkToken, "log in with a Clerk session JWT (use '-' to read from stdin)")
	loginCmd.Flags().BoolVar(&loginClerkBrowser, "clerk", loginClerkBrowser, "log in via the browser-based Clerk flow")
	loginCmd.Flags().StringVar(&loginWebURL, "web-url", loginWebURL, "web base URL for the --clerk browser flow")
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
	registerServer := defaultServerAddr()
	registerWebURL := defaultWebURL()
	registerUsername := ""
	registerEmail := ""
	registerDisplayName := ""
	authRegisterAgentCmd := &cobra.Command{
		Use:   "register-agent",
		Short: "Register this agent with its own account and API key (no browser)",
		Long: `Register this agent with its own personal account, home slice, and a
long-lived API key, without a browser or a human. The key is saved as the CLI
credential and never printed.

Username rules: 4-63 characters; lowercase letters, digits and '-' (uppercase
is lowered and '_' becomes '-'); must not start or end with '-'; must be
unique across Gitslice; some names are reserved.

--email names the human who may later claim co-ownership. They sign in at
<web-url>/claims with that (verified) email and accept the agent.

Safe to retry: an interrupted registration is remembered and the same command
resumes it instead of failing because the username is now taken.`,
		Args: noArgs("gs auth register-agent --username NAME --email OWNER_EMAIL [--display-name NAME] [--server addr]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthRegisterAgent(cmd.Context(), *opts, registerAgentOptions{
				serverAddr:  registerServer,
				webURL:      registerWebURL,
				username:    registerUsername,
				email:       registerEmail,
				displayName: registerDisplayName,
			})
		},
	}
	authRegisterAgentCmd.Flags().StringVar(&registerServer, "server", registerServer, "server gRPC address")
	authRegisterAgentCmd.Flags().StringVar(&registerWebURL, "web-url", registerWebURL, "web app base URL used in the printed claim link")
	authRegisterAgentCmd.Flags().StringVar(&registerUsername, "username", "", "agent account username: 4-63 chars, lowercase letters, digits, '-'; unique")
	authRegisterAgentCmd.Flags().StringVar(&registerEmail, "email", "", "email of the human who may later claim co-ownership")
	authRegisterAgentCmd.Flags().StringVar(&registerDisplayName, "display-name", "", "human-readable agent name (defaults to the username)")
	genKeyOut := ""
	genServiceKeyCmd := &cobra.Command{
		Use:   "gen-service-key",
		Short: "Generate an Ed25519 service-token keypair (testing/service accounts)",
		Args:  noArgs("gs auth gen-service-key [--out-dir DIR]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGenServiceKey(*opts, genKeyOut)
		},
	}
	genServiceKeyCmd.Flags().StringVar(&genKeyOut, "out-dir", "", "write service-key.pem and service-key.pub.pem to this directory instead of stdout")

	mintKeyPath := ""
	mintSubject := ""
	mintEmail := ""
	mintIssuer := servicetoken.DefaultIssuer
	mintTTL := 24 * time.Hour
	mintServiceTokenCmd := &cobra.Command{
		Use:   "mint-service-token",
		Short: "Mint a signed service token to authenticate without Clerk (testing)",
		Args:  noArgs("gs auth mint-service-token --key PRIV.pem --subject ID [--email E] [--issuer I] [--ttl 24h]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMintServiceToken(*opts, mintKeyPath, mintSubject, mintEmail, mintIssuer, mintTTL)
		},
	}
	mintServiceTokenCmd.Flags().StringVar(&mintKeyPath, "key", "", "path to the Ed25519 private key PEM (required)")
	mintServiceTokenCmd.Flags().StringVar(&mintSubject, "subject", "", "subject id to authenticate as (required)")
	mintServiceTokenCmd.Flags().StringVar(&mintEmail, "email", "", "optional email claim")
	mintServiceTokenCmd.Flags().StringVar(&mintIssuer, "issuer", mintIssuer, "issuer claim (must match server GITSLICE_SERVICE_JWT_ISSUER)")
	mintServiceTokenCmd.Flags().DurationVar(&mintTTL, "ttl", mintTTL, "token lifetime")

	authCmd.AddCommand(loginCmd, authStatusCmd, authTokenCmd, authLogoutCmd, authRegisterAgentCmd, genServiceKeyCmd, mintServiceTokenCmd)
	authCmd.AddCommand(r.authKeysCommands(opts)...)

	return authCmd
}

// claimsCommand builds gs claims, which lists and accepts the agents waiting for a human owner.
func (r Runner) claimsCommand(opts *commandOptions) *cobra.Command {
	claimsCmd := &cobra.Command{
		Use:   "claims",
		Short: "Claim co-ownership of agents registered with your email",
		RunE:  requireSubcommand("claims"),
	}
	claimsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List agent accounts waiting for you to claim them",
		Args:  noArgs("gs claims list [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runClaimsList(cmd.Context(), *opts)
		},
	}
	claimsAcceptCmd := &cobra.Command{
		Use:   "accept <agent-subject-id>",
		Short: "Become a co-owner of an agent's account",
		Args:  exactArgs(1, "gs claims accept <agent-subject-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runClaimsAccept(cmd.Context(), *opts, args[0])
		},
	}
	claimsCmd.AddCommand(claimsListCmd, claimsAcceptCmd)

	return claimsCmd
}

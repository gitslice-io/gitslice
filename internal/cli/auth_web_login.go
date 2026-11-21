package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	defaultWebLoginName    = "web-session"
	defaultWebLoginExpires = "24h"
	webLoginKeyPurpose     = "web"
)

var openWebLoginBrowser = openBrowserURL

type authWebLoginOptions struct {
	Agent   string
	Name    string
	Slices  []string
	MaxRole string
	Expires string
	Path    string
	WebURL  string
	Open    bool
}

type authWebLoginOutput struct {
	Account        string       `json:"account"`
	AgentSubjectID string       `json:"agent_subject_id"`
	Key            apiKeyOutput `json:"key"`
	ExpiresAt      string       `json:"expires_at,omitempty"`
	URL            string       `json:"url"`
	RevokeCommand  string       `json:"revoke_command"`
}

func (r Runner) authWebLoginCommand(opts *commandOptions) *cobra.Command {
	in := authWebLoginOptions{
		Name:    defaultWebLoginName,
		Expires: defaultWebLoginExpires,
		WebURL:  defaultWebURL(),
	}
	cmd := &cobra.Command{
		Use:   "web-login",
		Short: "Mint a short-lived browser sign-in link for an agent",
		Long: `Mint a short-lived, revocable API key (purpose "web") and print a browser
sign-in URL that sets an HttpOnly web session cookie for the agent.

The credential travels in the URL fragment (#key=...) so it is never sent in
HTTP requests, server logs, or Referer headers, and is stripped from browser
history as soon as the page loads.

The sign-in URL is printed alone on stdout so scripts can capture it; session
details and the revoke command go to stderr.

  gs auth web-login
  gs auth web-login --expires 2h --slice acme/payments --role reader
  gs auth web-login --agent release-bot --open`,
		Args: noArgs("gs auth web-login [--agent username] [--expires 24h] [--name label] [--slice account/slice]... [--role reader|contributor|writer] [--path /...] [--web-url url] [--open]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAuthWebLogin(cmd.Context(), *opts, in)
		},
	}
	cmd.Flags().StringVar(&in.Agent, "agent", "", "agent account name (for owners creating a web link for an agent); omit for yourself")
	cmd.Flags().StringVar(&in.Name, "name", in.Name, "label for the web session API key")
	cmd.Flags().StringArrayVar(&in.Slices, "slice", nil, "limit the web session to a slice (<account>/<slice>); repeatable")
	cmd.Flags().StringVar(&in.MaxRole, "role", "", "cap maximum role in the web session to reader, contributor, or writer")
	cmd.Flags().StringVar(&in.Expires, "expires", in.Expires, "web session lifetime as a duration (e.g. 1h, 24h, or 7d)")
	cmd.Flags().StringVar(&in.Path, "path", "", "relative web path to open after signing in (e.g. /slices/acme/app)")
	cmd.Flags().StringVar(&in.WebURL, "web-url", in.WebURL, "web app base URL")
	cmd.Flags().BoolVar(&in.Open, "open", false, "open the sign-in URL in the local browser")
	withSchemaOutput[authWebLoginOutput](cmd)
	return cmd
}

func validateWebLoginPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\r\n\x00") {
		return "", userError(
			"invalid_path",
			fmt.Sprintf("invalid --path %q: must be a relative path starting with '/'", raw),
			"Pass a Gitslice web path such as /slices/acme/app or /accounts/my-agent.",
		)
	}
	return path, nil
}

func (r Runner) runAuthWebLogin(ctx context.Context, opts commandOptions, in authWebLoginOptions) error {
	expires := strings.TrimSpace(in.Expires)
	if expires == "" {
		expires = defaultWebLoginExpires
	}
	expiresInSeconds, err := parseAPIKeyDuration(expires)
	if err != nil {
		return err
	}
	maxRole, err := model.NormalizeAPIKeyMaxRole(in.MaxRole)
	if err != nil {
		return userError("invalid_role", err.Error(), "Use reader, contributor, or writer.")
	}
	returnPath, err := validateWebLoginPath(in.Path)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = defaultWebLoginName
	}

	var resp *corev1.CreateApiKeyResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Auth().CreateApiKey(s.Ctx(), &corev1.CreateApiKeyRequest{
			Agent:            in.Agent,
			Name:             name,
			Slices:           in.Slices,
			MaxRole:          maxRole,
			ExpiresInSeconds: expiresInSeconds,
			Purpose:          webLoginKeyPurpose,
		})
		return authKeysError(err)
	}); err != nil {
		return err
	}

	route := "agent-login"
	if returnPath != "" {
		route += "?returnTo=" + url.QueryEscape(returnPath)
	}
	baseLoginURL, err := webRouteURL(in.WebURL, route)
	if err != nil {
		return err
	}
	loginURL := baseLoginURL + "#key=" + url.QueryEscape(resp.Secret)

	keyOut := apiKeyToOutput(resp.Key)
	revokeCmd := fmt.Sprintf("gs auth keys revoke %s", keyOut.ID)
	if strings.TrimSpace(in.Agent) != "" {
		revokeCmd += " --agent " + strings.TrimSpace(in.Agent)
	}

	if in.Open {
		_ = openWebLoginBrowser(loginURL)
	}

	out := authWebLoginOutput{
		Account:        resp.Account,
		AgentSubjectID: resp.AgentSubjectId,
		Key:            keyOut,
		ExpiresAt:      keyOut.ExpiresAt,
		URL:            loginURL,
		RevokeCommand:  revokeCmd,
	}
	return writeSchemaOutput[authWebLoginOutput](r, opts, out, func() error {
		fmt.Fprintln(r.stdout(), loginURL)
		if !opts.Quiet {
			owner := resp.Account
			if owner == "" {
				owner = resp.AgentSubjectId
			}
			if keyOut.ExpiresAt != "" {
				fmt.Fprintf(r.stderr(), "web session key %s for %s (expires %s)\n", keyOut.ID, owner, keyOut.ExpiresAt)
			} else {
				fmt.Fprintf(r.stderr(), "web session key %s for %s\n", keyOut.ID, owner)
			}
			fmt.Fprintf(r.stderr(), "treat this URL as a secret; sign out in the browser or run: %s\n", revokeCmd)
		}
		return nil
	})
}

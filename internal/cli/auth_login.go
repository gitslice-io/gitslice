package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type authLoginOptions struct {
	serverAddr   string
	clerkToken   string
	clerkBrowser bool
	webURL       string
}

// runAuthLogin dispatches to the selected login mode: a Clerk session JWT
// (--clerk-token, or '-' for stdin) or the default browser-based Clerk flow.
func (r Runner) runAuthLogin(ctx context.Context, opts commandOptions, in authLoginOptions) error {
	switch {
	case in.clerkToken != "":
		token := in.clerkToken
		if token == "-" {
			raw, err := io.ReadAll(r.stdin())
			if err != nil {
				return err
			}
			token = string(raw)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return userError("invalid_args", "clerk token is empty", "Pass a Clerk session JWT to --clerk-token, or '-' to read it from stdin.")
		}
		return r.finishClerkLogin(ctx, opts, in.serverAddr, token)
	default:
		return r.runAuthLoginClerkBrowser(ctx, opts, in.serverAddr, in.webURL)
	}
}

// finishClerkLogin stores a Clerk session JWT as the bearer credential and
// verifies it by calling AuthService.GetAuthStatus, which runs the server-side
// Clerk verification and returns the provisioned subject ID.
func (r Runner) finishClerkLogin(ctx context.Context, opts commandOptions, serverAddr, token string) error {
	subjectID, err := r.verifyToken(ctx, serverAddr, token)
	if err != nil {
		return userError("clerk_login_failed", fmt.Sprintf("token was not accepted by %s: %v", serverAddr, err), "Check that the token is a current Clerk session JWT and that the server runs AUTH_PROVIDER=clerk.")
	}
	return r.persistAndReportLogin(opts, UserConfig{ServerAddr: serverAddr, Token: token, SubjectID: subjectID})
}

func (r Runner) runAuthLoginClerkBrowser(ctx context.Context, opts commandOptions, serverAddr, webURL string) error {
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	authClient := corev1.NewAuthServiceClient(conn)
	start, err := authClient.StartCliLogin(ctx, &corev1.StartCliLoginRequest{})
	if err != nil {
		return err
	}
	code := strings.TrimSpace(start.Code)
	if code == "" {
		return userError("clerk_login_failed", "server did not return a CLI login code", "Try gs auth login --clerk again.")
	}
	webURL = strings.TrimSpace(webURL)
	if webURL == "" {
		webURL = defaultWebURL()
	}
	if !strings.Contains(webURL, "://") {
		webURL = "http://" + webURL
	}
	loginURL := strings.TrimRight(webURL, "/") + "/cli-login?code=" + url.QueryEscape(code)

	promptWriter := r.stdout()
	if opts.jsonOutput() {
		promptWriter = r.stderr()
	}
	if !opts.Quiet {
		fmt.Fprintln(promptWriter, "Open this URL on any device to sign in:")
		fmt.Fprintln(promptWriter, loginURL)
	}
	_ = openBrowserURL(loginURL)
	if !opts.Quiet {
		fmt.Fprintln(promptWriter, "Waiting for sign-in...")
	}

	deadline := time.Now().UTC().Add(10 * time.Minute)
	if strings.TrimSpace(start.ExpiresAt) != "" {
		if expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(start.ExpiresAt)); err == nil {
			deadline = expiresAt
		}
	}
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	pollInterval := time.Duration(start.PollIntervalSeconds) * time.Second
	if pollInterval < time.Second {
		pollInterval = time.Second
	}

	for {
		poll, err := authClient.PollCliLogin(waitCtx, &corev1.PollCliLoginRequest{Code: code})
		if err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return userError("cli_login_expired", "CLI login session expired", "Run gs auth login again to start a new sign-in.")
			}
			return err
		}
		switch poll.Status {
		case "approved":
			if strings.TrimSpace(poll.Token) == "" || strings.TrimSpace(poll.SubjectId) == "" {
				return userError("clerk_login_failed", "server approved CLI login without a session token", "Try gs auth login --clerk again.")
			}
			return r.persistAndReportLogin(opts, UserConfig{ServerAddr: serverAddr, Token: poll.Token, SubjectID: poll.SubjectId})
		case "expired":
			return userError("cli_login_expired", "CLI login session expired", "Run gs auth login again to start a new sign-in.")
		case "pending":
		default:
			return userError("clerk_login_failed", "server returned unknown CLI login status: "+poll.Status, "Try gs auth login --clerk again.")
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-timer.C:
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return userError("cli_login_expired", "CLI login session expired", "Run gs auth login again to start a new sign-in.")
			}
			return waitCtx.Err()
		}
	}
}

// verifyToken dials serverAddr and calls AuthService.GetAuthStatus using token as
// the bearer credential, returning the authenticated subject ID.
func (r Runner) verifyToken(ctx context.Context, serverAddr, token string) (string, error) {
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, UserConfig{Token: token}), &corev1.GetAuthStatusRequest{})
	if err != nil {
		return "", err
	}
	return res.SubjectId, nil
}

func (r Runner) persistAndReportLogin(opts commandOptions, cfg UserConfig) error {
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"server_addr": cfg.ServerAddr,
			"subject_id":  cfg.SubjectID,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "logged in as %s\n", cfg.SubjectID)
	return nil
}

type clerkLoginCallbackResult struct {
	Token string
	Error string
}

func clerkLoginCallbackHandler(expectedState string, resultCh chan<- clerkLoginCallbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/callback" {
			http.NotFound(w, req)
			return
		}
		values := req.URL.Query()
		if values.Get("state") != expectedState {
			http.Error(w, "invalid login state", http.StatusBadRequest)
			return
		}
		result := clerkLoginCallbackResult{
			Token: values.Get("token"),
			Error: values.Get("error"),
		}
		if result.Error == "" && result.Token == "" {
			result.Error = "clerk login callback did not include a token"
		}
		if result.Error == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintln(w, "<!doctype html><title>Gitslice Login Complete</title><p>Login complete. Return to your terminal.</p>")
		} else {
			http.Error(w, result.Error, http.StatusBadRequest)
		}
		select {
		case resultCh <- result:
		default:
		}
	})
}

// clerkLoginURL builds the web sign-in handoff URL for the browser Clerk flow.
// The web app is expected to sign the user in via Clerk and redirect to
// callbackURL with ?state=<state>&token=<clerk session jwt> (or ?error=...).
func clerkLoginURL(webURL, callbackURL, state string) (string, error) {
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
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/cli-login"
	query := parsed.Query()
	query.Set("callback_url", callbackURL)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

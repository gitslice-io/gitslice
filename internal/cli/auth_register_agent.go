package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/usernames"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type registerAgentOptions struct {
	serverAddr     string
	webURL         string
	username       string
	email          string
	displayName    string
	requestedSlice string
	requestedRole  string
}

type registerAgentOutput struct {
	ServerAddr        string `json:"server_addr"`
	SubjectID         string `json:"subject_id"`
	Account           string `json:"account"`
	OwnerEmail        string `json:"owner_email"`
	ClaimURL          string `json:"claim_url"`
	ClaimInstructions string `json:"claim_instructions"`
	RequestedSlice    string `json:"requested_slice,omitempty"`
	RequestedRole     string `json:"requested_role,omitempty"`
	Resumed           bool   `json:"resumed"`
}

// pendingAgentRegistration is persisted before RegisterAgent is called so an
// interrupted registration (timeout, cold start, lost response) can be resumed
// with the same registration token instead of failing on the taken username.
type pendingAgentRegistration struct {
	ServerAddr string `json:"server_addr"`
	Username   string `json:"username"`
	OwnerEmail string `json:"owner_email"`
	Token      string `json:"registration_token"`
	CreatedAt  string `json:"created_at"`
}

const (
	registerAgentAttempts       = 3
	registerAgentAttemptTimeout = 60 * time.Second
)

// registerAgentRetryDelay is a variable so tests can shorten it.
var registerAgentRetryDelay = 2 * time.Second

func (r Runner) pendingAgentRegistrationPath() string {
	return filepath.Join(filepath.Dir(r.userConfigPath()), "pending-agent-registration.json")
}

func (r Runner) readPendingAgentRegistration() (pendingAgentRegistration, bool) {
	raw, err := os.ReadFile(r.pendingAgentRegistrationPath())
	if err != nil {
		return pendingAgentRegistration{}, false
	}
	var pending pendingAgentRegistration
	if err := json.Unmarshal(raw, &pending); err != nil || len(pending.Token) < model.MinRegistrationTokenLength {
		return pendingAgentRegistration{}, false
	}
	return pending, true
}

func (r Runner) writePendingAgentRegistration(pending pendingAgentRegistration) error {
	return savePendingAgentRegistration(r.pendingAgentRegistrationPath(), pending)
}

func savePendingAgentRegistration(path string, pending pendingAgentRegistration) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return writeJSONFile(path, pending, 0o600)
}

func (r Runner) clearPendingAgentRegistration() {
	_ = os.Remove(r.pendingAgentRegistrationPath())
}

// runAuthRegisterAgent self-registers an agent via the unauthenticated
// AuthService.RegisterAgent RPC and saves the returned API key as the CLI's
// bearer credential, so later commands run as the agent. The key itself is never
// printed; use `gs auth token` when a script needs it.
func (r Runner) runAuthRegisterAgent(ctx context.Context, opts commandOptions, in registerAgentOptions) error {
	out, err := r.registerAgent(ctx, opts, in)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "registered agent %s with account %s; API key saved to %s\n", out.SubjectID, out.Account, r.userConfigPath())
	if out.RequestedSlice != "" {
		fmt.Fprintf(r.Stdout, "requested %s access to %s (pending owner approval)\n", out.RequestedRole, out.RequestedSlice)
	}
	fmt.Fprintf(r.Stdout, "\nTell your owner (%s) how to claim this agent:\n%s\n", out.OwnerEmail, out.ClaimInstructions)
	fmt.Fprintf(r.Stdout, "\nStart working in your home slice:\n  gs init %s:home\n", out.Account)
	return nil
}

func (r Runner) registerAgent(ctx context.Context, opts commandOptions, in registerAgentOptions) (registerAgentOutput, error) {
	if strings.TrimSpace(in.username) == "" || strings.TrimSpace(in.email) == "" {
		return registerAgentOutput{}, userError("invalid_args", "--username and --email are required", "Run gs auth register-agent --username NAME --email OWNER_EMAIL.")
	}
	// Validate locally with the server's own rules so mistakes fail fast.
	username, err := usernames.Normalize(in.username)
	if err != nil {
		return registerAgentOutput{}, userError("invalid_username", err.Error(), "Usernames are 4-63 characters of lowercase letters, digits and '-', must not start or end with '-', and must be unique. See gs auth register-agent --help.")
	}
	email, err := model.NormalizeOwnerEmail(in.email)
	if err != nil {
		return registerAgentOutput{}, userError("invalid_email", strings.TrimPrefix(err.Error(), "invalid: "), "Pass the email of the human who should be able to claim this agent, e.g. --email you@example.com.")
	}
	var requestedRef *corev1.SliceRef
	requestedRole := strings.TrimSpace(in.requestedRole)
	requestedSliceLabel := ""
	if rawSlice := strings.TrimSpace(in.requestedSlice); rawSlice != "" {
		account, sliceName, ok := parseSimpleSliceRef(rawSlice)
		if !ok {
			return registerAgentOutput{}, userError("invalid_slice", "invalid --request-slice "+rawSlice, "Use account/slice, e.g. --request-slice gitslice/gitslice.")
		}
		requestedRef = &corev1.SliceRef{Account: account, Slice: sliceName}
		requestedSliceLabel = account + "/" + sliceName
		if requestedRole == "" {
			requestedRole = "writer"
		}
	} else if requestedRole != "" {
		return registerAgentOutput{}, userError("invalid_args", "--request-role requires --request-slice", "Pass --request-slice account/slice alongside --request-role.")
	}

	serverAddr := strings.TrimSpace(in.serverAddr)
	if serverAddr == "" {
		serverAddr = defaultServerAddr()
	}

	pending, resuming := r.readPendingAgentRegistration()
	if !resuming || pending.ServerAddr != serverAddr || pending.Username != username || pending.OwnerEmail != email {
		token, err := objectid.RandomID("reg")
		if err != nil {
			return registerAgentOutput{}, err
		}
		pending = pendingAgentRegistration{
			ServerAddr: serverAddr,
			Username:   username,
			OwnerEmail: email,
			Token:      token,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		resuming = false
		if err := r.writePendingAgentRegistration(pending); err != nil {
			return registerAgentOutput{}, err
		}
	} else if !opts.Quiet {
		fmt.Fprintf(r.stderr(), "resuming an earlier registration attempt for %s\n", username)
	}

	var resp *corev1.RegisterAgentResponse
	var lastErr error
	for attempt := 1; attempt <= registerAgentAttempts; attempt++ {
		resp, lastErr = r.registerAgentOnce(ctx, serverAddr, &corev1.RegisterAgentRequest{
			Username:          username,
			OwnerEmail:        email,
			DisplayName:       in.displayName,
			RegistrationToken: pending.Token,
			RequestedSlice:    requestedRef,
			RequestedRole:     requestedRole,
		})
		if lastErr == nil || !retryableRegisterError(lastErr) || attempt == registerAgentAttempts {
			break
		}
		if !opts.Quiet {
			fmt.Fprintf(r.stderr(), "registration attempt %d did not complete (%v); retrying safely...\n", attempt, lastErr)
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-time.After(registerAgentRetryDelay):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		if retryableRegisterError(lastErr) {
			// Keep the pending token: the account may exist, and re-running the
			// command resumes it.
			return registerAgentOutput{}, userError("registration_incomplete",
				fmt.Sprintf("could not confirm the registration with %s: %v", serverAddr, lastErr),
				"The account may or may not have been created. Re-run the same command: it resumes this registration safely instead of failing on the username.")
		}
		r.clearPendingAgentRegistration()
		switch grpcstatus.Code(lastErr) {
		case codes.FailedPrecondition:
			return registerAgentOutput{}, userError("agent_signup_failed", grpcstatus.Convert(lastErr).Message(), "The username may be taken (pick another), or the server may not allow agent sign-up (GITSLICE_AGENT_SIGNUP_ENABLED).")
		case codes.InvalidArgument:
			return registerAgentOutput{}, userError("invalid_args", grpcstatus.Convert(lastErr).Message(), "Check the username and email and try again. See gs auth register-agent --help.")
		case codes.ResourceExhausted:
			return registerAgentOutput{}, userError("rate_limited", grpcstatus.Convert(lastErr).Message(), "Too many agent registrations from this address; try again later.")
		}
		return registerAgentOutput{}, lastErr
	}
	if strings.TrimSpace(resp.ApiKey) == "" || strings.TrimSpace(resp.SubjectId) == "" {
		return registerAgentOutput{}, userError("agent_signup_failed", "server registered the agent without returning an API key", "Re-run the same command to resume the registration.")
	}

	cfg := UserConfig{ServerAddr: serverAddr, Token: resp.ApiKey, SubjectID: resp.SubjectId}
	if existing, err := r.readPartialUserConfig(); err == nil {
		cfg.Aliases = existing.Aliases
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return registerAgentOutput{}, err
	}
	r.clearPendingAgentRegistration()

	claimURL, _ := webRouteURL(in.webURL, "claims")
	return registerAgentOutput{
		ServerAddr:        serverAddr,
		SubjectID:         resp.SubjectId,
		Account:           resp.Account,
		OwnerEmail:        email,
		ClaimURL:          claimURL,
		ClaimInstructions: agentClaimInstructionsWithSlice(email, claimURL, resp.SubjectId, requestedSliceLabel, requestedRole),
		RequestedSlice:    requestedSliceLabel,
		RequestedRole:     requestedRole,
		Resumed:           resuming,
	}, nil
}

func parseSimpleSliceRef(raw string) (account, slice string, ok bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "/") {
		account, slice, ok = strings.Cut(raw, "/")
	} else if strings.Contains(raw, ":") {
		account, slice, ok = strings.Cut(raw, ":")
	}
	account = strings.TrimSpace(account)
	slice = strings.TrimSpace(slice)
	if !ok || account == "" || slice == "" || strings.ContainsAny(account+slice, "/:") {
		return "", "", false
	}
	return account, slice, true
}

// agentClaimInstructions is what the agent passes on to its human owner. The
// email must match the one given at registration exactly, and it must be a
// verified email of the account the human signs in with. A person who signed
// up under another address is the usual reason a claim never shows up.
func agentClaimInstructions(email, claimURL, agentSubjectID string) string {
	return agentClaimInstructionsWithSlice(email, claimURL, agentSubjectID, "", "")
}

func agentClaimInstructionsWithSlice(email, claimURL, agentSubjectID, requestedSlice, requestedRole string) string {
	sliceLine := ""
	if requestedSlice != "" {
		sliceLine = fmt.Sprintf("\n     (Accepting will also grant %s access to %s in one click.)", requestedRole, requestedSlice)
	}
	return fmt.Sprintf("  1. Sign up or sign in at %s with an account whose verified email is exactly %s.\n"+
		"     (Already have an account under another email? Add and verify %s in your profile first;\n"+
		"     agents registered to an email can be claimed only by an account that has it verified.)\n"+
		"  2. Click Accept next to this agent, or run: gs claims accept %s%s\n"+
		"  Not showing up? gs claims list prints the emails it looked for.", claimURL, email, email, agentSubjectID, sliceLine)
}

func (r Runner) registerAgentOnce(ctx context.Context, serverAddr string, req *corev1.RegisterAgentRequest) (*corev1.RegisterAgentResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, registerAgentAttemptTimeout)
	defer cancel()
	conn, err := r.dialServer(attemptCtx, serverAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return corev1.NewAuthServiceClient(conn).RegisterAgent(attemptCtx, req)
}

// retryableRegisterError reports failures after which the registration may or
// may not have happened (timeouts, dropped connections, cold starts).
func retryableRegisterError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch grpcstatus.Code(err) {
	case codes.DeadlineExceeded, codes.Unavailable, codes.Aborted:
		return true
	}
	return false
}

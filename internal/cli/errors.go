package cli

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

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

func (r Runner) enhanceCommandError(err error) error {
	if err == nil {
		return nil
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		return err
	}
	switch grpcstatus.Code(err) {
	case codes.Unauthenticated:
		return r.unauthenticatedError(err)
	case codes.PermissionDenied:
		return r.permissionDeniedError(err)
	case codes.Unavailable:
		if explained := explainUnavailable(err); explained != nil {
			return explained
		}
	}
	return err
}

// unauthenticatedError says which of the two ways a request goes
// unauthenticated this was: no saved token (private slices need a signed-in
// member), or a token the server did not accept.
func (r Runner) unauthenticatedError(err error) error {
	cfg, _ := r.readPartialUserConfig()
	if strings.TrimSpace(cfg.Token) == "" {
		return commandError{
			Code:    "unauthenticated",
			Message: "not signed in: this needs a signed-in account (a private slice is open only to members of the account that owns it)",
			Hint:    "Sign in with gs auth login, or gs auth register-agent for an agent, then try again. If the slice belongs to an agent you own, claim co-ownership at " + claimsURL() + ". gs doctor checks the setup.",
			Cause:   err,
		}
	}
	message := grpcstatus.Convert(err).Message()
	if message == "" {
		message = "unauthenticated"
	}
	return commandError{
		Code:    "unauthenticated",
		Message: "authentication failed: " + firstNonEmpty(cfg.ServerAddr, "the server") + " did not accept the saved token (" + message + ")",
		Hint:    r.authRecoveryHint(),
		Cause:   err,
	}
}

func (r Runner) authRecoveryHint() string {
	return "The token may have expired or been revoked, or be for another server. Run gs auth status to inspect the saved token; if it is invalid, run gs auth login (or gs auth register-agent for an agent). gs doctor checks the whole setup."
}

// permissionDeniedError explains a request the server refused for the
// signed-in subject, and how to get access.
func (r Runner) permissionDeniedError(err error) error {
	cfg, _ := r.readPartialUserConfig()
	message := "permission denied"
	if subject := strings.TrimSpace(cfg.SubjectID); subject != "" {
		message += " for " + subject
	}
	if detail := grpcstatus.Convert(err).Message(); detail != "" && detail != "unauthorized" && detail != "permission denied" {
		message += ": " + detail
	}
	return commandError{
		Code:    "permission_denied",
		Message: message,
		Hint: "A private slice is open only to members of the account that owns it, and changing a slice needs write access. " +
			"If the slice belongs to an agent you own, claim co-ownership at " + claimsURL() + " (signed in with the email the agent registered with); " +
			"otherwise ask an owner of the account to share the slice with you (gs slice share <slice> <username>). gs doctor <account/slice> checks your access.",
		Cause: err,
	}
}

func claimsURL() string {
	return firstNonEmpty(webResourceURL("/claims"), "https://gitslice.io/claims")
}

func userError(code, message, hint string) error {
	return commandError{Code: code, Message: message, Hint: hint}
}

func isUserErrorCode(err error, code string) bool {
	var cmdErr commandError
	return errors.As(err, &cmdErr) && cmdErr.Code == code
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
		body.Hint = "Run gs init <slice|account:slice>."
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

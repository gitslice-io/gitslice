package cli

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func grpcStatusCommandError(err error, st *grpcstatus.Status) (commandError, bool) {
	if st == nil || st.Code() == codes.OK || st.Code() == codes.Unknown {
		return commandError{}, false
	}
	msg := st.Message()
	switch st.Code() {
	case codes.NotFound:
		return commandError{Code: "not_found", Message: firstNonEmpty(msg, "not found"), Cause: err}, true
	case codes.AlreadyExists:
		return commandError{Code: "already_exists", Message: firstNonEmpty(msg, "already exists"), Cause: err}, true
	case codes.InvalidArgument:
		return commandError{Code: "invalid_argument", Message: firstNonEmpty(msg, "invalid argument"), Cause: err}, true
	case codes.FailedPrecondition:
		return commandError{Code: "failed_precondition", Message: firstNonEmpty(msg, "failed precondition"), Cause: err}, true
	case codes.Aborted:
		return commandError{Code: "aborted", Message: firstNonEmpty(msg, "operation aborted"), Retriable: true, Cause: err}, true
	case codes.ResourceExhausted:
		return commandError{Code: "resource_exhausted", Message: firstNonEmpty(msg, "resource exhausted"), Cause: err}, true
	case codes.Unimplemented:
		return commandError{Code: "unsupported", Message: firstNonEmpty(msg, "this server does not support this operation"), Hint: "Upgrade the server.", Cause: err}, true
	case codes.DeadlineExceeded:
		return commandError{Code: "deadline_exceeded", Message: firstNonEmpty(msg, "request timed out"), Retriable: true, Cause: err}, true
	case codes.Canceled:
		return commandError{Code: "canceled", Message: firstNonEmpty(msg, "request canceled"), Cause: err}, true
	case codes.Unavailable:
		if explained := explainUnavailable(err); explained != nil {
			var cmdErr commandError
			if errors.As(explained, &cmdErr) {
				return cmdErr, true
			}
		}
		return commandError{Code: "server_unavailable", Message: firstNonEmpty(msg, "server unavailable"), Retriable: true, Cause: err}, true
	case codes.Unauthenticated:
		return commandError{Code: "unauthenticated", Message: firstNonEmpty(msg, "unauthenticated"), Cause: err}, true
	case codes.PermissionDenied:
		return commandError{Code: "permission_denied", Message: firstNonEmpty(msg, "permission denied"), Cause: err}, true
	default:
		return commandError{}, false
	}
}

func (r Runner) enhanceCommandError(err error) error {
	if err == nil {
		return nil
	}
	if cmdErr, ok := asCommandError(err); ok {
		return cmdErr
	}
	baseErr, overrides := extractDomainErrorOverrides(err)
	st, ok := grpcstatus.FromError(baseErr)
	if !ok || st.Code() == codes.OK {
		return baseErr
	}
	var mapped commandError
	switch st.Code() {
	case codes.Unauthenticated:
		mapped = r.unauthenticatedCommandError(baseErr)
	case codes.PermissionDenied:
		mapped = r.permissionDeniedCommandError(baseErr)
	case codes.Unavailable:
		if explained := explainUnavailable(baseErr); explained != nil {
			if errors.As(explained, &mapped) {
				return mapped
			}
			return explained
		}
		var matched bool
		mapped, matched = grpcStatusCommandError(baseErr, st)
		if !matched {
			return baseErr
		}
	default:
		var matched bool
		mapped, matched = grpcStatusCommandError(baseErr, st)
		if !matched {
			return baseErr
		}
	}
	return applyDomainErrorOverride(mapped, st.Code(), st.Message(), overrides)
}

// unauthenticatedError says which of the two ways a request goes
// unauthenticated this was: no saved token (private slices need a signed-in
// member), or a token the server did not accept.
func (r Runner) unauthenticatedError(err error) error {
	return r.unauthenticatedCommandError(err)
}

func (r Runner) unauthenticatedCommandError(err error) commandError {
	cfg, _ := r.readPartialUserConfig()
	if strings.TrimSpace(cfg.Token) == "" {
		return commandError{
			Code:               "unauthenticated",
			Message:            "not signed in: this needs a signed-in account (a private slice is open only to members of the account that owns it)",
			Hint:               "Sign in with gs auth login, or gs onboard --email <owner-email> (or gs auth join <token>) for an agent, then try again. If the slice belongs to an agent you own, claim co-ownership at " + claimsURL() + ". gs doctor checks the setup.",
			RemediationCommand: "gs onboard --email <owner-email> --json",
			RemediationURL:     claimsURL(),
			Cause:              err,
		}
	}
	message := grpcstatus.Convert(err).Message()
	if message == "" {
		message = "unauthenticated"
	}
	return commandError{
		Code:               "unauthenticated",
		Message:            "authentication failed: " + firstNonEmpty(cfg.ServerAddr, "the server") + " did not accept the saved token (" + message + ")",
		Hint:               r.authRecoveryHint(),
		RemediationCommand: "gs auth status",
		Cause:              err,
	}
}

func (r Runner) authRecoveryHint() string {
	return "The token may have expired or been revoked, or be for another server. Run gs auth status to inspect the saved token; if it is invalid, run gs auth login (or gs auth register-agent for an agent). gs doctor checks the whole setup."
}

// permissionDeniedError explains a request the server refused for the
// signed-in subject, and how to get access.
func (r Runner) permissionDeniedError(err error) error {
	return r.permissionDeniedCommandError(err)
}

func (r Runner) permissionDeniedCommandError(err error) commandError {
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
		RemediationCommand: "gs doctor",
		RemediationURL:     claimsURL(),
		Cause:              err,
	}
}

func claimsURL() string {
	return firstNonEmpty(webResourceURL("/claims"), "https://gitslice.io/claims")
}

func classifyError(err error) errorResponse {
	return errorResponse{Error: classifyErrorBody(err, false)}
}

func populateDefaultRemediation(body errorBody) errorBody {
	if body.RemediationCommand == "" {
		switch body.Code {
		case "not_logged_in", "unauthenticated":
			body.RemediationCommand = "gs onboard --email <owner-email> --json"
		case "not_in_workspace":
			body.RemediationCommand = "gs init <account/slice>"
		case "sync_conflicts":
			body.RemediationCommand = "gs sync"
		case "permission_denied":
			body.RemediationCommand = "gs doctor"
		}
	}
	if body.RemediationURL == "" {
		switch body.Code {
		case "permission_denied", "unauthenticated":
			body.RemediationURL = claimsURL()
		}
	}
	return body
}

func classifyErrorBody(err error, mcpMode bool) errorBody {
	defaultCode := "command_failed"
	if mcpMode {
		defaultCode = "error"
	}
	if err == nil {
		return errorBody{Code: defaultCode}
	}
	if cmdErr, ok := asCommandError(err); ok {
		return populateDefaultRemediation(errorBody{
			Code:               cmdErr.Code,
			Message:            cmdErr.Message,
			Hint:               cmdErr.Hint,
			RemediationCommand: cmdErr.RemediationCommand,
			RemediationURL:     cmdErr.RemediationURL,
			Retriable:          cmdErr.Retriable,
		})
	}
	var cmdErr commandError
	baseErr, overrides := extractDomainErrorOverrides(err)
	if explained := explainUnavailable(baseErr); explained != nil && errors.As(explained, &cmdErr) {
		return populateDefaultRemediation(errorBody{
			Code:               cmdErr.Code,
			Message:            cmdErr.Message,
			Hint:               cmdErr.Hint,
			RemediationCommand: cmdErr.RemediationCommand,
			RemediationURL:     cmdErr.RemediationURL,
			Retriable:          cmdErr.Retriable,
		})
	}
	if st, ok := grpcstatus.FromError(baseErr); ok && st.Code() != codes.OK && st.Code() != codes.Unknown {
		if mapped, matched := grpcStatusCommandError(baseErr, st); matched {
			mapped = applyDomainErrorOverride(mapped, st.Code(), st.Message(), overrides)
			code := mapped.Code
			if mcpMode && len(overrides) == 0 {
				code = strings.ToLower(st.Code().String())
			}
			return populateDefaultRemediation(errorBody{
				Code:               code,
				Message:            mapped.Message,
				Hint:               mapped.Hint,
				RemediationCommand: mapped.RemediationCommand,
				RemediationURL:     mapped.RemediationURL,
				Retriable:          mapped.Retriable,
			})
		}
		if mcpMode {
			return populateDefaultRemediation(errorBody{
				Code:    strings.ToLower(st.Code().String()),
				Message: st.Message(),
			})
		}
	}

	msg := baseErr.Error()
	body := errorBody{
		Code:      defaultCode,
		Message:   msg,
		Retriable: false,
	}
	switch {
	case strings.Contains(msg, "not logged in"):
		body.Code = "not_logged_in"
		body.Hint = "Run gs auth login or gs onboard --email <owner-email>."
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
	return populateDefaultRemediation(body)
}

// mcpToolError is an error as a tool result: a stable code, the message, and
// what to do when the CLI knows.
func mcpToolError(err error) map[string]any {
	body := classifyErrorBody(err, true)
	out := map[string]any{"code": body.Code, "message": body.Message}
	if body.Hint != "" {
		out["hint"] = body.Hint
	}
	if body.RemediationCommand != "" {
		out["remediation_command"] = body.RemediationCommand
	}
	if body.RemediationURL != "" {
		out["remediation_url"] = body.RemediationURL
	}
	if body.Retriable {
		out["retriable"] = true
	}
	return out
}

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

type userCodedError interface {
	UserError() (code, message, hint string)
}

func asCommandError(err error) (commandError, bool) {
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		return cmdErr, true
	}
	var coded userCodedError
	if errors.As(err, &coded) {
		code, msg, hint := coded.UserError()
		return commandError{Code: code, Message: msg, Hint: hint, Cause: err}, true
	}
	return commandError{}, false
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

type domainErrorOverride struct {
	Code    string
	Message string
	Hint    string
	HintFn  func(message string) string
}

type domainErrorOverrides map[codes.Code]domainErrorOverride

type domainHintedError struct {
	cause     error
	overrides domainErrorOverrides
}

func (e *domainHintedError) Error() string {
	return e.cause.Error()
}

func (e *domainHintedError) Unwrap() error {
	return e.cause
}

func withDomainErrorHints(err error, overrides domainErrorOverrides) error {
	if err == nil {
		return nil
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		return err
	}
	return &domainHintedError{cause: err, overrides: overrides}
}

func extractDomainErrorOverrides(err error) (error, domainErrorOverrides) {
	var hinted *domainHintedError
	if errors.As(err, &hinted) && hinted != nil {
		return hinted.cause, hinted.overrides
	}
	return err, nil
}

func isTransportOrAuthGRPCStatus(code codes.Code) bool {
	switch code {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

func applyDomainErrorOverride(cmdErr commandError, code codes.Code, stMsg string, overrides domainErrorOverrides) commandError {
	if len(overrides) == 0 || isTransportOrAuthGRPCStatus(code) {
		return cmdErr
	}
	ov, ok := overrides[code]
	if !ok {
		return cmdErr
	}
	if ov.Code != "" {
		cmdErr.Code = ov.Code
	}
	if ov.Message != "" {
		cmdErr.Message = ov.Message
	}
	if ov.HintFn != nil {
		if hint := ov.HintFn(stMsg); hint != "" {
			cmdErr.Hint = hint
		}
	} else if ov.Hint != "" {
		cmdErr.Hint = ov.Hint
	}
	return cmdErr
}

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

func exitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 2
	}
	if cmdErr, ok := asCommandError(err); ok {
		switch cmdErr.Code {
		case "not_logged_in", "invalid_user_config", "unauthenticated":
			return 4
		case "sync_conflicts":
			return 3
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
	cmdErr, ok := asCommandError(err)
	return ok && cmdErr.Code == code
}

func classifyError(err error) errorResponse {
	return errorResponse{Error: classifyErrorBody(err, false)}
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
		return errorBody{
			Code:      cmdErr.Code,
			Message:   cmdErr.Message,
			Hint:      cmdErr.Hint,
			Retriable: cmdErr.Retriable,
		}
	}
	var cmdErr commandError
	baseErr, overrides := extractDomainErrorOverrides(err)
	if explained := explainUnavailable(baseErr); explained != nil && errors.As(explained, &cmdErr) {
		return errorBody{
			Code:      cmdErr.Code,
			Message:   cmdErr.Message,
			Hint:      cmdErr.Hint,
			Retriable: cmdErr.Retriable,
		}
	}
	if st, ok := grpcstatus.FromError(baseErr); ok && st.Code() != codes.OK && st.Code() != codes.Unknown {
		if mapped, matched := grpcStatusCommandError(baseErr, st); matched {
			mapped = applyDomainErrorOverride(mapped, st.Code(), st.Message(), overrides)
			code := mapped.Code
			if mcpMode && len(overrides) == 0 {
				code = strings.ToLower(st.Code().String())
			}
			return errorBody{
				Code:      code,
				Message:   mapped.Message,
				Hint:      mapped.Hint,
				Retriable: mapped.Retriable,
			}
		}
		if mcpMode {
			return errorBody{
				Code:    strings.ToLower(st.Code().String()),
				Message: st.Message(),
			}
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
	return body
}

// mcpToolError is an error as a tool result: a stable code, the message, and
// what to do when the CLI knows.
func mcpToolError(err error) map[string]any {
	body := classifyErrorBody(err, true)
	out := map[string]any{"code": body.Code, "message": body.Message}
	if body.Hint != "" {
		out["hint"] = body.Hint
	}
	if body.Retriable {
		out["retriable"] = true
	}
	return out
}

package clibase

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// Error is a structured user-facing CLI error with stable machine code and hint.
type Error struct {
	Code               string
	Message            string
	Hint               string
	RemediationCommand string
	RemediationURL     string
	Retriable          bool
	Cause              error
}

func (e Error) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func (e Error) Unwrap() error {
	return e.Cause
}

// UserCodedError is implemented by domain errors that carry a CLI code/hint.
type UserCodedError interface {
	UserError() (code, message, hint string)
}

// UserError constructs a structured CLI error.
func UserError(code, message, hint string) error {
	return Error{Code: code, Message: message, Hint: hint}
}

// AsError extracts a structured Error (or UserCodedError) from err.
func AsError(err error) (Error, bool) {
	var cmdErr Error
	if errors.As(err, &cmdErr) {
		return cmdErr, true
	}
	var coded UserCodedError
	if errors.As(err, &coded) {
		code, msg, hint := coded.UserError()
		return Error{Code: code, Message: msg, Hint: hint, Cause: err}, true
	}
	return Error{}, false
}

// IsUserErrorCode reports whether err carries the given structured error code.
func IsUserErrorCode(err error, code string) bool {
	cmdErr, ok := AsError(err)
	return ok && cmdErr.Code == code
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	Hint               string `json:"hint,omitempty"`
	RemediationCommand string `json:"remediation_command,omitempty"`
	RemediationURL     string `json:"remediation_url,omitempty"`
	Retriable          bool   `json:"retriable"`
}

type DomainErrorOverride struct {
	Code    string
	Message string
	Hint    string
	HintFn  func(message string) string
}

type DomainErrorOverrides map[codes.Code]DomainErrorOverride

type domainHintedError struct {
	cause     error
	overrides DomainErrorOverrides
}

func (e *domainHintedError) Error() string {
	return e.cause.Error()
}

func (e *domainHintedError) Unwrap() error {
	return e.cause
}

func WithDomainErrorHints(err error, overrides DomainErrorOverrides) error {
	if err == nil {
		return nil
	}
	var cmdErr Error
	if errors.As(err, &cmdErr) {
		return err
	}
	return &domainHintedError{cause: err, overrides: overrides}
}

func ExtractDomainErrorOverrides(err error) (error, DomainErrorOverrides) {
	var hinted *domainHintedError
	if errors.As(err, &hinted) && hinted != nil {
		return hinted.cause, hinted.overrides
	}
	return err, nil
}

func IsTransportOrAuthGRPCStatus(code codes.Code) bool {
	switch code {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

func ApplyDomainErrorOverride(cmdErr Error, code codes.Code, stMsg string, overrides DomainErrorOverrides) Error {
	if len(overrides) == 0 || IsTransportOrAuthGRPCStatus(code) {
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

func ExitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 2
	}
	if cmdErr, ok := AsError(err); ok {
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

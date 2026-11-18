package agentd

// Error is a structured CLI-facing error with a machine-readable code and optional hint.
type Error struct {
	Code    string
	Message string
	Hint    string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// UserError implements the CLI userCodedError bridge.
func (e *Error) UserError() (code, message, hint string) {
	if e == nil {
		return "", "", ""
	}
	return e.Code, e.Message, e.Hint
}

func NewError(code, message, hint string) error {
	return &Error{
		Code:    code,
		Message: message,
		Hint:    hint,
	}
}

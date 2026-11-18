package authctx

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
)

var (
	// ErrMalformedBearerToken indicates an Authorization header that is not a Bearer token.
	ErrMalformedBearerToken = errors.New("authorization must be a bearer token")
	// ErrEmptyBearerToken indicates a Bearer Authorization header with an empty token value.
	ErrEmptyBearerToken = errors.New("empty bearer token")
)

type subjectKey struct{}
type principalKey struct{}

// Principal describes the authenticated subject for a request and any
// credential-level grant carried by the API key used to authenticate it.
type Principal struct {
	SubjectID           string
	SubjectKind         string
	ControllerSubjectID string
	KeyID               string
	HasSliceScope       bool
	SliceIDs            []string
	MaxRole             string
	Purpose             string
	DaemonID            string
	ConversationID      string
}

// AllowsSlice reports whether the principal's credential grant covers sliceID.
func (p Principal) AllowsSlice(sliceID string) bool {
	if !p.HasSliceScope {
		return true
	}
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return false
	}
	return slices.Contains(p.SliceIDs, sliceID)
}

// HasKeyGrant reports whether the principal authenticated with an API key
// carrying slice scope or role attenuation.
func (p Principal) HasKeyGrant() bool {
	return p.KeyID != "" && (p.HasSliceScope || p.MaxRole != "")
}

func clonePrincipal(p Principal) Principal {
	p.SliceIDs = slices.Clone(p.SliceIDs)
	return p
}

// WithPrincipal attaches p (including its SubjectID) to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	cloned := clonePrincipal(p)
	ctx = context.WithValue(ctx, principalKey{}, cloned)
	return context.WithValue(ctx, subjectKey{}, cloned.SubjectID)
}

// PrincipalFrom returns the Principal attached via WithPrincipal, if present
// and matching the active SubjectID.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalKey{}).(Principal)
	if !ok || p.SubjectID == "" {
		return Principal{}, false
	}
	if sid, hasSid := ctx.Value(subjectKey{}).(string); hasSid && sid != p.SubjectID {
		return Principal{}, false
	}
	return clonePrincipal(p), true
}

// WithSubjectID attaches subjectID to ctx. If ctx previously held a Principal
// for a different subject, stale key metadata is cleared.
func WithSubjectID(ctx context.Context, subjectID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := ctx.Value(principalKey{}).(Principal); ok && existing.SubjectID != subjectID {
		ctx = context.WithValue(ctx, principalKey{}, Principal{})
	}
	return context.WithValue(ctx, subjectKey{}, subjectID)
}

func SubjectID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	subjectID, ok := ctx.Value(subjectKey{}).(string)
	if ok && subjectID != "" {
		return subjectID, true
	}
	if p, ok := ctx.Value(principalKey{}).(Principal); ok && p.SubjectID != "" {
		return p.SubjectID, true
	}
	return "", false
}

// ParseBearerAuthorization parses an HTTP/gRPC Authorization header value for a
// Bearer token. An empty header returns ("", false, nil). A non-empty header
// without a "Bearer " prefix returns ErrMalformedBearerToken, and a "Bearer "
// header with no token returns ErrEmptyBearerToken.
func ParseBearerAuthorization(header string) (token string, present bool, err error) {
	const prefix = "bearer "
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false, nil
	}
	if strings.EqualFold(header, "bearer") {
		return "", false, ErrEmptyBearerToken
	}
	if !strings.HasPrefix(strings.ToLower(header), prefix) {
		return "", false, ErrMalformedBearerToken
	}
	token = strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false, ErrEmptyBearerToken
	}
	return token, true, nil
}

// BearerToken returns the bearer token from an Authorization header if present
// and non-empty.
func BearerToken(header string) (string, bool) {
	token, ok, err := ParseBearerAuthorization(header)
	if err != nil || !ok {
		return "", false
	}
	return token, true
}

// BasicPassword extracts the password component from a Basic Authorization
// header value, if present and non-empty.
func BasicPassword(header string) (string, bool) {
	const prefix = "basic "
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), prefix) {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", false
	}
	_, password, ok := strings.Cut(string(raw), ":")
	if !ok || password == "" {
		return "", false
	}
	return password, true
}

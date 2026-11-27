package server

import (
	"context"
	"errors"
	"fmt"

	"gitslice.io/gitslice/internal/auth/clerk"
	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// principalResolver maps a verified bearer token to an authenticated Principal.
type principalResolver func(ctx context.Context, token string) (*authctx.Principal, error)

// newPrincipalResolver builds the token->principal resolver for the configured
// auth provider. With AUTH_PROVIDER=clerk it verifies Clerk session JWTs and
// JIT-provisions a subject; otherwise it resolves dev/fake session tokens and
// API keys via AuthStore.AuthenticateToken.
func newPrincipalResolver(auth storage.AuthStore, cfg Config) (principalResolver, error) {
	// Optional asymmetric-key service-token path for automated testing and
	// service accounts. Disabled unless GITSLICE_SERVICE_JWT_PUBLIC_KEY is set.
	serviceVerifier, err := servicetoken.NewVerifier(cfg.Auth.ServiceToken)
	if err != nil {
		return nil, err
	}

	var primary principalResolver
	if cfg.Auth.Provider == "clerk" {
		clerkVerifier, err := clerk.NewVerifier(cfg.Auth.Clerk)
		if err != nil {
			return nil, err
		}
		primary = func(ctx context.Context, token string) (*authctx.Principal, error) {
			if principal, err := auth.AuthenticateToken(ctx, token); err == nil {
				return principal, nil
			} else if !errors.Is(err, storage.ErrUnauthenticated) {
				return nil, err
			}
			claims, err := clerkVerifier.Verify(ctx, token)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", storage.ErrUnauthenticated, err)
			}
			subjectID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, claims.Subject, claims.Email)
			if err != nil {
				return nil, err
			}
			return &authctx.Principal{
				SubjectID:           subjectID,
				SubjectKind:         "user",
				ControllerSubjectID: subjectID,
			}, nil
		}
	} else {
		primary = func(ctx context.Context, token string) (*authctx.Principal, error) {
			return auth.AuthenticateToken(ctx, token)
		}
	}

	if serviceVerifier == nil {
		return primary, nil
	}
	// Try the strict service-token verifier first; a token not signed by the
	// configured key simply falls through to the primary provider.
	return func(ctx context.Context, token string) (*authctx.Principal, error) {
		if claims, err := serviceVerifier.Verify(ctx, token); err == nil {
			subjectID, err := auth.EnsureExternalSubject(ctx, storage.ProviderService, claims.Subject, claims.Email)
			if err != nil {
				return nil, err
			}
			if claims.EmailVerified {
				// Test-only affordance: a service token may vouch for its email.
				if err := auth.SetVerifiedEmails(ctx, subjectID, storage.ProviderService, []string{claims.Email}); err != nil {
					return nil, err
				}
			}
			return &authctx.Principal{
				SubjectID:           subjectID,
				SubjectKind:         "user",
				ControllerSubjectID: subjectID,
			}, nil
		}
		return primary(ctx, token)
	}, nil
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context {
	return s.ctx
}

func optionalBearerTokenValue(value string) (string, bool, error) {
	token, ok, err := authctx.ParseBearerAuthorization(value)
	if err != nil {
		return "", false, status.Error(codes.Unauthenticated, err.Error())
	}
	return token, ok, nil
}

func grpcAuthError(err error) error {
	if errors.Is(err, storage.ErrUnauthenticated) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("auth lookup failed: %v", err))
}

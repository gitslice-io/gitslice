package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/auth/clerk"
	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// subjectResolver maps a verified bearer token to an internal subject ID. It
// abstracts over the dev/fake session store and external providers such as Clerk.
type subjectResolver func(ctx context.Context, token string) (string, error)

// principalResolver maps a verified bearer token to an authenticated Principal.
type principalResolver func(ctx context.Context, token string) (*authctx.Principal, error)

func toPrincipalResolver(resolve subjectResolver) principalResolver {
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context, token string) (*authctx.Principal, error) {
		subjectID, err := resolve(ctx, token)
		if err != nil {
			return nil, err
		}
		return &authctx.Principal{SubjectID: subjectID}, nil
	}
}

// newSubjectResolver builds the token->subject resolver for the configured auth
// provider.
func newSubjectResolver(auth storage.AuthStore, cfg Config) (subjectResolver, error) {
	resolve, err := newPrincipalResolver(auth, cfg)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, token string) (string, error) {
		principal, err := resolve(ctx, token)
		if err != nil {
			return "", err
		}
		return principal.SubjectID, nil
	}, nil
}

// newPrincipalResolver builds the token->principal resolver for the configured
// auth provider. With AUTH_PROVIDER=clerk it verifies Clerk session JWTs and
// JIT-provisions a subject; otherwise it resolves dev/fake session tokens and
// API keys via AuthStore.AuthenticateToken.
func newPrincipalResolver(auth storage.AuthStore, cfg Config) (principalResolver, error) {
	// Optional asymmetric-key service-token path for automated testing and
	// service accounts. Disabled unless GITSLICE_SERVICE_JWT_PUBLIC_KEY is set.
	serviceVerifier, err := servicetoken.NewVerifier(cfg.ServiceToken)
	if err != nil {
		return nil, err
	}

	var primary principalResolver
	if cfg.AuthProvider == "clerk" {
		clerkVerifier, err := clerk.NewVerifier(cfg.Clerk)
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

func authStreamInterceptor(resolve subjectResolver) grpc.StreamServerInterceptor {
	return principalAuthStreamInterceptor(toPrincipalResolver(resolve))
}

func principalAuthStreamInterceptor(resolve principalResolver) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isPublicMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		ctx, err := authenticatedContext(stream.Context(), resolve)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{
			ServerStream: stream,
			ctx:          ctx,
		})
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context {
	return s.ctx
}

func authInterceptor(resolve subjectResolver) grpc.UnaryServerInterceptor {
	return principalAuthInterceptor(toPrincipalResolver(resolve))
}

func principalAuthInterceptor(resolve principalResolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isPublicMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authenticatedContext(ctx, resolve)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func isPublicMethod(method string) bool {
	return method == "/gitslice.core.v1.AuthService/StartCliLogin" ||
		method == "/gitslice.core.v1.AuthService/PollCliLogin" ||
		method == agentSignupMethod ||
		// A trigger run authenticates with its run token, in the request
		// (design/34_triggers.md).
		strings.HasPrefix(method, "/gitslice.core.v1.AutomationRunService/") ||
		strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func bearerToken(ctx context.Context) (string, error) {
	token, ok, err := optionalBearerToken(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing authorization bearer token")
	}
	return token, nil
}

func authenticatedContext(ctx context.Context, resolve principalResolver) (context.Context, error) {
	token, ok, err := optionalBearerToken(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return ctx, nil
	}
	principal, err := resolve(ctx, token)
	if err != nil {
		return nil, grpcAuthError(err)
	}
	if principal == nil {
		return nil, status.Error(codes.Unauthenticated, "missing authenticated principal")
	}
	return authctx.WithPrincipal(ctx, *principal), nil
}

func optionalBearerToken(ctx context.Context) (string, bool, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false, nil
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", false, nil
	}
	return optionalBearerTokenValue(values[0])
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

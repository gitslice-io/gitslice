package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FakeAccountService struct {
	Auth storage.AuthStore
}

type AuthService struct {
	Auth storage.AuthStore
}

func (s *FakeAccountService) Login(ctx context.Context, req *corev1.LoginRequest) (*corev1.LoginResponse, error) {
	token, subjectID, err := s.Auth.LoginDevUser(ctx, req.DevUser)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.LoginResponse{Token: token, SubjectId: subjectID}, nil
}

func (s *FakeAccountService) ApproveSignup(ctx context.Context, req *corev1.ApproveSignupRequest) (*corev1.ApproveSignupResponse, error) {
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, status.Error(codes.InvalidArgument, "state is required")
	}
	callback, err := parseLocalCallbackURL(req.CallbackUrl)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	token, subjectID, err := s.Auth.SignupUser(ctx, username)
	if err != nil {
		return nil, grpcError(err)
	}
	redirect := *callback
	query := redirect.Query()
	query.Set("token", token)
	query.Set("subject_id", subjectID)
	query.Set("state", state)
	redirect.RawQuery = query.Encode()
	return &corev1.ApproveSignupResponse{
		Token:       token,
		SubjectId:   subjectID,
		RedirectUrl: redirect.String(),
	}, nil
}

func (s *AuthService) GetAuthStatus(ctx context.Context, req *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.GetAuthStatusResponse{SubjectId: subjectID, Accounts: accounts, NeedsUsername: len(accounts) == 0}, nil
}

func (s *AuthService) CheckUsernameAvailable(ctx context.Context, req *corev1.CheckUsernameAvailableRequest) (*corev1.CheckUsernameAvailableResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	available, normalized, reason, err := s.Auth.UsernameAvailable(ctx, req.Username)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CheckUsernameAvailableResponse{
		Available:  available,
		Normalized: normalized,
		Reason:     reason,
	}, nil
}

func (s *AuthService) ChooseUsername(ctx context.Context, req *corev1.ChooseUsernameRequest) (*corev1.ChooseUsernameResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := s.Auth.ChooseUsername(ctx, subjectID, req.Username)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ChooseUsernameResponse{SubjectId: subjectID, Account: account}, nil
}

func parseLocalCallbackURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("callback_url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid callback_url: %w", err)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("callback_url must use http")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("callback_url host is required")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("callback_url must point to localhost")
		}
	}
	return parsed, nil
}

func requireSubject(ctx context.Context) (string, error) {
	subjectID, ok := authctx.SubjectID(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing subject")
	}
	return subjectID, nil
}

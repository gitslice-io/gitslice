package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type authStatusOutput struct {
	SignedIn   bool     `json:"signed_in"`
	ServerAddr string   `json:"server_addr,omitempty"`
	SubjectID  string   `json:"subject_id,omitempty"`
	Accounts   []string `json:"accounts,omitempty"`
	// Memberships are the same accounts with what each is and the subject's
	// role in it.
	Memberships []authMembershipOutput `json:"memberships,omitempty"`
	// SharedSlices are the slices shared with the subject from accounts it
	// may not belong to (design/26_slice_members.md); only gs auth status
	// looks them up.
	SharedSlices []authSharedSliceOutput `json:"shared_slices,omitempty"`
	// ActiveAccount is set by gs account use, while the user belongs to it.
	ActiveAccount string `json:"active_account,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type authMembershipOutput struct {
	Account string `json:"account"`
	// personal, organization or agent.
	Kind string `json:"kind,omitempty"`
	Role string `json:"role,omitempty"`
}

type authSharedSliceOutput struct {
	Slice string `json:"slice"`
	Role  string `json:"role"`
}

type authTokenOutput struct {
	Token      string `json:"token"`
	ServerAddr string `json:"server_addr"`
	SubjectID  string `json:"subject_id,omitempty"`
}

func (r Runner) runAuthStatus(ctx context.Context, opts commandOptions) error {
	status, err := r.probeAuthStatus(ctx)
	if err != nil {
		return err
	}
	// Everything the subject can reach in one answer: its accounts, and the
	// slices shared with it from elsewhere.
	if status.SignedIn {
		if shared, err := r.sharedSlicesForStatus(ctx); err == nil {
			status.SharedSlices = shared
		}
	}
	return r.writeAuthStatus(opts, status)
}

func (r Runner) sharedSlicesForStatus(ctx context.Context) ([]authSharedSliceOutput, error) {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	shared, err := listSharedSlices(callCtx, corev1.NewSliceServiceClient(conn))
	if err != nil {
		return nil, err
	}
	out := make([]authSharedSliceOutput, 0, len(shared))
	for _, s := range shared {
		out = append(out, authSharedSliceOutput{Slice: s.Ref, Role: s.Role})
	}
	return out, nil
}

func (r Runner) runAuthToken(ctx context.Context, opts commandOptions) error {
	cfg, subjectID, err := r.validatedAuthToken(ctx)
	if err != nil {
		return err
	}
	out := authTokenOutput{
		Token:      cfg.Token,
		ServerAddr: cfg.ServerAddr,
		SubjectID:  subjectID,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	fmt.Fprintln(r.Stdout, cfg.Token)
	return nil
}

func (r Runner) runAuthLogout(opts commandOptions) error {
	path := r.userConfigPath()
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	existed := true
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		existed = false
	}
	wasSignedIn := cfg.Token != "" || cfg.SubjectID != ""
	if existed {
		cfg.Token = ""
		cfg.SubjectID = ""
		if err := r.writeUserConfig(cfg); err != nil {
			return err
		}
	}
	out := map[string]any{
		"signed_in":     false,
		"was_signed_in": wasSignedIn,
		"config_path":   path,
	}
	if cfg.ServerAddr != "" {
		out["server_addr"] = cfg.ServerAddr
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if wasSignedIn {
		fmt.Fprintln(r.Stdout, "logged out")
	} else {
		fmt.Fprintln(r.Stdout, "already logged out")
	}
	if cfg.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server: %s\n", cfg.ServerAddr)
	}
	return nil
}

func (r Runner) validatedAuthToken(ctx context.Context) (UserConfig, string, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, "", err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, "", err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return UserConfig{}, "", userError("invalid_token", "saved auth token is invalid", r.authRecoveryHint())
		}
		return UserConfig{}, "", err
	}
	return cfg, res.SubjectId, nil
}

func (r Runner) probeAuthStatus(ctx context.Context) (authStatusOutput, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return authStatusOutput{}, err
		}
		return authStatusOutput{Reason: "not_logged_in"}, nil
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "invalid_config"}, nil
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "server_unavailable"}, err
	}
	defer conn.Close()
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			return authStatusOutput{
				ServerAddr: cfg.ServerAddr,
				Reason:     "invalid_token",
			}, nil
		}
		return authStatusOutput{ServerAddr: cfg.ServerAddr, Reason: "auth_check_failed"}, err
	}
	active := ""
	if containsFold(res.Accounts, cfg.ActiveAccount) {
		active = cfg.ActiveAccount
	}
	memberships := make([]authMembershipOutput, 0, len(res.GetMemberships()))
	for _, m := range res.GetMemberships() {
		memberships = append(memberships, authMembershipOutput{Account: m.GetAccount(), Kind: m.GetKind(), Role: m.GetRole()})
	}
	return authStatusOutput{
		SignedIn:      true,
		ServerAddr:    cfg.ServerAddr,
		SubjectID:     res.SubjectId,
		Accounts:      res.Accounts,
		Memberships:   memberships,
		ActiveAccount: active,
	}, nil
}

func (r Runner) writeAuthStatus(opts commandOptions, status authStatusOutput) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, status)
	}
	if opts.Quiet {
		return nil
	}
	if !status.SignedIn {
		fmt.Fprintln(r.Stdout, "not logged in")
		if status.ServerAddr != "" {
			fmt.Fprintf(r.Stdout, "server: %s\n", status.ServerAddr)
		}
		if status.Reason != "" && status.Reason != "not_logged_in" {
			fmt.Fprintf(r.Stdout, "reason: %s\n", strings.ReplaceAll(status.Reason, "_", " "))
		}
		return nil
	}
	if status.SubjectID != "" {
		fmt.Fprintf(r.Stdout, "signed in as %s\n", status.SubjectID)
	} else {
		fmt.Fprintln(r.Stdout, "signed in")
	}
	fmt.Fprintf(r.Stdout, "server: %s\n", status.ServerAddr)
	if status.ActiveAccount != "" {
		fmt.Fprintf(r.Stdout, "active account: %s\n", status.ActiveAccount)
	}
	if len(status.Memberships) > 0 {
		parts := make([]string, 0, len(status.Memberships))
		for _, m := range status.Memberships {
			parts = append(parts, fmt.Sprintf("%s (%s)", m.Account, strings.Trim(m.Kind+", "+m.Role, ", ")))
		}
		fmt.Fprintf(r.Stdout, "accounts: %s\n", strings.Join(parts, ", "))
	}
	if len(status.SharedSlices) > 0 {
		parts := make([]string, 0, len(status.SharedSlices))
		for _, s := range status.SharedSlices {
			parts = append(parts, fmt.Sprintf("%s (%s)", s.Slice, s.Role))
		}
		fmt.Fprintf(r.Stdout, "shared with you: %s\n", strings.Join(parts, ", "))
	}
	return nil
}

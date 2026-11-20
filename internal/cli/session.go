package cli

import (
	"context"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// Session holds a single authenticated UserConfig, gRPC ClientConn, and
// lazily constructed gRPC service clients for a CLI command execution.
type Session struct {
	r       Runner
	cfg     UserConfig
	conn    *grpc.ClientConn
	callCtx context.Context

	authClient           corev1.AuthServiceClient
	sliceClient          corev1.SliceServiceClient
	changesetClient      corev1.ChangesetServiceClient
	changesetStackClient corev1.ChangesetStackServiceClient
	repositoryClient     corev1.RepositoryServiceClient
	blobClient           corev1.BlobServiceClient
	agentClient          corev1.AgentServiceClient
	checkClient          corev1.CheckServiceClient
	secretClient         corev1.SecretServiceClient
	automationClient     corev1.AutomationServiceClient
	feedbackClient       corev1.FeedbackServiceClient
}

// ChangesetTarget holds the resolved changeset selector and optional local
// workspace state for a CLI command.
type ChangesetTarget struct {
	Config                UserConfig
	Workspace             WorkspaceConfig
	State                 WorkspaceState
	HasWorkspace          bool
	ChangesetID           string
	UsingWorkspaceCurrent bool
}

// SliceResolveOptions configures how an optional or required slice reference
// is resolved from a CLI flag/argument or the current workspace.
type SliceResolveOptions struct {
	FallbackToWorkspace bool
	MissingCode         string
	MissingMessage      string
	MissingHint         string
}

func newSession(r Runner, cfg UserConfig, conn *grpc.ClientConn, callCtx context.Context) *Session {
	if callCtx == nil {
		callCtx = authContext(context.Background(), cfg)
	}
	return &Session{
		r:       r,
		cfg:     cfg,
		conn:    conn,
		callCtx: callCtx,
	}
}

func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *Session) Config() UserConfig {
	return s.cfg
}

func (s *Session) Conn() *grpc.ClientConn {
	return s.conn
}

func (s *Session) Ctx() context.Context {
	return s.callCtx
}

func (s *Session) AuthCtx(ctx context.Context) context.Context {
	return authContext(ctx, s.cfg)
}

func (s *Session) DefaultAccount() (string, error) {
	return s.r.defaultSliceAccount(s.callCtx, s.cfg, s.conn)
}

func (s *Session) ResolveAccount(raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return normalizeCLISlug(raw, "account")
	}
	return s.DefaultAccount()
}

func (s *Session) ActiveAccount() (string, error) {
	return s.r.activeAccount(s.callCtx, s.cfg, s.conn)
}

func (s *Session) PersonalAccountSlug() (string, error) {
	return s.r.personalAccountSlug(s.callCtx, s.conn)
}

func (s *Session) ResolveOptionalSliceRef(value string) (*corev1.SliceRef, error) {
	if strings.TrimSpace(value) != "" {
		return s.ResolveSliceRef(value, false)
	}
	if ws, err := s.r.readWorkspaceConfig(); err == nil && strings.TrimSpace(ws.Account) != "" && strings.TrimSpace(ws.Slice) != "" {
		return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, nil
	}
	return nil, nil
}

func (s *Session) Auth() corev1.AuthServiceClient {
	if s.authClient == nil {
		s.authClient = corev1.NewAuthServiceClient(s.conn)
	}
	return s.authClient
}

func (s *Session) Slice() corev1.SliceServiceClient {
	if s.sliceClient == nil {
		s.sliceClient = corev1.NewSliceServiceClient(s.conn)
	}
	return s.sliceClient
}

func (s *Session) Changeset() corev1.ChangesetServiceClient {
	if s.changesetClient == nil {
		s.changesetClient = corev1.NewChangesetServiceClient(s.conn)
	}
	return s.changesetClient
}

func (s *Session) ChangesetStack() corev1.ChangesetStackServiceClient {
	if s.changesetStackClient == nil {
		s.changesetStackClient = corev1.NewChangesetStackServiceClient(s.conn)
	}
	return s.changesetStackClient
}

func (s *Session) Repository() corev1.RepositoryServiceClient {
	if s.repositoryClient == nil {
		s.repositoryClient = corev1.NewRepositoryServiceClient(s.conn)
	}
	return s.repositoryClient
}

func (s *Session) Blob() corev1.BlobServiceClient {
	if s.blobClient == nil {
		s.blobClient = corev1.NewBlobServiceClient(s.conn)
	}
	return s.blobClient
}

func (s *Session) Agent() corev1.AgentServiceClient {
	if s.agentClient == nil {
		s.agentClient = corev1.NewAgentServiceClient(s.conn)
	}
	return s.agentClient
}

func (s *Session) Check() corev1.CheckServiceClient {
	if s.checkClient == nil {
		s.checkClient = corev1.NewCheckServiceClient(s.conn)
	}
	return s.checkClient
}

func (s *Session) Secret() corev1.SecretServiceClient {
	if s.secretClient == nil {
		s.secretClient = corev1.NewSecretServiceClient(s.conn)
	}
	return s.secretClient
}

func (s *Session) Automation() corev1.AutomationServiceClient {
	if s.automationClient == nil {
		s.automationClient = corev1.NewAutomationServiceClient(s.conn)
	}
	return s.automationClient
}

func (s *Session) Feedback() corev1.FeedbackServiceClient {
	if s.feedbackClient == nil {
		s.feedbackClient = corev1.NewFeedbackServiceClient(s.conn)
	}
	return s.feedbackClient
}

func (s *Session) ResolveSliceRef(value string, fallbackToWorkspace bool) (*corev1.SliceRef, error) {
	return s.ResolveSliceRefWithOptions(value, SliceResolveOptions{
		FallbackToWorkspace: fallbackToWorkspace,
	})
}

func (s *Session) ResolveSliceRefWithOptions(value string, opts SliceResolveOptions) (*corev1.SliceRef, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return s.r.resolveSliceRefInput(s.callCtx, s.cfg, s.conn, trimmed)
	}
	if !opts.FallbackToWorkspace {
		return s.r.resolveSliceRefInput(s.callCtx, s.cfg, s.conn, "")
	}
	return s.r.workspaceSliceRef(opts)
}

func (s *Session) ResolveSlice(value string) (*corev1.Slice, *corev1.SliceRef, error) {
	ref, err := s.ResolveSliceRef(value, false)
	if err != nil {
		return nil, nil, err
	}
	slice, err := s.Slice().ResolveSlice(s.callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return nil, nil, err
	}
	return slice, ref, nil
}

func (s *Session) ResolveChangesetTarget(requestedID string) (ChangesetTarget, error) {
	return s.r.resolveChangesetTargetWithConfig(s.cfg, true, requestedID)
}

func (s *Session) GetChangeset(changesetID string) (*corev1.Changeset, error) {
	return s.Changeset().GetChangeset(s.callCtx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
}

func (r Runner) workspaceSliceRef(opts SliceResolveOptions) (*corev1.SliceRef, error) {
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		if isUserErrorCode(err, "not_in_workspace") || opts.MissingCode != "" {
			return nil, userError(
				firstNonEmpty(opts.MissingCode, "slice_required"),
				firstNonEmpty(opts.MissingMessage, "--slice is required outside a workspace"),
				firstNonEmpty(opts.MissingHint, "Pass --slice account/slice."),
			)
		}
		return nil, err
	}
	if strings.TrimSpace(ws.Account) == "" || strings.TrimSpace(ws.Slice) == "" {
		return nil, userError(
			firstNonEmpty(opts.MissingCode, "slice_required"),
			firstNonEmpty(opts.MissingMessage, "--slice is required outside a workspace"),
			firstNonEmpty(opts.MissingHint, "Pass --slice account/slice."),
		)
	}
	return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, nil
}

func (r Runner) resolveChangesetTarget(requestedID string) (ChangesetTarget, error) {
	return r.resolveChangesetTargetWithConfig(UserConfig{}, false, requestedID)
}

func (r Runner) resolveChangesetTargetWithConfig(preloadedCfg UserConfig, hasPreloadedCfg bool, requestedID string) (ChangesetTarget, error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" {
		var cfg UserConfig
		var ws WorkspaceConfig
		var state WorkspaceState
		var err error
		if hasPreloadedCfg {
			cfg = preloadedCfg
			ws, err = r.readWorkspaceConfig()
			if err == nil {
				state, err = r.readWorkspaceState()
			}
		} else {
			cfg, ws, state, err = r.loadLocalState()
		}
		if err != nil {
			return ChangesetTarget{}, err
		}
		if state.CurrentChangesetID == "" {
			return ChangesetTarget{}, userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first or pass a changeset id.")
		}
		return ChangesetTarget{
			Config:                cfg,
			Workspace:             ws,
			State:                 state,
			HasWorkspace:          true,
			ChangesetID:           state.CurrentChangesetID,
			UsingWorkspaceCurrent: true,
		}, nil
	}

	cfg := preloadedCfg
	if !hasPreloadedCfg {
		var err error
		cfg, err = r.readUserConfig()
		if err != nil {
			return ChangesetTarget{}, err
		}
	}
	var ws WorkspaceConfig
	var state WorkspaceState
	hasWorkspace := false
	usingWorkspaceCurrent := false
	if workspaceCfg, err := r.readWorkspaceConfig(); err == nil {
		ws = workspaceCfg
		state, err = r.readWorkspaceState()
		if err != nil {
			return ChangesetTarget{}, err
		}
		hasWorkspace = true
		usingWorkspaceCurrent = requestedID == state.CurrentChangesetID || sameChangesetSelector(requestedID, state.CurrentChangesetID)
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return ChangesetTarget{}, err
	}
	return ChangesetTarget{
		Config:                cfg,
		Workspace:             ws,
		State:                 state,
		HasWorkspace:          hasWorkspace,
		ChangesetID:           requestedID,
		UsingWorkspaceCurrent: usingWorkspaceCurrent,
	}, nil
}

func (r Runner) withConfigSession(ctx context.Context, cfg UserConfig, fn func(s *Session) error) error {
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	s := newSession(r, cfg, conn, authContext(ctx, cfg))
	defer s.Close()
	return fn(s)
}

func (r Runner) withSession(ctx context.Context, fn func(s *Session) error) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	return r.withConfigSession(ctx, cfg, fn)
}

func (r Runner) withSliceSession(ctx context.Context, rawSlice string, fallbackToWorkspace bool, fn func(s *Session, ref *corev1.SliceRef) error) error {
	return r.withSliceSessionOpts(ctx, rawSlice, SliceResolveOptions{
		FallbackToWorkspace: fallbackToWorkspace,
	}, fn)
}

func (r Runner) withSliceSessionOpts(ctx context.Context, rawSlice string, opts SliceResolveOptions, fn func(s *Session, ref *corev1.SliceRef) error) error {
	var preResolved *corev1.SliceRef
	if strings.TrimSpace(rawSlice) == "" && opts.FallbackToWorkspace {
		var err error
		preResolved, err = r.workspaceSliceRef(opts)
		if err != nil {
			return err
		}
	}
	return r.withSession(ctx, func(s *Session) error {
		ref := preResolved
		if ref == nil {
			var err error
			ref, err = s.ResolveSliceRefWithOptions(rawSlice, opts)
			if err != nil {
				return err
			}
		}
		return fn(s, ref)
	})
}

func (r Runner) withChangesetSession(ctx context.Context, requestedID string, fn func(s *Session, target ChangesetTarget) error) error {
	target, err := r.resolveChangesetTarget(requestedID)
	if err != nil {
		return err
	}
	return r.withConfigSession(ctx, target.Config, func(s *Session) error {
		return fn(s, target)
	})
}

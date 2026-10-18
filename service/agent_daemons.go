package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxDaemonDisplayNameRunes = 64

// ListDaemons lists the daemons the caller registered and those registered
// under accounts the caller owns or administers: an agent's daemons show to the
// people who co-own the agent, not only to the agent itself. Each daemon
// carries the slices that use it as their CI daemon, among those the caller can
// read.
func (s *AgentService) ListDaemons(ctx context.Context, req *corev1.ListDaemonsRequest) (*corev1.ListDaemonsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	own, err := s.Agents.ListDaemons(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	accounts, err := s.administeredAccounts(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	shared, err := s.Agents.ListDaemonsForAccounts(ctx, accounts)
	if err != nil {
		return nil, grpcError(err)
	}
	seen := map[string]bool{}
	var daemons []*corev1.AgentDaemon
	for _, d := range own {
		seen[d.Id] = true
		d.Mine, d.Manageable = true, true
		daemons = append(daemons, d)
	}
	for _, d := range shared {
		if !seen[d.Id] {
			seen[d.Id] = true
			d.Manageable = true
			daemons = append(daemons, d)
		}
	}
	// The daemons other people exposed to a slice, for the people who choose
	// that slice's CI daemon.
	if ref := req.GetForSlice(); ref.GetAccount() != "" || ref.GetSlice() != "" {
		slice, err := s.Slices.Resolve(ctx, ref)
		if err != nil {
			return nil, grpcError(err)
		}
		// Only the people who choose the slice's CI daemon are offered the
		// daemons exposed to it; for anyone else the list is as without a slice.
		chooses, err := canAuthorizeAccount(ctx, s.Auth, subjectID, slice.GetRef().GetAccount(), authz.ActionAdmin)
		if err != nil {
			return nil, err
		}
		if chooses {
			exposed, err := s.Agents.ListDaemonsExposedToSlice(ctx, slice.Id)
			if err != nil {
				return nil, grpcError(err)
			}
			for _, d := range exposed {
				if !seen[d.Id] {
					seen[d.Id] = true
					daemons = append(daemons, d)
				}
			}
		}
	}
	sort.SliceStable(daemons, func(i, j int) bool { return daemons[i].CreatedAt < daemons[j].CreatedAt })
	if err := s.attachCISlices(ctx, subjectID, daemons); err != nil {
		return nil, err
	}
	if err := s.attachExposures(ctx, subjectID, daemons); err != nil {
		return nil, err
	}
	return &corev1.ListDaemonsResponse{Daemons: daemons}, nil
}

// ExposeDaemon exposes a daemon to a slice, so the slice's owners and admins
// may choose it as their CI daemon. A daemon runs the checks of the slices that
// name it and receives their secrets, so only its owner decides which slices it
// is exposed to: the identity that registered it, or an owner or admin of its
// account. They must be able to read the slice they name.
func (s *AgentService) ExposeDaemon(ctx context.Context, req *corev1.ExposeDaemonRequest) (*corev1.AgentDaemon, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	daemon, mine, err := s.manageableDaemon(ctx, subjectID, strings.TrimSpace(req.GetDaemonId()))
	if err != nil {
		return nil, err
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.GetSlice(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.Agents.ExposeDaemon(ctx, daemon.Id, slice.Id, subjectID); err != nil {
		return nil, grpcError(err)
	}
	return s.daemonForManager(ctx, subjectID, daemon, mine)
}

// UnexposeDaemon takes a slice off the list of slices a daemon is exposed to.
// A slice that still names the daemon has its checks refused until it names
// another: the daemon's owner can always withdraw it.
func (s *AgentService) UnexposeDaemon(ctx context.Context, req *corev1.UnexposeDaemonRequest) (*corev1.AgentDaemon, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	daemon, mine, err := s.manageableDaemon(ctx, subjectID, strings.TrimSpace(req.GetDaemonId()))
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, req.GetSlice())
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.Agents.UnexposeDaemon(ctx, daemon.Id, slice.Id); err != nil {
		return nil, grpcError(err)
	}
	return s.daemonForManager(ctx, subjectID, daemon, mine)
}

// daemonForManager is the daemon as its manager sees it, with the slices that
// use it and those it is exposed to.
func (s *AgentService) daemonForManager(ctx context.Context, subjectID string, daemon *corev1.AgentDaemon, mine bool) (*corev1.AgentDaemon, error) {
	fresh, err := s.Agents.GetDaemon(ctx, daemon.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	fresh.Mine, fresh.Manageable = mine, true
	list := []*corev1.AgentDaemon{fresh}
	if err := s.attachCISlices(ctx, subjectID, list); err != nil {
		return nil, err
	}
	if err := s.attachExposures(ctx, subjectID, list); err != nil {
		return nil, err
	}
	return fresh, nil
}

// RenameDaemon sets the name a daemon is shown by. The daemon's owner (the
// subject that registered it) and the owners and admins of its account may.
// The name it registered with is unchanged, so a restart of gs agent start
// --name finds the same daemon and the new name stays.
func (s *AgentService) RenameDaemon(ctx context.Context, req *corev1.RenameDaemonRequest) (*corev1.AgentDaemon, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	daemonID := strings.TrimSpace(req.GetDaemonId())
	if daemonID == "" {
		return nil, status.Error(codes.InvalidArgument, "daemon_id is required")
	}
	name, err := normalizeDaemonDisplayName(req.GetDisplayName())
	if err != nil {
		return nil, err
	}
	daemon, mine, err := s.manageableDaemon(ctx, subjectID, daemonID)
	if err != nil {
		return nil, err
	}
	renamed, err := s.Agents.RenameDaemon(ctx, daemon.Id, name)
	switch {
	case errors.Is(err, storage.ErrConflict):
		return nil, status.Error(codes.AlreadyExists, "another daemon of "+daemon.Account+" is already shown as "+name)
	case err != nil:
		return nil, grpcError(err)
	}
	return s.daemonForManager(ctx, subjectID, renamed, mine)
}

// normalizeDaemonDisplayName trims a name and checks it: up to 64 characters,
// none of them control characters. Empty clears the name.
func normalizeDaemonDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxDaemonDisplayNameRunes {
		return "", status.Error(codes.InvalidArgument, "display_name is at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", status.Error(codes.InvalidArgument, "display_name has a control character")
		}
	}
	return name, nil
}

// manageableDaemon returns the daemon if the subject registered it or
// administers its account, and whether the subject registered it. Anyone else
// gets NotFound, so daemon ids do not reveal which exist.
func (s *AgentService) manageableDaemon(ctx context.Context, subjectID, daemonID string) (*corev1.AgentDaemon, bool, error) {
	return manageableDaemon(ctx, s.Auth, s.Agents, subjectID, daemonID)
}

// manageableDaemon is the daemon a subject may manage and use: one they
// registered, or one registered under an account they own or administer.
// Renaming it and making it a slice's CI daemon both need this, because a
// daemon runs the code and holds the secrets of the slices that name it, and
// only its owner may offer it to them.
func manageableDaemon(ctx context.Context, auth storage.AuthStore, agents storage.AgentStore, subjectID, daemonID string) (*corev1.AgentDaemon, bool, error) {
	daemon, err := agents.GetDaemon(ctx, daemonID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, status.Error(codes.NotFound, "daemon not found")
	}
	if err != nil {
		return nil, false, grpcError(err)
	}
	owner, err := agents.DaemonSubject(ctx, daemonID)
	if err != nil {
		return nil, false, grpcError(err)
	}
	if owner == subjectID {
		return daemon, true, nil
	}
	if daemon.Account != "" {
		ok, err := canAuthorizeAccount(ctx, auth, subjectID, daemon.Account, authz.ActionAdmin)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return daemon, false, nil
		}
	}
	return nil, false, status.Error(codes.NotFound, "daemon not found")
}

// administeredAccounts lists the accounts the subject owns or administers.
func (s *AgentService) administeredAccounts(ctx context.Context, subjectID string) ([]string, error) {
	slugs, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	var accounts []string
	for _, slug := range slugs {
		ok, err := canAuthorizeAccount(ctx, s.Auth, subjectID, slug, authz.ActionAdmin)
		if err != nil {
			return nil, err
		}
		if ok {
			accounts = append(accounts, slug)
		}
	}
	return accounts, nil
}

// attachExposures fills in the slices each daemon the subject manages is
// exposed to, leaving out the ones the subject cannot read.
func (s *AgentService) attachExposures(ctx context.Context, subjectID string, daemons []*corev1.AgentDaemon) error {
	var ids []string
	byID := map[string]*corev1.AgentDaemon{}
	for _, d := range daemons {
		d.ExposedTo = nil
		if d.Manageable {
			ids = append(ids, d.Id)
			byID[d.Id] = d
		}
	}
	if len(ids) == 0 {
		return nil
	}
	exposures, err := s.Agents.ListDaemonExposures(ctx, ids)
	if err != nil {
		return grpcError(err)
	}
	readable := map[string]*corev1.SliceRef{} // by slice id; nil when not readable
	for daemonID, sliceIDs := range exposures {
		for _, sliceID := range sliceIDs {
			ref, known := readable[sliceID]
			if !known {
				slice, err := s.Slices.Get(ctx, sliceID)
				if err == nil {
					err = authz.New(s.Auth).Authorize(ctx, subjectID, slice, authz.ActionRead)
				}
				switch {
				case err == nil:
					ref = slice.Ref
				case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrNotFound):
					ref = nil
				default:
					return grpcError(err)
				}
				readable[sliceID] = ref
			}
			if ref != nil {
				byID[daemonID].ExposedTo = append(byID[daemonID].ExposedTo, ref)
			}
		}
	}
	return nil
}

// attachCISlices fills in the slices that use each daemon as their CI daemon,
// leaving out the ones the subject cannot read.
func (s *AgentService) attachCISlices(ctx context.Context, subjectID string, daemons []*corev1.AgentDaemon) error {
	if len(daemons) == 0 || s.Slices == nil {
		return nil
	}
	ids := make([]string, len(daemons))
	byID := make(map[string]*corev1.AgentDaemon, len(daemons))
	for i, d := range daemons {
		ids[i] = d.Id
		byID[d.Id] = d
		d.CiSlices = nil
	}
	slices, err := s.Slices.ListByCIDaemon(ctx, ids)
	if err != nil {
		return grpcError(err)
	}
	for _, slice := range slices {
		err := authz.New(s.Auth).Authorize(ctx, subjectID, slice, authz.ActionRead)
		if errors.Is(err, storage.ErrUnauthorized) || errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return grpcError(err)
		}
		if d := byID[slice.CiDaemonId]; d != nil {
			d.CiSlices = append(d.CiSlices, slice.Ref)
		}
	}
	return nil
}

package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Claims on check runs (design/32 layer 3); see storage.CheckStore.

type memoryCheckRunClaim struct {
	instance string
	expires  time.Time
}

func memoryLease(lease time.Duration) time.Duration {
	if lease <= 0 {
		return storage.CheckRunLease
	}
	return lease
}

// openRun reports whether the run is queued or running and not superseded.
func (s *CheckStore) openRun(run *corev1.CheckRun) bool {
	return (run.Status == "queued" || run.Status == "running") && s.b.checkRunSupersededBy[run.Id] == ""
}

func (s *CheckStore) sortedRuns(match func(*corev1.CheckRun) bool) []*corev1.CheckRun {
	var out []*corev1.CheckRun
	for _, run := range s.b.checkRuns {
		if match(run) {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].Id < out[j].Id
	})
	return out
}

func cloneRuns(in []*corev1.CheckRun) []*corev1.CheckRun {
	out := make([]*corev1.CheckRun, 0, len(in))
	for _, run := range in {
		out = append(out, cloneCheckRun(run))
	}
	return out
}

func (s *CheckStore) ClaimCheckRuns(ctx context.Context, daemonID, instance string, limit int, lease time.Duration) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	daemonID, instance = strings.TrimSpace(daemonID), strings.TrimSpace(instance)
	if daemonID == "" || instance == "" {
		return nil, storage.ErrInvalid
	}
	now := time.Now()
	runs := s.sortedRuns(func(run *corev1.CheckRun) bool {
		claim, claimed := s.b.checkRunClaims[run.Id]
		return run.DaemonId == daemonID && run.Status == "queued" && s.openRun(run) && (!claimed || claim.expires.Before(now))
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	for _, run := range runs {
		s.b.checkRunClaims[run.Id] = memoryCheckRunClaim{instance: instance, expires: now.Add(memoryLease(lease))}
	}
	return cloneRuns(runs), nil
}

func (s *CheckStore) ListClaimedCheckRuns(ctx context.Context, instance string) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	instance = strings.TrimSpace(instance)
	return cloneRuns(s.sortedRuns(func(run *corev1.CheckRun) bool {
		claim, claimed := s.b.checkRunClaims[run.Id]
		return claimed && claim.instance == instance && s.openRun(run)
	})), nil
}

func (s *CheckStore) RenewCheckRunClaim(ctx context.Context, runID, instance string, lease time.Duration) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	run := s.b.checkRuns[strings.TrimSpace(runID)]
	if run == nil || !(run.Status == "queued" || run.Status == "running") {
		return nil
	}
	s.b.checkRunClaims[run.Id] = memoryCheckRunClaim{instance: strings.TrimSpace(instance), expires: time.Now().Add(memoryLease(lease))}
	return nil
}

func (s *CheckStore) RenewCheckRunClaims(ctx context.Context, instance string, lease time.Duration) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	instance = strings.TrimSpace(instance)
	for id, claim := range s.b.checkRunClaims {
		run := s.b.checkRuns[id]
		if claim.instance == instance && run != nil && (run.Status == "queued" || run.Status == "running") {
			claim.expires = time.Now().Add(memoryLease(lease))
			s.b.checkRunClaims[id] = claim
		}
	}
	return nil
}

func (s *CheckStore) ReleaseCheckRunClaims(ctx context.Context, instance string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	instance = strings.TrimSpace(instance)
	for id, claim := range s.b.checkRunClaims {
		if run := s.b.checkRuns[id]; claim.instance == instance && run != nil && run.Status == "queued" {
			delete(s.b.checkRunClaims, id)
		}
	}
	return nil
}

func (s *CheckStore) CountUnclaimedCheckRuns(ctx context.Context, daemonID string) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now()
	n := 0
	for _, run := range s.b.checkRuns {
		claim, claimed := s.b.checkRunClaims[run.Id]
		if run.DaemonId == strings.TrimSpace(daemonID) && run.Status == "queued" && s.openRun(run) && (!claimed || claim.expires.Before(now)) {
			n++
		}
	}
	return n, nil
}

func (s *CheckStore) HasPendingCheckRuns(ctx context.Context, daemonID, instance string) (bool, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	daemonID, instance = strings.TrimSpace(daemonID), strings.TrimSpace(instance)
	now := time.Now()
	for _, run := range s.b.checkRuns {
		if run.DaemonId != daemonID || !s.openRun(run) {
			continue
		}
		claim, claimed := s.b.checkRunClaims[run.Id]
		mine := claimed && claim.instance == instance
		if run.Status == "queued" && (!claimed || mine || claim.expires.Before(now)) {
			return true, nil
		}
		if run.Status == "running" && mine {
			return true, nil
		}
	}
	return false, nil
}

func (s *CheckStore) TakeLapsedCheckRuns(ctx context.Context, limit int) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now()
	runs := s.sortedRuns(func(run *corev1.CheckRun) bool {
		claim, claimed := s.b.checkRunClaims[run.Id]
		return claimed && claim.expires.Before(now) && (run.Status == "queued" || run.Status == "running")
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	for _, run := range runs {
		delete(s.b.checkRunClaims, run.Id)
	}
	return cloneRuns(runs), nil
}

func (s *CheckStore) ListStrandedCheckRuns(ctx context.Context, minAge time.Duration) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now()
	return cloneRuns(s.sortedRuns(func(run *corev1.CheckRun) bool {
		if run.Status != "queued" || !s.openRun(run) || run.DaemonId == "" {
			return false
		}
		if _, claimed := s.b.checkRunClaims[run.Id]; claimed {
			return false
		}
		created, err := time.Parse(time.RFC3339Nano, run.CreatedAt)
		if err != nil || now.Sub(created) < minAge {
			return false
		}
		daemon := s.b.cloneDaemon(s.b.agentDaemons[run.DaemonId])
		return daemon == nil || daemon.Status != "online"
	})), nil
}

func (s *CheckStore) ReassignCheckRun(ctx context.Context, runID, fromDaemon, toDaemon string) (bool, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	run := s.b.checkRuns[strings.TrimSpace(runID)]
	if run == nil || run.DaemonId != strings.TrimSpace(fromDaemon) || run.Status != "queued" {
		return false, nil
	}
	if _, claimed := s.b.checkRunClaims[run.Id]; claimed {
		return false, nil
	}
	run.DaemonId = strings.TrimSpace(toDaemon)
	return true, nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Merge on submit (design/07 section 7.4). A submit is refused when a path the
// patchset changes moved on since its base. In a slice with merge_on_submit,
// the server instead takes the patchset onto the latest base, merging line by
// line the files both sides changed (the restack merge, rebaseEdits), makes
// that a new patchset (which reruns CI, like any new patchset) and submits it.
// Overlapping changes are refused with their paths: the author resolves them
// in a workspace, where they can be tested.

// isStaleBase reports whether a submit was refused because the base went
// stale.
func isStaleBase(err error) bool {
	return err != nil && errors.Is(err, storage.ErrConflict) && strings.Contains(err.Error(), storage.StaleBaseReason)
}

// mergeStaleOnSubmit makes the merged patchset for a changeset whose submit
// was refused for a stale base. It returns nil when merging does not apply (the
// slice has it off, or the changeset depends on another one, which restack
// handles), and an error naming the paths when the merge conflicts.
func (s *ChangesetService) mergeStaleOnSubmit(ctx context.Context, cs *corev1.Changeset) (*corev1.Patchset, error) {
	current := currentPatchset(cs)
	if current == nil || cs.GetParentChangesetId() != "" || current.GetBaseKind() == "patchset" || len(current.GetConflicts()) > 0 {
		return nil, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.GetAuthoringSlice())
	if err != nil {
		return nil, grpcError(err)
	}
	if !slice.GetMergeOnSubmit() {
		return nil, nil
	}
	targetRef := cs.GetTargetRef()
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	head, err := s.Repository.GetRef(ctx, targetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	stack := ChangesetStackService{
		Auth:        s.Auth,
		Blobs:       s.Blobs,
		Changesets:  s.Changesets,
		Repository:  s.Repository,
		Slices:      s.Slices,
		Checks:      s.Checks,
		ObjectStore: s.ObjectStore,
		validator:   s.validator,
		dispatcher:  s.dispatcher,
	}
	update, err := stack.rebasedUpdate(ctx, cs, current, head.GetCommitId(), "", &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.GetId(),
		ExpectedCurrentPatchsetId: cs.GetCurrentPatchsetId(),
		BaseCommitId:              head.GetCommitId(),
		BaseKind:                  "commit",
	})
	if err != nil {
		return nil, err
	}
	if len(update.GetConflicts()) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"the base moved, and merging the latest changes in conflicts at %s; run gs sync to merge them in your workspace, then submit again",
			conflictPathList(update.GetConflicts()))
	}
	update.PatchsetKind = storage.SubmitMergeKind
	return s.UpdateChangeset(ctx, update)
}

// mergeOnSubmitAttempts bounds how often a submit merges again because the
// base moved once more while it was merging: agents landing on one slice at
// once do this to each other.
const mergeOnSubmitAttempts = 8

// mergeAndSubmit is what a submit does after it was refused for a stale base:
// merge the changeset onto the latest base (mergeStaleOnSubmit) and submit the
// merge, again each time the base moves under it. applied is false when merging
// does not apply (the slice has it off, or the changeset is not a plain one),
// and the caller keeps the refusal. The error is a gRPC status error; a submit
// that ends on a pending check says it merged first.
func (s *ChangesetService) mergeAndSubmit(ctx context.Context, cs *corev1.Changeset) (*corev1.SubmitChangesetResponse, error, bool) {
	// Merges onto one target are taken one at a time on this server, so
	// agents landing together queue instead of each making the others' merge
	// stale; the loop below is for what still races (another instance, or a
	// submit that did not merge).
	unlock := lockMergeTarget(cs)
	defer unlock()
	var merged *corev1.Patchset
	for attempt := 0; attempt < mergeOnSubmitAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err(), true
			case <-time.After(time.Duration(attempt)*20*time.Millisecond + time.Duration(rand.Intn(20))*time.Millisecond):
			}
		}
		latest, err := s.Changesets.Get(ctx, cs.GetId())
		if err != nil {
			return nil, grpcError(err), merged != nil
		}
		next, err := s.mergeStaleOnSubmit(ctx, latest)
		if err != nil {
			return nil, err, true
		}
		if next == nil {
			return nil, nil, merged != nil
		}
		merged = next
		refreshed, err := s.Changesets.Get(ctx, cs.GetId())
		if err != nil {
			return nil, grpcError(err), true
		}
		extra, err := s.skippedRequiredCheckStatuses(ctx, refreshed)
		if err != nil {
			return nil, grpcError(err), true
		}
		res, err := s.submitPatchset(ctx, cs.GetId(), merged.GetId(), extra)
		if err == nil {
			return res, nil, true
		}
		if !isStaleBase(err) {
			return nil, withMergedNote(grpcError(err), merged), true
		}
	}
	return nil, status.Errorf(codes.Aborted, "the base kept moving while the changeset was merged onto it (%d tries); submit again", mergeOnSubmitAttempts), true
}

// conflictPathList names up to five conflicting paths.
func conflictPathList(conflicts []*corev1.PatchsetConflict) string {
	const shown = 5
	var paths []string
	for _, conflict := range conflicts {
		if len(paths) == shown {
			break
		}
		paths = append(paths, conflict.GetPath())
	}
	list := strings.Join(paths, ", ")
	if extra := len(conflicts) - len(paths); extra > 0 {
		list += fmt.Sprintf(" and %d more", extra)
	}
	return list
}

// acceptedExpectedPatchset is the patchset a submit asks for, allowing for a
// merge the server made: a client that submitted patchset N and was told it
// was merged keeps naming N while it waits for the merge's checks, and that
// means the server's merge of N.
func acceptedExpectedPatchset(cs *corev1.Changeset, expected string) string {
	current := currentPatchset(cs)
	if expected == "" || current == nil || expected == current.GetId() || current.GetKind() != storage.SubmitMergeKind {
		return expected
	}
	for i, patchset := range cs.GetPatchsets() {
		if patchset.GetId() == current.GetId() && i > 0 && cs.GetPatchsets()[i-1].GetId() == expected {
			return current.GetId()
		}
	}
	return expected
}

// mergedNote says, in front of a submit's answer, that the server merged the
// changeset, so a reason such as a pending check is read against the merge.
func mergedNote(merged *corev1.Patchset) string {
	return fmt.Sprintf("the base had moved, so the server merged the changeset onto the latest base as patchset %d, which runs CI again", merged.GetNumber())
}

// withMergedNote puts mergedNote in front of err's message, keeping its code.
func withMergedNote(err error, merged *corev1.Patchset) error {
	converted := status.Convert(err)
	return status.Error(converted.Code(), mergedNote(merged)+": "+converted.Message())
}

// mergeLocks holds one mutex per slice and target ref being merged into.
var mergeLocks sync.Map

func lockMergeTarget(cs *corev1.Changeset) func() {
	key := cs.GetAuthoringSlice().GetAccount() + "/" + cs.GetAuthoringSlice().GetSlice() + "@" + cs.GetTargetRef()
	value, _ := mergeLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

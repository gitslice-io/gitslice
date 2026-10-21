package service

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RenameSlice gives a slice a new name; see the proto for what keeps working.
func (s *SliceService) RenameSlice(ctx context.Context, req *corev1.RenameSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	if isHomeSlice(current.Ref) {
		return nil, status.Error(codes.InvalidArgument, "the home slice cannot be renamed")
	}
	newName, err := normalizeServiceSlug(req.NewName, "new slice name")
	if err != nil {
		return nil, err
	}
	renamed, err := s.Slices.Rename(ctx, current.Id, newName)
	if err != nil {
		return nil, grpcError(err)
	}
	return renamed, nil
}

// SetSliceArchived archives a slice or restores it.
func (s *SliceService) SetSliceArchived(ctx context.Context, req *corev1.SetSliceArchivedRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	if isHomeSlice(current.Ref) {
		return nil, status.Error(codes.InvalidArgument, "the home slice cannot be archived")
	}
	updated, err := s.Slices.SetArchived(ctx, current.Id, req.Archived)
	if err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

// maxSliceDescriptionRunes bounds a slice description: a note for its page,
// not a README.
const maxSliceDescriptionRunes = 500

// SetSliceDescription sets or clears a slice's description.
func (s *SliceService) SetSliceDescription(ctx context.Context, req *corev1.SetSliceDescriptionRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	description, err := normalizeSliceDescription(req.Description)
	if err != nil {
		return nil, err
	}
	updated, err := s.Slices.SetDescription(ctx, current.Id, description)
	if err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

// SetSliceMergeOnSubmit turns merging stale changesets on submit on or off
// for a slice (design/07 section 7.4). Owners and admins of its account may.
func (s *SliceService) SetSliceMergeOnSubmit(ctx context.Context, req *corev1.SetSliceMergeOnSubmitRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	updated, err := s.Slices.SetMergeOnSubmit(ctx, current.Id, req.Enabled)
	if err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

// normalizeSliceDescription trims a description and refuses one that is too
// long or holds control characters other than line breaks and tabs.
func normalizeSliceDescription(description string) (string, error) {
	description = strings.TrimSpace(strings.ReplaceAll(description, "\r\n", "\n"))
	if n := utf8.RuneCountInString(description); n > maxSliceDescriptionRunes {
		return "", status.Errorf(codes.InvalidArgument, "description is %d characters; the limit is %d", n, maxSliceDescriptionRunes)
	}
	if !utf8.ValidString(description) {
		return "", status.Error(codes.InvalidArgument, "description is not valid UTF-8")
	}
	for _, r := range description {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", status.Errorf(codes.InvalidArgument, "description holds a control character (%U)", r)
		}
	}
	return description, nil
}

// retireSlice removes a slice that has changesets. Its history stays on the
// paths it covered, so every path it includes must either still be covered by
// another slice or hold no files, and none of its changesets may be open.
func (s *SliceService) retireSlice(ctx context.Context, slice *corev1.Slice) error {
	if isHomeSlice(slice.Ref) {
		return status.Error(codes.InvalidArgument, "the home slice cannot be deleted")
	}
	_, open, err := s.Slices.ChangesetCounts(ctx, slice.Id)
	if err != nil {
		return grpcError(err)
	}
	if open > 0 {
		return status.Errorf(codes.FailedPrecondition,
			"slice has %d open changeset(s); land or abandon them first (gs cs list --slice %s/%s)", open, slice.Ref.Account, slice.Ref.Slice)
	}
	orphaned, err := s.pathsLeftWithoutASlice(ctx, slice)
	if err != nil {
		return err
	}
	if len(orphaned) > 0 {
		return status.Errorf(codes.FailedPrecondition,
			"slice has changesets, and %s would be left with files and no slice that includes it; add the path to another slice or remove its files first, or archive this slice instead",
			strings.Join(orphaned, ", "))
	}
	if err := s.Slices.Retire(ctx, slice.Id); err != nil {
		return grpcError(err)
	}
	return nil
}

// pathsLeftWithoutASlice returns the slice's included paths that hold files
// and are covered by no other slice that is not archived.
func (s *SliceService) pathsLeftWithoutASlice(ctx context.Context, slice *corev1.Slice) ([]string, error) {
	if s.Repository == nil {
		return nil, status.Error(codes.Internal, "repository store is not configured")
	}
	included := slice.GetDefinition().GetIncludedPaths()
	covering, err := s.Slices.CoveringIDsByPath(ctx, included)
	if err != nil {
		return nil, grpcError(err)
	}
	target, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	var orphaned []string
	for _, p := range included {
		covered, err := s.coveredByAnother(ctx, slice.Id, covering[p])
		if err != nil {
			return nil, err
		}
		if covered {
			continue
		}
		if _, err := s.Repository.GetEntry(ctx, target.CommitId, p); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue // nothing there to leave behind
			}
			return nil, grpcError(err)
		}
		orphaned = append(orphaned, p)
	}
	return orphaned, nil
}

func (s *SliceService) coveredByAnother(ctx context.Context, sliceID string, coveringIDs []string) (bool, error) {
	for _, id := range coveringIDs {
		if id == sliceID {
			continue
		}
		other, err := s.Slices.Get(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return false, grpcError(err)
		}
		if !other.Archived {
			return true, nil
		}
	}
	return false, nil
}

// resolveAuthoringSlice finds the slice a changeset or stack was authored in.
// A retired slice no longer resolves, but its changesets stay readable, so for
// reading it is looked up as it was.
func resolveAuthoringSlice(ctx context.Context, slices storage.SliceStore, ref *corev1.SliceRef, action authz.Action) (*corev1.Slice, error) {
	slice, err := slices.Resolve(ctx, ref)
	if errors.Is(err, storage.ErrNotFound) && action == authz.ActionRead {
		return slices.ResolveRetired(ctx, ref)
	}
	return slice, err
}

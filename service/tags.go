package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxTagNameLength = 200

// CreateTag records an immutable release tag for a slice
// (design/21_self_hosting.md). The Git projection publishes it as
// refs/tags/<name>.
func (s *SliceService) CreateTag(ctx context.Context, req *corev1.CreateTagRequest) (*corev1.Tag, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionWrite); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := validateTagName(name); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tag name %q: %v", name, err)
	}
	commitID := strings.TrimSpace(req.GetCommitId())
	if commitID == "" {
		head, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		commitID = head.CommitId
	} else if _, err := s.Repository.GetCommit(ctx, commitID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.InvalidArgument, "commit %q not found", commitID)
		}
		return nil, grpcError(err)
	}
	var version int64
	if slice.Definition != nil {
		version = slice.Definition.Version
	}
	tag, _, err := s.Slices.CreateTag(ctx, storage.SliceTag{
		SliceID:           slice.Id,
		Name:              name,
		CommitID:          commitID,
		DefinitionVersion: version,
		Message:           strings.TrimSpace(req.GetMessage()),
		CreatedBy:         subjectID,
	})
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Error(codes.AlreadyExists, err.Error())
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.tagsProto(ctx, slice.Ref, []storage.SliceTag{*tag})
	if err != nil {
		return nil, grpcError(err)
	}
	return out[0], nil
}

func (s *SliceService) ListTags(ctx context.Context, req *corev1.ListTagsRequest) (*corev1.ListTagsResponse, error) {
	subjectID := optionalSubject(ctx)
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	tags, err := s.Slices.ListTags(ctx, slice.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.tagsProto(ctx, slice.Ref, tags)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListTagsResponse{Tags: out}, nil
}

func (s *SliceService) tagsProto(ctx context.Context, ref *corev1.SliceRef, tags []storage.SliceTag) ([]*corev1.Tag, error) {
	subjects := make([]string, 0, len(tags))
	for _, tag := range tags {
		subjects = append(subjects, tag.CreatedBy)
	}
	usernames, err := s.Auth.UsernamesForSubjects(ctx, subjects)
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.Tag, 0, len(tags))
	for _, tag := range tags {
		createdBy := tag.CreatedBy
		if username := usernames[tag.CreatedBy]; username != "" {
			createdBy = username
		}
		out = append(out, &corev1.Tag{
			Slice:             ref,
			Name:              tag.Name,
			CommitId:          tag.CommitID,
			DefinitionVersion: tag.DefinitionVersion,
			Message:           tag.Message,
			CreatedBy:         createdBy,
			CreatedAt:         tag.CreatedAt,
		})
	}
	return out, nil
}

// validateTagName applies the subset of git check-ref-format rules a tag
// needs, so every tag can be published as refs/tags/<name>.
func validateTagName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > maxTagNameLength {
		return fmt.Errorf("name must be %d characters or fewer", maxTagNameLength)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '+', r == '/':
		default:
			return fmt.Errorf("only letters, digits, '.', '_', '-', '+' and '/' are allowed")
		}
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") {
		return fmt.Errorf("'..' and '//' are not allowed")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("must not start with '-' or end with '.'")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("each '/'-separated part must be non-empty, not start with '.', and not end with '.lock'")
		}
	}
	return nil
}

package service

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/gitmirror"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// GitMirrorPusher adapts gitcompat.Projector.PushMirror to gitmirror.PushFunc.
func GitMirrorPusher(push func(ctx context.Context, account, slice string, target gitcompat.MirrorTarget) (*gitcompat.MirrorResult, error)) gitmirror.PushFunc {
	if push == nil {
		return nil
	}
	return func(ctx context.Context, account, slice string, target gitmirror.Target) (*gitmirror.PushResult, error) {
		res, err := push(ctx, account, slice, gitcompat.MirrorTarget{
			URL:        target.URL,
			Branch:     target.Branch,
			Username:   target.Username,
			Token:      target.Token,
			Resolve:    target.Resolve,
			AllowLocal: target.AllowLocal,
		})
		if err != nil || res == nil {
			return nil, err
		}
		return &gitmirror.PushResult{
			Head:           res.Head,
			NativeCommitID: res.NativeCommitID,
			Exported:       res.Exported,
			NewTags:        res.NewTags,
			Subdir:         res.Subdir,
		}, nil
	}
}

func (s *SliceService) mirrorRunner() *gitmirror.Runner {
	return s.gitMirrorRunner
}

func gitMirrorProto(mirror *storage.GitMirror, slice *corev1.Slice) *corev1.SliceGitMirror {
	out := &corev1.SliceGitMirror{
		Slice:                  &corev1.SliceRef{Account: slice.GetRef().GetAccount(), Slice: slice.GetRef().GetSlice()},
		Url:                    mirror.URL,
		Branch:                 mirror.Branch,
		Username:               mirror.Username,
		TokenSecret:            mirror.TokenSecret,
		Pending:                mirror.Pending(),
		LastAttemptAt:          formatTime(mirror.LastAttemptAt),
		LastPushedAt:           formatTime(mirror.LastPushedAt),
		LastPushedCommit:       mirror.LastPushedCommit,
		LastPushedNativeCommit: mirror.LastPushedNative,
		LastError:              mirror.LastError,
		Failures:               int32(mirror.Failures),
		CreatedBy:              mirror.CreatedBy,
		UpdatedAt:              formatTime(mirror.UpdatedAt),
	}
	if mirror.Failures > 0 && mirror.Pending() {
		out.NextAttemptAt = formatTime(mirror.NextAttemptAt)
	}
	if root, err := gitcompat.MirrorSubdir(slice); err == nil && root != "" {
		out.Root = "/" + root
	}
	return out
}

// gitMirrorSlice resolves the slice of a mirror request and checks that the
// caller owns or administers its account.
func (s *SliceService) gitMirrorSlice(ctx context.Context, ref *corev1.SliceRef, allowArchived bool) (string, *corev1.Slice, *gitmirror.Runner, error) {
	runner := s.mirrorRunner()
	if runner == nil || runner.Mirrors == nil {
		return "", nil, nil, status.Error(codes.Unimplemented, "this server has no Git mirrors")
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	normalized, err := normalizeServiceSliceRef(ref)
	if err != nil {
		return "", nil, nil, err
	}
	slice, err := s.Slices.Resolve(ctx, normalized)
	if err != nil {
		return "", nil, nil, grpcError(err)
	}
	if allowArchived {
		if err := authorizeSliceOwnerAllowArchived(ctx, s.Auth, subjectID, slice); err != nil {
			return "", nil, nil, err
		}
	} else {
		if err := authorizeSliceOwner(ctx, s.Auth, subjectID, slice); err != nil {
			return "", nil, nil, err
		}
	}
	return subjectID, slice, runner, nil
}

// GetSliceGitMirror returns the slice's Git mirror.
func (s *SliceService) GetSliceGitMirror(ctx context.Context, req *corev1.GetSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	_, slice, runner, err := s.gitMirrorSlice(ctx, req.GetSlice(), true)
	if err != nil {
		return nil, err
	}
	mirror, err := runner.Mirrors.GetGitMirror(ctx, slice.GetId())
	if errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "this slice has no Git mirror")
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return gitMirrorProto(mirror, slice), nil
}

// SetSliceGitMirror creates or replaces the slice's Git mirror.
func (s *SliceService) SetSliceGitMirror(ctx context.Context, req *corev1.SetSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	subjectID, slice, runner, err := s.gitMirrorSlice(ctx, req.GetSlice(), false)
	if err != nil {
		return nil, err
	}
	target, err := runner.ValidateURL(req.GetUrl())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "url: %v", err)
	}
	branch, err := gitmirror.ValidateBranch(req.GetBranch())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	username, err := gitmirror.ValidateUsername(req.GetUsername())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	secret := strings.TrimSpace(req.GetTokenSecret())
	if secret != "" {
		if !storage.ValidSliceSecretName(secret) {
			return nil, status.Errorf(codes.InvalidArgument, "token_secret %q must match ^[A-Z_][A-Z0-9_]*$", secret)
		}
		if _, err := runner.SecretAt(ctx, slice, secret); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	mirror, err := runner.Mirrors.PutGitMirror(ctx, storage.GitMirror{
		SliceID: slice.GetId(), URL: target, Branch: branch, Username: username, TokenSecret: secret, CreatedBy: subjectID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	runner.Notify()
	return gitMirrorProto(mirror, slice), nil
}

// DeleteSliceGitMirror stops mirroring the slice.
func (s *SliceService) DeleteSliceGitMirror(ctx context.Context, req *corev1.DeleteSliceGitMirrorRequest) (*corev1.Empty, error) {
	_, slice, runner, err := s.gitMirrorSlice(ctx, req.GetSlice(), true)
	if err != nil {
		return nil, err
	}
	if err := runner.Mirrors.DeleteGitMirror(ctx, slice.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

// PushSliceGitMirror pushes the slice's mirror now.
func (s *SliceService) PushSliceGitMirror(ctx context.Context, req *corev1.PushSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	_, slice, runner, err := s.gitMirrorSlice(ctx, req.GetSlice(), false)
	if err != nil {
		return nil, err
	}
	if _, err := runner.Mirrors.GetGitMirror(ctx, slice.GetId()); errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "this slice has no Git mirror")
	} else if err != nil {
		return nil, grpcError(err)
	}
	if err := runner.Mirrors.MarkGitMirrorsPending(ctx, []string{slice.GetId()}, true); err != nil {
		return nil, grpcError(err)
	}
	runner.Notify()
	mirror, err := runner.Mirrors.GetGitMirror(ctx, slice.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return gitMirrorProto(mirror, slice), nil
}

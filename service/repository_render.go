package service

import (
	"context"
	"path"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/markdown"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// renderMaxBytes is the largest Markdown file RenderFile renders: a folder's
// README is shown inline only up to this size (the web viewer's
// README_MAX_BYTES); a bigger one is linked instead.
const renderMaxBytes = 256 << 10

// RenderFile renders a Markdown file to safe HTML (internal/markdown), with
// the same read authorization as ReadFile.
func (s *RepositoryService) RenderFile(ctx context.Context, req *corev1.RenderFileRequest) (*corev1.RenderFileResponse, error) {
	subjectID := optionalSubject(ctx)
	p, err := paths.Canonical(req.GetPath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !isMarkdownPath(p) {
		return nil, status.Errorf(codes.InvalidArgument, "%s is not a Markdown file", p)
	}
	root := ""
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
		root = projectRootFor(slice.GetDefinition().GetIncludedPaths(), p)
	} else if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	entry, err := s.Repository.GetFile(ctx, req.GetCommitId(), p)
	if err != nil {
		return nil, grpcError(err)
	}
	if entry.Size > renderMaxBytes {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to show inline", p, renderMaxBytes)
	}
	read, err := readFile(ctx, s.Repository, s.ObjectStore, &corev1.ReadFileRequest{CommitId: req.GetCommitId(), Path: p})
	if err != nil {
		return nil, err
	}
	html, err := markdown.Render(read.GetData(), markdown.Options{Directory: path.Dir(p), Root: root})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rendering %s: %v", p, err)
	}
	return &corev1.RenderFileResponse{Html: html, ContentHash: read.GetContentHash()}, nil
}

func isMarkdownPath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// projectRootFor is the slice's included path that holds the document, the
// longest when several do, as the web viewer's projectRoot picks it.
func projectRootFor(includedPaths []string, document string) string {
	best := ""
	for _, included := range includedPaths {
		root, err := paths.Canonical(included)
		if err != nil {
			continue
		}
		prefix := root + "/"
		if root == "/" {
			prefix = "/"
		}
		if (document == root || strings.HasPrefix(document, prefix)) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

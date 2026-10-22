package service

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *RepositoryService) GetCommit(ctx context.Context, req *corev1.GetCommitRequest) (*corev1.Commit, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	commit, err := s.Repository.GetCommit(ctx, req.CommitId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.ensureCommitRead(ctx, subjectID, commit); err != nil {
		return nil, err
	}
	if err := s.resolveCommitAuthors(ctx, commit); err != nil {
		return nil, grpcError(err)
	}
	return commit, nil
}

// attachGitImports sets Commit.git_import on commits published by a Git
// import, so clients can show the original author, date and message.
func (s *RepositoryService) attachGitImports(ctx context.Context, commits ...*corev1.Commit) error {
	ids := make([]string, 0, len(commits))
	for _, commit := range commits {
		if commit != nil && commit.Id != "" {
			ids = append(ids, commit.Id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	imports, err := s.Repository.GitImportsForCommits(ctx, ids)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		if commit == nil {
			continue
		}
		if record, ok := imports[commit.Id]; ok {
			commit.GitImport = gitImportInfo(record)
		}
	}
	return nil
}

// attachChangesets sets Commit.changeset_id, so clients can link a commit to
// the changeset that landed it (commits have no web page of their own).
func (s *RepositoryService) attachChangesets(ctx context.Context, commits ...*corev1.Commit) error {
	ids := make([]string, 0, len(commits))
	for _, commit := range commits {
		if commit != nil && commit.Id != "" {
			ids = append(ids, commit.Id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	landed, err := s.Repository.ChangesetsForCommits(ctx, ids)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		if commit != nil {
			commit.ChangesetId = landed[commit.Id]
		}
	}
	return nil
}

func gitImportInfo(record storage.GitImportedCommitRecord) *corev1.GitImportInfo {
	return &corev1.GitImportInfo{
		GitCommitId: record.GitCommitID,
		AuthorName:  record.AuthorName,
		AuthorEmail: record.AuthorEmail,
		AuthoredAt:  record.AuthoredAt,
		Message:     record.FullMessage,
	}
}

// resolveCommitAuthors rewrites each Commit.Author from the internal subject id
// to the author's username (personal account slug). Authors that don't resolve
// to a personal account (e.g. imported git authors, system commits) are left
// unchanged so the response still carries a stable identifier.
func (s *RepositoryService) resolveCommitAuthors(ctx context.Context, commits ...*corev1.Commit) error {
	seen := map[string]struct{}{}
	var subjectIDs []string
	for _, commit := range commits {
		if commit == nil {
			continue
		}
		author := strings.TrimSpace(commit.Author)
		if author == "" {
			continue
		}
		if _, ok := seen[author]; ok {
			continue
		}
		seen[author] = struct{}{}
		subjectIDs = append(subjectIDs, author)
	}
	// The three lookups read only ids and write different fields, so they run
	// together: each is a storage round trip.
	var usernames map[string]string
	var group errgroup.Group
	group.Go(func() error { return s.attachGitImports(ctx, commits...) })
	group.Go(func() error { return s.attachChangesets(ctx, commits...) })
	if len(subjectIDs) > 0 {
		group.Go(func() error {
			var err error
			usernames, err = s.Auth.UsernamesForSubjects(ctx, subjectIDs)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for _, commit := range commits {
		if commit == nil {
			continue
		}
		if username := usernames[commit.Author]; username != "" {
			commit.Author = username
		}
	}
	return nil
}

func (s *RepositoryService) ResolveCommit(ctx context.Context, req *corev1.ResolveCommitRequest) (*corev1.ResolveCommitResponse, error) {
	subjectID := optionalSubject(ctx)
	idPrefix, matchedPrefix, err := normalizeCommitIDPrefix(req.CommitId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	filters, err := s.listCommitFilters(ctx, subjectID, &corev1.ListCommitsRequest{
		RefName:     req.RefName,
		Path:        req.Path,
		Slice:       req.Slice,
		FollowMoves: req.FollowMoves,
	})
	if err != nil {
		return nil, err
	}
	if filters.filtered && len(filters.prefixes) == 0 && len(filters.entityRefs) == 0 {
		return nil, status.Error(codes.NotFound, "commit not found")
	}
	candidates, err := s.Repository.ResolveCommitCandidates(ctx, storage.CommitResolveFilter{
		RefName:                     req.RefName,
		IDPrefix:                    idPrefix,
		PathPrefixes:                filters.prefixes,
		EntityRefs:                  filters.entityRefs,
		IncludePrefixesWithEntities: filters.includePrefixesWithEntities,
		Limit:                       4,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	switch len(candidates) {
	case 0:
		return nil, status.Error(codes.NotFound, "commit not found")
	case 1:
		if err := s.resolveCommitAuthors(ctx, candidates[0]); err != nil {
			return nil, grpcError(err)
		}
		return &corev1.ResolveCommitResponse{Commit: candidates[0], MatchedPrefix: matchedPrefix}, nil
	default:
		ids := make([]string, 0, len(candidates))
		for _, commit := range candidates {
			ids = append(ids, shortCommitIDForMessage(commit.Id))
		}
		return nil, status.Errorf(codes.FailedPrecondition, "COMMIT_PREFIX_AMBIGUOUS: commit prefix %s matched multiple commits: %s", matchedPrefix, strings.Join(ids, ", "))
	}
}

func (s *RepositoryService) ListCommits(ctx context.Context, req *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error) {
	subjectID := optionalSubject(ctx)
	filters, err := s.listCommitFilters(ctx, subjectID, req)
	if err != nil {
		return nil, err
	}
	page, err := s.listCommitPage(ctx, req.RefName, filters, int(req.Limit), req.PageToken)
	if err != nil {
		return nil, err
	}
	if err := s.resolveCommitAuthors(ctx, page.Commits...); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListCommitsResponse{Commits: page.Commits, NextPageToken: page.NextPageToken}, nil
}

func normalizeCommitIDPrefix(value string) (idPrefix string, matchedPrefix string, err error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", "", fmt.Errorf("commit id is required")
	}
	hexPart := value
	if strings.HasPrefix(value, "sha256:") {
		hexPart = strings.TrimPrefix(value, "sha256:")
	}
	if len(hexPart) < 8 {
		return "", "", fmt.Errorf("commit id prefix must be at least 8 hex characters")
	}
	if len(hexPart) > 64 {
		return "", "", fmt.Errorf("commit id prefix must be at most 64 hex characters")
	}
	for _, r := range hexPart {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return "", "", fmt.Errorf("commit id prefix must contain only hex characters")
	}
	prefix := "sha256:" + hexPart
	return prefix, prefix, nil
}

func shortCommitIDForMessage(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type listCommitFilters struct {
	prefixes                    []string
	entityRefs                  []storage.HistoryEntityRef
	includePrefixesWithEntities bool
	filtered                    bool
}

func (s *RepositoryService) listCommitFilters(ctx context.Context, subjectID string, req *corev1.ListCommitsRequest) (listCommitFilters, error) {
	var prefixes []string
	filtered := false
	if req.Slice != nil {
		filtered = true
		if req.Slice.Account == "" || req.Slice.Slice == "" {
			return listCommitFilters{}, status.Error(codes.InvalidArgument, "slice ref is required")
		}
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return listCommitFilters{}, err
		}
		for _, included := range slice.Definition.IncludedPaths {
			prefix, err := repositoryReadPath(included)
			if err != nil {
				return listCommitFilters{}, status.Error(codes.InvalidArgument, err.Error())
			}
			prefixes = append(prefixes, prefix)
		}
	}
	pathFilter := strings.TrimSpace(req.Path)
	if pathFilter == "" {
		if len(prefixes) == 0 {
			accountPrefixes, err := s.readableAccountPrefixes(ctx, subjectID)
			if err != nil {
				return listCommitFilters{}, err
			}
			return listCommitFilters{prefixes: accountPrefixes, filtered: true}, nil
		}
		return listCommitFilters{prefixes: prefixes, filtered: filtered}, nil
	}
	p, err := repositoryReadPath(pathFilter)
	if err != nil {
		return listCommitFilters{}, status.Error(codes.InvalidArgument, err.Error())
	}
	if len(prefixes) == 0 {
		if p == "/" {
			accountPrefixes, err := s.readableAccountPrefixes(ctx, subjectID)
			if err != nil {
				return listCommitFilters{}, err
			}
			return listCommitFilters{prefixes: accountPrefixes, filtered: true}, nil
		}
		if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
			return listCommitFilters{}, err
		}
		prefixes = []string{p}
	} else {
		prefixes = intersectPathPrefixes(p, prefixes)
	}
	filters := listCommitFilters{prefixes: prefixes, filtered: true}
	if p == "/" || (req.FollowMoves != nil && !req.GetFollowMoves()) || len(prefixes) == 0 {
		return filters, nil
	}
	refs, includePrefixes, err := s.historyEntityRefsForPathPrefixes(ctx, req.RefName, p, prefixes)
	if err != nil {
		return listCommitFilters{}, err
	}
	if len(refs) == 0 {
		return filters, nil
	}
	filters.entityRefs = refs
	filters.includePrefixesWithEntities = includePrefixes
	return filters, nil
}

func intersectPathPrefixes(pathFilter string, prefixes []string) []string {
	if pathFilter == "/" {
		return prefixes
	}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		switch {
		case repositoryPathContains(pathFilter, prefix):
			out = append(out, prefix)
		case repositoryPathContains(prefix, pathFilter):
			out = append(out, pathFilter)
		}
	}
	return out
}

func (s *RepositoryService) historyEntityRefsForPathPrefixes(ctx context.Context, refName, pathFilter string, prefixes []string) ([]storage.HistoryEntityRef, bool, error) {
	entities, err := s.Repository.CurrentPathEntitiesByPrefixes(ctx, refName, prefixes)
	if err != nil {
		return nil, false, grpcError(err)
	}
	exactEntities, err := s.Repository.CurrentPathEntitiesByPaths(ctx, refName, []string{pathFilter})
	if err != nil {
		return nil, false, grpcError(err)
	}
	ancestorEntities, err := s.Repository.CurrentPathEntitiesByPaths(ctx, refName, ancestorRepositoryPaths(pathFilter))
	if err != nil {
		return nil, false, grpcError(err)
	}
	entities = append(entities, ancestorEntities...)
	seen := map[string]struct{}{}
	out := make([]storage.HistoryEntityRef, 0, len(entities))
	for _, entity := range entities {
		if entity.AccountID == "" || entity.EntityID == "" {
			continue
		}
		key := entity.AccountID + "\x00" + entity.EntityID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, storage.HistoryEntityRef{AccountID: entity.AccountID, EntityID: entity.EntityID})
	}
	includePrefixes := true
	for _, entity := range exactEntities {
		if entity.Path == pathFilter && entity.Kind == "file" {
			includePrefixes = false
			break
		}
	}
	return out, includePrefixes, nil
}

func ancestorRepositoryPaths(p string) []string {
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" {
		return nil
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")
	if len(segments) <= 1 {
		return nil
	}
	out := make([]string, 0, len(segments)-1)
	for i := 1; i < len(segments); i++ {
		out = append(out, "/"+strings.Join(segments[:i], "/"))
	}
	return out
}

func repositoryPathContains(prefix, p string) bool {
	if prefix == "/" {
		return true
	}
	prefix = strings.TrimRight(prefix, "/")
	p = strings.TrimRight(p, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

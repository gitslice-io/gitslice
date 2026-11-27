package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/blame"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// SetBlameFromIndex turns planning blame steps from the history index on or
// off (on by default); for tests.
func SetBlameFromIndex(on bool) { blame.SetFromIndex(on) }

func (s *RepositoryService) blameEngine() *blame.Engine {
	return &blame.Engine{
		Repository: s.Repository,
		Blames:     s.Blames,
		Objects:    s.ObjectStore,
	}
}

func (s *RepositoryService) bulkImportActive() bool {
	ts := s.lastBulkImportUnixNano.Load()
	return ts > 0 && time.Since(time.Unix(0, ts)) < blame.WarmBulkImportQuietWindow
}

// NewBlameWarmer returns a standalone blame warmer worker using gate (or the
// service's configured gate / activity.Default when nil).
func (s *RepositoryService) NewBlameWarmer(gate *activity.Gate) *blame.Warmer {
	if gate == nil {
		gate = s.activityGate()
	}
	return blame.NewWarmer(s.blameEngine(), gate, s.bulkImportActive)
}

func (s *RepositoryService) BlameFile(ctx context.Context, req *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error) {
	trace := blame.NewTrace()
	ctx = blame.WithTrace(ctx, trace)
	var walked *blame.Result
	defer func() {
		slog.Info("blame", trace.LogAttrs(req.GetPath(), walked)...)
	}()
	subjectID := optionalSubject(ctx)
	p, err := paths.Canonical(req.GetPath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var authorizedSlice *corev1.Slice
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
		authorizedSlice = slice
	} else if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	pathAllowedCache := map[string]bool{p: true}
	canReadPath := func(candidate string) bool {
		canonical, err := repositoryReadPath(candidate)
		if err != nil || canonical == "/" {
			return false
		}
		if allowed, seen := pathAllowedCache[canonical]; seen {
			return allowed
		}
		allowed := false
		if authorizedSlice != nil && ensureRepositoryPathInSlice(authorizedSlice, canonical) == nil {
			allowed = true
		} else if s.ensureRepositoryPathRead(ctx, subjectID, canonical) == nil {
			allowed = true
		}
		pathAllowedCache[canonical] = allowed
		return allowed
	}
	refName := strings.TrimSpace(req.GetRefName())
	if err := requireDefaultTargetRef(refName); err != nil {
		return nil, err
	}
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	trace.Mark("access")

	engine := s.blameEngine()
	var head *corev1.Ref
	var headEntry *storage.FileEntry
	var filters listCommitFilters
	var group errgroup.Group
	group.Go(func() error {
		var err error
		if head, err = s.Repository.GetRef(ctx, refName); err != nil {
			return grpcError(err)
		}
		var found bool
		if headEntry, found, err = engine.Entry(ctx, head.CommitId, p); err != nil {
			return blameError(err)
		}
		if !found {
			return status.Errorf(codes.NotFound, "no file at %s", p)
		}
		return nil
	})
	group.Go(func() error {
		var err error
		filters, err = s.listCommitFilters(ctx, subjectID, &corev1.ListCommitsRequest{RefName: refName, Path: p, Slice: req.Slice})
		return err
	})
	if err := group.Wait(); err != nil {
		return nil, err
	}
	trace.Mark("head")

	result, err := engine.Walk(ctx, refName, blame.CommitFilters{
		Prefixes:                    filters.prefixes,
		EntityRefs:                  filters.entityRefs,
		IncludePrefixesWithEntities: filters.includePrefixesWithEntities,
		Filtered:                    filters.filtered,
	}, head.CommitId, p, headEntry, canReadPath)
	if err != nil {
		return nil, blameError(err)
	}
	walked = &result
	trace.Mark("walk")

	out := &corev1.BlameFileResponse{CommitId: head.CommitId, Path: p, LineCount: int32(len(result.Owners)), Truncated: result.Truncated}
	var ids []string
	named := map[string]bool{}
	for start := 0; start < len(result.Owners); {
		end := start
		for end < len(result.Owners) && result.Owners[end] == result.Owners[start] {
			end++
		}
		owner := result.Owners[start]
		out.Ranges = append(out.Ranges, &corev1.BlameRange{StartLine: int32(start + 1), LineCount: int32(end - start), CommitId: owner})
		if !named[owner] {
			named[owner] = true
			ids = append(ids, owner)
		}
		start = end
	}
	var missing []string
	for _, id := range ids {
		if result.Commits[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		read, err := s.Repository.GetCommits(ctx, missing)
		if err != nil {
			return nil, grpcError(err)
		}
		for _, id := range missing {
			if read[id] == nil {
				return nil, grpcError(storage.ErrNotFound)
			}
			result.Commits[id] = read[id]
		}
	}
	for _, id := range ids {
		cloned := proto.Clone(result.Commits[id]).(*corev1.Commit)
		filtered := cloned.ChangedPaths[:0]
		for _, changed := range cloned.ChangedPaths {
			if canReadPath(changed) {
				filtered = append(filtered, changed)
			}
		}
		cloned.ChangedPaths = filtered
		out.Commits = append(out.Commits, cloned)
	}
	trace.Mark("commits")
	if err := s.resolveCommitAuthors(ctx, out.Commits...); err != nil {
		return nil, grpcError(err)
	}
	trace.Mark("authors")
	return out, nil
}

func blameError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, blame.ErrTooLarge) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, blame.ErrBinary) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return grpcError(err)
}

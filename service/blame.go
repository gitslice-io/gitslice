package service

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Blame (design/27_blame.md): which commit last changed each line of a file.
// The server walks the file's history newest first (following moves, as path
// history does), diffs each version against its parent's, and credits a line
// to the commit where it stops matching. The walk reads one version per
// commit and stops at blameMaxCommits.

const (
	blameMaxCommits = 500
	blameMaxBytes   = 4 << 20
	blamePageSize   = 100
)

func (s *RepositoryService) BlameFile(ctx context.Context, req *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error) {
	subjectID := optionalSubject(ctx)
	p, err := paths.Canonical(req.GetPath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
	} else if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	refName := strings.TrimSpace(req.GetRefName())
	if err := requireDefaultTargetRef(refName); err != nil {
		return nil, err
	}
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	head, err := s.Repository.GetRef(ctx, refName)
	if err != nil {
		return nil, grpcError(err)
	}
	current, found, err := s.blameVersion(ctx, head.CommitId, p)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "no file at %s", p)
	}
	if bytes.IndexByte(current.data[:min(len(current.data), 8000)], 0) >= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s is a binary file", p)
	}

	filters, err := s.listCommitFilters(ctx, subjectID, &corev1.ListCommitsRequest{RefName: refName, Path: p, Slice: req.Slice})
	if err != nil {
		return nil, err
	}
	walk := newBlameWalk(len(current.lines))
	tracked, trackedPath := current, p
	var last *corev1.Commit
	seen, token := 0, ""
	historyEnded := false
pages:
	for {
		page, err := s.listCommitPage(ctx, refName, filters, blamePageSize, token)
		if err != nil {
			return nil, err
		}
		for _, commit := range page.Commits {
			if seen++; seen > blameMaxCommits {
				break pages
			}
			last = commit
			parent, parentPath, found := blameVersion{}, trackedPath, false
			if len(commit.ParentIds) > 0 {
				if parent, found, err = s.blameVersion(ctx, commit.ParentIds[0], trackedPath); err != nil {
					return nil, err
				}
				if !found {
					// Moved in this commit? Read it where it was.
					if parentPath, err = s.blameMovedFrom(ctx, commit, trackedPath, tracked); err != nil {
						return nil, err
					}
					if parentPath != "" {
						if parent, found, err = s.blameVersion(ctx, commit.ParentIds[0], parentPath); err != nil {
							return nil, err
						}
					}
				}
			}
			if !found {
				// Created here: every line still unaccounted for is this commit's.
				walk.creditRest(commit.Id)
				break pages
			}
			if parent.hash != tracked.hash {
				walk.step(commit.Id, diffutil.MatchLines(parent.lines, tracked.lines, 0))
			}
			tracked, trackedPath = parent, parentPath
			if walk.remaining == 0 {
				break pages
			}
		}
		if page.NextPageToken == "" {
			historyEnded = true
			break
		}
		token = page.NextPageToken
	}
	truncated := walk.remaining > 0 && !(historyEnded && last == nil)
	switch {
	case walk.remaining > 0 && last != nil:
		walk.creditRest(last.Id)
	case walk.remaining > 0:
		// No recorded history for the path: the head commit is all there is.
		walk.creditRest(head.CommitId)
	}

	out := &corev1.BlameFileResponse{CommitId: head.CommitId, Path: p, LineCount: int32(len(current.lines)), Truncated: truncated}
	ids := []string{}
	seenIDs := map[string]bool{}
	for start := 0; start < len(walk.owner); {
		end := start
		for end < len(walk.owner) && walk.owner[end] == walk.owner[start] {
			end++
		}
		out.Ranges = append(out.Ranges, &corev1.BlameRange{StartLine: int32(start + 1), LineCount: int32(end - start), CommitId: walk.owner[start]})
		if !seenIDs[walk.owner[start]] {
			seenIDs[walk.owner[start]] = true
			ids = append(ids, walk.owner[start])
		}
		start = end
	}
	for _, id := range ids {
		commit, err := s.Repository.GetCommit(ctx, id)
		if err != nil {
			return nil, grpcError(err)
		}
		out.Commits = append(out.Commits, commit)
	}
	if err := s.resolveCommitAuthors(ctx, out.Commits...); err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

// listCommitPage reads one page of commits for the filters, as ListCommits
// does.
func (s *RepositoryService) listCommitPage(ctx context.Context, refName string, filters listCommitFilters, limit int, token string) (*storage.CommitListPage, error) {
	var page *storage.CommitListPage
	var err error
	switch {
	case filters.filtered && len(filters.prefixes) == 0 && len(filters.entityRefs) == 0:
		return &storage.CommitListPage{}, nil
	case len(filters.entityRefs) > 0 && filters.includePrefixesWithEntities:
		page, err = s.Repository.ListCommitPageByEntityRefsOrPathPrefixes(ctx, refName, filters.entityRefs, filters.prefixes, limit, token)
	case len(filters.entityRefs) > 0:
		page, err = s.Repository.ListCommitPageByEntityRefs(ctx, refName, filters.entityRefs, limit, token)
	case len(filters.prefixes) == 0:
		page, err = s.Repository.ListCommitPage(ctx, refName, limit, token)
	default:
		page, err = s.Repository.ListCommitPageByPathPrefixes(ctx, refName, filters.prefixes, limit, token)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return page, nil
}

type blameVersion struct {
	hash  string
	data  []byte
	lines []string
}

// blameVersion reads the file at path in a commit; found is false when there
// is no file there.
func (s *RepositoryService) blameVersion(ctx context.Context, commitID, p string) (blameVersion, bool, error) {
	entry, err := s.Repository.GetFile(ctx, commitID, p)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return blameVersion{}, false, nil
		}
		return blameVersion{}, false, grpcError(err)
	}
	if entry.Size > blameMaxBytes {
		return blameVersion{}, false, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to blame", p, blameMaxBytes)
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return blameVersion{}, false, grpcError(err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return blameVersion{}, false, grpcError(err)
	}
	data := buf.Bytes()
	return blameVersion{hash: entry.ContentHash, data: data, lines: splitBlameLines(data)}, true, nil
}

// blameMovedFrom finds where a file was before a commit that moved it: among
// the paths the commit changed, one that had a file in the parent and none
// after, choosing the one sharing the most lines with the file.
func (s *RepositoryService) blameMovedFrom(ctx context.Context, commit *corev1.Commit, movedTo string, moved blameVersion) (string, error) {
	if len(commit.ParentIds) == 0 {
		return "", nil
	}
	have := map[string]int{}
	for _, line := range moved.lines {
		have[line]++
	}
	best, bestShared := "", -1
	for _, changed := range commit.ChangedPaths {
		candidate, err := paths.Canonical(changed)
		if err != nil || candidate == movedTo {
			continue
		}
		if _, stillThere, err := s.blameVersion(ctx, commit.Id, candidate); err != nil || stillThere {
			continue
		}
		before, found, err := s.blameVersion(ctx, commit.ParentIds[0], candidate)
		if err != nil || !found {
			continue
		}
		shared, counts := 0, map[string]int{}
		for _, line := range before.lines {
			if counts[line] < have[line] {
				counts[line]++
				shared++
			}
		}
		if shared > bestShared {
			best, bestShared = candidate, shared
		}
	}
	return best, nil
}

func splitBlameLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// blameWalk tracks, for each line of the file at the head, the line it is in
// the version being read, until a commit is credited with it.
type blameWalk struct {
	owner     []string
	pos       []int
	remaining int
}

func newBlameWalk(lines int) *blameWalk {
	w := &blameWalk{owner: make([]string, lines), pos: make([]int, lines), remaining: lines}
	for i := range w.pos {
		w.pos[i] = i
	}
	return w
}

// step credits commitID with the lines that have no match in its parent's
// version (match maps the commit's version to the parent's), and moves the
// rest to their lines in the parent's version.
func (w *blameWalk) step(commitID string, match []int) {
	for i, owner := range w.owner {
		if owner != "" {
			continue
		}
		if j := match[w.pos[i]]; j >= 0 {
			w.pos[i] = j
			continue
		}
		w.owner[i] = commitID
		w.remaining--
	}
}

func (w *blameWalk) creditRest(commitID string) {
	for i, owner := range w.owner {
		if owner == "" {
			w.owner[i] = commitID
			w.remaining--
		}
	}
}

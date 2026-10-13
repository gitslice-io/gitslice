package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
//
// A file's blame at the commit that last changed it is that commit's parent
// version's blame carried through one diff: lines the diff matches keep
// their owner, the rest are the commit's. Blames are stored as snapshots
// (storage.BlameStore), so a blame walks back through the file's history
// (following moves, as path history does) only until it reaches a version
// with a snapshot, or the commit that created the file, and then carries the
// blame forward one diff per newer version. The newest result is stored for
// the next blame. The walk reads at most blameMaxCommits versions.

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
	headEntry, found, err := s.blameEntry(ctx, head.CommitId, p)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "no file at %s", p)
	}
	filters, err := s.listCommitFilters(ctx, subjectID, &corev1.ListCommitsRequest{RefName: refName, Path: p, Slice: req.Slice})
	if err != nil {
		return nil, err
	}

	result, err := s.blameWalk(ctx, refName, filters, head.CommitId, p, headEntry)
	if err != nil {
		return nil, err
	}

	out := &corev1.BlameFileResponse{CommitId: head.CommitId, Path: p, LineCount: int32(len(result.owners)), Truncated: result.truncated}
	var ids []string
	named := map[string]bool{}
	for start := 0; start < len(result.owners); {
		end := start
		for end < len(result.owners) && result.owners[end] == result.owners[start] {
			end++
		}
		owner := result.owners[start]
		out.Ranges = append(out.Ranges, &corev1.BlameRange{StartLine: int32(start + 1), LineCount: int32(end - start), CommitId: owner})
		if !named[owner] {
			named[owner] = true
			ids = append(ids, owner)
		}
		start = end
	}
	for _, id := range ids {
		commit := result.commits[id]
		if commit == nil {
			// Named by a stored snapshot, from beyond this walk.
			if commit, err = s.Repository.GetCommit(ctx, id); err != nil {
				return nil, grpcError(err)
			}
		}
		out.Commits = append(out.Commits, commit)
	}
	if err := s.resolveCommitAuthors(ctx, out.Commits...); err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

type blameResult struct {
	owners    []string
	truncated bool
	// commits holds the commits the walk read, by id.
	commits map[string]*corev1.Commit
}

// blameStep is one commit of the walk: the file's version right after the
// commit, and right before it (none when the commit created it).
type blameStep struct {
	commit  *corev1.Commit
	path    string
	version blameVersion
	parent  *blameVersion
}

func (s *RepositoryService) blameWalk(ctx context.Context, refName string, filters listCommitFilters, headCommitID, p string, headEntry *storage.FileEntry) (blameResult, error) {
	result := blameResult{commits: map[string]*corev1.Commit{}}
	trackedPath, trackedHash := p, headEntry.ContentHash
	var trackedLines []string
	loaded := false
	var steps []blameStep
	var base *storage.BlameSnapshot
	created := false
	seen, token := 0, ""
	first := true
walk:
	for {
		page, err := s.listCommitPage(ctx, refName, filters, blamePageSize, token)
		if err != nil {
			return result, err
		}
		commits := page.Commits
		if first {
			first = false
			recent, err := s.blameUnlistedCommits(ctx, headCommitID, p, headEntry.ContentHash, commits)
			if err != nil {
				return result, err
			}
			commits = append(recent, commits...)
		}
		snapshots := map[string]map[string]*storage.BlameSnapshot{}
		if s.Blames != nil && len(commits) > 0 {
			ids := make([]string, 0, len(commits))
			for _, commit := range commits {
				ids = append(ids, commit.Id)
			}
			if snapshots, err = s.Blames.BlameSnapshots(ctx, ids); err != nil {
				slog.Warn("blame snapshots unavailable", "error", err)
				snapshots = map[string]map[string]*storage.BlameSnapshot{}
			}
		}
		for _, commit := range commits {
			result.commits[commit.Id] = commit
			// A stored blame of exactly the version being tracked ends the walk.
			if snapshot := snapshots[commit.Id][trackedPath]; snapshot != nil && snapshot.ContentHash == trackedHash {
				base = snapshot
				break walk
			}
			if seen++; seen > blameMaxCommits {
				result.truncated = true
				break walk
			}
			if !loaded {
				if trackedLines, err = s.blameLines(ctx, trackedPath, trackedHash, headEntry.Size); err != nil {
					return result, err
				}
				loaded = true
			}
			step := blameStep{commit: commit, path: trackedPath, version: blameVersion{hash: trackedHash, lines: trackedLines}}
			parentPath := trackedPath
			if len(commit.ParentIds) > 0 {
				parent, found, err := s.blameVersion(ctx, commit.ParentIds[0], trackedPath)
				if err != nil {
					return result, err
				}
				if !found {
					// Moved in this commit? Read it where it was.
					if parentPath, err = s.blameMovedFrom(ctx, commit, trackedPath, step.version); err != nil {
						return result, err
					}
					if parentPath != "" {
						if parent, found, err = s.blameVersion(ctx, commit.ParentIds[0], parentPath); err != nil {
							return result, err
						}
					}
				}
				if found {
					step.parent = &parent
				}
			}
			steps = append(steps, step)
			if step.parent == nil {
				created = true
				break walk
			}
			trackedPath, trackedHash, trackedLines = parentPath, step.parent.hash, step.parent.lines
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}

	// The blame of the version just before the oldest step, then carried
	// forward through each step.
	var owners []string
	switch {
	case base != nil:
		owners = base.Owners
		result.truncated = base.Truncated
	case created:
		// The oldest step created the file; it starts the carry below.
	case len(steps) > 0:
		// The walk stopped (a limit, or history that ends without creating
		// the file): what is older is credited to the oldest commit read.
		oldest := steps[len(steps)-1]
		owners = make([]string, len(oldest.parent.lines))
		for i := range owners {
			owners[i] = oldest.commit.Id
		}
		result.truncated = true
	default:
		// No recorded history for the path: the head commit is all there is.
		lines, err := s.blameLines(ctx, p, headEntry.ContentHash, headEntry.Size)
		if err != nil {
			return result, err
		}
		owners = make([]string, len(lines))
		for i := range owners {
			owners[i] = headCommitID
		}
		result.owners = owners
		return result, nil
	}
	for i := len(steps) - 1; i >= 0; i-- {
		owners = carryBlame(owners, steps[i])
	}
	result.owners = owners

	// Store what was computed for the next blame: the blame of the version
	// the newest step left, which is the head's, unless history listing has
	// not caught up with the head yet.
	if s.Blames != nil && len(steps) > 0 {
		newest := steps[0]
		if entry, found, err := s.blameEntry(ctx, newest.commit.Id, newest.path); err == nil && found && entry.ContentHash == newest.version.hash {
			if err := s.Blames.PutBlameSnapshot(ctx, storage.BlameSnapshot{
				CommitID:    newest.commit.Id,
				Path:        newest.path,
				ContentHash: newest.version.hash,
				Truncated:   result.truncated,
				Owners:      owners,
			}); err != nil {
				slog.Warn("storing blame snapshot failed", "commit_id", newest.commit.Id, "path", newest.path, "error", err)
			}
		}
	}
	return result, nil
}

// blameUnlistedMax bounds the walk along the main line for commits path
// history does not list yet.
const blameUnlistedMax = 100

// blameUnlistedCommits finds the commits that changed the file but are not in
// its path history yet: history is indexed shortly after a commit lands, so a
// blame right after one could otherwise credit its lines to an older commit.
// When the file at the head is not the version the newest listed commit left,
// it walks the main line back from the head, collecting the commits that
// changed the file, until the file is that version again.
func (s *RepositoryService) blameUnlistedCommits(ctx context.Context, headCommitID, p, headHash string, listed []*corev1.Commit) ([]*corev1.Commit, error) {
	listedHash := ""
	if len(listed) > 0 {
		entry, found, err := s.blameEntry(ctx, listed[0].Id, p)
		if err != nil {
			return nil, err
		}
		if found {
			listedHash = entry.ContentHash
		}
		if listedHash == headHash {
			return nil, nil
		}
	}
	var recent []*corev1.Commit
	commitID, hash := headCommitID, headHash
	for i := 0; i < blameUnlistedMax; i++ {
		if len(listed) > 0 && commitID == listed[0].Id {
			break
		}
		commit, err := s.Repository.GetCommit(ctx, commitID)
		if err != nil {
			return nil, grpcError(err)
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		parentHash := ""
		entry, found, err := s.blameEntry(ctx, commit.ParentIds[0], p)
		if err != nil {
			return nil, err
		}
		if found {
			parentHash = entry.ContentHash
		}
		if parentHash != hash {
			recent = append(recent, commit)
		}
		if parentHash == listedHash {
			break
		}
		commitID, hash = commit.ParentIds[0], parentHash
	}
	return recent, nil
}

// carryBlame takes the blame of a step's parent version (owners, one per
// line) to the step's own version: matched lines keep their owner, the rest
// are the step's commit's. A step that created the file owns every line.
func carryBlame(owners []string, step blameStep) []string {
	out := make([]string, len(step.version.lines))
	if step.parent == nil {
		for i := range out {
			out[i] = step.commit.Id
		}
		return out
	}
	if step.parent.hash == step.version.hash {
		copy(out, owners)
		return out
	}
	match := diffutil.MatchLines(step.parent.lines, step.version.lines, 0)
	for j, i := range match {
		if i >= 0 && i < len(owners) {
			out[j] = owners[i]
		} else {
			out[j] = step.commit.Id
		}
	}
	return out
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
	lines []string
}

// blameEntry finds the file at path in a commit; found is false when there
// is none.
func (s *RepositoryService) blameEntry(ctx context.Context, commitID, p string) (*storage.FileEntry, bool, error) {
	entry, err := s.Repository.GetFile(ctx, commitID, p)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, grpcError(err)
	}
	if entry.Size > blameMaxBytes {
		return nil, false, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to blame", p, blameMaxBytes)
	}
	return entry, true, nil
}

// blameLines reads a file version's lines, refusing binary files.
func (s *RepositoryService) blameLines(ctx context.Context, p, contentHash string, size int64) ([]string, error) {
	if size > blameMaxBytes {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to blame", p, blameMaxBytes)
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, grpcError(err)
	}
	data := buf.Bytes()
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s is a binary file", p)
	}
	return splitBlameLines(data), nil
}

// blameVersion reads the file at path in a commit; found is false when there
// is no file there.
func (s *RepositoryService) blameVersion(ctx context.Context, commitID, p string) (blameVersion, bool, error) {
	entry, found, err := s.blameEntry(ctx, commitID, p)
	if err != nil || !found {
		return blameVersion{}, found, err
	}
	lines, err := s.blameLines(ctx, p, entry.ContentHash, entry.Size)
	if err != nil {
		return blameVersion{}, false, err
	}
	return blameVersion{hash: entry.ContentHash, lines: lines}, true, nil
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
		if _, stillThere, err := s.blameEntry(ctx, commit.Id, candidate); err != nil || stillThere {
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

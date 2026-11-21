package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
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
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionTag); err != nil {
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
	storeCtx, tagAttr := withCapturedAttribution(ctx, s.Auth, subjectID)
	tag, _, err := s.Slices.CreateTag(storeCtx, storage.SliceTag{
		SliceID:           slice.Id,
		Name:              name,
		CommitID:          commitID,
		DefinitionVersion: version,
		Message:           strings.TrimSpace(req.GetMessage()),
		CreatedBy:         subjectID,
		Attribution:       tagAttr,
	})
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Error(codes.AlreadyExists, err.Error())
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.tagsProto(ctx, map[string]*corev1.SliceRef{slice.Id: slice.Ref}, []storage.SliceTag{*tag})
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
	var (
		tags         []storage.SliceTag
		sliceRefByID = map[string]*corev1.SliceRef{slice.Id: slice.Ref}
	)
	if isHomeSlice(slice.Ref) {
		tags, sliceRefByID, err = s.listHomeAggregatedTags(ctx, subjectID, slice)
	} else {
		tags, err = s.Slices.ListTags(ctx, slice.Id)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.tagsProto(ctx, sliceRefByID, tags)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListTagsResponse{Tags: out}, nil
}

// listHomeAggregatedTags collects tags across all readable slices in the
// account for the account's home slice in a single account-wide tag query.
// Because every tag snapshots a global monorepo commit, tags with different
// names (even for the same commit) are all returned; tags that share a name
// across slices are deduplicated with the home slice's own tag taking
// precedence, followed by the newest tag.
func (s *SliceService) listHomeAggregatedTags(ctx context.Context, subjectID string, home *corev1.Slice) ([]storage.SliceTag, map[string]*corev1.SliceRef, error) {
	account := home.Ref.Account
	p, hasPrincipal := authctx.PrincipalFrom(ctx)
	scoped := hasPrincipal && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope
	member := false
	if subjectID != "" {
		memberCtx := ctx
		if scoped {
			// A slice-scoped key may only see slices inside its scope that the
			// underlying subject is allowed to read (including private slices
			// of its own account).
			memberCtx = authctx.WithPrincipal(ctx, authctx.Principal{SubjectID: subjectID})
		}
		var err error
		if member, err = canAuthorizeAccount(memberCtx, s.Auth, subjectID, account, authz.ActionRead); err != nil {
			return nil, nil, err
		}
	}
	publicOnly := !member
	sharedWith := ""
	if publicOnly && subjectID != "" {
		sharedWith = subjectID
	}
	allTags, err := s.Slices.ListAccountTags(ctx, account, publicOnly, sharedWith)
	if err != nil {
		return nil, nil, err
	}
	sliceRefByID := map[string]*corev1.SliceRef{home.Id: home.Ref}
	byName := make(map[string]storage.AccountSliceTag, len(allTags))
	for _, tag := range allTags {
		if scoped && !p.AllowsSlice(tag.SliceID) {
			continue
		}
		if _, ok := sliceRefByID[tag.SliceID]; !ok {
			sliceRefByID[tag.SliceID] = &corev1.SliceRef{Account: account, Slice: tag.SliceSlug}
		}
		existing, ok := byName[tag.Name]
		if !ok || homeTagWins(tag, existing, home.Id) {
			byName[tag.Name] = tag
		}
	}
	out := make([]storage.SliceTag, 0, len(byName))
	for _, tag := range byName {
		out = append(out, tag.SliceTag)
	}
	sort.Slice(out, func(i, j int) bool {
		if cmp := compareTagTimesDesc(out[i].CreatedAt, out[j].CreatedAt); cmp != 0 {
			return cmp < 0
		}
		return out[i].Name < out[j].Name
	})
	return out, sliceRefByID, nil
}

func homeTagWins(candidate, current storage.AccountSliceTag, homeSliceID string) bool {
	candHome := candidate.SliceID == homeSliceID
	currHome := current.SliceID == homeSliceID
	if candHome != currHome {
		return candHome
	}
	if cmp := compareTagTimesDesc(candidate.CreatedAt, current.CreatedAt); cmp != 0 {
		return cmp < 0
	}
	if candidate.SliceSlug != current.SliceSlug {
		return candidate.SliceSlug < current.SliceSlug
	}
	return candidate.SliceID < current.SliceID
}

func compareTagTimesDesc(a, b string) int {
	ta, ea := time.Parse(time.RFC3339Nano, a)
	tb, eb := time.Parse(time.RFC3339Nano, b)
	if ea == nil && eb == nil {
		if ta.After(tb) {
			return -1
		}
		if tb.After(ta) {
			return 1
		}
		return 0
	}
	if a > b {
		return -1
	}
	if a < b {
		return 1
	}
	return 0
}

func (s *SliceService) tagsProto(ctx context.Context, sliceRefByID map[string]*corev1.SliceRef, tags []storage.SliceTag) ([]*corev1.Tag, error) {
	subjects := make([]string, 0, len(tags))
	for _, tag := range tags {
		subjects = append(subjects, tag.CreatedBy)
	}
	usernames, err := newSubjectDirectory(s.Auth).usernames(ctx, subjects...)
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.Tag, 0, len(tags))
	attrs := make([]*corev1.ActorAttribution, 0, len(tags))
	for _, tag := range tags {
		createdBy := tag.CreatedBy
		if username := usernames[tag.CreatedBy]; username != "" {
			createdBy = username
		}
		attr := tag.Attribution
		if attr.ActingSubjectID == "" {
			attr.ActingSubjectID = tag.CreatedBy
		}
		attrProto := attr.ToProto()
		if attrProto != nil {
			attrs = append(attrs, attrProto)
		}
		out = append(out, &corev1.Tag{
			Slice:             sliceRefByID[tag.SliceID],
			Name:              tag.Name,
			CommitId:          tag.CommitID,
			DefinitionVersion: tag.DefinitionVersion,
			Message:           tag.Message,
			CreatedBy:         createdBy,
			CreatedAt:         tag.CreatedAt,
			Attribution:       attrProto,
		})
	}
	if err := enrichActorAttributions(ctx, s.Auth, s.Agents, attrs...); err != nil {
		return nil, err
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

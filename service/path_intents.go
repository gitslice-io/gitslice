package service

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ChangesetService) CreatePathIntent(ctx context.Context, req *corev1.CreatePathIntentRequest) (*corev1.PathIntent, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.GetSlice() == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.GetSlice(), authz.ActionPropose)
	if err != nil {
		return nil, err
	}

	var includedPaths []string
	if slice.GetDefinition() != nil {
		includedPaths = slice.GetDefinition().GetIncludedPaths()
	}
	normalizedPattern, err := intents.ValidateSlicePattern(req.GetPattern(), includedPaths)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	msg := strings.TrimSpace(req.GetMessage())
	if !utf8.ValidString(msg) {
		return nil, status.Error(codes.InvalidArgument, "message must be valid UTF-8")
	}
	if utf8.RuneCountInString(msg) > intents.MaxMessageLength {
		return nil, status.Errorf(codes.InvalidArgument, "message exceeds %d characters", intents.MaxMessageLength)
	}

	ttlSec := req.GetTtlSeconds()
	if ttlSec < 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_seconds must not be negative")
	}
	ttl := intents.DefaultTTL
	if ttlSec > 0 {
		if ttlSec > int64(intents.MaxTTL/time.Second) {
			return nil, status.Error(codes.InvalidArgument, "intent duration exceeds maximum 7d")
		}
		ttl = time.Duration(ttlSec) * time.Second
	}

	changesetID := strings.TrimSpace(req.GetChangesetId())
	if changesetID != "" {
		cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionPropose)
		if err != nil {
			return nil, err
		}
		if !sameSliceRef(cs.GetAuthoringSlice(), slice.GetRef()) {
			return nil, status.Error(codes.InvalidArgument, "changeset does not belong to slice")
		}
		if cs.GetStatus() != "draft" {
			return nil, status.Error(codes.FailedPrecondition, "changeset is not open")
		}
		changesetID = cs.GetId()
	}

	now := time.Now().UTC()
	active, err := s.Changesets.ListPathIntents(ctx, slice.GetId(), now)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(active) >= intents.MaxActivePerSlice {
		return nil, status.Errorf(codes.ResourceExhausted, "slice %s/%s already has %d active intents; clear some (gs intent clear) or let them expire", slice.GetRef().GetAccount(), slice.GetRef().GetSlice(), len(active))
	}
	mine := 0
	for _, item := range active {
		if item.SubjectID == subjectID {
			mine++
		}
	}
	if mine >= intents.MaxActivePerSubject {
		return nil, status.Errorf(codes.ResourceExhausted, "you already have %d active intents on %s/%s; clear some (gs intent clear) or let them expire", mine, slice.GetRef().GetAccount(), slice.GetRef().GetSlice())
	}
	created, err := s.Changesets.CreatePathIntent(ctx, storage.PathIntent{
		SliceID:     slice.GetId(),
		Account:     slice.GetRef().GetAccount(),
		Slice:       slice.GetRef().GetSlice(),
		SubjectID:   subjectID,
		Pattern:     normalizedPattern,
		Message:     msg,
		ChangesetID: changesetID,
		ExpiresAt:   now.Add(ttl),
		CreatedAt:   now,
	})
	if err != nil {
		return nil, grpcError(err)
	}

	items, err := s.toProtoPathIntents(ctx, []storage.PathIntent{*created})
	if err != nil {
		return nil, grpcError(err)
	}
	return items[0], nil
}

func (s *ChangesetService) ListPathIntents(ctx context.Context, req *corev1.ListPathIntentsRequest) (*corev1.ListPathIntentsResponse, error) {
	if req == nil || req.GetSlice() == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	subjectID := optionalSubject(ctx)
	slice, err := resolveAuthorizedSliceAllowArchived(ctx, s.Auth, s.Slices, subjectID, req.GetSlice(), authz.ActionRead)
	if err != nil {
		return nil, err
	}

	items, err := s.Changesets.ListPathIntents(ctx, slice.GetId(), time.Now().UTC())
	if err != nil {
		return nil, grpcError(err)
	}
	protoItems, err := s.toProtoPathIntents(ctx, items)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListPathIntentsResponse{
		Slice:   slice.GetRef(),
		Intents: protoItems,
	}, nil
}

func (s *ChangesetService) DeletePathIntent(ctx context.Context, req *corev1.DeletePathIntentRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	intentID := strings.TrimSpace(req.GetIntentId())
	if intentID == "" {
		return nil, status.Error(codes.InvalidArgument, "intent_id is required")
	}

	intent, err := s.Changesets.GetPathIntent(ctx, intentID)
	if err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Slices.Resolve(ctx, &corev1.SliceRef{Account: intent.Account, Slice: intent.Slice})
	if err != nil {
		return nil, grpcError(err)
	}

	if principal, ok := authctx.PrincipalFrom(ctx); ok && strings.TrimSpace(principal.SubjectID) == strings.TrimSpace(subjectID) && principal.HasKeyGrant() {
		if !principal.AllowsSlice(slice.GetId()) {
			return nil, status.Error(codes.PermissionDenied, "API key is not scoped to this slice")
		}
		if principal.MaxRole == "reader" {
			return nil, status.Error(codes.PermissionDenied, "reader API keys cannot delete path intents")
		}
	}

	allowed := intent.SubjectID == subjectID
	if !allowed && s.Auth != nil {
		controlled, ctrlErr := isControllerOfSubject(ctx, s.Auth, subjectID, intent.SubjectID)
		if ctrlErr != nil {
			return nil, grpcError(ctrlErr)
		}
		if controlled {
			// Ensure controller either holds ActionPropose directly or via delegation on the slice.
			if err := authorizeWithDelegation(ctx, s.Auth, subjectID, intent.SubjectID, slice, authz.ActionPropose); err == nil {
				allowed = true
			}
		}
	}
	if !allowed {
		if err := authorizeAllowArchived(ctx, s.Auth, subjectID, slice, authz.ActionManageMembers); err != nil {
			return nil, err
		}
	}

	if err := s.Changesets.DeletePathIntent(ctx, intent.ID); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *ChangesetService) toProtoPathIntents(ctx context.Context, items []storage.PathIntent) ([]*corev1.PathIntent, error) {
	if len(items) == 0 {
		return nil, nil
	}
	subjectIDs := make([]string, 0, len(items))
	seenSubjects := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.SubjectID == "" {
			continue
		}
		if _, ok := seenSubjects[item.SubjectID]; !ok {
			seenSubjects[item.SubjectID] = struct{}{}
			subjectIDs = append(subjectIDs, item.SubjectID)
		}
	}

	var subjects map[string]storage.Subject
	var usernames map[string]string
	if s.Auth != nil && len(subjectIDs) > 0 {
		var err error
		subjects, err = s.Auth.SubjectsByIDs(ctx, subjectIDs)
		if err != nil {
			return nil, err
		}
		needNames := slices.Clone(subjectIDs)
		for _, subj := range subjects {
			ctrl := strings.TrimSpace(subj.ControllerSubjectID)
			if ctrl != "" && ctrl != subj.ID {
				if _, ok := seenSubjects[ctrl]; !ok {
					seenSubjects[ctrl] = struct{}{}
					needNames = append(needNames, ctrl)
				}
			}
		}
		usernames, err = newSubjectDirectory(s.Auth).usernames(ctx, needNames...)
		if err != nil {
			return nil, err
		}
	}

	out := make([]*corev1.PathIntent, 0, len(items))
	for _, item := range items {
		username := usernames[item.SubjectID]
		var ctrlID, ctrlUsername string
		if subj, ok := subjects[item.SubjectID]; ok {
			ctrl := strings.TrimSpace(subj.ControllerSubjectID)
			if ctrl != "" && ctrl != item.SubjectID {
				ctrlID = ctrl
				ctrlUsername = usernames[ctrl]
			}
		}
		out = append(out, &corev1.PathIntent{
			Id:                  item.ID,
			Slice:               &corev1.SliceRef{Account: item.Account, Slice: item.Slice},
			SliceId:             item.SliceID,
			SubjectId:           item.SubjectID,
			Username:            username,
			ControllerSubjectId: ctrlID,
			ControllerUsername:  ctrlUsername,
			Pattern:             item.Pattern,
			Message:             item.Message,
			ChangesetId:         item.ChangesetID,
			ExpiresAt:           item.ExpiresAt.UTC().Format(time.RFC3339),
			CreatedAt:           item.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (s *ChangesetService) linkMatchingIntents(ctx context.Context, sliceID, authorSubjectID, changesetID, title, description string, changedPaths []string) {
	linkMatchingIntentsForStore(ctx, s.Changesets, sliceID, authorSubjectID, changesetID, title, description, changedPaths)
}

func linkMatchingIntentsForStore(ctx context.Context, store storage.ChangesetStore, sliceID, authorSubjectID, changesetID, title, description string, changedPaths []string) {
	if store == nil || sliceID == "" || authorSubjectID == "" || changesetID == "" {
		return
	}
	active, err := store.ListPathIntents(ctx, sliceID, time.Now().UTC())
	if err != nil || len(active) == 0 {
		return
	}
	mentioned := intents.ExtractMentionedIntentIDs(title, description)
	if len(mentioned) == 0 && len(changedPaths) == 0 {
		return
	}
	var matchIDs []string
	for _, item := range active {
		if item.SubjectID != authorSubjectID || item.ChangesetID != "" {
			continue
		}
		if slices.Contains(mentioned, item.ID) || len(intents.OverlappingPaths(item.Pattern, changedPaths)) > 0 {
			matchIDs = append(matchIDs, item.ID)
		}
	}
	if len(matchIDs) == 0 {
		return
	}
	_ = store.LinkPathIntentsToChangeset(ctx, matchIDs, authorSubjectID, sliceID, changesetID)
}

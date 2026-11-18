package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

const advisoryOverlapTimeout = 2 * time.Second

type overlapWarningOutput struct {
	Kind         string   `json:"kind"`
	ID           string   `json:"id"`
	Owner        string   `json:"owner,omitempty"`
	Pattern      string   `json:"pattern,omitempty"`
	Message      string   `json:"message,omitempty"`
	ExpiresAt    string   `json:"expires_at,omitempty"`
	MatchedPaths []string `json:"matched_paths,omitempty"`
	Summary      string   `json:"summary"`
}

func excludeChangesetIDsFromStack(currentChangesetID string, stack *corev1.ChangesetStack) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(currentChangesetID)
	if stack != nil {
		for _, entry := range stack.GetEntries() {
			add(entry.GetChangesetId())
			if entry.GetChangeset() != nil {
				add(entry.GetChangeset().GetId())
			}
		}
	}
	return ids
}

func (r Runner) bestEffortSubjectID(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig) string {
	if s := strings.TrimSpace(cfg.SubjectID); s != "" {
		return s
	}
	if strings.TrimSpace(cfg.Token) == "" || conn == nil {
		return ""
	}
	res, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.GetSubjectId())
}

func (r Runner) fetchAdvisorySliceState(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, sliceRef *corev1.SliceRef) ([]intents.ActiveIntent, []intents.OpenChangeset, string, time.Time) {
	now := time.Now().UTC()
	if conn == nil || sliceRef == nil || strings.TrimSpace(sliceRef.GetAccount()) == "" || strings.TrimSpace(sliceRef.GetSlice()) == "" {
		return nil, nil, "", now
	}
	callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), advisoryOverlapTimeout)
	defer cancel()

	subjectID := r.bestEffortSubjectID(callCtx, conn, cfg)
	csClient := corev1.NewChangesetServiceClient(conn)

	var active []intents.ActiveIntent
	if intentResp, err := csClient.ListPathIntents(callCtx, &corev1.ListPathIntentsRequest{Slice: sliceRef}); err == nil {
		for _, item := range intentResp.GetIntents() {
			exp, _ := time.Parse(time.RFC3339, item.GetExpiresAt())
			active = append(active, intents.ActiveIntent{
				ID:               item.GetId(),
				SliceID:          item.GetSliceId(),
				Pattern:          item.GetPattern(),
				Message:          item.GetMessage(),
				CreatorSubjectID: item.GetSubjectId(),
				CreatorName:      formatIntentActorLabel(item.GetUsername(), item.GetControllerUsername(), item.GetSubjectId()),
				ChangesetID:      item.GetChangesetId(),
				ExpiresAt:        exp,
			})
		}
	}

	var openCS []intents.OpenChangeset
	if csResp, err := csClient.ListChangesets(callCtx, &corev1.ListChangesetsRequest{
		AuthoringSlice: sliceRef,
		Statuses:       []string{"draft"},
		Summary:        true,
		Limit:          200,
	}); err == nil {
		for _, cs := range csResp.GetChangesets() {
			idLabel := firstNonEmpty(displayChangesetID(cs), model.ShortChangesetID(cs.GetId()), cs.GetId())
			openCS = append(openCS, intents.OpenChangeset{
				ID:            cs.GetId(),
				Title:         cs.GetTitle(),
				Author:        firstNonEmpty(cs.GetAuthor(), idLabel),
				AffectedPaths: cs.GetAffectedPaths(),
			})
		}
	}

	return active, openCS, subjectID, now
}

func (r Runner) collectPathOverlapWarnings(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, sliceRef *corev1.SliceRef, targetPaths, excludeChangesetIDs []string) []overlapWarningOutput {
	if len(targetPaths) == 0 || conn == nil || sliceRef == nil {
		return nil
	}
	active, openCS, subjectID, now := r.fetchAdvisorySliceState(ctx, conn, cfg, sliceRef)
	if len(active) == 0 && len(openCS) == 0 {
		return nil
	}
	rawWarnings := intents.FindPathOverlaps(targetPaths, active, openCS, intents.OverlapOptions{
		ExcludeSubjectID:    subjectID,
		ExcludeChangesetIDs: excludeChangesetIDs,
		Now:                 now,
	})
	return toOverlapWarningOutputs(rawWarnings, now)
}

func (r Runner) collectPatternOverlapWarnings(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, sliceRef *corev1.SliceRef, candidatePattern string, excludeChangesetIDs []string) []overlapWarningOutput {
	if strings.TrimSpace(candidatePattern) == "" || conn == nil || sliceRef == nil {
		return nil
	}
	active, openCS, subjectID, now := r.fetchAdvisorySliceState(ctx, conn, cfg, sliceRef)
	if len(active) == 0 && len(openCS) == 0 {
		return nil
	}
	rawWarnings, err := intents.FindPatternOverlaps(candidatePattern, active, openCS, intents.OverlapOptions{
		ExcludeSubjectID:    subjectID,
		ExcludeChangesetIDs: excludeChangesetIDs,
		Now:                 now,
	})
	if err != nil {
		return nil
	}
	return toOverlapWarningOutputs(rawWarnings, now)
}

func toOverlapWarningOutputs(warnings []intents.OverlapWarning, now time.Time) []overlapWarningOutput {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]overlapWarningOutput, 0, len(warnings))
	for _, w := range warnings {
		displayID := w.ID
		if w.Kind == "changeset" {
			displayID = firstNonEmpty(model.ShortChangesetID(w.ID), w.ID)
		}
		formatted := w
		formatted.ID = displayID
		var expStr string
		if !w.ExpiresAt.IsZero() {
			expStr = w.ExpiresAt.UTC().Format(time.RFC3339)
		}
		out = append(out, overlapWarningOutput{
			Kind:         w.Kind,
			ID:           w.ID,
			Owner:        w.Owner,
			Pattern:      w.Pattern,
			Message:      w.Message,
			ExpiresAt:    expStr,
			MatchedPaths: w.MatchedPaths,
			Summary:      intents.FormatWarningLine(formatted, now),
		})
	}
	return out
}

func printOverlapWarnings(w io.Writer, warnings []overlapWarningOutput) {
	for _, item := range warnings {
		if strings.TrimSpace(item.Summary) != "" {
			fmt.Fprintln(w, item.Summary)
		}
	}
}

func formatIntentActorLabel(username, controllerUsername, fallbackSubjectID string) string {
	actor := strings.TrimSpace(username)
	if actor == "" {
		actor = strings.TrimSpace(fallbackSubjectID)
	}
	ctrl := strings.TrimSpace(controllerUsername)
	if ctrl != "" && ctrl != actor {
		return fmt.Sprintf("%s (for %s)", actor, ctrl)
	}
	return actor
}

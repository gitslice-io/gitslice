package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type pathIntentOutput struct {
	ID                  string                 `json:"id"`
	Slice               string                 `json:"slice"`
	SliceID             string                 `json:"slice_id,omitempty"`
	SubjectID           string                 `json:"subject_id"`
	Username            string                 `json:"username,omitempty"`
	ControllerSubjectID string                 `json:"controller_subject_id,omitempty"`
	ControllerUsername  string                 `json:"controller_username,omitempty"`
	Pattern             string                 `json:"pattern"`
	Message             string                 `json:"message,omitempty"`
	ChangesetID         string                 `json:"changeset_id,omitempty"`
	ExpiresAt           string                 `json:"expires_at"`
	CreatedAt           string                 `json:"created_at,omitempty"`
	OverlapWarnings     []overlapWarningOutput `json:"overlap_warnings,omitempty"`
}

func (r Runner) intentCommand(opts *commandOptions) *cobra.Command {
	var (
		sliceFlag   string
		ttlRaw      string
		message     string
		changesetID string
	)

	intentCmd := &cobra.Command{
		Use:   "intent <pattern>",
		Short: "Declare, list, or clear advisory path intents on a slice",
		Long: "Declare an advisory path or glob reservation so other agents and teammates see overlap warnings in gs status, gs sync, and gs create:\n" +
			"  gs intent 'acme/payment/src/billing/**' --for 2h -m \"refactor billing\"\n" +
			"  gs intent 'src/billing/**' --for 2h -m \"refactor billing\"   # inside a slice workspace\n" +
			"  gs intent list [account/slice]\n" +
			"  gs intent clear [<id-or-glob>] [--all]",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return userError(
					"intent_pattern_required",
					"path or glob pattern is required",
					"Usage: gs intent '<account>/<slice>/<glob>' [--for 2h] [-m \"reason\"] [--changeset <id>], or run gs intent list / gs intent clear.",
				)
			}
			if len(args) > 1 {
				return userError(
					"unexpected_arguments",
					fmt.Sprintf("gs intent takes 1 pattern argument, got %d", len(args)),
					"Quote glob patterns so your shell does not expand them: gs intent 'src/billing/**' -m \"reason\".",
				)
			}
			return r.runIntentCreate(cmd.Context(), *opts, sliceFlag, args[0], ttlRaw, message, changesetID)
		},
	}
	intentCmd.PersistentFlags().StringVar(&sliceFlag, "slice", "", "target slice (account/slice or account:slice); defaults to current workspace")
	intentCmd.Flags().StringVar(&ttlRaw, "for", "2h", "intent lifetime (for example 30m, 2h, 1d; max 7d)")
	intentCmd.Flags().StringVarP(&message, "message", "m", "", "optional note describing planned work")
	intentCmd.Flags().StringVar(&changesetID, "changeset", "", "optional changeset ID to link (clears when landed or abandoned)")

	listCmd := &cobra.Command{
		Use:     "list [account/slice]",
		Aliases: []string{"ls"},
		Short:   "List active advisory path intents on a slice",
		Args:    maxArgs(1, "gs intent list [account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawSlice := sliceFlag
			if len(args) == 1 {
				rawSlice = args[0]
			}
			return r.runIntentList(cmd.Context(), *opts, rawSlice)
		},
	}

	var clearAll bool
	clearCmd := &cobra.Command{
		Use:     "clear [<id-or-glob>]",
		Aliases: []string{"rm", "delete"},
		Short:   "Clear one of your advisory path intents by ID or pattern, or --all",
		Args:    maxArgs(1, "gs intent clear [<id-or-glob>] [--all]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			return r.runIntentClear(cmd.Context(), *opts, sliceFlag, target, clearAll)
		},
	}
	clearCmd.Flags().BoolVar(&clearAll, "all", false, "clear all of your active intents on the target slice")

	intentCmd.AddCommand(listCmd, clearCmd)
	return intentCmd
}

func (r Runner) runIntentCreate(ctx context.Context, opts commandOptions, sliceFlag, rawPattern, ttlRaw, message, changesetID string) error {
	ttl := intents.DefaultTTL
	if strings.TrimSpace(ttlRaw) != "" {
		parsed, err := intents.ParseDuration(ttlRaw)
		if err != nil {
			return userError("invalid_duration", err.Error(), "Pass a positive duration up to 7d, such as 30m, 2h, or 1d.")
		}
		ttl = parsed
	}

	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	sliceRef, canonicalPattern, err := r.resolveIntentTarget(callCtx, cfg, conn, sliceFlag, rawPattern)
	if err != nil {
		return err
	}

	normPattern, err := intents.NormalizePattern(canonicalPattern)
	if err != nil {
		return userError("invalid_intent_pattern", err.Error(), "Pass a slice-relative glob (e.g. 'src/billing/**') or '<account>/<slice>/<glob>'.")
	}

	csID := strings.TrimSpace(changesetID)
	var excludeCS []string
	if csID == "" {
		if state, stErr := r.readWorkspaceState(); stErr == nil && state.CurrentChangesetID != "" {
			if ws, wsErr := r.readWorkspaceConfig(); wsErr == nil && ws.Account == sliceRef.GetAccount() && ws.Slice == sliceRef.GetSlice() {
				excludeCS = append(excludeCS, state.CurrentChangesetID)
			}
		}
	} else {
		excludeCS = append(excludeCS, csID)
	}

	warnings := r.collectPatternOverlapWarnings(ctx, conn, cfg, sliceRef, normPattern, excludeCS)

	created, err := corev1.NewChangesetServiceClient(conn).CreatePathIntent(callCtx, &corev1.CreatePathIntentRequest{
		Slice:       sliceRef,
		Pattern:     normPattern,
		Message:     strings.TrimSpace(message),
		TtlSeconds:  int64(ttl / time.Second),
		ChangesetId: csID,
	})
	if err != nil {
		return intentRPCError(err)
	}

	out := pathIntentFromProto(created)
	out.OverlapWarnings = warnings
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		exp, _ := time.Parse(time.RFC3339, out.ExpiresAt)
		rem := intents.FormatRemaining(time.Until(exp))
		if out.Message != "" {
			fmt.Fprintf(r.Stdout, "declared intent %s on %s (%s, %s left): %q\n", out.ID, out.Pattern, out.Slice, rem, out.Message)
		} else {
			fmt.Fprintf(r.Stdout, "declared intent %s on %s (%s, %s left)\n", out.ID, out.Pattern, out.Slice, rem)
		}
		printOverlapWarnings(r.Stdout, warnings)
	}
	return nil
}

func (r Runner) runIntentList(ctx context.Context, opts commandOptions, rawSlice string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	sliceRef, err := r.resolveIntentSliceRef(callCtx, cfg, conn, rawSlice)
	if err != nil {
		return err
	}

	resp, err := corev1.NewChangesetServiceClient(conn).ListPathIntents(callCtx, &corev1.ListPathIntentsRequest{
		Slice: sliceRef,
	})
	if err != nil {
		return intentRPCError(err)
	}

	items := make([]pathIntentOutput, 0, len(resp.GetIntents()))
	for _, item := range resp.GetIntents() {
		items = append(items, pathIntentFromProto(item))
	}
	label := sliceRefLabel(resp.GetSlice())
	if label == "" {
		label = sliceRefLabel(sliceRef)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"slice":   label,
			"intents": items,
		})
	}
	if opts.Quiet {
		return nil
	}
	if len(items) == 0 {
		fmt.Fprintf(r.Stdout, "no active path intents on %s\n", label)
		return nil
	}
	now := time.Now().UTC()
	for _, item := range items {
		actor := formatIntentActorLabel(item.Username, item.ControllerUsername, item.SubjectID)
		exp, _ := time.Parse(time.RFC3339, item.ExpiresAt)
		rem := intents.FormatRemaining(exp.Sub(now))
		line := fmt.Sprintf("%s\t%s\t%s\t%s left", item.ID, item.Pattern, actor, rem)
		if item.ChangesetID != "" {
			line += fmt.Sprintf("\t[%s]", item.ChangesetID)
		}
		if item.Message != "" {
			line += fmt.Sprintf("\t%q", item.Message)
		}
		fmt.Fprintln(r.Stdout, line)
	}
	return nil
}

func (r Runner) runIntentClear(ctx context.Context, opts commandOptions, sliceFlag, target string, clearAll bool) error {
	target = strings.TrimSpace(target)
	if clearAll && target != "" {
		return userError("conflicting_flags", "cannot combine a target with --all", "Pass either gs intent clear <id-or-glob> or gs intent clear --all.")
	}
	if !clearAll && target == "" {
		return userError("intent_target_required", "pass an intent ID, a glob pattern, or --all", "Usage: gs intent clear [<id-or-glob>] [--all]")
	}

	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	csClient := corev1.NewChangesetServiceClient(conn)

	// Direct delete by explicit intent ID ("int_...").
	if !clearAll && strings.HasPrefix(target, "int_") && !strings.Contains(target, "/") {
		if _, err := csClient.DeletePathIntent(callCtx, &corev1.DeletePathIntentRequest{IntentId: target}); err != nil {
			return intentRPCError(err)
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, map[string]any{
				"deleted_ids": []string{target},
				"deleted":     1,
			})
		}
		if !opts.Quiet {
			fmt.Fprintf(r.Stdout, "cleared intent %s\n", target)
		}
		return nil
	}

	subjectID := r.bestEffortSubjectID(callCtx, conn, cfg)
	var sliceRef *corev1.SliceRef
	var normPattern string
	if clearAll {
		sliceRef, err = r.resolveIntentSliceRef(callCtx, cfg, conn, sliceFlag)
		if err != nil {
			return err
		}
	} else {
		sliceRef, normPattern, err = r.resolveIntentTarget(callCtx, cfg, conn, sliceFlag, target)
		if err != nil {
			return err
		}
		normPattern, err = intents.NormalizePattern(normPattern)
		if err != nil {
			return userError("invalid_intent_pattern", err.Error(), "Pass an intent ID (int_...) or a valid path/glob pattern.")
		}
	}

	listResp, err := csClient.ListPathIntents(callCtx, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil {
		return intentRPCError(err)
	}

	deletedIDs := []string{}
	for _, item := range listResp.GetIntents() {
		ownedByCaller := subjectID != "" && (item.GetSubjectId() == subjectID || item.GetControllerSubjectId() == subjectID)
		if !ownedByCaller {
			continue
		}
		if !clearAll && item.GetPattern() != normPattern && !intents.PatternsOverlap(normPattern, item.GetPattern()) {
			continue
		}
		if _, err := csClient.DeletePathIntent(callCtx, &corev1.DeletePathIntentRequest{IntentId: item.GetId()}); err != nil {
			return intentRPCError(err)
		}
		deletedIDs = append(deletedIDs, item.GetId())
	}

	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"slice":       sliceRefLabel(sliceRef),
			"deleted_ids": deletedIDs,
			"deleted":     len(deletedIDs),
		})
	}
	if !opts.Quiet {
		if len(deletedIDs) == 0 {
			fmt.Fprintf(r.Stdout, "no matching intents to clear on %s\n", sliceRefLabel(sliceRef))
		} else {
			fmt.Fprintf(r.Stdout, "cleared %d intent(s): %s\n", len(deletedIDs), strings.Join(deletedIDs, ", "))
		}
	}
	return nil
}

func (r Runner) resolveIntentSliceRef(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, rawSlice string) (*corev1.SliceRef, error) {
	rawSlice = strings.TrimSpace(rawSlice)
	if rawSlice != "" {
		return r.resolveSliceRefInput(ctx, cfg, conn, rawSlice)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil || strings.TrimSpace(ws.Account) == "" || strings.TrimSpace(ws.Slice) == "" {
		return nil, userError(
			"slice_required",
			"slice is required outside a workspace",
			"Pass <account>/<slice> or --slice <account/slice>, or run inside a slice checkout.",
		)
	}
	return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, nil
}

func (r Runner) resolveIntentTarget(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceFlag, rawTarget string) (*corev1.SliceRef, string, error) {
	raw := strings.TrimSpace(rawTarget)
	if raw == "" {
		return nil, "", userError("intent_pattern_required", "path or glob pattern is required", "Usage: gs intent '<account>/<slice>/<glob>'")
	}
	if strings.Contains(raw, `\`) {
		return nil, "", userError("invalid_intent_pattern", "intent pattern cannot contain backslashes", "Use forward slashes '/' in intent patterns.")
	}
	if strings.HasPrefix(raw, "!") {
		return nil, "", userError("invalid_intent_pattern", "intent pattern negation ('!') is not supported", "Pass a positive path or glob pattern.")
	}

	// 1. Explicit --slice flag.
	if strings.TrimSpace(sliceFlag) != "" {
		sliceRef, err := r.resolveSliceRefInput(ctx, cfg, conn, sliceFlag)
		if err != nil {
			return nil, "", err
		}
		pat, err := r.expandPatternAgainstSlice(ctx, conn, sliceRef, nil, raw)
		return sliceRef, pat, err
	}

	ws, wsErr := r.readWorkspaceConfig()
	hasWS := wsErr == nil && strings.TrimSpace(ws.Account) != "" && strings.TrimSpace(ws.Slice) != ""

	// 2. Canonical path starting with '/'.
	if strings.HasPrefix(raw, "/") {
		if hasWS {
			if _, vErr := intents.ValidateSlicePattern(raw, ws.IncludedPaths); vErr == nil {
				return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, raw, nil
			}
		}
		segs := splitNonEmptySlash(raw)
		if len(segs) < 2 {
			return nil, "", userError("invalid_intent_pattern", "canonical intent pattern must start with /<account>/<slice>", "Example: gs intent '/acme/payment/src/**'")
		}
		sliceRef, err := parseSliceRef(segs[0] + ":" + segs[1])
		if err != nil {
			return nil, "", err
		}
		return sliceRef, raw, nil
	}

	// 3. Explicit account:slice/... syntax with colon.
	firstSeg, rest, hasSlash := strings.Cut(raw, "/")
	if strings.Contains(firstSeg, ":") {
		sliceRef, err := parseSliceRef(firstSeg)
		if err != nil {
			return nil, "", err
		}
		if !hasSlash || strings.TrimSpace(rest) == "" {
			pat, err := r.expandPatternAgainstSlice(ctx, conn, sliceRef, nil, "**")
			return sliceRef, pat, err
		}
		pat, err := r.expandPatternAgainstSlice(ctx, conn, sliceRef, nil, rest)
		return sliceRef, pat, err
	}

	// 4. Inside a workspace checkout.
	if hasWS {
		wsRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
		// Check if "/"+raw already falls inside this workspace's included paths
		// (handles 'account/slice/glob' inside the matching workspace).
		if _, vErr := intents.ValidateSlicePattern("/"+raw, ws.IncludedPaths); vErr == nil {
			return wsRef, "/" + raw, nil
		}
		if raw == ws.Account+"/"+ws.Slice {
			return wsRef, "/" + raw + "/**", nil
		}

		// If raw looks like '<other-account>/<other-slice>/<glob>' and the first
		// segment does not exist as a local directory in this workspace, check if
		// that remote slice exists and owns "/"+raw.
		segs := splitNonEmptySlash(raw)
		if len(segs) >= 3 && !strings.ContainsAny(segs[0], "*?[{.") && !strings.ContainsAny(segs[1], "*?[{.") {
			if !r.localSegmentExistsInWorkspace(ws, segs[0]) {
				if otherRef, pErr := parseSliceRef(segs[0] + ":" + segs[1]); pErr == nil {
					if otherSlice, sErr := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: otherRef}); sErr == nil && otherSlice != nil {
						var inc []string
						if otherSlice.GetDefinition() != nil {
							inc = otherSlice.GetDefinition().GetIncludedPaths()
						}
						if _, vErr := intents.ValidateSlicePattern("/"+raw, inc); vErr == nil {
							return otherRef, "/" + raw, nil
						}
					}
				}
			}
		}

		pat, err := r.expandPatternAgainstSlice(ctx, conn, wsRef, ws.IncludedPaths, raw)
		return wsRef, pat, err
	}

	// 5. Outside any workspace: expect '<account>/<slice>/<glob>' (or '<account>/<slice>').
	segs := splitNonEmptySlash(raw)
	if len(segs) < 2 {
		return nil, "", userError(
			"slice_required",
			"outside a workspace, pattern must start with <account>/<slice>/",
			"Example: gs intent 'acme/payment/src/billing/**' --for 2h -m \"refactor\"",
		)
	}
	sliceRef, err := parseSliceRef(segs[0] + ":" + segs[1])
	if err != nil {
		return nil, "", err
	}
	if len(segs) == 2 && !strings.HasSuffix(raw, "/") {
		return sliceRef, "/" + segs[0] + "/" + segs[1] + "/**", nil
	}
	return sliceRef, "/" + raw, nil
}

func (r Runner) expandPatternAgainstSlice(ctx context.Context, conn *grpc.ClientConn, sliceRef *corev1.SliceRef, knownIncluded []string, raw string) (string, error) {
	if strings.HasPrefix(raw, "/") {
		return raw, nil
	}
	trailingSlash := strings.HasSuffix(raw, "/")
	cleanRel := strings.TrimPrefix(raw, "./")
	cleanRel = strings.TrimLeft(cleanRel, "/")

	acctSlicePrefix := sliceRef.GetAccount() + "/" + sliceRef.GetSlice()
	if cleanRel == acctSlicePrefix {
		return "/" + acctSlicePrefix + "/**", nil
	}
	if strings.HasPrefix(cleanRel, acctSlicePrefix+"/") {
		return "/" + cleanRel, nil
	}
	colonPrefix := sliceRef.GetAccount() + ":" + sliceRef.GetSlice() + "/"
	if strings.HasPrefix(cleanRel, colonPrefix) {
		return "/" + acctSlicePrefix + "/" + strings.TrimPrefix(cleanRel, colonPrefix), nil
	}

	included := knownIncluded
	if len(included) == 0 && conn != nil {
		if resolved, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: sliceRef}); err == nil && resolved.GetDefinition() != nil {
			included = resolved.GetDefinition().GetIncludedPaths()
		}
	}

	if len(included) > 0 {
		if _, err := intents.ValidateSlicePattern("/"+cleanRel, included); err == nil {
			return "/" + cleanRel, nil
		}
		if len(included) > 1 {
			return "", userError(
				"ambiguous_intent_pattern",
				fmt.Sprintf("slice %s includes multiple path roots; pass a full path under one of %s", sliceRefLabel(sliceRef), strings.Join(included, ", ")),
				"Example: gs intent '"+strings.TrimRight(included[0], "/")+"/src/**'",
			)
		}
		root, err := paths.CanonicalPrefix(included[0])
		if err == nil {
			joined := strings.TrimRight(root, "/") + "/" + strings.TrimRight(cleanRel, "/")
			if trailingSlash && !strings.HasSuffix(joined, "/") {
				joined += "/"
			}
			return joined, nil
		}
	}

	joined := "/" + acctSlicePrefix + "/" + strings.TrimRight(cleanRel, "/")
	if trailingSlash && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined, nil
}

func (r Runner) localSegmentExistsInWorkspace(ws WorkspaceConfig, seg string) bool {
	root, err := r.workspaceRoot()
	if err != nil {
		return false
	}
	if st, err := os.Stat(filepath.Join(root, seg)); err == nil && st.IsDir() {
		return true
	}
	if len(ws.IncludedPaths) == 1 {
		incRel := strings.TrimPrefix(ws.IncludedPaths[0], "/")
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(incRel), seg)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func splitNonEmptySlash(s string) []string {
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pathIntentFromProto(item *corev1.PathIntent) pathIntentOutput {
	if item == nil {
		return pathIntentOutput{}
	}
	return pathIntentOutput{
		ID:                  item.GetId(),
		Slice:               sliceRefLabel(item.GetSlice()),
		SliceID:             item.GetSliceId(),
		SubjectID:           item.GetSubjectId(),
		Username:            item.GetUsername(),
		ControllerSubjectID: item.GetControllerSubjectId(),
		ControllerUsername:  item.GetControllerUsername(),
		Pattern:             item.GetPattern(),
		Message:             item.GetMessage(),
		ChangesetID:         item.GetChangesetId(),
		ExpiresAt:           item.GetExpiresAt(),
		CreatedAt:           item.GetCreatedAt(),
	}
}

func intentRPCError(err error) error {
	st, ok := grpcstatus.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()
	switch st.Code() {
	case codes.InvalidArgument:
		return userError("invalid_intent", msg, "Check pattern syntax, slice bounds, and --for duration (max 7d).")
	case codes.PermissionDenied:
		return userError("permission_denied", msg, "Creating intents requires contributor access; clearing requires owning the intent or slice admin.")
	case codes.NotFound:
		return userError("not_found", msg, "Check the slice reference or intent ID.")
	case codes.FailedPrecondition, codes.Aborted:
		return userError("conflict", msg, "Ensure the target slice and linked changeset are open and writable.")
	case codes.Unauthenticated:
		return userError("unauthenticated", msg, "Run gs auth login or set GS_TOKEN.")
	default:
		return err
	}
}

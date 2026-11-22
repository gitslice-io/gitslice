package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/intents"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
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

	withSchemaOutput[pathIntentOutput](intentCmd)
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

	var out pathIntentOutput
	if err := r.withSession(ctx, func(s *Session) error {
		sliceRef, canonicalPattern, err := s.resolveIntentTarget(sliceFlag, rawPattern)
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

		warnings := r.collectPatternOverlapWarnings(ctx, s.Conn(), s.Config(), sliceRef, normPattern, excludeCS)

		created, err := s.Changeset().CreatePathIntent(s.Ctx(), &corev1.CreatePathIntentRequest{
			Slice:       sliceRef,
			Pattern:     normPattern,
			Message:     strings.TrimSpace(message),
			TtlSeconds:  int64(ttl / time.Second),
			ChangesetId: csID,
		})
		if err != nil {
			return intentRPCError(err)
		}

		out = pathIntentFromProto(created)
		out.OverlapWarnings = warnings
		return nil
	}); err != nil {
		return err
	}

	return writeSchemaOutput[pathIntentOutput](r, opts, out, func() error {
		exp, _ := time.Parse(time.RFC3339, out.ExpiresAt)
		rem := intents.FormatRemaining(time.Until(exp))
		if out.Message != "" {
			fmt.Fprintf(r.Stdout, "declared intent %s on %s (%s, %s left): %q\n", out.ID, out.Pattern, out.Slice, rem, out.Message)
		} else {
			fmt.Fprintf(r.Stdout, "declared intent %s on %s (%s, %s left)\n", out.ID, out.Pattern, out.Slice, rem)
		}
		printOverlapWarnings(r.Stdout, out.OverlapWarnings)
		return nil
	})
}

var intentSliceResolveOptions = SliceResolveOptions{
	FallbackToWorkspace: true,
	MissingCode:         "slice_required",
	MissingMessage:      "slice is required outside a workspace",
	MissingHint:         "Pass <account>/<slice> or --slice <account/slice>, or run inside a slice checkout.",
}

func (r Runner) runIntentList(ctx context.Context, opts commandOptions, rawSlice string) error {
	return r.withSliceSessionOpts(ctx, rawSlice, intentSliceResolveOptions, func(s *Session, sliceRef *corev1.SliceRef) error {
		resp, err := s.Changeset().ListPathIntents(s.Ctx(), &corev1.ListPathIntentsRequest{
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
		if opts.JSONOutput() {
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
	})
}

func (r Runner) runIntentClear(ctx context.Context, opts commandOptions, sliceFlag, target string, clearAll bool) error {
	target = strings.TrimSpace(target)
	if clearAll && target != "" {
		return userError("conflicting_flags", "cannot combine a target with --all", "Pass either gs intent clear <id-or-glob> or gs intent clear --all.")
	}
	if !clearAll && target == "" {
		return userError("intent_target_required", "pass an intent ID, a glob pattern, or --all", "Usage: gs intent clear [<id-or-glob>] [--all]")
	}

	return r.withSession(ctx, func(s *Session) error {
		csClient := s.Changeset()
		callCtx := s.Ctx()

		if !clearAll && strings.HasPrefix(target, "int_") && !strings.Contains(target, "/") {
			if _, err := csClient.DeletePathIntent(callCtx, &corev1.DeletePathIntentRequest{IntentId: target}); err != nil {
				return intentRPCError(err)
			}
			if opts.JSONOutput() {
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

		subjectID := r.bestEffortSubjectID(callCtx, s.Conn(), s.Config())
		var sliceRef *corev1.SliceRef
		var normPattern string
		var err error
		if clearAll {
			sliceRef, err = s.ResolveSliceRefWithOptions(sliceFlag, intentSliceResolveOptions)
			if err != nil {
				return err
			}
		} else {
			sliceRef, normPattern, err = s.resolveIntentTarget(sliceFlag, target)
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

		if opts.JSONOutput() {
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
	})
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

var intentErrorHints = domainErrorOverrides{
	codes.InvalidArgument: {
		Code: "invalid_intent",
		Hint: "Check pattern syntax, slice bounds, and --for duration (max 7d).",
	},
	codes.PermissionDenied: {
		Hint: "Creating intents requires contributor access; clearing requires owning the intent or slice admin.",
	},
	codes.NotFound: {
		Hint: "Check the slice reference or intent ID.",
	},
	codes.FailedPrecondition: {
		Code: "conflict",
		Hint: "Ensure the target slice and linked changeset are open and writable.",
	},
	codes.Aborted: {
		Code: "conflict",
		Hint: "Ensure the target slice and linked changeset are open and writable.",
	},
}

func intentRPCError(err error) error {
	return withDomainErrorHints(err, intentErrorHints)
}

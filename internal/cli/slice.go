package cli

import (
	"context"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (r Runner) runSliceCreate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, visibility string, requiredApprovals, requiredHumanApprovals int, requiredChecks []string, approvers, description string) error {
	includedPaths, err := expandSliceIncludedPaths(includedPaths)
	if err != nil {
		return err
	}
	if requiredApprovals < 0 {
		return userError("invalid_args", "required approvals must be zero or greater", "Pass --required-approvals 0 or higher.")
	}
	if requiredHumanApprovals < 0 {
		return userError("invalid_args", "required human approvals must be zero or greater", "Pass --required-human-approvals 0 or higher.")
	}
	return r.withSliceSession(ctx, sliceRef, false, func(s *Session, ref *corev1.SliceRef) error {
		if len(includedPaths) == 0 {
			includedPaths = defaultSliceIncludedPaths(ref)
		}
		slice, err := s.Slice().CreateSlice(s.Ctx(), &corev1.CreateSliceRequest{
			Ref:                    ref,
			IncludedPaths:          includedPaths,
			Visibility:             visibility,
			RequiredApprovals:      int32(requiredApprovals),
			RequiredHumanApprovals: int32(requiredHumanApprovals),
			RequiredChecks:         requiredChecks,
			Approvers:              strings.TrimSpace(approvers),
			Description:            description,
		})
		if err != nil {
			// A slice covers paths that hold files already; a new account's files
			// start in its home slice, which covers everything in the account.
			if message := grpcstatus.Convert(err).Message(); grpcstatus.Code(err) == codes.FailedPrecondition && strings.Contains(message, "included path does not exist") {
				return userError("included_path_missing", message,
					"A slice covers paths that already hold files. Add files there first, for example from a workspace of the account's home slice (gs init "+ref.Account+"/home), then create the slice; or work in "+ref.Account+"/home directly.")
			}
			return err
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, sliceToOutput(slice))
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "created slice %s\n", sliceRefLabel(slice.Ref))
		if link := webSliceResourceURL(slice.Ref); link != "" {
			fmt.Fprintf(r.Stdout, "view: %s\n", link)
		}
		return writeSliceText(r.Stdout, slice)
	})
}

func (r Runner) runSliceList(ctx context.Context, opts commandOptions, account string, sharedOnly, includeArchived bool) error {
	return r.withSession(ctx, func(s *Session) error {
		// Listing your own account also lists what others shared with you.
		defaultAccount := strings.TrimSpace(account) == ""
		var shared []sharedSliceOutput
		var err error
		if sharedOnly || defaultAccount {
			if shared, err = listSharedSlices(s.Ctx(), s.Slice()); err != nil {
				return err
			}
		}
		if sharedOnly {
			if opts.jsonOutput() {
				return r.writeJSONOutput(opts, map[string]any{"shared": nonNilShared(shared)})
			}
			if opts.Quiet {
				return nil
			}
			if len(shared) == 0 {
				fmt.Fprintln(r.Stdout, "no slices are shared with you")
				return nil
			}
			r.writeSharedSlicesText(shared)
			return nil
		}
		if strings.TrimSpace(account) == "" {
			account, err = r.defaultSliceAccount(s.Ctx(), s.Config(), s.Conn())
			if err != nil {
				return err
			}
		} else {
			account, err = normalizeCLISlug(account, "account")
			if err != nil {
				return err
			}
		}
		slices, err := listAllSlicesIncluding(s.Ctx(), s.Slice(), account, includeArchived)
		if err != nil {
			return err
		}
		out := make([]sliceOutput, 0, len(slices))
		for _, slice := range slices {
			out = append(out, sliceToOutput(slice))
		}
		if opts.jsonOutput() {
			payload := map[string]any{
				"account": account,
				"slices":  out,
			}
			if defaultAccount {
				payload["shared"] = nonNilShared(shared)
			}
			return r.writeJSONOutput(opts, payload)
		}
		if opts.Quiet {
			return nil
		}
		if len(out) == 0 {
			fmt.Fprintf(r.Stdout, "no slices found for account %s\n", account)
		} else {
			fmt.Fprintf(r.Stdout, "slices for account %s:\n", account)
			for _, slice := range out {
				if slice.Archived {
					fmt.Fprintf(r.Stdout, "  %s (archived)\n", slice.Ref)
				} else {
					fmt.Fprintf(r.Stdout, "  %s\n", slice.Ref)
				}
				fmt.Fprintf(r.Stdout, "    visibility: %s\n", slice.Visibility)
				fmt.Fprintf(r.Stdout, "    included paths: %s\n", strings.Join(slice.IncludedPaths, ", "))
				fmt.Fprintf(r.Stdout, "    required approvals: %d\n", slice.RequiredApprovals)
				if slice.RequiredHumanApprovals > 0 {
					fmt.Fprintf(r.Stdout, "    required human approvals: %d\n", slice.RequiredHumanApprovals)
				}
				if slice.Approvers != "" {
					fmt.Fprintf(r.Stdout, "    approvers: %s\n", slice.Approvers)
				}
				if len(slice.RequiredChecks) > 0 {
					fmt.Fprintf(r.Stdout, "    required checks: %s\n", strings.Join(slice.RequiredChecks, ", "))
				}
			}
		}
		if len(shared) > 0 {
			r.writeSharedSlicesText(shared)
		}
		return nil
	})
}

func nonNilShared(shared []sharedSliceOutput) []sharedSliceOutput {
	if shared == nil {
		return []sharedSliceOutput{}
	}
	return shared
}

func (r Runner) runSliceInfo(ctx context.Context, opts commandOptions, sliceRef string) error {
	slice, err := r.resolveSlice(ctx, sliceRef)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, sliceToOutput(slice))
	}
	if opts.Quiet {
		return nil
	}
	return writeSliceText(r.Stdout, slice)
}

func (r Runner) runSlicePaths(ctx context.Context, opts commandOptions, sliceRef string) error {
	slice, err := r.resolveSlice(ctx, sliceRef)
	if err != nil {
		return err
	}
	out := sliceToOutput(slice)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"ref":            out.Ref,
			"included_paths": out.IncludedPaths,
		})
	}
	if opts.Quiet {
		return nil
	}
	for _, p := range out.IncludedPaths {
		fmt.Fprintln(r.Stdout, p)
	}
	return nil
}

func (r Runner) runSliceHistory(ctx context.Context, opts commandOptions, sliceRef string, pageSize int) error {
	if pageSize < 0 {
		return userError("invalid_args", "page size must be zero or greater", "Pass --page-size 0 or higher.")
	}
	return r.withSliceSession(ctx, sliceRef, false, func(s *Session, ref *corev1.SliceRef) error {
		slice, err := s.Slice().ResolveSlice(s.Ctx(), &corev1.ResolveSliceRequest{Ref: ref})
		if err != nil {
			return err
		}
		res, err := s.Slice().ListSliceDefinitionVersions(s.Ctx(), &corev1.ListSliceDefinitionVersionsRequest{
			SliceId:  slice.Id,
			PageSize: int32(pageSize),
		})
		if err != nil {
			return err
		}
		versions := make([]sliceDefinitionVersionOutput, 0, len(res.Versions))
		for _, version := range res.Versions {
			versions = append(versions, sliceDefinitionVersionToOutput(version))
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, map[string]any{
				"ref":      sliceRefLabel(slice.Ref),
				"slice_id": slice.Id,
				"versions": versions,
			})
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "history for %s:\n", sliceRefLabel(slice.Ref))
		if len(versions) == 0 {
			fmt.Fprintln(r.Stdout, "  no definition versions")
			return nil
		}
		for _, version := range versions {
			fmt.Fprintf(r.Stdout, "  version: %d\n", version.Version)
			fmt.Fprintf(r.Stdout, "    definition_hash: %s\n", version.DefinitionHash)
			fmt.Fprintf(r.Stdout, "    visibility: %s\n", version.Visibility)
			fmt.Fprintf(r.Stdout, "    included_paths: %s\n", strings.Join(version.IncludedPaths, ", "))
			fmt.Fprintf(r.Stdout, "    required_approvals: %d\n", version.RequiredApprovals)
			fmt.Fprintf(r.Stdout, "    required_human_approvals: %d\n", version.RequiredHumanApprovals)
			if version.Approvers != "" {
				fmt.Fprintf(r.Stdout, "    approvers: %s\n", version.Approvers)
			}
			if len(version.RequiredChecks) > 0 {
				fmt.Fprintf(r.Stdout, "    required_checks: %s\n", strings.Join(version.RequiredChecks, ", "))
			} else {
				fmt.Fprintln(r.Stdout, "    required_checks: none")
			}
			if version.CreatedBy != "" {
				fmt.Fprintf(r.Stdout, "    created_by: %s\n", version.CreatedBy)
			} else {
				fmt.Fprintln(r.Stdout, "    created_by: unknown")
			}
			fmt.Fprintf(r.Stdout, "    created_at: %s\n", version.CreatedAt)
		}
		return nil
	})
}

func (r Runner) runSliceUpdate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, includesChanged bool, visibility string, visibilityChanged bool, requiredApprovals int, requiredApprovalsChanged bool, requiredHumanApprovals int, requiredHumanApprovalsChanged bool, requiredChecks []string, requiredChecksChanged bool, clearRequiredChecks bool, approvers string, approversChanged bool, description *string, mergeOnSubmit *bool) error {
	if requiredChecksChanged && clearRequiredChecks {
		return userError("invalid_args", "required checks can be replaced or cleared, not both", "Use --required-check or --clear-required-checks.")
	}
	definitionChanged := includesChanged || visibilityChanged || requiredApprovalsChanged || requiredHumanApprovalsChanged || requiredChecksChanged || clearRequiredChecks || approversChanged
	if !definitionChanged && description == nil && mergeOnSubmit == nil {
		return userError("invalid_args", "no slice updates requested", "Pass --include, --visibility, --required-approvals, --required-human-approvals, --required-check, --clear-required-checks, --approvers, --description, or --merge-on-submit.")
	}
	if requiredApprovals < 0 {
		return userError("invalid_args", "required approvals must be zero or greater", "Pass --required-approvals 0 or higher.")
	}
	if requiredHumanApprovals < 0 {
		return userError("invalid_args", "required human approvals must be zero or greater", "Pass --required-human-approvals 0 or higher.")
	}
	return r.withSliceSession(ctx, sliceRef, false, func(s *Session, ref *corev1.SliceRef) error {
		client := s.Slice()
		callCtx := s.Ctx()
		current, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
		if err != nil {
			return err
		}
		nextIncluded := append([]string{}, current.Definition.IncludedPaths...)
		if includesChanged {
			includedPaths, err = expandSliceIncludedPaths(includedPaths)
			if err != nil {
				return err
			}
			nextIncluded = includedPaths
		}
		nextVisibility := current.Definition.Visibility
		if visibilityChanged {
			nextVisibility = visibility
		}
		nextRequiredApprovals := current.Definition.RequiredApprovals
		if requiredApprovalsChanged {
			nextRequiredApprovals = int32(requiredApprovals)
		}
		nextRequiredHumanApprovals := current.Definition.RequiredHumanApprovals
		if requiredHumanApprovalsChanged {
			nextRequiredHumanApprovals = int32(requiredHumanApprovals)
		}
		nextRequiredChecks := append([]string{}, current.Definition.RequiredChecks...)
		if clearRequiredChecks {
			nextRequiredChecks = nil
		} else if requiredChecksChanged {
			nextRequiredChecks = requiredChecks
		}
		nextApprovers := current.Definition.Approvers
		if approversChanged {
			nextApprovers = strings.TrimSpace(approvers)
		}
		if definitionChanged {
			_, err = client.UpdateSliceDefinition(callCtx, &corev1.UpdateSliceDefinitionRequest{
				SliceId:                current.Id,
				ExpectedDefinitionHash: current.DefinitionHash,
				Definition: &corev1.SliceDefinition{
					IncludedPaths:          nextIncluded,
					Visibility:             nextVisibility,
					RequiredApprovals:      nextRequiredApprovals,
					RequiredHumanApprovals: nextRequiredHumanApprovals,
					RequiredChecks:         nextRequiredChecks,
					Approvers:              nextApprovers,
				},
			})
			if err != nil {
				return err
			}
		}
		if description != nil {
			if _, err := client.SetSliceDescription(callCtx, &corev1.SetSliceDescriptionRequest{Slice: ref, Description: *description}); err != nil {
				return err
			}
		}
		if mergeOnSubmit != nil {
			if _, err := client.SetSliceMergeOnSubmit(callCtx, &corev1.SetSliceMergeOnSubmitRequest{Slice: ref, Enabled: *mergeOnSubmit}); err != nil {
				return err
			}
		}
		updated, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
		if err != nil {
			return err
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, sliceToOutput(updated))
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "updated slice %s\n", sliceRefLabel(updated.Ref))
		if link := webSliceResourceURL(updated.Ref); link != "" {
			fmt.Fprintf(r.Stdout, "view: %s\n", link)
		}
		return writeSliceText(r.Stdout, updated)
	})
}

func (r Runner) runSliceDelete(ctx context.Context, opts commandOptions, sliceRef string, yes bool) error {
	if !yes {
		return userError("confirmation_required", "slice deletion requires --yes", "Run gs slice delete "+sliceRef+" --yes to confirm.")
	}
	return r.withSliceSession(ctx, sliceRef, false, func(s *Session, ref *corev1.SliceRef) error {
		slice, err := s.Slice().ResolveSlice(s.Ctx(), &corev1.ResolveSliceRequest{Ref: ref})
		if err != nil {
			return err
		}
		deleted, err := s.Slice().DeleteSlice(s.Ctx(), &corev1.DeleteSliceRequest{SliceId: slice.Id})
		if err != nil {
			return err
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, map[string]any{
				"slice_id":     slice.Id,
				"ref":          sliceRefLabel(slice.Ref),
				"history_kept": deleted.GetHistoryKept(),
			})
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "deleted slice %s\n", sliceRefLabel(slice.Ref))
		if deleted.GetHistoryKept() {
			fmt.Fprintln(r.Stdout, "its changesets remain, so its history stays readable on the paths it covered; the name stays reserved")
		}
		return nil
	})
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type sliceOutput struct {
	ID                string   `json:"id"`
	Ref               string   `json:"ref"`
	Account           string   `json:"account"`
	Slice             string   `json:"slice"`
	Version           int64    `json:"version"`
	Visibility        string   `json:"visibility"`
	CIDaemonID        string   `json:"ci_daemon_id,omitempty"`
	CIDaemonName      string   `json:"ci_daemon_name,omitempty"`
	IncludedPaths     []string `json:"included_paths"`
	RequiredApprovals int32    `json:"required_approvals"`
	RequiredChecks    []string `json:"required_checks"`
	DefinitionHash    string   `json:"definition_hash"`
	Archived          bool     `json:"archived,omitempty"`
	Description       string   `json:"description,omitempty"`
	FormerNames       []string `json:"former_names,omitempty"`
	WebURL            string   `json:"web_url,omitempty"`
}

type sliceDefinitionVersionOutput struct {
	SliceID           string   `json:"slice_id"`
	Version           int64    `json:"version"`
	DefinitionHash    string   `json:"definition_hash"`
	Visibility        string   `json:"visibility"`
	IncludedPaths     []string `json:"included_paths"`
	RequiredApprovals int32    `json:"required_approvals"`
	RequiredChecks    []string `json:"required_checks"`
	CreatedBy         string   `json:"created_by"`
	CreatedAt         string   `json:"created_at"`
}

func (r Runner) runSliceCreate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, visibility string, requiredApprovals int, requiredChecks []string, description string) error {
	includedPaths, err := expandSliceIncludedPaths(includedPaths)
	if err != nil {
		return err
	}
	if requiredApprovals < 0 {
		return userError("invalid_args", "required approvals must be zero or greater", "Pass --required-approvals 0 or higher.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	if len(includedPaths) == 0 {
		includedPaths = defaultSliceIncludedPaths(ref)
	}
	slice, err := corev1.NewSliceServiceClient(conn).CreateSlice(callCtx, &corev1.CreateSliceRequest{
		Ref:               ref,
		IncludedPaths:     includedPaths,
		Visibility:        visibility,
		RequiredApprovals: int32(requiredApprovals),
		RequiredChecks:    requiredChecks,
		Description:       description,
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
}

func (r Runner) runSliceList(ctx context.Context, opts commandOptions, account string, sharedOnly, includeArchived bool) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Listing your own account also lists what others shared with you.
	defaultAccount := strings.TrimSpace(account) == ""
	var shared []sharedSliceOutput
	if sharedOnly || defaultAccount {
		if shared, err = listSharedSlices(callCtx, corev1.NewSliceServiceClient(conn)); err != nil {
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
		account, err = r.defaultSliceAccount(callCtx, cfg, conn)
		if err != nil {
			return err
		}
	} else {
		account, err = normalizeCLISlug(account, "account")
		if err != nil {
			return err
		}
	}
	slices, err := listAllSlicesIncluding(callCtx, corev1.NewSliceServiceClient(conn), account, includeArchived)
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
			if len(slice.RequiredChecks) > 0 {
				fmt.Fprintf(r.Stdout, "    required checks: %s\n", strings.Join(slice.RequiredChecks, ", "))
			}
		}
	}
	if len(shared) > 0 {
		r.writeSharedSlicesText(shared)
	}
	return nil
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
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	client := corev1.NewSliceServiceClient(conn)
	slice, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	res, err := client.ListSliceDefinitionVersions(callCtx, &corev1.ListSliceDefinitionVersionsRequest{
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
}

func (r Runner) runSliceUpdate(ctx context.Context, opts commandOptions, sliceRef string, includedPaths []string, includesChanged bool, visibility string, visibilityChanged bool, requiredApprovals int, requiredApprovalsChanged bool, requiredChecks []string, requiredChecksChanged bool, clearRequiredChecks bool, description *string) error {
	if requiredChecksChanged && clearRequiredChecks {
		return userError("invalid_args", "required checks can be replaced or cleared, not both", "Use --required-check or --clear-required-checks.")
	}
	definitionChanged := includesChanged || visibilityChanged || requiredApprovalsChanged || requiredChecksChanged || clearRequiredChecks
	if !definitionChanged && description == nil {
		return userError("invalid_args", "no slice updates requested", "Pass --include, --visibility, --required-approvals, --required-check, --clear-required-checks, or --description.")
	}
	if requiredApprovals < 0 {
		return userError("invalid_args", "required approvals must be zero or greater", "Pass --required-approvals 0 or higher.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	client := corev1.NewSliceServiceClient(conn)
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
	nextRequiredChecks := append([]string{}, current.Definition.RequiredChecks...)
	if clearRequiredChecks {
		nextRequiredChecks = nil
	} else if requiredChecksChanged {
		nextRequiredChecks = requiredChecks
	}
	if definitionChanged {
		_, err = client.UpdateSliceDefinition(callCtx, &corev1.UpdateSliceDefinitionRequest{
			SliceId:                current.Id,
			ExpectedDefinitionHash: current.DefinitionHash,
			Definition: &corev1.SliceDefinition{
				IncludedPaths:     nextIncluded,
				Visibility:        nextVisibility,
				RequiredApprovals: nextRequiredApprovals,
				RequiredChecks:    nextRequiredChecks,
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
}

func (r Runner) runSliceDelete(ctx context.Context, opts commandOptions, sliceRef string, yes bool) error {
	if !yes {
		return userError("confirmation_required", "slice deletion requires --yes", "Run gs slice delete "+sliceRef+" --yes to confirm.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	client := corev1.NewSliceServiceClient(conn)
	slice, err := client.ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	deleted, err := client.DeleteSlice(callCtx, &corev1.DeleteSliceRequest{SliceId: slice.Id})
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
}

func defaultSliceIncludedPaths(ref *corev1.SliceRef) []string {
	if ref != nil && ref.Slice == "home" {
		return []string{"/" + ref.Account}
	}
	return []string{"/" + ref.Account + "/" + ref.Slice}
}

func expandSliceIncludedPaths(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, userError("invalid_include", "included path is required", "Pass each included path as --include /account/path, or separate multiple paths with commas.")
			}
			out = append(out, part)
		}
	}
	return out, nil
}

func writeSliceText(w io.Writer, slice *corev1.Slice) error {
	out := sliceToOutput(slice)
	fmt.Fprintf(w, "ref: %s\n", out.Ref)
	fmt.Fprintf(w, "id: %s\n", out.ID)
	fmt.Fprintf(w, "version: %d\n", out.Version)
	fmt.Fprintf(w, "visibility: %s\n", out.Visibility)
	if out.Description != "" {
		fmt.Fprintf(w, "description: %s\n", strings.ReplaceAll(out.Description, "\n", "\n  "))
	}
	if out.Archived {
		fmt.Fprintln(w, "archived: yes (read-only; gs slice unarchive restores it)")
	}
	if len(out.FormerNames) > 0 {
		fmt.Fprintf(w, "former_names: %s\n", strings.Join(out.FormerNames, ", "))
	}
	if out.CIDaemonID != "" {
		fmt.Fprintf(w, "ci_daemon_id: %s\n", out.CIDaemonID)
		if out.CIDaemonName != "" {
			fmt.Fprintf(w, "ci_daemon: %s\n", out.CIDaemonName)
		}
	} else {
		fmt.Fprintln(w, "ci_daemon_id: none")
	}
	fmt.Fprintf(w, "required_approvals: %d\n", out.RequiredApprovals)
	if len(out.RequiredChecks) > 0 {
		fmt.Fprintf(w, "required_checks: %s\n", strings.Join(out.RequiredChecks, ", "))
	} else {
		fmt.Fprintln(w, "required_checks: none")
	}
	fmt.Fprintf(w, "definition_hash: %s\n", out.DefinitionHash)
	fmt.Fprintln(w, "included_paths:")
	for _, p := range out.IncludedPaths {
		fmt.Fprintf(w, "  %s\n", p)
	}
	return nil
}

func sliceToOutput(slice *corev1.Slice) sliceOutput {
	if slice == nil {
		return sliceOutput{}
	}
	out := sliceOutput{
		ID:             slice.Id,
		DefinitionHash: slice.DefinitionHash,
		CIDaemonID:     slice.CiDaemonId,
		CIDaemonName:   slice.CiDaemonName,
		Archived:       slice.Archived,
		Description:    slice.Description,
		FormerNames:    append([]string(nil), slice.FormerNames...),
		WebURL:         webSliceResourceURL(slice.Ref),
	}
	if slice.Ref != nil {
		out.Account = slice.Ref.Account
		out.Slice = slice.Ref.Slice
		out.Ref = sliceRefLabel(slice.Ref)
	}
	if slice.Definition != nil {
		out.Version = slice.Definition.Version
		out.Visibility = slice.Definition.Visibility
		out.IncludedPaths = append([]string{}, slice.Definition.IncludedPaths...)
		out.RequiredApprovals = slice.Definition.RequiredApprovals
		out.RequiredChecks = append([]string{}, slice.Definition.RequiredChecks...)
	}
	return out
}

func sliceDefinitionVersionToOutput(version *corev1.SliceDefinitionVersion) sliceDefinitionVersionOutput {
	if version == nil {
		return sliceDefinitionVersionOutput{}
	}
	return sliceDefinitionVersionOutput{
		SliceID:           version.SliceId,
		Version:           version.Version,
		DefinitionHash:    version.DefinitionHash,
		Visibility:        version.Visibility,
		IncludedPaths:     append([]string{}, version.IncludedPaths...),
		RequiredApprovals: version.RequiredApprovals,
		RequiredChecks:    append([]string{}, version.RequiredChecks...),
		CreatedBy:         version.CreatedBy,
		CreatedAt:         version.CreatedAt,
	}
}

func sliceRefLabel(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return ref.Account + ":" + ref.Slice
}

func webSliceResourceURL(ref *corev1.SliceRef) string {
	if ref == nil || strings.TrimSpace(ref.Account) == "" || strings.TrimSpace(ref.Slice) == "" {
		return ""
	}
	return webResourceURL("/slices/" + url.PathEscape(ref.Account) + "/" + url.PathEscape(ref.Slice))
}

func parseSliceRef(value string) (*corev1.SliceRef, error) {
	accountPart, slicePart, ok := storage.SplitSliceHandle(value)
	if !ok {
		return nil, userError("invalid_slice_ref", "slice must be account:slice", "Pass a slice reference such as acme:payment.")
	}
	account, err := normalizeCLISlug(accountPart, "account")
	if err != nil {
		return nil, err
	}
	slug, err := normalizeCLISlug(slicePart, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func (r Runner) resolveSliceRefInput(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, value string) (*corev1.SliceRef, error) {
	value = strings.TrimSpace(value)
	// A qualified handle uses account:slice (or the legacy account:slice); a bare
	// token is a slice slug expanded against the signed-in account.
	if strings.ContainsAny(value, ":/") {
		return parseSliceRef(value)
	}
	slug, err := normalizeCLISlug(value, "slice")
	if err != nil {
		return nil, err
	}
	account, err := r.defaultSliceAccount(ctx, cfg, conn)
	if err != nil {
		var cmdErr commandError
		if errors.As(err, &cmdErr) && cmdErr.Code == "account_required" {
			return nil, userError("account_required", "account is required for bare slice ref "+slug, "Use account:slice or run gs auth status to check the signed-in account.")
		}
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func normalizeCLISlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", userError("invalid_slug", name+" is required", "Use lower-case letters, numbers, '-' or '_'.")
	}
	if len(value) > 63 {
		return "", userError("invalid_slug", name+" must be 63 characters or fewer", "Use a shorter "+name+".")
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", userError("invalid_slug", name+" must not start or end with '-'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", userError("invalid_slug", name+" may contain only letters, numbers, '-' or '_'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	return value, nil
}

// listAllSlices reads every page of an account's slices.
func listAllSlices(ctx context.Context, client corev1.SliceServiceClient, account string) ([]*corev1.Slice, error) {
	return listAllSlicesIncluding(ctx, client, account, false)
}

// listAllSlicesIncluding lists an account's slices; archived ones only when
// includeArchived is set.
func listAllSlicesIncluding(ctx context.Context, client corev1.SliceServiceClient, account string, includeArchived bool) ([]*corev1.Slice, error) {
	var all []*corev1.Slice
	cursor := ""
	for {
		page, err := client.ListSlices(ctx, &corev1.ListSlicesRequest{Account: account, Cursor: cursor, PageSize: 100, IncludeArchived: includeArchived})
		if err != nil {
			return nil, err
		}
		all = append(all, page.GetSlices()...)
		if page.GetNextCursor() == "" {
			return all, nil
		}
		cursor = page.GetNextCursor()
	}
}

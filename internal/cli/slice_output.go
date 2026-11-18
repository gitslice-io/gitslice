package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type sliceOutput struct {
	ID                     string   `json:"id"`
	Ref                    string   `json:"ref"`
	Account                string   `json:"account"`
	Slice                  string   `json:"slice"`
	Version                int64    `json:"version"`
	Visibility             string   `json:"visibility"`
	CIDaemonID             string   `json:"ci_daemon_id,omitempty"`
	CIDaemonName           string   `json:"ci_daemon_name,omitempty"`
	CIDaemonIDs            []string `json:"ci_daemon_ids,omitempty"`
	CIDaemonNames          []string `json:"ci_daemon_names,omitempty"`
	IncludedPaths          []string `json:"included_paths"`
	RequiredApprovals      int32    `json:"required_approvals"`
	RequiredHumanApprovals int32    `json:"required_human_approvals"`
	Approvers              string   `json:"approvers,omitempty"`
	RequiredChecks         []string `json:"required_checks"`
	DefinitionHash         string   `json:"definition_hash"`
	Archived               bool     `json:"archived,omitempty"`
	Description            string   `json:"description,omitempty"`
	MergeOnSubmit          bool     `json:"merge_on_submit"`
	FormerNames            []string `json:"former_names,omitempty"`
	WebURL                 string   `json:"web_url,omitempty"`
}

type sliceDefinitionVersionOutput struct {
	SliceID                string   `json:"slice_id"`
	Version                int64    `json:"version"`
	DefinitionHash         string   `json:"definition_hash"`
	Visibility             string   `json:"visibility"`
	IncludedPaths          []string `json:"included_paths"`
	RequiredApprovals      int32    `json:"required_approvals"`
	RequiredHumanApprovals int32    `json:"required_human_approvals"`
	Approvers              string   `json:"approvers,omitempty"`
	RequiredChecks         []string `json:"required_checks"`
	CreatedBy              string   `json:"created_by"`
	CreatedAt              string   `json:"created_at"`
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
	if len(out.CIDaemonIDs) > 1 {
		// The pool (design/32 layer 3): runs go to the first one online.
		members := make([]string, len(out.CIDaemonIDs))
		for i, id := range out.CIDaemonIDs {
			members[i] = id
			if i < len(out.CIDaemonNames) && out.CIDaemonNames[i] != "" && out.CIDaemonNames[i] != id {
				members[i] = out.CIDaemonNames[i] + " (" + id + ")"
			}
		}
		fmt.Fprintf(w, "ci_daemons: %s\n", strings.Join(members, ", "))
	}
	fmt.Fprintf(w, "required_approvals: %d\n", out.RequiredApprovals)
	fmt.Fprintf(w, "required_human_approvals: %d\n", out.RequiredHumanApprovals)
	if out.Approvers != "" {
		fmt.Fprintf(w, "approvers: %s\n", out.Approvers)
	}
	if out.MergeOnSubmit {
		fmt.Fprintln(w, "merge_on_submit: on (a stale changeset is merged onto the latest base at submit)")
	} else {
		fmt.Fprintln(w, "merge_on_submit: off")
	}
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
		CIDaemonIDs:    append([]string(nil), slice.CiDaemonIds...),
		CIDaemonNames:  append([]string(nil), slice.CiDaemonNames...),
		Archived:       slice.Archived,
		Description:    slice.Description,
		MergeOnSubmit:  slice.MergeOnSubmit,
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
		out.RequiredHumanApprovals = slice.Definition.RequiredHumanApprovals
		out.Approvers = slice.Definition.Approvers
		out.RequiredChecks = append([]string{}, slice.Definition.RequiredChecks...)
	}
	return out
}

func sliceDefinitionVersionToOutput(version *corev1.SliceDefinitionVersion) sliceDefinitionVersionOutput {
	if version == nil {
		return sliceDefinitionVersionOutput{}
	}
	return sliceDefinitionVersionOutput{
		SliceID:                version.SliceId,
		Version:                version.Version,
		DefinitionHash:         version.DefinitionHash,
		Visibility:             version.Visibility,
		IncludedPaths:          append([]string{}, version.IncludedPaths...),
		RequiredApprovals:      version.RequiredApprovals,
		RequiredHumanApprovals: version.RequiredHumanApprovals,
		Approvers:              version.Approvers,
		RequiredChecks:         append([]string{}, version.RequiredChecks...),
		CreatedBy:              version.CreatedBy,
		CreatedAt:              version.CreatedAt,
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
	accountPart, slicePart, ok := model.SplitSliceHandle(value)
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

// parseOnOff reads an on/off flag value.
func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("not on or off: %q", value)
}

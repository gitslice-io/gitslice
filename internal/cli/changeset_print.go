package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func printChangesetDetails(w io.Writer, cs *corev1.Changeset, explain bool) {
	fmt.Fprintf(w, "changeset: %s\n", firstNonEmpty(displayChangesetID(cs), cs.Id))
	fmt.Fprintf(w, "status: %s\n", cs.Status)
	if cs.SubmitBlockedReason != "" {
		fmt.Fprintf(w, "submit_blocked_reason: %s\n", displaySubmitBlockedReason(cs.SubmitBlockedReason))
	}
	if cs.Title != "" {
		fmt.Fprintf(w, "title: %s\n", cs.Title)
	}
	if cs.Author != "" {
		fmt.Fprintf(w, "author: %s\n", cs.Author)
	}
	fmt.Fprintf(w, "authoring_slice: %s\n", changesetSliceLabel(cs.AuthoringSlice))
	if cs.TargetRef != "" {
		fmt.Fprintf(w, "target_ref: %s\n", cs.TargetRef)
	}
	if cs.BaseCommitId != "" {
		fmt.Fprintf(w, "base_commit: %s\n", cs.BaseCommitId)
	}
	if cs.CurrentPatchsetNumber > 0 {
		fmt.Fprintf(w, "current_patchset: %d\n", cs.CurrentPatchsetNumber)
	}
	if cs.CommitId != "" {
		fmt.Fprintf(w, "commit: %s\n", cs.CommitId)
	}
	if len(cs.AffectedPaths) > 0 {
		fmt.Fprintln(w, "affected_paths:")
		printIndentedPaths(w, cs.AffectedPaths, "  ")
	}
	printPatchsets(w, cs)
	if explain {
		printChangesetExplain(w, cs)
	}
}

func printChangesetOneLine(w io.Writer, cs *corev1.Changeset) {
	title := strings.TrimSpace(cs.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(w, "  %s %s patchset=%d %s\n", firstNonEmpty(displayChangesetID(cs), cs.Id), cs.Status, cs.CurrentPatchsetNumber, title)
}

func printPatchsets(w io.Writer, cs *corev1.Changeset) {
	printPatchsetsColor(w, cs, false)
}

func printPatchsetsColor(w io.Writer, cs *corev1.Changeset, color bool) {
	fmt.Fprintln(w, "patchsets:")
	if len(cs.Patchsets) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, patchset := range cs.Patchsets {
		current := ""
		if patchset.Id == cs.CurrentPatchsetId {
			current = " current"
		}
		changed := patchsetChangedPaths(patchset)
		number := colorize(color, ansiYellow, fmt.Sprintf("%d", patchset.Number))
		current = colorize(color, ansiGreen, current)
		fmt.Fprintf(w, "  %s%s changed=%d\n", number, current, len(changed))
		if len(changed) > 0 {
			printIndentedPathsColor(w, changed, "    ", color)
		}
	}
}

func printChangesetExplain(w io.Writer, cs *corev1.Changeset) {
	patchset := currentPatchset(cs)
	fmt.Fprintln(w, "validation:")
	if patchset == nil {
		fmt.Fprintln(w, "  no current patchset")
		return
	}
	printSubmitRequirements(w, patchset.SubmitRequirements)
	fmt.Fprintln(w, "read_set:")
	printIndentedPaths(w, pathSetEntryPaths(patchset.ReadSet), "  ")
	fmt.Fprintln(w, "write_set:")
	printIndentedPaths(w, pathSetEntryPaths(patchset.WriteSet), "  ")
	if len(patchset.PathBases) > 0 {
		fmt.Fprintln(w, "path_bases:")
		for _, base := range patchset.PathBases {
			state := "missing"
			if base.Exists {
				state = base.EntryKind
			}
			fmt.Fprintf(w, "  %s %s %s\n", base.Path, state, shortID(base.EntryFingerprint))
		}
	}
}

func printSubmitRequirements(w io.Writer, req *corev1.SubmitRequirements) {
	fmt.Fprintln(w, "submit_requirements:")
	if req == nil {
		fmt.Fprintln(w, "  none")
		return
	}
	if req.RequiredApprovals > 0 {
		fmt.Fprintf(w, "  required_approvals: %d\n", req.RequiredApprovals)
	}
	printRequirementField(w, "required_checks", req.RequiredChecks)
	printRequirementField(w, "path_lock_ids", req.PathLockIds)
	if req.SourceSliceDefinitionHash != "" {
		fmt.Fprintf(w, "  source_slice_definition_hash: %s\n", req.SourceSliceDefinitionHash)
	}
	if req.SourcePathLockSetHash != "" {
		fmt.Fprintf(w, "  source_path_lock_set_hash: %s\n", req.SourcePathLockSetHash)
	}
	if req.RequiredApprovals == 0 && len(req.RequiredChecks) == 0 && len(req.PathLockIds) == 0 && req.SourceSliceDefinitionHash == "" && req.SourcePathLockSetHash == "" {
		fmt.Fprintln(w, "  none")
	}
}

func printRequirementField(w io.Writer, name string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s: %s\n", name, strings.Join(values, ", "))
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil || len(cs.Patchsets) == 0 {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func currentPatchsetSubmitRequirements(cs *corev1.Changeset) *corev1.SubmitRequirements {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil
	}
	return patchset.SubmitRequirements
}

func patchsetChangedPaths(patchset *corev1.Patchset) []string {
	if patchset == nil {
		return nil
	}
	if len(patchset.ChangedPaths) > 0 {
		return append([]string{}, patchset.ChangedPaths...)
	}
	return changedPathsFromEdits(patchset.FileEdits)
}

func displayChangesetID(cs *corev1.Changeset) string {
	if cs == nil {
		return ""
	}
	return storage.ShortChangesetID(cs.Id)
}

func pathSetEntryPaths(entries []*corev1.PathSetEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Path == "" {
			continue
		}
		p := entry.Path
		if entry.Recursive {
			p += "/**"
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func changesetSliceLabel(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return ref.Account + ":" + ref.Slice
}

func changedPathsFromEdits(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "rename":
			if edit.OldPath != "" {
				seen[edit.OldPath] = struct{}{}
			}
			if edit.Path != "" {
				seen[edit.Path] = struct{}{}
			}
		default:
			if edit.Path != "" {
				seen[edit.Path] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

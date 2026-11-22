package cli

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/model"
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
	if cs.Description != "" {
		fmt.Fprintf(w, "description:\n%s\n", indentLines(cs.Description, "  "))
	}
	authorLine := model.FormatActorAttribution(effectiveChangesetAttribution(cs), cs.Author)
	if authorLine != "" {
		fmt.Fprintf(w, "author: %s\n", authorLine)
	}
	if cs.OriginalAuthor != "" {
		fmt.Fprintf(w, "original_author: %s\n", cs.OriginalAuthor)
	}
	if len(cs.Collaborators) > 0 {
		fmt.Fprintf(w, "collaborators: %s\n", strings.Join(cs.Collaborators, ", "))
	}
	if subLine := model.FormatActorAttribution(cs.GetSubmittedAttribution(), ""); subLine != "" {
		fmt.Fprintf(w, "submitted_by: %s\n", subLine)
	}
	if len(cs.GetApprovals()) > 0 {
		appLabels := make([]string, 0, len(cs.GetApprovals()))
		for _, app := range cs.GetApprovals() {
			if app == nil {
				continue
			}
			lbl := model.FormatActorAttribution(app.GetAttribution(), firstNonEmpty(app.GetUsername(), app.GetSubjectId()))
			if lbl != "" {
				appLabels = append(appLabels, lbl)
			}
		}
		if len(appLabels) > 0 {
			fmt.Fprintf(w, "approved_by: %s\n", strings.Join(appLabels, ", "))
		}
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
	if len(cs.AffectedPaths) > 0 && !sameAsOnlyPatchsetPaths(cs) {
		fmt.Fprintln(w, "affected_paths:")
		printIndentedPaths(w, cs.AffectedPaths, "  ")
	}
	printPatchsets(w, cs)
	if explain {
		printChangesetExplain(w, cs)
	}
}

func sameAsOnlyPatchsetPaths(cs *corev1.Changeset) bool {
	if cs == nil || len(cs.Patchsets) != 1 {
		return false
	}
	return slices.Equal(cs.AffectedPaths, patchsetChangedPaths(cs.Patchsets[0]))
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
		psActor := model.FormatActorAttribution(patchset.GetAttribution(), patchset.GetAuthor())
		csActor := model.FormatActorAttribution(effectiveChangesetAttribution(cs), cs.GetAuthor())
		psSuffix := ""
		if psActor != "" && (psActor != csActor || patchset.GetAttribution().GetDaemonId() != "" || patchset.GetAttribution().GetConversationId() != "") {
			psSuffix = fmt.Sprintf(" by %s", psActor)
		}
		fmt.Fprintf(w, "  %s%s changed=%d%s\n", number, current, len(changed), psSuffix)
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
	if req.RequiredHumanApprovals > 0 {
		fmt.Fprintf(w, "  required_human_approvals: %d\n", req.RequiredHumanApprovals)
	}
	if req.Approvers != "" {
		fmt.Fprintf(w, "  approvers: %s\n", req.Approvers)
	}
	printRequirementField(w, "required_checks", req.RequiredChecks)
	printRequirementField(w, "path_lock_ids", req.PathLockIds)
	if req.SourceSliceDefinitionHash != "" {
		fmt.Fprintf(w, "  source_slice_definition_hash: %s\n", req.SourceSliceDefinitionHash)
	}
	if req.SourcePathLockSetHash != "" {
		fmt.Fprintf(w, "  source_path_lock_set_hash: %s\n", req.SourcePathLockSetHash)
	}
	if req.RequiredApprovals == 0 && req.RequiredHumanApprovals == 0 && req.Approvers == "" && len(req.RequiredChecks) == 0 && len(req.PathLockIds) == 0 && req.SourceSliceDefinitionHash == "" && req.SourcePathLockSetHash == "" {
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
	return model.ShortChangesetID(cs.Id)
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

// indentLines indents every non-empty line of text.
func indentLines(text, indent string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

func effectiveChangesetAttribution(cs *corev1.Changeset) *corev1.ActorAttribution {
	if cs == nil {
		return nil
	}
	base := cs.GetAttribution()
	ps := currentPatchset(cs)
	psAttr := ps.GetAttribution()
	if base == nil || strings.TrimSpace(base.GetActingSubjectId()) == "" {
		if psAttr != nil {
			return psAttr
		}
		return base
	}
	if psAttr == nil || strings.TrimSpace(psAttr.GetActingSubjectId()) != strings.TrimSpace(base.GetActingSubjectId()) {
		return base
	}
	merged := &corev1.ActorAttribution{
		ActingSubjectId:     base.GetActingSubjectId(),
		ActingUsername:      base.GetActingUsername(),
		ControllerSubjectId: base.GetControllerSubjectId(),
		ControllerUsername:  base.GetControllerUsername(),
		KeyId:               base.GetKeyId(),
		DaemonId:            firstNonEmpty(base.GetDaemonId(), psAttr.GetDaemonId()),
		DaemonName:          firstNonEmpty(base.GetDaemonName(), psAttr.GetDaemonName()),
		ConversationId:      firstNonEmpty(base.GetConversationId(), psAttr.GetConversationId()),
	}
	return merged
}

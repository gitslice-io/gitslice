package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type stackOutput struct {
	StackID             string             `json:"dependency_id"`
	Status              string             `json:"status"`
	ActiveChangesetID   string             `json:"active_changeset_id,omitempty"`
	RootChangesetID     string             `json:"root_changeset_id,omitempty"`
	Entries             []stackEntryOutput `json:"changesets"`
	SubmitStatus        string             `json:"submit_status,omitempty"`
	RestackedChangesets []string           `json:"updated_changesets,omitempty"`
}

type stackEntryOutput struct {
	ChangesetID       string `json:"changeset_id"`
	ChangesetHandle   string `json:"changeset_handle,omitempty"`
	PatchsetID        string `json:"patchset_id,omitempty"`
	PatchsetNumber    int64  `json:"patchset_number,omitempty"`
	ParentChangesetID string `json:"base_changeset_id,omitempty"`
	ParentPatchsetID  string `json:"base_patchset_id,omitempty"`
	DisplayOrder      int64  `json:"display_order"`
	SiblingOrder      int64  `json:"sibling_order"`
	Depth             int64  `json:"depth"`
	State             string `json:"state"`
	Status            string `json:"status,omitempty"`
}

func stackOutputFromProto(stack *corev1.ChangesetStack) stackOutput {
	out := stackOutput{
		StackID:           stack.Id,
		Status:            stack.Status,
		ActiveChangesetID: stack.ActiveEntryId,
		RootChangesetID:   stack.RootEntryId,
		Entries:           make([]stackEntryOutput, 0, len(stack.Entries)),
	}
	for _, entry := range stack.Entries {
		item := stackEntryOutput{
			ChangesetID:       entry.ChangesetId,
			ParentChangesetID: entry.ParentChangesetId,
			ParentPatchsetID:  entry.ParentPatchsetId,
			DisplayOrder:      entry.DisplayOrder,
			SiblingOrder:      entry.SiblingOrder,
			Depth:             entry.Depth,
			State:             displayDependencyState(entry.State),
		}
		if entry.Changeset != nil {
			item.ChangesetHandle = displayChangesetID(entry.Changeset)
			item.PatchsetID = entry.Changeset.CurrentPatchsetId
			item.PatchsetNumber = entry.Changeset.CurrentPatchsetNumber
			item.Status = entry.Changeset.Status
		}
		out.Entries = append(out.Entries, item)
	}
	return out
}

func printStack(w io.Writer, stack *corev1.ChangesetStack) {
	fmt.Fprintf(w, "dependencies: %s\n", stack.Id)
	fmt.Fprintf(w, "status: %s\n", stack.Status)
	if stack.ActiveEntryId != "" {
		fmt.Fprintf(w, "active: %s\n", firstNonEmpty(storage.ShortChangesetID(stack.ActiveEntryId), stack.ActiveEntryId))
	}
	fmt.Fprintln(w, "changesets:")
	if len(stack.Entries) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, entry := range stack.Entries {
		label := firstNonEmpty(storage.ShortChangesetID(entry.ChangesetId), entry.ChangesetId)
		status := displayDependencyState(entry.State)
		patchset := int64(0)
		if entry.Changeset != nil {
			label = firstNonEmpty(displayChangesetID(entry.Changeset), label)
			if entry.Changeset.Status != "" {
				status = entry.Changeset.Status
			}
			patchset = entry.Changeset.CurrentPatchsetNumber
		}
		prefix := strings.Repeat("  ", int(entry.Depth)+1)
		if patchset > 0 {
			fmt.Fprintf(w, "%s%s patchset %d %s\n", prefix, label, patchset, status)
		} else {
			fmt.Fprintf(w, "%s%s %s\n", prefix, label, status)
		}
	}
}

func displayDependencyState(state string) string {
	switch strings.TrimSpace(state) {
	case "needs_restack":
		return "needs_base_update"
	default:
		return state
	}
}

func stackEntryByChangesetSelector(stack *corev1.ChangesetStack, selector string) *corev1.ChangesetStackEntry {
	selector = strings.TrimSpace(selector)
	if selector == "" || stack == nil {
		return nil
	}
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		if sameChangesetSelector(selector, entry.ChangesetId) {
			return entry
		}
		if entry.Changeset != nil && sameChangesetSelector(selector, displayChangesetID(entry.Changeset)) {
			return entry
		}
	}
	return nil
}

func stackChildren(stack *corev1.ChangesetStack, parentChangesetID string) []*corev1.ChangesetStackEntry {
	if stack == nil {
		return nil
	}
	out := []*corev1.ChangesetStackEntry{}
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		if entry.ParentChangesetId == parentChangesetID {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SiblingOrder == out[j].SiblingOrder {
			return out[i].ChangesetId < out[j].ChangesetId
		}
		return out[i].SiblingOrder < out[j].SiblingOrder
	})
	return out
}

func sameChangesetSelector(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	aBody, aOK := changesetSelectorBody(a)
	bBody, bOK := changesetSelectorBody(b)
	if !aOK || !bOK {
		return false
	}
	return strings.HasPrefix(aBody, bBody) || strings.HasPrefix(bBody, aBody)
}

func changesetSelectorBody(selector string) (string, bool) {
	prefix, ok := storage.ChangesetIDLookupPrefix(selector)
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(prefix, "cs_"), true
}

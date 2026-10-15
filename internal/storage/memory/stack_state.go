package memory

import (
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (b *backend) hydrateStackLocked(stack *corev1.ChangesetStack) *corev1.ChangesetStack {
	out := cloneStack(stack)
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].DisplayOrder == out.Entries[j].DisplayOrder {
			return out.Entries[i].ChangesetId < out.Entries[j].ChangesetId
		}
		return out.Entries[i].DisplayOrder < out.Entries[j].DisplayOrder
	})
	for _, entry := range out.Entries {
		if cs := b.changesets[entry.ChangesetId]; cs != nil {
			entry.Changeset = cloneChangeset(cs)
			storage.PopulateChangesetHandles(entry.Changeset)
		}
	}
	return out
}

func (b *backend) attachChangesetToStackLocked(cs *corev1.Changeset, stackID, parentChangesetID, parentPatchsetID string) error {
	stack := b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return storage.ErrNotFound
	}
	if !sameSliceRef(stack.AuthoringSlice, cs.AuthoringSlice) || stack.TargetRef != cs.TargetRef {
		return storage.ErrInvalid
	}
	parentChangesetID = strings.TrimSpace(parentChangesetID)
	parentPatchsetID = strings.TrimSpace(parentPatchsetID)

	var parentEntry *corev1.ChangesetStackEntry
	if parentChangesetID != "" {
		parent := b.changesets[parentChangesetID]
		if parent == nil || parent.StackId != stack.Id {
			return storage.ErrInvalid
		}
		parentEntry = stackEntryByChangeset(stack, parentChangesetID)
		if parentEntry == nil {
			return storage.ErrInvalid
		}
		if parentPatchsetID == "" {
			parentPatchsetID = parent.CurrentPatchsetId
		}
		if parentPatchsetID == "" || parentPatchsetID != parent.CurrentPatchsetId {
			return storage.ErrConflict
		}
	} else if stack.RootEntryId != "" {
		return storage.ErrConflict
	}

	displayOrder := int64(1)
	siblingOrder := int64(1)
	for _, entry := range stack.Entries {
		if entry.DisplayOrder >= displayOrder {
			displayOrder = entry.DisplayOrder + 1
		}
		if entry.ParentChangesetId == parentChangesetID && entry.SiblingOrder >= siblingOrder {
			siblingOrder = entry.SiblingOrder + 1
		}
	}
	depth := int64(0)
	if parentEntry != nil {
		depth = parentEntry.Depth + 1
	}
	entry := &corev1.ChangesetStackEntry{
		StackId:           stack.Id,
		ChangesetId:       cs.Id,
		ParentChangesetId: parentChangesetID,
		ParentPatchsetId:  parentPatchsetID,
		SiblingOrder:      siblingOrder,
		DisplayOrder:      displayOrder,
		Depth:             depth,
		State:             "draft",
	}
	cs.StackId = stack.Id
	cs.StackOrder = displayOrder
	cs.StackDepth = depth
	cs.SiblingOrder = siblingOrder
	cs.ParentChangesetId = parentChangesetID
	cs.ParentPatchsetId = parentPatchsetID
	if parentChangesetID == "" {
		cs.BaseKind = "commit"
		stack.RootEntryId = cs.Id
	} else {
		cs.BaseKind = "patchset"
	}
	stack.ActiveEntryId = cs.Id
	stack.Entries = append(stack.Entries, cloneStackEntry(entry))
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func stackEntryByChangeset(stack *corev1.ChangesetStack, changesetID string) *corev1.ChangesetStackEntry {
	if stack == nil {
		return nil
	}
	for _, entry := range stack.Entries {
		if entry.ChangesetId == changesetID {
			return entry
		}
	}
	return nil
}

func (b *backend) markChildrenStaleLocked(stackID, parentChangesetID, newParentPatchsetID string) {
	stack := b.stacks[stackID]
	if stack == nil {
		return
	}
	for _, entry := range stack.Entries {
		if entry.ParentChangesetId == parentChangesetID && entry.ParentPatchsetId != "" && entry.ParentPatchsetId != newParentPatchsetID {
			entry.State = "needs_restack"
		}
	}
}

func (b *backend) markSubtreeStateLocked(stack *corev1.ChangesetStack, rootChangesetID, state string) {
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range stack.Entries {
			if entry == nil {
				continue
			}
			if _, ok := descendants[entry.ChangesetId]; ok {
				continue
			}
			if _, ok := descendants[entry.ParentChangesetId]; ok {
				descendants[entry.ChangesetId] = struct{}{}
				changed = true
			}
		}
	}
	for _, entry := range stack.Entries {
		if _, ok := descendants[entry.ChangesetId]; ok {
			entry.State = state
		}
	}
}

func (b *backend) stackSubtreeIDsLocked(stack *corev1.ChangesetStack, rootChangesetID string) map[string]struct{} {
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range stack.Entries {
			if entry == nil {
				continue
			}
			if _, ok := descendants[entry.ChangesetId]; ok {
				continue
			}
			if _, ok := descendants[entry.ParentChangesetId]; ok {
				descendants[entry.ChangesetId] = struct{}{}
				changed = true
			}
		}
	}
	return descendants
}

func (b *backend) refreshStackStatusLocked(stackID string) {
	if stackID == "" {
		return
	}
	stack := b.stacks[stackID]
	if stack == nil || len(stack.Entries) == 0 {
		return
	}
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		cs := b.changesets[entry.ChangesetId]
		if cs == nil || (cs.Status != "submitted" && cs.Status != "abandoned") {
			return
		}
	}
	stack.Status = "closed"
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (b *backend) stackEntryHasAncestorLocked(stack *corev1.ChangesetStack, changesetID, ancestorID string) bool {
	for changesetID != "" {
		if changesetID == ancestorID {
			return true
		}
		entry := stackEntryByChangeset(stack, changesetID)
		if entry == nil {
			return false
		}
		changesetID = entry.ParentChangesetId
	}
	return false
}

func (b *backend) reorderStackSiblingLocked(stack *corev1.ChangesetStack, moved *corev1.ChangesetStackEntry, requestedOrder int64) {
	if moved == nil {
		return
	}
	siblings := make([]*corev1.ChangesetStackEntry, 0)
	for _, entry := range stack.Entries {
		if entry == nil || entry.ChangesetId == moved.ChangesetId || entry.ParentChangesetId != moved.ParentChangesetId {
			continue
		}
		siblings = append(siblings, entry)
	}
	sort.Slice(siblings, func(i, j int) bool {
		if siblings[i].SiblingOrder == siblings[j].SiblingOrder {
			return siblings[i].ChangesetId < siblings[j].ChangesetId
		}
		return siblings[i].SiblingOrder < siblings[j].SiblingOrder
	})
	if requestedOrder <= 0 {
		requestedOrder = int64(len(siblings) + 1)
	}
	index := int(requestedOrder - 1)
	if index < 0 {
		index = 0
	}
	if index > len(siblings) {
		index = len(siblings)
	}
	siblings = append(siblings, nil)
	copy(siblings[index+1:], siblings[index:])
	siblings[index] = moved
	for i, entry := range siblings {
		entry.SiblingOrder = int64(i + 1)
		if cs := b.changesets[entry.ChangesetId]; cs != nil {
			cs.SiblingOrder = entry.SiblingOrder
		}
	}
}

func (b *backend) recomputeStackDisplayLocked(stack *corev1.ChangesetStack) {
	children := map[string][]*corev1.ChangesetStackEntry{}
	var roots []*corev1.ChangesetStackEntry
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		if entry.ParentChangesetId == "" {
			roots = append(roots, entry)
		} else {
			children[entry.ParentChangesetId] = append(children[entry.ParentChangesetId], entry)
		}
	}
	sortStackEntriesBySibling(roots)
	for parent := range children {
		sortStackEntriesBySibling(children[parent])
	}
	var order int64
	var walk func(entries []*corev1.ChangesetStackEntry, depth int64)
	walk = func(entries []*corev1.ChangesetStackEntry, depth int64) {
		for _, entry := range entries {
			order++
			entry.DisplayOrder = order
			entry.Depth = depth
			if cs := b.changesets[entry.ChangesetId]; cs != nil {
				cs.StackOrder = order
				cs.StackDepth = depth
				cs.ParentChangesetId = entry.ParentChangesetId
				cs.ParentPatchsetId = entry.ParentPatchsetId
			}
			walk(children[entry.ChangesetId], depth+1)
		}
	}
	walk(roots, 0)
}

func sortStackEntriesBySibling(entries []*corev1.ChangesetStackEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SiblingOrder == entries[j].SiblingOrder {
			return entries[i].ChangesetId < entries[j].ChangesetId
		}
		return entries[i].SiblingOrder < entries[j].SiblingOrder
	})
}

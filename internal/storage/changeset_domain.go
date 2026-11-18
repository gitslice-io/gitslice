package storage

import (
	"fmt"
	"slices"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// SubmitPreconditionInput captures pure domain state validated before changeset
// approval/check/path-base evaluation.
type SubmitPreconditionInput struct {
	Status                    string
	CurrentPatchsetID         string
	ExpectedCurrentPatchsetID string
	HasConflicts              bool
	HasParent                 bool
	ParentStatus              string
	SliceArchived             bool
	SliceDeleted              bool
	ChangedPaths              []string
	IncludedPaths             []string
}

// ValidateSubmitDraftHeader validates changeset status and expected patchset ID
// prior to loading or applying a draft patchset for submit.
func ValidateSubmitDraftHeader(status, currentPatchsetID, expectedCurrentPatchsetID string) error {
	if status != "draft" {
		return ErrConflict
	}
	if expectedCurrentPatchsetID != "" && currentPatchsetID != expectedCurrentPatchsetID {
		return ErrConflict
	}
	if currentPatchsetID == "" {
		return ErrConflict
	}
	return nil
}

// ValidatePatchsetNoConflicts rejects patchsets that still carry unresolved merge conflicts.
func ValidatePatchsetNoConflicts(conflicts []*corev1.PatchsetConflict) error {
	if len(conflicts) > 0 {
		return fmt.Errorf("%w: unresolved patchset conflicts", ErrConflict)
	}
	return nil
}

// ValidateSubmitParentStatus checks that a stacked parent changeset has already
// been submitted or queued for publish.
func ValidateSubmitParentStatus(parentStatus string) (SubmitBlockCode, string) {
	if parentStatus != "submitted" && parentStatus != "pending_publish" {
		return SubmitBlockCodeBlockedOnParent, "BlockedOnBaseChangeset"
	}
	return SubmitBlockCodeNone, ""
}

// ValidateSubmitSliceState ensures the authoring slice is neither deleted nor archived.
func ValidateSubmitSliceState(archived, deleted bool) error {
	if deleted {
		return fmt.Errorf("%w: slice is deleted", ErrConflict)
	}
	if archived {
		return fmt.Errorf("%w: slice is archived", ErrConflict)
	}
	return nil
}

// ValidateSubmitIncludedPaths verifies every changed path in the patchset still
// falls within the slice's latest included_paths prefixes.
func ValidateSubmitIncludedPaths(changedPaths, includedPaths []string) (SubmitBlockCode, string) {
	for _, p := range changedPaths {
		if !paths.InAnyPrefix(includedPaths, p) {
			return SubmitBlockCodeRequirementsChanged, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p)
		}
	}
	return SubmitBlockCodeNone, ""
}

// ValidateSubmitPreconditions runs all pure changeset, parent, and slice
// preconditions for a submit attempt. When a block code/reason is returned, err
// is nil so the store can record the block onto the draft changeset.
func ValidateSubmitPreconditions(in SubmitPreconditionInput) (SubmitBlockCode, string, error) {
	if err := ValidateSubmitDraftHeader(in.Status, in.CurrentPatchsetID, in.ExpectedCurrentPatchsetID); err != nil {
		return SubmitBlockCodeNone, "", err
	}
	if in.HasConflicts {
		return SubmitBlockCodeNone, "", fmt.Errorf("%w: unresolved patchset conflicts", ErrConflict)
	}
	if in.HasParent {
		if code, reason := ValidateSubmitParentStatus(in.ParentStatus); reason != "" {
			return code, reason, nil
		}
	}
	if err := ValidateSubmitSliceState(in.SliceArchived, in.SliceDeleted); err != nil {
		return SubmitBlockCodeNone, "", err
	}
	if code, reason := ValidateSubmitIncludedPaths(in.ChangedPaths, in.IncludedPaths); reason != "" {
		return code, reason, nil
	}
	return SubmitBlockCodeNone, "", nil
}

// PathHeadFromFile constructs a PathHead with its canonical file fingerprint.
func PathHeadFromFile(entry FileEntry) PathHead {
	return PathHead{
		Path:             entry.Path,
		Exists:           true,
		EntryFingerprint: FileEntryFingerprint(entry),
		BlobID:           entry.BlobID,
		ContentHash:      entry.ContentHash,
		Mode:             entry.Mode,
		Size:             entry.Size,
	}
}

// PathHeadFromTreeEntry constructs a PathHead for either a file or directory entry.
func PathHeadFromTreeEntry(entry TreeEntry) PathHead {
	if entry.Kind == "directory" {
		return PathHead{
			Path:             entry.Path,
			Exists:           true,
			EntryFingerprint: DirectoryEntryFingerprint(entry.TreeID),
		}
	}
	return PathHeadFromFile(FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	})
}

// ValidateAcceptedPathBase verifies that a patchset's recorded PathBase still
// matches the target ref's current PathHead.
func ValidateAcceptedPathBase(head PathHead, base *corev1.PathBase) error {
	if !head.Exists {
		if base.GetExists() || base.GetEntryFingerprint() != MissingEntryFingerprint() {
			return ErrConflict
		}
		return nil
	}
	if !base.GetExists() {
		return ErrConflict
	}
	if head.EntryFingerprint != base.GetEntryFingerprint() {
		return ErrConflict
	}
	return nil
}

// ResolveChangesetSelectorMatches resolves changeset ID prefix and commit ID
// prefix candidate lists with consistent ambiguity and not-found errors.
func ResolveChangesetSelectorMatches(selector string, isChangeset, isCommit bool, changesetMatches, commitMatches []string) (string, error) {
	if !isChangeset && !isCommit {
		return selector, nil
	}
	if isChangeset {
		if len(changesetMatches) > 1 {
			return "", fmt.Errorf("changeset id prefix %q is ambiguous: %w", selector, ErrInvalid)
		}
		if len(changesetMatches) == 1 {
			return changesetMatches[0], nil
		}
	}
	if isCommit {
		if len(commitMatches) > 1 {
			return "", fmt.Errorf("commit id prefix %q is ambiguous: %w", selector, ErrInvalid)
		}
		if len(commitMatches) == 1 {
			return commitMatches[0], nil
		}
	}
	if !isChangeset {
		return selector, nil
	}
	return "", ErrNotFound
}

// ValidateCollaboratorChange validates inputs and changeset status before adding
// or removing a collaborator.
func ValidateCollaboratorChange(status, subjectID string) (string, error) {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return "", ErrInvalid
	}
	if status != "draft" {
		return "", fmt.Errorf("%w: collaborators can only be modified while a changeset is open; this one is %s", ErrConflict, status)
	}
	return subjectID, nil
}

// ApplyCollaboratorAddition adds subjectID to collaborators unless subjectID is
// the author or already listed.
func ApplyCollaboratorAddition(author string, collaborators []string, subjectID string) ([]string, bool) {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" || subjectID == strings.TrimSpace(author) || slices.Contains(collaborators, subjectID) {
		return collaborators, false
	}
	return append(collaborators, subjectID), true
}

// ApplyCollaboratorRemoval removes subjectID from collaborators.
func ApplyCollaboratorRemoval(collaborators []string, subjectID string) []string {
	subjectID = strings.TrimSpace(subjectID)
	var out []string
	for _, existing := range collaborators {
		if strings.TrimSpace(existing) != subjectID {
			out = append(out, existing)
		}
	}
	return out
}

// ValidateHandoff validates inputs and changeset status before handing off
// authorship of an open changeset.
func ValidateHandoff(status, newAuthorSubjectID string) (string, error) {
	newAuthorSubjectID = strings.TrimSpace(newAuthorSubjectID)
	if newAuthorSubjectID == "" {
		return "", ErrInvalid
	}
	if status != "draft" {
		return "", fmt.Errorf("%w: a changeset can only be handed off while it is open; this one is %s", ErrConflict, status)
	}
	return newAuthorSubjectID, nil
}

// ApplyHandoff computes the updated author, original author, and collaborator
// list when handing a changeset to newAuthorSubjectID.
func ApplyHandoff(author, originalAuthor string, collaborators []string, newAuthorSubjectID string) (string, string, []string, bool) {
	newAuthorSubjectID = strings.TrimSpace(newAuthorSubjectID)
	if newAuthorSubjectID == "" || newAuthorSubjectID == strings.TrimSpace(author) {
		return author, originalAuthor, collaborators, false
	}
	if strings.TrimSpace(originalAuthor) == "" {
		originalAuthor = author
	}
	return newAuthorSubjectID, originalAuthor, ApplyCollaboratorRemoval(collaborators, newAuthorSubjectID), true
}

// ValidateAccountMemberRole normalizes and validates an organization account role.
func ValidateAccountMemberRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if AccountRoleRank(role) >= len(AccountRoles) {
		return "", fmt.Errorf("%w: invalid account role %q", ErrInvalid, role)
	}
	return role, nil
}

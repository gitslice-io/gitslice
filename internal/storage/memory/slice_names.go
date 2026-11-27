package memory

import (
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/usernames"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func validateSliceDefinitionProto(ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	return storage.ValidateSliceDefinition(ref, definition)
}

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	return storage.NormalizeSliceRef(ref)
}

func normalizeSlug(value string) string {
	slug, err := storage.NormalizeSlug(value, "slug")
	if err != nil {
		return ""
	}
	return slug
}

func normalizeSignupUsername(username string) (string, error) {
	return usernames.Normalize(username)
}

func sliceID(account, slice string) string { return "slice_" + account + "_" + slice }

func sliceRefKey(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return normalizeSlug(ref.Account) + "/" + normalizeSlug(ref.Slice)
}

func sameSliceRef(a, b *corev1.SliceRef) bool { return sliceRefKey(a) == sliceRefKey(b) }

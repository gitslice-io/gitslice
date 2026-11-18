package storage

import (
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const ShortChangesetIDLen = model.ShortChangesetIDLen

func ShortChangesetID(id string) string {
	return model.ShortChangesetID(id)
}

func ChangesetIDLookupPrefix(selector string) (prefix string, ok bool) {
	return model.ChangesetIDLookupPrefix(selector)
}

func CommitIDLookupPrefix(selector string) (prefix string, ok bool) {
	return model.CommitIDLookupPrefix(selector)
}

func SliceHandle(ref *corev1.SliceRef) string {
	return model.SliceHandle(ref)
}

func SplitSliceHandle(ref string) (account, slice string, ok bool) {
	return model.SplitSliceHandle(ref)
}

func PopulateChangesetHandles(cs *corev1.Changeset) {
	if cs == nil {
		return
	}
}

package service

import (
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func selectChangesetPatchset(cs *corev1.Changeset, selector string) (*corev1.Patchset, error) {
	if len(cs.Patchsets) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no patchsets")
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if cs.CurrentPatchsetId == "" {
			return cs.Patchsets[len(cs.Patchsets)-1], nil
		}
		selector = cs.CurrentPatchsetId
	}
	if n, err := strconv.ParseInt(selector, 10, 64); err == nil {
		for _, patchset := range cs.Patchsets {
			if patchset.Number == n {
				return patchset, nil
			}
		}
		return nil, status.Errorf(codes.NotFound, "patchset number %d not found", n)
	}
	if changesetSelector, n, ok := parsePatchsetIDSelector(selector); ok {
		if !changesetSelectorMatchesID(changesetSelector, cs.Id) {
			return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
		}
		for _, patchset := range cs.Patchsets {
			if patchset.Number == n {
				return patchset, nil
			}
		}
		return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == selector {
			return patchset, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "patchset %s not found", selector)
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	if len(cs.Patchsets) == 0 {
		return nil
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func parsePatchsetIDSelector(selector string) (changesetSelector string, patchsetNumber int64, ok bool) {
	dot := strings.LastIndex(selector, ".")
	if dot <= 0 || dot == len(selector)-1 {
		return "", 0, false
	}
	changesetSelector = selector[:dot]
	if _, ok := storage.ChangesetIDLookupPrefix(changesetSelector); !ok {
		return "", 0, false
	}
	n, err := strconv.ParseInt(selector[dot+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return changesetSelector, n, true
}

func changesetSelectorMatchesID(selector, id string) bool {
	prefix, ok := storage.ChangesetIDLookupPrefix(selector)
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.ToLower(id), prefix)
}

func sameSliceRef(a, b *corev1.SliceRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Account == b.Account && a.Slice == b.Slice
}

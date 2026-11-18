package storage

import (
	"fmt"
	"strings"
)

// SliceSort controls the ordering of SliceStore.List* queries.
type SliceSort string

const (
	SliceSortName   SliceSort = "name"
	SliceSortActive SliceSort = "active"
	SliceSortNewest SliceSort = "newest"
)

// NormalizeSliceSort validates and normalizes a ListSlicesRequest.Sort string.
// An empty string defaults to SliceSortName.
func NormalizeSliceSort(raw string) (SliceSort, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(SliceSortName):
		return SliceSortName, nil
	case string(SliceSortActive):
		return SliceSortActive, nil
	case string(SliceSortNewest):
		return SliceSortNewest, nil
	default:
		return "", fmt.Errorf("%w: unsupported slice sort %q (must be name, active, or newest)", ErrInvalid, raw)
	}
}

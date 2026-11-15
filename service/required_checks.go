package service

import (
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// isRequiredSliceCheck reports whether checkName is one of the slice's
// required checks.
func isRequiredSliceCheck(slice *corev1.Slice, checkName string) bool {
	checkName = strings.TrimSpace(checkName)
	if checkName == "" || slice == nil || slice.Definition == nil {
		return false
	}
	for _, req := range slice.Definition.RequiredChecks {
		if strings.TrimSpace(req) == checkName {
			return true
		}
	}
	return false
}

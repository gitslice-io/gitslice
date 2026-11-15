package cli

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// looksLikeChangesetID reports whether gs show's argument can only be a
// changeset: a full cs_ id or a /cs/ link.
func looksLikeChangesetID(arg string) bool {
	arg = strings.TrimSpace(arg)
	return strings.HasPrefix(arg, "cs_") || strings.Contains(arg, "/cs/")
}

// commitNotFound reports whether resolving gs show's argument as a commit
// found none, so it may name a changeset instead.
func commitNotFound(err error) bool {
	if isUserErrorCode(err, "invalid_commit_id") || isUserErrorCode(err, "not_found") {
		return true
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) && cmdErr.Cause != nil {
		err = cmdErr.Cause
	}
	return grpcstatus.Code(err) == codes.NotFound
}

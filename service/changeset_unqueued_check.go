package service

import (
	"context"
	"regexp"

	"google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

var missingCheckPattern = regexp.MustCompile(`required check "([^"]+)" has no result`)

// explainUnqueuedCheck adds to a "required check has no result" refusal when
// no run of the check exists for the patchset at all: then nothing is about
// to report it, and waiting will not help (for example, the automation that
// would run it is not read, or no CI daemon serves the slice).
func (s *ChangesetService) explainUnqueuedCheck(ctx context.Context, cs *corev1.Changeset, err error) error {
	st, ok := status.FromError(err)
	if !ok || s.Checks == nil || cs == nil {
		return err
	}
	m := missingCheckPattern.FindStringSubmatch(st.Message())
	if m == nil {
		return err
	}
	runs, lerr := s.Checks.ListCheckRuns(ctx, cs.GetId(), cs.GetCurrentPatchsetId())
	if lerr != nil {
		return err
	}
	for _, run := range runs {
		if run.GetCheckName() == m[1] {
			return err // queued, running or reported: it is on its way
		}
	}
	return status.Errorf(st.Code(), "%s; nothing has queued it for this patchset, so waiting will not help: "+
		"no automation with a check action lists it, or no CI daemon serves the slice. "+
		"Check `gs auto ls` for problems, or start it with `gs auto run <automation> --changeset %s`",
		st.Message(), storage.ShortChangesetID(cs.GetId()))
}

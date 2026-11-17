package memory

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

func (b *backend) resolveAttributionLocked(ctx context.Context, fallbackSubjectID string) storage.ActorAttribution {
	attr := storage.ActorAttributionFrom(ctx, fallbackSubjectID)
	if attr.ActingSubjectID != "" && attr.ControllerSubjectID == "" {
		if subj, ok := b.subjects[attr.ActingSubjectID]; ok {
			ctrl := strings.TrimSpace(subj.ControllerSubjectID)
			if ctrl != "" && ctrl != attr.ActingSubjectID {
				attr.ControllerSubjectID = ctrl
			}
		}
	}
	return attr.Normalize()
}

package authctx

import "context"

type subjectKey struct{}

func WithSubjectID(ctx context.Context, subjectID string) context.Context {
	return context.WithValue(ctx, subjectKey{}, subjectID)
}

func SubjectID(ctx context.Context) (string, bool) {
	subjectID, ok := ctx.Value(subjectKey{}).(string)
	return subjectID, ok && subjectID != ""
}

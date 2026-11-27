package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

type subjectUsernameStore interface {
	UsernamesForSubjects(ctx context.Context, subjectIDs []string) (map[string]string, error)
}

type subjectLookupStore interface {
	subjectUsernameStore
	SubjectsByIDs(ctx context.Context, subjectIDs []string) (map[string]storage.Subject, error)
}

type roleAndSubjectStore interface {
	storage.RoleLookupStore
	subjectLookupStore
}

// subjectDirectory centralizes single-subject and batch username resolution
// across service handlers.
type subjectDirectory struct {
	auth subjectUsernameStore
}

func newSubjectDirectory(auth subjectUsernameStore) subjectDirectory {
	return subjectDirectory{auth: auth}
}

func (d subjectDirectory) usernames(ctx context.Context, subjectIDs ...string) (map[string]string, error) {
	if d.auth == nil || len(subjectIDs) == 0 {
		return nil, nil
	}
	unique := make([]string, 0, len(subjectIDs))
	seen := make(map[string]struct{}, len(subjectIDs))
	for _, raw := range subjectIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	return d.auth.UsernamesForSubjects(ctx, unique)
}

func (d subjectDirectory) username(ctx context.Context, subjectID string) (string, error) {
	id := strings.TrimSpace(subjectID)
	if id == "" || d.auth == nil {
		return "", nil
	}
	names, err := d.usernames(ctx, id)
	if err != nil {
		return "", err
	}
	return names[id], nil
}

func (d subjectDirectory) usernameOrID(ctx context.Context, subjectID string) (string, error) {
	id := strings.TrimSpace(subjectID)
	name, err := d.username(ctx, id)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	return id, nil
}

func (d subjectDirectory) bestEffortUsername(ctx context.Context, subjectID string) string {
	name, err := d.username(ctx, subjectID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

func (d subjectDirectory) bestEffortUsernames(ctx context.Context, subjectIDs ...string) map[string]string {
	names, err := d.usernames(ctx, subjectIDs...)
	if err != nil {
		return nil
	}
	return names
}

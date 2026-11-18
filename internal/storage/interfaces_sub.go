package storage

import (
	"context"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// SubmitInput configures a ChangesetStore.SubmitChangeset call.
type SubmitInput struct {
	ChangesetID               string
	ExpectedCurrentPatchsetID string
	ExtraCheckStatuses        map[string]string
	BypassRequirements        bool
}

// RoleLookupStore provides the narrow membership and role lookups required for
// request authorization.
type RoleLookupStore interface {
	AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error)
	// SliceRole returns the role the subject was given on the slice itself
	// (design/26_slice_members.md), or ErrNotFound when it has none.
	SliceRole(ctx context.Context, subjectID, sliceID string) (string, error)
	// SliceGroupRole returns the highest role any account group containing
	// subjectID was given on sliceID, or ErrNotFound when none applies.
	SliceGroupRole(ctx context.Context, subjectID, sliceID string) (string, error)
}

// APIKeyStore manages API key issuance, revocation, and bearer-token authentication.
type APIKeyStore interface {
	// ListAPIKeys lists the API keys of the target subject (agentAccount == ""
	// means actorID itself; otherwise the agent whose personal account is
	// agentAccount), newest first.
	ListAPIKeys(ctx context.Context, actorID, agentAccount string) (agentSubjectID, account string, keys []APIKey, err error)
	// CreateAPIKey issues a new key for the target subject and returns it with
	// its secret, which is not stored.
	CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (key APIKey, secret, agentSubjectID, account string, err error)
	// RevokeAPIKey ends a key and all transitive child keys at once (idempotent).
	RevokeAPIKey(ctx context.Context, actorID, agentAccount, keyID string) (APIKey, error)
	// AuthenticateToken resolves a bearer token to an authctx.Principal.
	AuthenticateToken(ctx context.Context, token string) (*authctx.Principal, error)
	// SubjectForToken resolves a bearer token to a subject, delegating to AuthenticateToken.
	SubjectForToken(ctx context.Context, token string) (*Subject, error)
}

// AccountGroupStore manages account-scoped groups (@account/name, design/36 section 5).
type AccountGroupStore interface {
	CreateGroup(ctx context.Context, accountSlug, name, description, createdBySubjectID string) (*AccountGroup, error)
	GetGroup(ctx context.Context, accountSlug, name string) (*AccountGroup, error)
	ListGroups(ctx context.Context, accountSlug string) ([]AccountGroup, error)
	DeleteGroup(ctx context.Context, accountSlug, name string) error
	AddGroupMember(ctx context.Context, accountSlug, name, subjectID, addedBySubjectID string) (*AccountGroup, error)
	RemoveGroupMember(ctx context.Context, accountSlug, name, subjectID string) (*AccountGroup, error)
}

// StackStore manages changeset dependency stacks.
type StackStore interface {
	CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error)
	GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error)
	ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error)
	SetStackStatus(ctx context.Context, stackID, stackStatus string) error
	// SetStackBaseCommit records the commit a restack moved the tree's root
	// changesets onto.
	SetStackBaseCommit(ctx context.Context, stackID, baseCommitID string) error
	MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error)
	ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error)
	DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error)
}

// PathIntentStore manages advisory path intents (design/36 section 6).
type PathIntentStore interface {
	CreatePathIntent(ctx context.Context, intent PathIntent) (*PathIntent, error)
	GetPathIntent(ctx context.Context, intentID string) (*PathIntent, error)
	ListPathIntents(ctx context.Context, sliceID string, now time.Time) ([]PathIntent, error)
	DeletePathIntent(ctx context.Context, intentID string) error
	LinkPathIntentsToChangeset(ctx context.Context, intentIDs []string, subjectID, sliceID, changesetID string) error
}

// SliceSecretStore manages account- and slice-scoped secrets (design/33_secrets.md).
type SliceSecretStore interface {
	Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error)
	CreateSecret(ctx context.Context, account, name, value string, paths []string, actorID string) (*Secret, error)
	UpdateSecret(ctx context.Context, secretID string, value *string, paths []string, actorID string) (*Secret, error)
	DeleteSecret(ctx context.Context, secretID string) error
	GetSecret(ctx context.Context, secretID string) (*Secret, error)
	ListSecrets(ctx context.Context, account string) ([]Secret, error)
	SecretValuesAt(ctx context.Context, folder string) (map[string]string, error)
	ListSliceSecrets(ctx context.Context, sliceID string) ([]SliceSecret, error)
	SetSliceSecret(ctx context.Context, sliceID, name, value, actorID string) error
	DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error
}

// SliceMemberStore manages direct and group slice memberships (design/26_slice_members.md).
type SliceMemberStore interface {
	ListMembers(ctx context.Context, sliceID string) ([]SliceMember, error)
	SetMember(ctx context.Context, sliceID, subjectID, role, addedBy string) (*SliceMember, error)
	RemoveMember(ctx context.Context, sliceID, subjectID string) error
	ListGroupMembers(ctx context.Context, sliceID string) ([]SliceGroupMember, error)
	SetGroupMember(ctx context.Context, sliceID, groupID, role, addedBy string) (*SliceGroupMember, error)
	RemoveGroupMember(ctx context.Context, sliceID, groupID string) error
	ListShared(ctx context.Context, subjectID string) ([]SharedSlice, error)
}

// SliceTagStore manages immutable slice commit tags.
type SliceTagStore interface {
	CreateTag(ctx context.Context, tag SliceTag) (*SliceTag, bool, error)
	ListTags(ctx context.Context, sliceID string) ([]SliceTag, error)
}

package storage

import (
	"time"

	"gitslice.io/gitslice/internal/model"
)

// AccountGroup is an account-level named list of user and agent subjects
// referenced as @account/name (design/36_multi_agent_collaboration.md).
type AccountGroup struct {
	ID          string
	Account     string
	Name        string
	Handle      string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Members     []AccountGroupMember
	MemberCount int32
}

// AccountGroupMember is one subject belonging to an AccountGroup.
type AccountGroupMember struct {
	SubjectID   string
	Username    string
	DisplayName string
	Kind        string
	AddedBy     string
	CreatedAt   time.Time
}

// SliceGroupMember is an account group given a role on one slice.
type SliceGroupMember struct {
	SliceID     string
	GroupID     string
	Account     string
	Name        string
	Handle      string
	Role        string
	AddedBy     string
	CreatedAt   time.Time
	MemberCount int32
}

func NormalizeGroupName(name string) (string, error) {
	return model.NormalizeGroupName(name)
}

func NormalizeGroupAccount(account string) (string, error) {
	return model.NormalizeGroupAccount(account)
}

func FormatGroupHandle(account, name string) string {
	return model.FormatGroupHandle(account, name)
}

func ParseGroupRef(ref string) (account, name string, ok bool) {
	return model.ParseGroupRef(ref)
}

func ParseGroupRefStrict(ref string) (string, string, error) {
	return model.ParseGroupRefStrict(ref)
}

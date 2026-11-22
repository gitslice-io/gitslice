package cli

import (
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type secretOutput struct {
	ID        string   `json:"id"`
	Account   string   `json:"account"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	CreatedBy string   `json:"created_by,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

func secretToOutput(secret *corev1.Secret) secretOutput {
	return secretOutput{
		ID:        secret.GetId(),
		Account:   secret.GetAccount(),
		Name:      secret.GetName(),
		Paths:     append([]string{}, secret.GetScope().GetPaths()...),
		CreatedBy: secret.GetCreatedBy(),
		UpdatedBy: secret.GetUpdatedBy(),
		CreatedAt: secret.GetCreatedAt(),
		UpdatedAt: secret.GetUpdatedAt(),
	}
}

func secretScopeLabel(secret secretOutput) string {
	if len(secret.Paths) == 0 {
		return "no folders"
	}
	return strings.Join(secret.Paths, ",")
}

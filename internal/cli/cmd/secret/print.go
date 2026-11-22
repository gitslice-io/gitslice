package secret

import (
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type output struct {
	ID        string   `json:"id"`
	Account   string   `json:"account"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	CreatedBy string   `json:"created_by,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

func toOutput(s *corev1.Secret) output {
	return output{
		ID:        s.GetId(),
		Account:   s.GetAccount(),
		Name:      s.GetName(),
		Paths:     append([]string{}, s.GetScope().GetPaths()...),
		CreatedBy: s.GetCreatedBy(),
		UpdatedBy: s.GetUpdatedBy(),
		CreatedAt: s.GetCreatedAt(),
		UpdatedAt: s.GetUpdatedAt(),
	}
}

func scopeLabel(s output) string {
	if len(s.Paths) == 0 {
		return "no folders"
	}
	return strings.Join(s.Paths, ",")
}

package cli

import (
	"fmt"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type accountCIRunnersOutput struct {
	Account     string   `json:"account"`
	DaemonIDs   []string `json:"daemon_ids"`
	DaemonNames []string `json:"daemon_names"`
}

func (r Runner) printAccountCIRunners(out accountCIRunnersOutput) error {
	if len(out.DaemonIDs) == 0 {
		fmt.Fprintf(r.Stdout, "no CI runners configured for %s\n", out.Account)
		return nil
	}
	fmt.Fprintf(r.Stdout, "account: %s\n", out.Account)
	for i, id := range out.DaemonIDs {
		name := ""
		if i < len(out.DaemonNames) {
			name = out.DaemonNames[i]
		}
		if name != "" && name != id {
			fmt.Fprintf(r.Stdout, "  %d. %s (%s)\n", i+1, name, id)
		} else {
			fmt.Fprintf(r.Stdout, "  %d. %s\n", i+1, id)
		}
	}
	return nil
}

type invitationOutput struct {
	Account   string `json:"account"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	InvitedBy string `json:"invited_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func invitationFromProto(i *corev1.AccountInvitation) invitationOutput {
	return invitationOutput{Account: i.GetAccount(), Username: i.GetUsername(), Role: i.GetRole(), InvitedBy: i.GetInvitedBy(), CreatedAt: i.GetCreatedAt()}
}

type profileOutput struct {
	Account     string `json:"account"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func profileFromProto(p *corev1.AccountProfile) profileOutput {
	return profileOutput{Account: p.GetAccount(), Kind: p.GetKind(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(), Website: p.GetWebsite(), CreatedAt: p.GetCreatedAt()}
}

func (r Runner) writeProfile(opts commandOptions, p profileOutput) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, p)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s (%s)\n", p.Account, p.Kind)
	for _, field := range [][2]string{{"name", p.DisplayName}, {"description", p.Description}, {"website", p.Website}, {"created", p.CreatedAt}} {
		if field[1] != "" {
			fmt.Fprintf(r.Stdout, "%s: %s\n", field[0], field[1])
		}
	}
	return nil
}

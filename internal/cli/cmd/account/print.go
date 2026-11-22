package account

import (
	"fmt"
	"io"

	"gitslice.io/gitslice/internal/cli/clibase"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type ciRunnersOutput struct {
	Account     string   `json:"account"`
	DaemonIDs   []string `json:"daemon_ids"`
	DaemonNames []string `json:"daemon_names"`
}

func printCIRunners(w io.Writer, out ciRunnersOutput) error {
	if len(out.DaemonIDs) == 0 {
		fmt.Fprintf(w, "no CI runners configured for %s\n", out.Account)
		return nil
	}
	fmt.Fprintf(w, "account: %s\n", out.Account)
	for i, id := range out.DaemonIDs {
		name := ""
		if i < len(out.DaemonNames) {
			name = out.DaemonNames[i]
		}
		if name != "" && name != id {
			fmt.Fprintf(w, "  %d. %s (%s)\n", i+1, name, id)
		} else {
			fmt.Fprintf(w, "  %d. %s\n", i+1, id)
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

func writeProfile(app clibase.App, opts clibase.Options, p profileOutput) error {
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, p)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(app.Stdout(), "%s (%s)\n", p.Account, p.Kind)
	for _, field := range [][2]string{{"name", p.DisplayName}, {"description", p.Description}, {"website", p.Website}, {"created", p.CreatedAt}} {
		if field[1] != "" {
			fmt.Fprintf(app.Stdout(), "%s: %s\n", field[0], field[1])
		}
	}
	return nil
}

package cli

import (
	"context"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type pendingClaimOutput struct {
	AgentSubjectID   string `json:"agent_subject_id"`
	AgentDisplayName string `json:"agent_display_name"`
	Account          string `json:"account"`
	OwnerEmail       string `json:"owner_email"`
	CreatedAt        string `json:"created_at"`
}

func (r Runner) runClaimsList(ctx context.Context, opts commandOptions) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).ListPendingClaims(authCtx, &corev1.ListPendingClaimsRequest{})
	if err != nil {
		return claimsError(err)
	}
	out := make([]pendingClaimOutput, 0, len(resp.Claims))
	for _, claim := range resp.Claims {
		out = append(out, pendingClaimOutput{
			AgentSubjectID:   claim.AgentSubjectId,
			AgentDisplayName: claim.AgentDisplayName,
			Account:          claim.Account,
			OwnerEmail:       claim.OwnerEmail,
			CreatedAt:        claim.CreatedAt,
		})
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if len(out) == 0 {
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, noClaimsMessage(resp.VerifiedEmails))
		}
		return nil
	}
	for _, claim := range out {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", claim.AgentSubjectID, claim.Account, claim.AgentDisplayName, claim.CreatedAt)
	}
	return nil
}

// noClaimsMessage explains an empty claims list. A claim matches only an
// agent's owner email against the caller's verified emails, so saying which
// emails were searched is what lets someone see a mismatch.
func noClaimsMessage(verifiedEmails []string) string {
	if len(verifiedEmails) == 0 {
		return "no agent accounts are waiting to be claimed: your account has no verified email.\n" +
			"hint: add and verify the email the agent was registered with in your profile, then run gs claims list again."
	}
	return "no agent accounts are waiting to be claimed for your verified email(s): " + strings.Join(verifiedEmails, ", ") + "\n" +
		"hint: an agent can be claimed only by an account with the email it was registered with as a verified email.\n" +
		"      If it was registered with another address, add and verify that address in your profile, then run gs claims list again."
}

func (r Runner) runClaimsAccept(ctx context.Context, opts commandOptions, agentSubjectID string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).AcceptClaim(authCtx, &corev1.AcceptClaimRequest{AgentSubjectId: strings.TrimSpace(agentSubjectID)})
	if err != nil {
		return claimsError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"agent_subject_id": strings.TrimSpace(agentSubjectID),
			"account":          resp.Account,
		})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "you now co-own account %s\n", resp.Account)
	}
	return nil
}

func claimsError(err error) error {
	switch grpcstatus.Code(err) {
	case codes.NotFound:
		return userError("claim_not_found", grpcstatus.Convert(err).Message(), "Run gs claims list to see agents registered with your verified email.")
	case codes.FailedPrecondition, codes.Unavailable:
		return userError("claim_unavailable", grpcstatus.Convert(err).Message(), "The server could not verify your email; try again later.")
	}
	return err
}

package service

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/secretmask"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// dispatchAllowedPatchsetChecks queues CI runs with secrets for a patchset
// after AllowPatchsetSecrets records an allowance. On first allowance it reruns
// every prior CI check (which previously ran without secrets) and dispatches any
// runnable checks that have not run yet; repeated idempotent calls only fill in
// missing runnable checks without disturbing active or completed runs.
func (d *checkDispatcher) dispatchAllowedPatchsetChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, newlyAllowed bool) ([]*corev1.CheckRun, error) {
	if d == nil || d.Checks == nil || cs == nil || slice == nil || patchset == nil {
		return nil, nil
	}
	if strings.TrimSpace(patchset.ResultTreeId) == "" {
		return nil, nil
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" || refusal != "" {
		return nil, nil
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		slog.Warn("failed to resolve check plan for allowed patchset secrets", "changeset_id", cs.GetId(), "patchset_id", patchset.GetId(), "error", err)
		return nil, nil
	}
	existing, err := d.Checks.ListCheckRuns(ctx, cs.GetId(), patchset.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	existingCI := map[string]*corev1.CheckRun{}
	existingAny := map[string]*corev1.CheckRun{}
	for _, run := range existing {
		if run == nil {
			continue
		}
		existingAny[run.GetCheckName()] = run
		if run.GetProvenance() == "ci" {
			existingCI[run.GetCheckName()] = run
		}
	}

	var queued []*corev1.CheckRun
	handled := map[string]bool{}
	if newlyAllowed {
		for _, prior := range existing {
			if prior == nil || prior.GetProvenance() != "ci" {
				continue
			}
			spec, ok := runnableCICheck(plan, prior.GetCheckName())
			if !ok {
				continue
			}
			handled[spec.Name] = true
			run, err := d.enqueueCheckRun(ctx, cs, slice, patchset, spec, daemonID, prior, "superseded by secret allowance")
			if err != nil {
				return nil, err
			}
			queued = append(queued, run)
		}
	}
	for _, spec := range plan.Runnable {
		if handled[spec.Name] || existingCI[spec.Name] != nil {
			continue
		}
		if !spec.OutOfSlice && !isRequiredSliceCheck(slice, spec.Name) && !spec.Required && existingAny[spec.Name] != nil {
			continue
		}
		run, err := d.enqueueCheckRun(ctx, cs, slice, patchset, spec, daemonID, nil, "")
		if err != nil {
			return nil, err
		}
		queued = append(queued, run)
	}
	if len(queued) > 0 {
		d.handOut(ctx, daemonID)
	}
	return queued, nil
}

// runSecretMasker hides the secrets the run's check was given (those at the
// folder that defines it) in what its daemon sends back, whatever the
// daemon's version: a check that prints a secret must not store it in a log
// that every reader of the changeset can open.
// Daemons mask too, with the values they were sent (CheckRunSpec.secret_env),
// which also covers a secret changed while the check ran.
func (s *AgentService) runSecretMasker(ctx context.Context, run *corev1.CheckRun) *secretmask.Masker {
	if s.Changesets == nil || s.Slices == nil || run == nil {
		return nil
	}
	cs, err := s.Changesets.Get(ctx, run.ChangesetId)
	if err != nil {
		slog.Warn("failed to load changeset to mask check secrets", "run_id", run.Id, "error", err)
		return nil
	}
	secrets, err := s.Slices.SecretValuesAt(ctx, checkSecretFolder(cs.GetAuthoringSlice().GetAccount(), run.CheckName))
	if err != nil {
		slog.Warn("failed to load secrets to mask check output", "run_id", run.Id, "check", run.CheckName, "error", err)
		return nil
	}
	values := make([]string, 0, len(secrets))
	for _, value := range secrets {
		values = append(values, value)
	}
	return secretmask.New(values)
}

// secretsForChecks returns, by check name, the secrets each check gets:
// those that apply at the folder of the checks.yaml that defines it
// (design/33_secrets.md). Each folder is read once.
func (d *checkDispatcher) secretsForChecks(ctx context.Context, account string, specs []checks.CheckSpec) (map[string]map[string]string, error) {
	if d == nil || d.Slices == nil {
		return nil, fmt.Errorf("slice store is not configured")
	}
	byFolder := map[string]map[string]string{}
	out := make(map[string]map[string]string, len(specs))
	for _, spec := range specs {
		folder := checkSecretFolder(account, spec.Name)
		values, ok := byFolder[folder]
		if !ok {
			var err error
			if values, err = d.Slices.SecretValuesAt(ctx, folder); err != nil {
				return nil, err
			}
			byFolder[folder] = values
		}
		out[spec.Name] = values
	}
	return out, nil
}

// patchsetApprovedForGitsliceSecrets reports whether CI checks running for
// patchset may receive folder/account secrets. Unapproved draft edits under
// .gitslice/ could alter checks.yaml to exfiltrate secrets, so secrets are
// withheld unless patchset.Author is an account admin, an account admin
// explicitly allowed secrets for this patchset, or an account admin approved
// this specific patchset. For all other edits, secrets are passed when the
// patchset author has ActionUseSecrets on the slice (writer+), a member with
// ActionUseSecrets explicitly allowed secrets for this patchset, or a member
// with ActionUseSecrets has approved the patchset (so contributors run
// secretless CI until allowed by a writer).
func (d *checkDispatcher) patchsetApprovedForGitsliceSecrets(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset) bool {
	if slice == nil || slice.GetRef() == nil || patchset == nil {
		return false
	}
	account := slice.GetRef().GetAccount()
	var repo storage.RepositoryStore
	if d != nil {
		repo = d.Repository
	}
	headCommitID, err := targetRefHeadCommitID(ctx, repo, cs)
	if err != nil {
		return false
	}
	touched, err := patchsetTouchedGitslicePathAt(ctx, repo, headCommitID, patchset)
	if err != nil {
		return false
	}
	touchedGitslice := touched != ""
	patchsetAuthor := strings.TrimSpace(patchset.GetAuthor())
	if patchsetAuthor == "" && cs != nil {
		patchsetAuthor = strings.TrimSpace(cs.GetAuthor())
	}
	allows := func(subjectID string) bool {
		if !touchedGitslice {
			return authz.New(d.Auth).AuthorizeAllowArchived(ctx, subjectID, slice, authz.ActionUseSecrets) == nil
		}
		ok, err := canAuthorizeAccount(ctx, d.Auth, subjectID, account, authz.ActionAdmin)
		return err == nil && ok
	}
	if d == nil || d.Auth == nil {
		return !touchedGitslice
	}
	if patchsetAuthor != "" && allows(patchsetAuthor) {
		return true
	}
	if d.Checks != nil && cs != nil {
		if allowance, err := d.Checks.GetPatchsetSecretAllowance(ctx, cs.GetId(), patchset.GetId()); err == nil {
			if subjectID := strings.TrimSpace(allowance.AllowedBySubjectID); subjectID != "" && allows(subjectID) {
				return true
			}
		}
	}
	if d.Changesets == nil || cs == nil {
		return false
	}
	approvers, err := d.Changesets.ApprovalSubjects(ctx, cs.GetId(), patchset.GetId())
	if err != nil {
		return false
	}
	for _, subjectID := range approvers {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" || subjectID == patchsetAuthor || (patchsetAuthor == "" && subjectID == strings.TrimSpace(cs.GetAuthor())) {
			continue
		}
		if allows(subjectID) {
			return true
		}
	}
	return false
}

// checkSecretFolder is the folder whose secrets a check gets: the folder of
// the checks.yaml that defines it, read from its id ("<dir>/<name>", or
// "<name>" at the root) as a path in the account.
func checkSecretFolder(account, checkName string) string {
	dir := path.Dir("/" + strings.Trim(strings.TrimSpace(checkName), "/"))
	return gitslicecfg.AccountStoragePath(account, dir)
}

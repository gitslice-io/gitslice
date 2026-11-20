package service

import (
	"context"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/owners"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetFolderPolicy returns the effective folder policy at a canonical folder:
// nearest .gitslice/owners.yaml, inherited .gitslice/checks.yaml definitions
// (including required gates), inherited .gitslice/automations.yaml triggers,
// effective folder secrets (scoped by caller authorization), and the account's
// default CI runner pool.
func (s *AutomationService) GetFolderPolicy(ctx context.Context, req *corev1.GetFolderPolicyRequest) (*corev1.FolderPolicyResponse, error) {
	if s == nil || s.Repository == nil || s.Tree.Objects == nil {
		return nil, status.Error(codes.Unavailable, "folder policy inspection is not configured on this server")
	}
	subjectID := optionalSubject(ctx)
	folder, err := gspaths.CanonicalPrefix(strings.TrimSpace(req.GetPath()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "path: %v", err)
	}
	account := strings.Split(strings.Trim(folder, "/"), "/")[0]
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, account, folder, authz.ActionRead); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(req.GetRef())
	if err := requireDefaultTargetRef(ref); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = automations.DefaultRef
	}
	head, err := s.Repository.GetRef(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId())
	if err != nil {
		return nil, grpcError(err)
	}

	out := &corev1.FolderPolicyResponse{
		Folder:   folder,
		Account:  account,
		CommitId: head.GetCommitId(),
	}

	// 1. Nearest .gitslice/owners.yaml governing this folder.
	ownerReader := newGitsliceTreeReader(s.Repository, s.Tree.Objects, maxAutomationFileBytes, "", automations.ErrNotFound)
	for _, cfgPath := range owners.ConfigPaths(folder + "/_") {
		data, exists, readErr := ownerReader.ReadConfig(ctx, tree, cfgPath)
		if readErr != nil {
			return nil, grpcError(readErr)
		}
		if !exists {
			continue
		}
		sourceFolder := strings.TrimSuffix(cfgPath, "/"+owners.ConfigName)
		parsed, parseErr := owners.Parse(sourceFolder, data)
		if parseErr != nil {
			out.Problems = append(out.Problems, &corev1.FolderPolicyProblem{
				Kind:    "owners",
				Folder:  sourceFolder,
				File:    cfgPath,
				Message: parseErr.Error(),
			})
			break
		}
		out.OwnerConfigPath = cfgPath
		out.OwnerSourceFolder = sourceFolder
		for _, rule := range parsed.Rules {
			out.OwnerRules = append(out.OwnerRules, &corev1.FolderOwnerRule{
				Paths:        append([]string(nil), rule.Paths...),
				Owners:       append([]string(nil), rule.Owners...),
				Require:      int32(rule.Require),
				RequireHuman: int32(rule.RequireHuman),
			})
		}
		break
	}

	// 2. Checks along ancestor chain from /<account> down to folder.
	logicalFolder := ""
	accountPrefix := "/" + account
	if folder != accountPrefix {
		logicalFolder = strings.TrimPrefix(folder, accountPrefix+"/")
	}
	checkResolver := checks.NewResolver(checkTreeReader{
		repository: s.Repository,
		objects:    s.Tree.Objects,
		account:    account,
	})
	folderChecks, checkProblems, err := checks.ListForFolder(ctx, checkResolver, tree, logicalFolder)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, fc := range folderChecks {
		defFolder := gitslicecfg.AccountStoragePath(account, fc.SourceDir)
		cfgPath := strings.TrimSuffix(defFolder, "/") + "/.gitslice/" + checks.ConfigName
		out.Checks = append(out.Checks, &corev1.FolderCheckDefinition{
			Id:         fc.ID,
			Name:       fc.LocalName,
			Folder:     defFolder,
			ConfigPath: cfgPath,
			Run:        fc.Check.Run,
			Paths:      append([]string(nil), fc.Check.Paths...),
			StartedBy:  fc.Check.StartedBy,
			Required:   fc.Check.Required,
		})
	}
	for _, p := range checkProblems {
		defFolder := gitslicecfg.AccountStoragePath(account, p.SourceDir)
		cfgPath := strings.TrimSuffix(defFolder, "/") + "/.gitslice/" + checks.ConfigName
		out.Problems = append(out.Problems, &corev1.FolderPolicyProblem{
			Kind:    "checks",
			Folder:  defFolder,
			File:    cfgPath,
			Message: p.Message,
		})
	}

	// 3. Triggers along ancestor chain from /<account> down to folder.
	matches, trigProblems, err := automations.List(ctx, s.Tree, tree, account, folder)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, m := range matches {
		out.Automations = append(out.Automations, automationDefinitionProto(m))
	}
	for _, p := range trigProblems {
		out.Problems = append(out.Problems, &corev1.FolderPolicyProblem{
			Kind:    "triggers",
			Folder:  p.Folder,
			File:    p.File,
			Message: p.Message,
		})
	}

	// 4. Effective secrets applying at folder (metadata only, authorized by role).
	if s.Slices != nil && subjectID != "" {
		isAdmin, adminErr := canAuthorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin)
		if adminErr != nil {
			return nil, adminErr
		}
		canSeeSecretNames := isAdmin
		if !canSeeSecretNames {
			canSeeSecretNames = s.ensureAutomationFolderAccess(ctx, subjectID, account, folder, authz.ActionUseSecrets) == nil
		}
		if canSeeSecretNames {
			out.CanManageSecrets = isAdmin
			allSecrets, secErr := s.Slices.ListSecrets(ctx, account)
			if secErr != nil {
				return nil, grpcError(secErr)
			}
			type effectiveSecret struct {
				secret       storage.Secret
				sourceFolder string
			}
			byName := map[string]effectiveSecret{}
			for _, sec := range allSecrets {
				at, ok := storage.SecretAppliesAt(sec.Paths, folder)
				if !ok {
					continue
				}
				if prev, exists := byName[sec.Name]; !exists || len(at) > len(prev.sourceFolder) {
					byName[sec.Name] = effectiveSecret{secret: sec, sourceFolder: at}
				}
			}
			names := make([]string, 0, len(byName))
			for name := range byName {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				eff := byName[name]
				entry := &corev1.FolderSecretEntry{Name: name}
				if isAdmin {
					entry.SecretId = eff.secret.ID
					entry.SourceFolder = eff.sourceFolder
					entry.Paths = append([]string(nil), eff.secret.Paths...)
				}
				out.Secrets = append(out.Secrets, entry)
			}
		}

		// 5. Account CI runner pool for account members.
		if authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead) == nil {
			ids, names, poolErr := s.Slices.AccountCIDaemons(ctx, account)
			if poolErr == nil {
				out.CiDaemonIds = ids
				out.CiDaemonNames = names
			}
		}
	}

	return out, nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sliceTemplateLookup resolves action.uses references against Gitslice's global
// repository tree and immutable slice tags, enforcing cross-account readability.
type sliceTemplateLookup struct {
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
	Auth       storage.AuthStore
}

func (l *sliceTemplateLookup) ResolveTemplateTree(ctx context.Context, callerAccount, callerCommitID, callerRootTreeID string, ref automations.UsesRef) (*automations.ResolvedTemplateTree, error) {
	if l == nil || l.Repository == nil {
		return nil, fmt.Errorf("%w: repository unavailable", storage.ErrInvalid)
	}
	sameAccount := strings.EqualFold(strings.TrimSpace(ref.Account), strings.TrimSpace(callerAccount))
	if !sameAccount && ref.Pin == "" {
		return nil, fmt.Errorf("%w: cross-account uses %s must be pinned with @<tag> or @sha256:<commit>", storage.ErrInvalid, ref.Raw)
	}

	// Find readable slices covering ref.Folder (and collect available immutable tags).
	var readableSliceIDs []string
	var tagNames []string
	tagSeen := map[string]bool{}
	tagCommits := map[string]string{}
	if l.Slices != nil {
		covering, err := l.Slices.CoveringIDsByPath(ctx, []string{ref.Folder})
		if err != nil {
			return nil, err
		}
		for _, sliceID := range covering[ref.Folder] {
			slice, err := l.Slices.Get(ctx, sliceID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					continue
				}
				return nil, err
			}
			isPublic := strings.EqualFold(strings.TrimSpace(slice.GetDefinition().GetVisibility()), "public")
			if !sameAccount && !isPublic {
				continue
			}
			readableSliceIDs = append(readableSliceIDs, sliceID)
			tags, err := l.Slices.ListTags(ctx, sliceID)
			if err != nil {
				continue
			}
			for _, t := range tags {
				name := strings.TrimSpace(t.Name)
				if name == "" {
					continue
				}
				if !tagSeen[name] {
					tagSeen[name] = true
					tagNames = append(tagNames, name)
					tagCommits[name] = t.CommitID
				}
			}
		}
	}
	sort.Strings(tagNames)

	if !sameAccount && len(readableSliceIDs) == 0 {
		return nil, fmt.Errorf("%w: template %s is not in a public slice readable by %s", storage.ErrUnauthorized, ref.Raw, callerAccount)
	}

	switch ref.PinKind {
	case "":
		// Same-account live reference: use caller's tree/commit when provided, else head of refs/global/main.
		if callerRootTreeID != "" {
			return &automations.ResolvedTemplateTree{
				CommitID:      callerCommitID,
				RootTreeID:    callerRootTreeID,
				ResolvedRef:   "live",
				AvailableTags: tagNames,
			}, nil
		}
		head, err := l.Repository.GetRef(ctx, automations.DefaultRef)
		if err != nil {
			return nil, err
		}
		treeID, err := l.Repository.RootTreeForCommit(ctx, head.GetCommitId())
		if err != nil {
			return nil, err
		}
		return &automations.ResolvedTemplateTree{
			CommitID:      head.GetCommitId(),
			RootTreeID:    treeID,
			ResolvedRef:   automations.DefaultRef,
			AvailableTags: tagNames,
		}, nil

	case "commit":
		treeID, err := l.Repository.RootTreeForCommit(ctx, ref.Pin)
		if err != nil {
			return nil, err
		}
		return &automations.ResolvedTemplateTree{
			CommitID:      ref.Pin,
			RootTreeID:    treeID,
			ResolvedRef:   ref.Pin,
			AvailableTags: tagNames,
		}, nil

	case "tag":
		commitID := tagCommits[ref.Pin]
		if commitID == "" {
			return nil, fmt.Errorf("%w: tag %q not found on any readable slice covering %s", storage.ErrNotFound, ref.Pin, ref.Folder)
		}
		treeID, err := l.Repository.RootTreeForCommit(ctx, commitID)
		if err != nil {
			return nil, err
		}
		return &automations.ResolvedTemplateTree{
			CommitID:      commitID,
			RootTreeID:    treeID,
			ResolvedRef:   ref.Pin,
			AvailableTags: tagNames,
		}, nil

	default:
		return nil, fmt.Errorf("%w: unsupported pin kind %q", storage.ErrInvalid, ref.PinKind)
	}
}

// enrichAutomationMatch best-effort resolves action.uses on a listed automation
// so CLI and Web UI see inherited unbound template inputs.
func (s *AutomationService) enrichAutomationMatch(ctx context.Context, account, commitID, treeID string, m automations.Match) automations.Match {
	if m.Automation.Action.Kind != automations.ActionUses || m.Automation.Action.Uses == nil {
		return m
	}
	ref, err := automations.ParseUsesRef(m.Automation.Action.Uses.Ref)
	if err != nil {
		return m
	}
	var lookup automations.TemplateLookup
	if s.Engine != nil {
		lookup = s.Engine.TemplateLookup()
	}
	if lookup == nil {
		lookup = &sliceTemplateLookup{Repository: s.Repository, Slices: s.Slices, Auth: s.Auth}
	}
	loaded, err := automations.ResolveTemplate(ctx, s.Tree, lookup, account, commitID, treeID, ref)
	if err != nil {
		return m
	}
	m.Automation.Inputs = automations.EffectiveInputsForUses(m.Automation, loaded.Automation)
	return m
}

func (s *AutomationService) InspectAutomationTemplate(ctx context.Context, req *corev1.InspectAutomationTemplateRequest) (*corev1.InspectAutomationTemplateResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := automations.ParseUsesRef(req.GetRef())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, ref.Account, ref.Folder, authz.ActionRead); err != nil {
		return nil, err
	}
	callerAccount := strings.TrimSpace(req.GetCallerAccount())
	if callerAccount == "" && ref.Pin == "" {
		// Authenticated reader inspecting a template directly at HEAD.
		callerAccount = ref.Account
	}
	lookup := &sliceTemplateLookup{Repository: s.Repository, Slices: s.Slices, Auth: s.Auth}
	loaded, err := automations.ResolveTemplate(ctx, s.Tree, lookup, callerAccount, "", "", ref)
	if err != nil {
		return nil, grpcError(err)
	}
	match := automations.Match{
		ID:         ref.ID,
		Name:       ref.Name,
		Folder:     ref.Folder,
		Automation: loaded.Automation,
	}
	return &corev1.InspectAutomationTemplateResponse{
		Template:      automationDefinitionProto(match),
		Account:       ref.Account,
		Folder:        ref.Folder,
		CommitId:      loaded.CommitID,
		ResolvedRef:   loaded.ResolvedRef,
		AvailableTags: loaded.AvailableTags,
	}, nil
}

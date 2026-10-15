package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

func (r Runner) shellScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceRef string) (rootPath, scopeLabel string, workspaceScoped bool, syntheticDirs map[string]*corev1.TreeEntry, mutationSlice *corev1.Slice, err error) {
	if strings.TrimSpace(sliceRef) != "" {
		return r.explicitShellScope(ctx, cfg, conn, sliceRef)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		var cmdErr commandError
		if errors.As(err, &cmdErr) && cmdErr.Code == "not_in_workspace" {
			scopeLabel, syntheticDirs, err := r.personalHomeShellScope(ctx, cfg, conn)
			if err != nil {
				return "", "", false, nil, nil, err
			}
			if scopeLabel != "" {
				slice, err := r.personalHomeSlice(ctx, cfg, conn)
				if err != nil {
					return "", "", false, nil, nil, err
				}
				return "/", scopeLabel, false, syntheticDirs, slice, nil
			}
			return "/", "/", false, nil, nil, nil
		}
		return "", "", false, nil, nil, err
	}
	if len(ws.IncludedPaths) == 0 {
		return "", "", false, nil, nil, fmt.Errorf("workspace has no included paths")
	}
	rootPath, err = canonicalIncludedRoot(ws.IncludedPaths[0])
	if err != nil {
		return "", "", false, nil, nil, err
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
	})
	if err != nil {
		return "", "", false, nil, nil, err
	}
	return rootPath, ws.Account + ":" + ws.Slice, true, nil, slice, nil
}

func (r Runner) explicitShellScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceRef string) (rootPath, scopeLabel string, workspaceScoped bool, syntheticDirs map[string]*corev1.TreeEntry, mutationSlice *corev1.Slice, err error) {
	ref, err := r.resolveSliceRefInput(ctx, cfg, conn, sliceRef)
	if err != nil {
		return "", "", false, nil, nil, err
	}
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return "", "", false, nil, nil, err
	}
	if slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return "", "", false, nil, nil, fmt.Errorf("slice %s:%s has no included paths", ref.Account, ref.Slice)
	}
	rootPath = "/"
	return rootPath, ref.Account + ":" + ref.Slice, true, nil, slice, nil
}

func shellProjectionRoots(sliceRef string, slice *corev1.Slice, workspaceScoped bool) ([]string, error) {
	if strings.TrimSpace(sliceRef) == "" && workspaceScoped {
		return nil, nil
	}
	if slice == nil || slice.Definition == nil || len(slice.Definition.IncludedPaths) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, len(slice.Definition.IncludedPaths))
	for _, included := range slice.Definition.IncludedPaths {
		root, err := canonicalIncludedRoot(included)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func (r Runner) personalHomeShellScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (string, map[string]*corev1.TreeEntry, error) {
	slice, err := r.personalHomeSlice(ctx, cfg, conn)
	if err != nil {
		var cmdErr commandError
		if errors.As(err, &cmdErr) && cmdErr.Code == "no_home_slice" {
			return "", nil, nil
		}
		return "", nil, err
	}
	accountSlug := slice.Ref.Account
	homeRoot := "/" + accountSlug
	if slice.Definition != nil && len(slice.Definition.IncludedPaths) > 0 {
		root, err := cleanShellGlobalPath(slice.Definition.IncludedPaths[0])
		if err != nil {
			return "", nil, err
		}
		homeRoot = root
	}
	return accountSlug + ":home", map[string]*corev1.TreeEntry{
		homeRoot: &corev1.TreeEntry{
			Path: homeRoot,
			Name: path.Base(homeRoot),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		},
	}, nil
}

func canonicalIncludedRoot(value string) (string, error) {
	cleaned, err := cleanShellGlobalPath(value)
	if err != nil {
		return "", err
	}
	if cleaned == "/" {
		return "", fmt.Errorf("workspace included path must not be repository root")
	}
	return cleaned, nil
}

package cli

import (
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func workspaceRef(ws WorkspaceConfig) *corev1.WorkspaceRef {
	return &corev1.WorkspaceRef{Id: ws.Account + ":" + ws.Slice}
}

func workspaceInputToGlobalPath(ws WorkspaceConfig, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(ws.IncludedPaths) == 0 {
		return "", fmt.Errorf("workspace has no included paths")
	}
	if value == "" || value == "." {
		return canonicalIncludedRoot(ws.IncludedPaths[0])
	}
	if strings.HasPrefix(value, "/") {
		globalPath, err := cleanShellGlobalPath(value)
		if err != nil {
			return "", err
		}
		if !paths.InAnyPrefix(ws.IncludedPaths, globalPath) {
			return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
		}
		return globalPath, nil
	}
	return workspaceRelativePathToGlobalPath(ws, value)
}

func workspaceRelativePathToGlobalPath(ws WorkspaceConfig, value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if len(ws.IncludedPaths) == 0 {
		return "", fmt.Errorf("workspace has no included paths")
	}
	canonicalRel, err := cleanShellGlobalPath(value)
	if err == nil && paths.InAnyPrefix(ws.IncludedPaths, canonicalRel) {
		return canonicalRel, nil
	}
	globalPath, err := paths.FromWorkspacePath(ws.IncludedPaths[0], value)
	if err != nil {
		return "", err
	}
	if !paths.InAnyPrefix(ws.IncludedPaths, globalPath) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
	}
	return globalPath, nil
}

func workspaceRelPath(ws WorkspaceConfig, globalPath string) (string, error) {
	cleaned, err := cleanShellGlobalPath(globalPath)
	if err != nil {
		return "", err
	}
	if paths.InAnyPrefix(ws.IncludedPaths, cleaned) {
		return strings.TrimPrefix(cleaned, "/"), nil
	}
	return "", userError("outside_slice", "path is outside the workspace slice: "+globalPath, "Use a path under the workspace's bound slice.")
}

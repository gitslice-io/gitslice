package cli

import (
	"errors"
	"io"

	"gitslice.io/gitslice/internal/workspace"
)

// Error-translating adapters over internal/workspace preserving internal/cli's
// userError codes and hints. Non-translating helpers are called directly on
// internal/workspace.

func translateWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	var reserved *workspace.ReservedPathError
	if errors.As(err, &reserved) {
		return userError("reserved_path", reserved.Error(), "Choose a path outside .gs.")
	}
	var damaged *workspace.DamagedError
	if errors.As(err, &damaged) {
		return userError("workspace_damaged", damaged.Error(),
			"Restore .gs/slice.json, or move the files you want to keep out and run gs init <slice|account:slice> in a new empty directory.")
	}
	if errors.Is(err, workspace.ErrNotInWorkspace) {
		return userError("not_in_workspace", "not in a gitslice workspace", "Run gs init <slice|account:slice>.")
	}
	var outside *workspace.OutsideSliceError
	if errors.As(err, &outside) {
		return userError("outside_slice", outside.Error(), "Use a path under the workspace's bound slice.")
	}
	return err
}

func ensureNotReservedGsPath(p string) error {
	return translateWorkspaceError(workspace.EnsureNotReservedGsPath(p))
}

func cleanConfinedRelPath(rel string) (string, error) {
	out, err := workspace.CleanConfinedRelPath(rel)
	return out, translateWorkspaceError(err)
}

func ensureConfinedDir(root, relDir string) (string, error) {
	out, err := workspace.EnsureConfinedDir(root, relDir)
	return out, translateWorkspaceError(err)
}

func writeConfinedWorkspaceFile(root, rel string, mode uint32, r io.Reader) (string, int64, error) {
	target, n, err := workspace.WriteConfinedFile(root, rel, mode, r)
	return target, n, translateWorkspaceError(err)
}

func removeConfinedWorkspaceFile(root, rel string) error {
	return translateWorkspaceError(workspace.RemoveConfinedFile(root, rel))
}

func readConfinedWorkspaceFile(root, rel string) ([]byte, error) {
	data, err := workspace.ReadConfinedFile(root, rel)
	return data, translateWorkspaceError(err)
}

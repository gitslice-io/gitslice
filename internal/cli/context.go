package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

type contextOutput struct {
	CWD               string                  `json:"cwd"`
	ConfigPath        string                  `json:"config_path"`
	ServerAddr        string                  `json:"server_addr,omitempty"`
	SignedIn          bool                    `json:"signed_in"`
	SubjectID         string                  `json:"subject_id,omitempty"`
	AuthReason        string                  `json:"auth_reason,omitempty"`
	AuthError         string                  `json:"auth_error,omitempty"`
	Workspace         *contextWorkspaceOutput `json:"workspace,omitempty"`
	ActiveSlice       string                  `json:"active_slice,omitempty"`
	ActiveSliceSource string                  `json:"active_slice_source,omitempty"`
}

type contextWorkspaceOutput struct {
	Root               string   `json:"root"`
	Ref                string   `json:"ref"`
	SliceID            string   `json:"slice_id"`
	DefinitionHash     string   `json:"definition_hash,omitempty"`
	IncludedPaths      []string `json:"included_paths,omitempty"`
	BaseCommitID       string   `json:"base_commit_id,omitempty"`
	CurrentChangesetID string   `json:"current_changeset_id,omitempty"`
	CurrentPatchsetID  string   `json:"current_patchset_id,omitempty"`
}

func (r Runner) runContext(ctx context.Context, opts commandOptions) error {
	authStatus, authErr := r.probeAuthStatus(ctx)
	out := contextOutput{
		CWD:        r.cwd(),
		ConfigPath: r.userConfigPath(),
		ServerAddr: authStatus.ServerAddr,
		SignedIn:   authStatus.SignedIn,
		SubjectID:  authStatus.SubjectID,
		AuthReason: authStatus.Reason,
	}
	if authErr != nil {
		out.AuthError = authErr.Error()
	}
	if root, err := r.workspaceRoot(); err == nil {
		ws, wsErr := r.readWorkspaceConfig()
		if wsErr != nil {
			return wsErr
		}
		state, stateErr := r.readWorkspaceState()
		if stateErr != nil {
			return stateErr
		}
		ref := ws.Account + ":" + ws.Slice
		out.Workspace = &contextWorkspaceOutput{
			Root:               root,
			Ref:                ref,
			SliceID:            ws.SliceID,
			DefinitionHash:     ws.DefinitionHash,
			IncludedPaths:      ws.IncludedPaths,
			BaseCommitID:       state.BaseCommitID,
			CurrentChangesetID: state.CurrentChangesetID,
			CurrentPatchsetID:  state.CurrentPatchsetID,
		}
		if out.Workspace.BaseCommitID == "" {
			out.Workspace.BaseCommitID = ws.BaseCommitID
		}
		out.ActiveSlice = ref
		out.ActiveSliceSource = "workspace"
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	} else if authStatus.SignedIn && len(authStatus.Accounts) > 0 {
		out.ActiveSlice = authStatus.Accounts[0] + ":home"
		out.ActiveSliceSource = "signed_in_home"
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	return r.writeContextText(out)
}

func (r Runner) writeContextText(out contextOutput) error {
	fmt.Fprintf(r.Stdout, "cwd: %s\n", out.CWD)
	fmt.Fprintf(r.Stdout, "config: %s\n", out.ConfigPath)
	if out.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server: %s\n", out.ServerAddr)
	}
	if out.SignedIn {
		if out.SubjectID != "" {
			fmt.Fprintf(r.Stdout, "auth: signed in as %s\n", out.SubjectID)
		} else {
			fmt.Fprintln(r.Stdout, "auth: signed in")
		}
	} else {
		fmt.Fprintln(r.Stdout, "auth: not signed in")
		if out.AuthReason != "" {
			fmt.Fprintf(r.Stdout, "auth_reason: %s\n", strings.ReplaceAll(out.AuthReason, "_", " "))
		}
		if out.AuthError != "" {
			fmt.Fprintf(r.Stdout, "auth_error: %s\n", out.AuthError)
		}
	}
	if out.Workspace != nil {
		fmt.Fprintf(r.Stdout, "workspace: %s\n", out.Workspace.Root)
		fmt.Fprintf(r.Stdout, "workspace_slice: %s\n", out.Workspace.Ref)
		if out.Workspace.BaseCommitID != "" {
			fmt.Fprintf(r.Stdout, "base_commit: %s\n", out.Workspace.BaseCommitID)
		}
		if out.Workspace.CurrentChangesetID != "" {
			fmt.Fprintf(r.Stdout, "current_changeset: %s\n", firstNonEmpty(storage.ShortChangesetID(out.Workspace.CurrentChangesetID), out.Workspace.CurrentChangesetID))
		}
	} else {
		fmt.Fprintln(r.Stdout, "workspace: none")
	}
	if out.ActiveSlice != "" {
		fmt.Fprintf(r.Stdout, "active_slice: %s (%s)\n", out.ActiveSlice, out.ActiveSliceSource)
	}
	return nil
}

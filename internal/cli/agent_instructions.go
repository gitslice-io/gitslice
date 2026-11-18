package cli

import (
	"gitslice.io/gitslice/internal/agentd"
)

func agentWorkspaceInstructions(includedPaths []string) string {
	return agentd.WorkspaceInstructions(includedPaths)
}

func agentWorkspaceEditableScope(includedPaths []string) string {
	return agentd.WorkspaceEditableScope(includedPaths)
}

func conversationIncludedPaths(workdir string) []string {
	return agentd.ConversationIncludedPaths(workdir)
}

package cli

import (
	"gitslice.io/gitslice/internal/agentd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func normalizeAgentWorkspaceEventLinks(event *corev1.AgentEvent, workdir string) {
	agentd.NormalizeWorkspaceEventLinks(event, workdir)
}

func rewriteAgentWorkspaceMarkdownLinks(text, workdir string) string {
	return agentd.RewriteWorkspaceMarkdownLinks(text, workdir)
}

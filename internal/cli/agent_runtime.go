package cli

import (
	"context"

	"gitslice.io/gitslice/internal/agentd"
)

type (
	agentRuntimeEvent = agentd.RuntimeEvent
	agentRuntime      = agentd.Runtime
	agentSession      = agentd.Session
	codexRuntime      = agentd.CodexRuntime
	codexTurn         = agentd.CodexTurn
	deltaAccum        = agentd.DeltaAccum
	safeBuffer        = agentd.SafeBuffer
)

var (
	codexSandboxMode           = agentd.CodexSandboxMode
	sanitizedAgentRuntimeEnv   = agentd.SanitizedRuntimeEnv
	withAgentRuntimeEnv        = agentd.WithRuntimeEnv
	agentRuntimeEnvFrom        = agentd.RuntimeEnvFrom
	mergeAgentEnv              = agentd.MergeEnv
	isSensitiveAgentHostEnvKey = agentd.IsSensitiveHostEnvKey
)

func (r Runner) agentRuntimeForName(name string) (agentRuntime, error) {
	return agentd.RuntimeForName(name, cliVersionInfo().Version)
}

func agentRuntimeForName(name string) (agentRuntime, error) {
	return agentd.RuntimeForName(name, cliVersionInfo().Version)
}

func withCodexBinary(binary string) codexRuntime {
	return codexRuntime{Binary: binary, Version: cliVersionInfo().Version}
}

var _ context.Context = nil

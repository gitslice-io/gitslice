package agentd

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/secretmask"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func ResolveRunChecksServerAddr(configuredAddr, requestedAddr string) (string, error) {
	configured := strings.TrimSpace(configuredAddr)
	requested := strings.TrimSpace(requestedAddr)
	if configured != "" {
		return configured, nil
	}
	if requested != "" && IsLoopbackDialAddr(requested) {
		return requested, nil
	}
	if requested == "" {
		return "", fmt.Errorf("check server address is not configured")
	}
	return "", fmt.Errorf("refusing untrusted check server address %q", requested)
}

func IsLoopbackDialAddr(addr string) bool {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "://") {
		parts := strings.SplitN(trimmed, "://", 2)
		if strings.EqualFold(parts[0], "unix") {
			return true
		}
		trimmed = parts[1]
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	host, _, err := net.SplitHostPort(trimmed)
	if err != nil {
		host = trimmed
	}
	return isLoopbackHost(host)
}

func CheckSpecFromProto(spec *corev1.CheckRunSpec) checks.CheckSpec {
	if spec == nil {
		return checks.CheckSpec{}
	}
	timeout := time.Duration(0)
	if spec.GetTimeoutMs() > 0 {
		timeout = time.Duration(spec.GetTimeoutMs()) * time.Millisecond
	}
	return checks.CheckSpec{
		Name:             spec.GetName(),
		Run:              spec.GetCommand(),
		Image:            spec.GetImage(),
		Setup:            append([]string(nil), spec.GetSetup()...),
		Cache:            append([]string(nil), spec.GetCache()...),
		WorkingDir:       spec.GetWorkingDir(),
		MaterializePaths: append([]string(nil), spec.GetMaterializePaths()...),
		Env:              copyCheckEnv(spec.GetEnv()),
		Network:          spec.GetNetwork(),
		Memory:           spec.GetMemory(),
		CPUs:             spec.GetCpus(),
		Timeout:          timeout,
		Build:            checkImageBuildFromProto(spec.GetBuild()),
		Services:         checkServicesFromProto(spec.GetServices()),
	}
}

func checkImageBuildFromProto(build *corev1.CheckImageBuild) *checks.ImageBuild {
	if build == nil || strings.TrimSpace(build.GetDockerfile()) == "" {
		return nil
	}
	return &checks.ImageBuild{
		ContextDir: build.GetContextDir(),
		Dockerfile: build.GetDockerfile(),
		Inputs:     append([]string(nil), build.GetInputs()...),
		Timeout:    time.Duration(build.GetTimeoutMs()) * time.Millisecond,
	}
}

func checkServicesFromProto(services []*corev1.CheckServiceContainer) []checks.ServiceSpec {
	var out []checks.ServiceSpec
	for _, service := range services {
		if service == nil {
			continue
		}
		out = append(out, checks.ServiceSpec{
			Name:         service.GetName(),
			Image:        service.GetImage(),
			Env:          copyCheckEnv(service.GetEnv()),
			Ready:        service.GetReady(),
			ReadyTimeout: time.Duration(service.GetReadyTimeoutMs()) * time.Millisecond,
			Memory:       service.GetMemory(),
		})
	}
	return out
}

func copyCheckEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func DetectContainerRuntimes() []string {
	rt := checkexec.DetectRuntime(context.Background())
	if !rt.OK() {
		return nil
	}
	return []string{rt.Name}
}

func CheckSecretMasker(spec *corev1.CheckRunSpec) *secretmask.Masker {
	var values []string
	for _, name := range spec.GetSecretEnv() {
		if value, ok := spec.GetEnv()[name]; ok {
			values = append(values, value)
		}
	}
	return secretmask.New(values)
}

func OneLineSummary(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) <= 200 {
		return value
	}
	return value[:197] + "..."
}

func RunCheckIDs(req *corev1.RunChecks) []string {
	if req == nil {
		return nil
	}
	ids := make([]string, 0, len(req.GetChecks()))
	for _, spec := range req.GetChecks() {
		id := strings.TrimSpace(spec.GetRunId())
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

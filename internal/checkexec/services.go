package checkexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

const (
	networkNamePrefix          = "gitslice-net-"
	serviceNamePrefix          = "gitslice-svc-"
	defaultServiceReadyTimeout = 60 * time.Second
	defaultServiceMemory       = "1g"
	serviceReadyPoll           = 500 * time.Millisecond
	serviceLogTailLines        = "40"
)

// runServices are the service containers of one check run and the network
// they share with it.
type runServices struct {
	rt         RuntimeInfo
	network    string
	containers []string
}

// startServices creates a network for one run, starts every service on it
// under its name, and waits until each is ready. The network is internal
// unless the check may use the network. On failure everything started is
// removed and the result says which service failed and why.
func startServices(ctx context.Context, rt RuntimeInfo, spec checks.CheckSpec) (*runServices, string, *Result, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return nil, "", nil, err
	}
	s := &runServices{rt: rt, network: networkNamePrefix + suffix}
	args := []string{"network", "create"}
	if !spec.Network {
		args = append(args, "--internal")
	}
	args = append(args, s.network)
	if out, err := exec.CommandContext(ctx, rt.Path, args...).CombinedOutput(); err != nil {
		return nil, "", nil, fmt.Errorf("create the run's network: %s", strings.TrimSpace(firstNonEmptyString(string(out), err.Error())))
	}

	var notes []string
	for _, service := range spec.Services {
		start := time.Now()
		result, err := s.start(ctx, service)
		if err != nil || result != nil {
			s.stop()
			return nil, "", result, err
		}
		notes = append(notes, fmt.Sprintf("%s (%s, ready in %s)", service.Name, service.Image, time.Since(start).Round(100*time.Millisecond)))
	}
	note := ""
	if len(notes) > 0 {
		note = "started services: " + strings.Join(notes, ", ")
	}
	return s, note, nil, nil
}

func (s *runServices) start(ctx context.Context, service checks.ServiceSpec) (*Result, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	name := serviceNamePrefix + suffix
	args := []string{
		"run", "-d",
		"--name", name,
		"--network", s.network,
		"--network-alias", service.Name,
		"--security-opt=no-new-privileges",
		"--pids-limit=" + defaultContainerPIDsLimit,
		"--memory=" + containerLimitValue(service.Memory, defaultServiceMemory),
	}
	for _, key := range sortedEnvKeys(service.Env) {
		args = append(args, "-e", key)
	}
	args = append(args, service.Image)
	cmd := exec.CommandContext(ctx, s.rt.Path, args...)
	cmd.Env = append(os.Environ(), envPairs(service.Env)...)
	out, err := cmd.CombinedOutput()
	s.containers = append(s.containers, name)
	if err != nil {
		return &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("service %s did not start", service.Name),
			Log:      string(out),
		}, nil
	}
	if strings.TrimSpace(service.Ready) == "" {
		return nil, nil
	}

	timeout := service.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultServiceReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if exec.CommandContext(ctx, s.rt.Path, "exec", name, "sh", "-c", service.Ready).Run() == nil {
			return nil, nil
		}
		state, _ := exec.CommandContext(ctx, s.rt.Path, "inspect", "--format", "{{.State.Running}}", name).Output()
		if strings.TrimSpace(string(state)) == "false" {
			return s.failed(service, "exited before it was ready", name), nil
		}
		if time.Now().After(deadline) {
			return s.failed(service, fmt.Sprintf("was not ready after %s", timeout), name), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(serviceReadyPoll):
		}
	}
}

func (s *runServices) failed(service checks.ServiceSpec, why, container string) *Result {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	logs, _ := exec.CommandContext(ctx, s.rt.Path, "logs", "--tail", serviceLogTailLines, container).CombinedOutput()
	return &Result{
		Status:   "errored",
		ExitCode: -1,
		Summary:  fmt.Sprintf("service %s %s", service.Name, why),
		Log:      fmt.Sprintf("service %s (%s) %s; its last log lines:\n%s", service.Name, service.Image, why, logs),
	}
}

// stop removes the run's services and network, whatever state they are in.
func (s *runServices) stop() {
	if s == nil {
		return
	}
	for _, name := range s.containers {
		removeContainer(s.rt.Path, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, s.rt.Path, "network", "rm", s.network).Run()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

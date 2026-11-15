package checkexec

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

const (
	networkNamePrefix          = "gitslice-net-"
	serviceNamePrefix          = "gitslice-svc-"
	defaultServiceReadyTimeout = 60 * time.Second
	defaultServiceMemory       = "1g"
	defaultServiceCPUs         = "1"
	serviceReadyPoll           = 500 * time.Millisecond
	serviceLogTailLines        = "40"
)

var validServiceHostPattern = regexp.MustCompile(`\A[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\z`)

// runServices are the service containers of one check run and the network
// they share with it.
type runServices struct {
	rt         RuntimeInfo
	network    string
	hosts      []string // service names the check resolves to 127.0.0.1, on the host network
	containers []string
}

// startServices creates a network for one run, starts every service on it
// under its name, and waits until each is ready. The network is internal
// unless the check may use the network. On failure everything started is
// removed and the result says which service failed and why.
func startServices(ctx context.Context, rt RuntimeInfo, spec checks.CheckSpec) (*runServices, string, *Result, error) {
	if len(spec.Services) == 0 {
		return nil, "", nil, nil
	}
	for _, service := range spec.Services {
		if err := validateServiceSpec(service); err != nil {
			return nil, "", nil, err
		}
	}

	suffix, err := randomSuffix()
	if err != nil {
		return nil, "", nil, err
	}
	s := &runServices{rt: rt, network: networkNamePrefix + suffix}
	if HostNetwork() {
		// No network of its own: the services listen on the host's, and the
		// check finds them by name at 127.0.0.1.
		s.network = hostNetwork
		for _, service := range spec.Services {
			s.hosts = append(s.hosts, service.Name)
		}
	} else {
		args := []string{"network", "create"}
		if !spec.Network {
			args = append(args, "--internal")
		}
		args = append(args, "--", s.network)
		netCmd := exec.CommandContext(ctx, rt.Path, args...)
		netCmd.Env = containerClientEnv()
		if out, err := netCmd.CombinedOutput(); err != nil {
			return nil, "", nil, fmt.Errorf("create the run's network: %s", strings.TrimSpace(firstNonEmptyString(string(out), err.Error())))
		}
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

func validateServiceSpec(service checks.ServiceSpec) error {
	if !validServiceHostPattern.MatchString(service.Name) {
		return fmt.Errorf("invalid service name %q", service.Name)
	}
	if err := checks.ValidateImageRef(service.Image); err != nil {
		return fmt.Errorf("service %q image: %w", service.Name, err)
	}
	if err := checks.ValidateEnvMap(service.Env); err != nil {
		return fmt.Errorf("service %q env: %w", service.Name, err)
	}
	if strings.ContainsRune(service.Ready, '\x00') {
		return fmt.Errorf("service %q ready command contains NUL", service.Name)
	}
	return nil
}

func serviceRunArgs(name, network string, service checks.ServiceSpec, envFile string) ([]string, error) {
	if err := validateServiceSpec(service); err != nil {
		return nil, err
	}
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n\x00") {
		return nil, fmt.Errorf("invalid service container name %q", name)
	}
	memLimit, err := cappedMemoryLimit(service.Memory, defaultServiceMemory)
	if err != nil {
		return nil, fmt.Errorf("service %q memory: %w", service.Name, err)
	}
	cpuLimit, err := cappedCPULimit("", defaultServiceCPUs)
	if err != nil {
		return nil, fmt.Errorf("service %q cpus: %w", service.Name, err)
	}

	args := []string{
		"run", "-d",
		"--name", name,
		"--network", network,
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		"--pids-limit=" + defaultContainerPIDsLimit,
		"--memory=" + memLimit,
		"--cpus=" + cpuLimit,
	}
	if network != hostNetwork {
		args = append(args, "--network-alias", service.Name)
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, "--", service.Image)
	return args, nil
}

func (s *runServices) start(ctx context.Context, service checks.ServiceSpec) (*Result, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	name := serviceNamePrefix + suffix
	envFile, cleanupEnv, err := writeContainerEnvFile(service.Env, "")
	if err != nil {
		return nil, err
	}
	defer cleanupEnv()

	args, err := serviceRunArgs(name, s.network, service, envFile)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, s.rt.Path, args...)
	cmd.Env = containerClientEnv()
	out, err := cmd.CombinedOutput()
	cleanupEnv()
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
		execCmd := exec.CommandContext(ctx, s.rt.Path, "exec", "--", name, "sh", "-c", service.Ready)
		execCmd.Env = containerClientEnv()
		if execCmd.Run() == nil {
			return nil, nil
		}
		inspectCmd := exec.CommandContext(ctx, s.rt.Path, "inspect", "--format", "{{.State.Running}}", "--", name)
		inspectCmd.Env = containerClientEnv()
		state, _ := inspectCmd.Output()
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
	logCmd := exec.CommandContext(ctx, s.rt.Path, "logs", "--tail", serviceLogTailLines, "--", container)
	logCmd.Env = containerClientEnv()
	logs, _ := logCmd.CombinedOutput()
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
	if s.network == "" || s.network == hostNetwork {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	rmCmd := exec.CommandContext(ctx, s.rt.Path, "network", "rm", "--", s.network)
	rmCmd.Env = containerClientEnv()
	_ = rmCmd.Run()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

package cli

import (
	"context"
	"path/filepath"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/agentd"
)

type (
	agentServiceProbes  = agentd.ServiceProbes
	agentInstallOptions = agentd.InstallOptions
)

func (r Runner) agentServiceProbes() agentServiceProbes {
	return agentd.MergeServiceProbes(r.AgentService)
}

func (r Runner) agentInstallCommand(opts *commandOptions) *cobra.Command {
	in := agentInstallOptions{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install an agent daemon as a user service",
		Args:  noArgs("gs agent install [--ci] [--name NAME] [--slice account/slice]... [--print]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentInstall(cmd.Context(), *opts, in)
		},
	}
	cmd.Flags().BoolVar(&in.CI, "ci", false, "install a checks-only daemon")
	cmd.Flags().StringVar(&in.Name, "name", "", "daemon name (defaults to this machine name)")
	cmd.Flags().StringArrayVar(&in.Slices, "slice", nil, "limit agent conversations to a slice (<account>/<slice>); repeatable")
	cmd.Flags().BoolVar(&in.Print, "print", false, "print the service definition without installing it")
	return cmd
}

func (r Runner) agentUninstallCommand(opts *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall [NAME]",
		Short: "Stop and remove an installed agent daemon",
		Args:  maxArgs(1, "gs agent uninstall [NAME]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return r.runAgentUninstall(cmd.Context(), *opts, name)
		},
	}
}

func (r Runner) runAgentInstall(ctx context.Context, opts commandOptions, in agentInstallOptions) error {
	if in.CI && len(in.Slices) > 0 {
		return userError("invalid_args", "--ci cannot be used with --slice", "Checks-only daemons select slices via gs agent expose / gs slice set-ci-daemon.")
	}
	allowedSlices, err := normalizeDaemonAllowedSlices(in.Slices)
	if err != nil {
		return err
	}
	workingDir, err := r.agentServiceWorkingDir(in.CI)
	if err != nil {
		return err
	}
	return agentd.InstallService(ctx, r.agentServiceProbes(), r.homeDir(), workingDir, in, allowedSlices, opts.Quiet, r.stdout())
}

func (r Runner) runAgentUninstall(ctx context.Context, opts commandOptions, requestedName string) error {
	return agentd.UninstallService(ctx, r.agentServiceProbes(), r.homeDir(), requestedName, opts.Quiet, r.stdout())
}

func (r Runner) agentServiceWorkingDir(ci bool) (string, error) {
	if ci {
		return filepath.Abs(r.homeDir())
	}
	if err := r.requireAgentWorkspaceDir(); err != nil {
		return "", err
	}
	return filepath.Abs(r.cwd())
}

var (
	renderSystemdAgentService = agentd.RenderSystemdService
	renderLaunchdAgentService = agentd.RenderLaunchdService
	agentServiceDefinition    = agentd.ServiceDefinition
	agentServiceFilePath      = agentd.ServiceFilePath
)

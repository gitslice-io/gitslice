package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/agentd"
)

type (
	agentDoctorCheck  = agentd.DoctorCheck
	agentDoctorOutput = agentd.DoctorOutput
)

type agentDoctorProbes = agentd.DoctorProbes

func defaultAgentDoctorProbes() agentDoctorProbes {
	return agentd.DefaultDoctorProbes()
}

func (r Runner) agentDoctorProbes() agentDoctorProbes {
	return agentd.MergeDoctorProbes(r.AgentDoctorProbes)
}

func (r Runner) agentDoctorCommand(opts *commandOptions) *cobra.Command {
	ci := false
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this machine can run an agent daemon",
		Args:  noArgs("gs agent doctor [--ci] [--json]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentDoctor(cmd.Context(), *opts, ci)
		},
	}
	cmd.Flags().BoolVar(&ci, "ci", false, "check a checks-only CI daemon")
	return cmd
}

func (r Runner) runAgentDoctor(ctx context.Context, opts commandOptions, ci bool) error {
	checks := r.agentDoctorChecks(ctx, ci)
	if opts.jsonOutput() {
		if err := r.writeJSONOutput(opts, agentDoctorOutput{Checks: checks}); err != nil {
			return err
		}
	} else if !opts.Quiet {
		for _, check := range checks {
			fmt.Fprintf(r.stdout(), "%s  %s  %s\n", check.Status, check.Name, check.Detail)
		}
	}
	return agentd.DoctorFailure(checks)
}

func (r Runner) agentDoctorChecks(ctx context.Context, ci bool) []agentDoctorCheck {
	cfg, cfgErr := r.readPartialUserConfig()
	return agentd.RunDoctorChecks(ctx, ci, agentd.DoctorConfig{
		Token:         cfg.Token,
		SubjectID:     cfg.SubjectID,
		ConfigPath:    r.userConfigPath(),
		ConfigErr:     cfgErr,
		DescribeToken: describeToken,
	}, r.agentDoctorProbes())
}

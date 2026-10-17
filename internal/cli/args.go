package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func requireSubcommand(name string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		_ = cmd.Help()
		return userError("missing_subcommand", "missing "+name+" subcommand", "Run gs "+name+" --help to list available subcommands.")
	}
}

func noArgs(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return userError("unexpected_args", "unexpected arguments: "+strings.Join(args, " "), "Usage: "+usage)
	}
}

func exactArgs(want int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == want {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected %d argument(s), got %d", want, len(args)), "Usage: "+usage)
	}
}

func maxArgs(max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= max {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected at most %d argument(s), got %d", max, len(args)), "Usage: "+usage)
	}
}

func rangeArgs(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min && len(args) <= max {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected %d to %d argument(s), got %d", min, max, len(args)), "Usage: "+usage)
	}
}

func minArgs(want int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= want {
			return nil
		}
		return userError("invalid_args", fmt.Sprintf("expected at least %d argument(s), got %d", want, len(args)), "Usage: "+usage)
	}
}

func parsePositiveDurationFlag(name, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	duration, err := time.ParseDuration(trimmed)
	if err != nil || duration <= 0 {
		return 0, userError("invalid_duration", "invalid --"+name+" duration "+value, "Use a positive duration such as 10s, 2m, or 500ms.")
	}
	return duration, nil
}

func parsePositiveIntArg(name, value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		return 0, userError("invalid_args", "invalid "+name+" "+value, "Use a positive integer.")
	}
	return parsed, nil
}

package cli

import (
	"context"
	"io"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/workspace"
	"google.golang.org/grpc"
)

// High-frequency type aliases, constants, and Runner adapters bridging
// internal/cli onto internal/cli/clibase. Low-fanout helpers are called
// directly on internal/cli/clibase.
type (
	commandOptions    = clibase.Options
	diffContextOption = clibase.DiffContext
	commandError      = clibase.Error
	userCodedError    = clibase.UserCodedError
	errorResponse     = clibase.ErrorResponse
	errorBody         = clibase.ErrorBody
)

const (
	ansiReset  = clibase.AnsiReset
	ansiDim    = clibase.AnsiDim
	ansiBlue   = clibase.AnsiBlue
	ansiCyan   = clibase.AnsiCyan
	ansiGreen  = clibase.AnsiGreen
	ansiRed    = clibase.AnsiRed
	ansiYellow = clibase.AnsiYellow

	schemaGroupAnnotation      = clibase.SchemaGroupAnnotation
	schemaExcludedAnnotation   = clibase.SchemaExcludedAnnotation
	schemaCollapsedAnnotation  = clibase.SchemaCollapsedAnnotation
	schemaUseAnnotation        = clibase.SchemaUseAnnotation
	schemaSummaryAnnotation    = clibase.SchemaSummaryAnnotation
	schemaOutputAnnotation     = clibase.SchemaOutputAnnotation
	schemaOutputSetAnnotation  = clibase.SchemaOutputSetAnnotation
	schemaOutputTypeAnnotation = clibase.SchemaOutputTypeAnnotation
)

func requireSubcommand(name string) func(*cobra.Command, []string) error {
	return clibase.RequireSubcommand(name)
}
func noArgs(usage string) cobra.PositionalArgs              { return clibase.NoArgs(usage) }
func exactArgs(want int, usage string) cobra.PositionalArgs { return clibase.ExactArgs(want, usage) }
func maxArgs(max int, usage string) cobra.PositionalArgs    { return clibase.MaxArgs(max, usage) }
func rangeArgs(min, max int, usage string) cobra.PositionalArgs {
	return clibase.RangeArgs(min, max, usage)
}
func parsePositiveDurationFlag(name, value string) (time.Duration, error) {
	return clibase.ParsePositiveDurationFlag(name, value)
}

func isTerminal(w any) bool                            { return clibase.IsTerminal(w) }
func colorize(enabled bool, code, value string) string { return clibase.Colorize(enabled, code, value) }

func (r Runner) writeJSONOutput(opts commandOptions, v any) error {
	return clibase.WriteJSONOutput(r.Stdout, opts, v)
}
func writeSchemaOutput[T any](r Runner, opts commandOptions, val T, textFn func() error) error {
	return clibase.WriteSchemaOutput(r.Stdout, opts, val, textFn)
}

func startProgress(ctx context.Context, verb, unit string, total int) *clibase.ProgressReporter {
	return clibase.StartProgress(ctx, verb, unit, total)
}

func userError(code, message, hint string) error { return clibase.UserError(code, message, hint) }
func isUserErrorCode(err error, code string) bool {
	return clibase.IsUserErrorCode(err, code)
}
func exitCodeForError(err error) int { return clibase.ExitCodeForError(err) }

func markSchemaGroup(cmds ...*cobra.Command)    { clibase.MarkSchemaGroup(cmds...) }
func markSchemaExcluded(cmds ...*cobra.Command) { clibase.MarkSchemaExcluded(cmds...) }
func withSchemaOutput[T any](cmds ...*cobra.Command) {
	clibase.WithSchemaOutput[T](cmds...)
}
func withSchemaOutputFields(cmd *cobra.Command, fields ...string) {
	clibase.WithSchemaOutputFields(cmd, fields...)
}

// runnerApp adapts Runner to clibase.App for extracted internal/cli/cmd/* subpackages.
type runnerApp struct {
	r Runner
}

func (r Runner) asApp() clibase.App { return runnerApp{r: r} }

func (a runnerApp) Stdin() io.Reader                       { return a.r.stdin() }
func (a runnerApp) Stdout() io.Writer                      { return a.r.stdout() }
func (a runnerApp) Stderr() io.Writer                      { return a.r.stderr() }
func (a runnerApp) Cwd() string                            { return a.r.cwd() }
func (a runnerApp) HomeDir() string                        { return a.r.homeDir() }
func (a runnerApp) UserConfigPath() string                 { return a.r.userConfigPath() }
func (a runnerApp) Version() string                        { return cliVersionInfo().Version }
func (a runnerApp) Getenv(key string) string               { return a.r.Getenv(key) }
func (a runnerApp) ColorEnabled(opts clibase.Options) bool { return a.r.colorEnabled(opts) }
func (a runnerApp) ReadUserConfig() (workspace.UserConfig, error) {
	return a.r.readUserConfig()
}
func (a runnerApp) ReadPartialUserConfig() (workspace.UserConfig, error) {
	return a.r.readPartialUserConfig()
}
func (a runnerApp) WriteUserConfig(cfg workspace.UserConfig) error {
	return a.r.writeUserConfig(cfg)
}
func (a runnerApp) ReadWorkspaceConfig() (workspace.Config, error) {
	return a.r.readWorkspaceConfig()
}
func (a runnerApp) WithSession(ctx context.Context, fn func(s clibase.Session) error) error {
	return a.r.withSession(ctx, func(s *Session) error {
		return fn(s)
	})
}
func (a runnerApp) DialServer(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return a.r.dialServer(ctx, addr)
}
func (a runnerApp) WriteJSONOutput(opts clibase.Options, v any) error {
	return a.r.writeJSONOutput(opts, v)
}

var _ clibase.Session = (*Session)(nil)
var _ clibase.App = runnerApp{}

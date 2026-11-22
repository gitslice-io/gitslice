package cli

import (
	"context"
	"io"
	"reflect"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// Type aliases and thin wrappers bridging internal/cli onto internal/cli/clibase.
type (
	commandOptions       = clibase.Options
	diffContextOption    = clibase.DiffContext
	commandError         = clibase.Error
	userCodedError       = clibase.UserCodedError
	errorResponse        = clibase.ErrorResponse
	errorBody            = clibase.ErrorBody
	domainErrorOverride  = clibase.DomainErrorOverride
	domainErrorOverrides = clibase.DomainErrorOverrides
	progressSink         = clibase.ProgressSink
	progressReporter     = clibase.ProgressReporter
)

const (
	defaultDiffContext = clibase.DefaultDiffContextLines

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

func addDiffContextFlag(cmd *cobra.Command, value *int) { clibase.AddDiffContextFlag(cmd, value) }
func diffContextFromFlag(cmd *cobra.Command, value int) diffContextOption {
	return clibase.DiffContextFromFlag(cmd, value)
}

func requireSubcommand(name string) func(*cobra.Command, []string) error {
	return clibase.RequireSubcommand(name)
}
func noArgs(usage string) cobra.PositionalArgs              { return clibase.NoArgs(usage) }
func exactArgs(want int, usage string) cobra.PositionalArgs { return clibase.ExactArgs(want, usage) }
func maxArgs(max int, usage string) cobra.PositionalArgs    { return clibase.MaxArgs(max, usage) }
func rangeArgs(min, max int, usage string) cobra.PositionalArgs {
	return clibase.RangeArgs(min, max, usage)
}
func minArgs(want int, usage string) cobra.PositionalArgs { return clibase.MinArgs(want, usage) }
func parsePositiveDurationFlag(name, value string) (time.Duration, error) {
	return clibase.ParsePositiveDurationFlag(name, value)
}
func parsePositiveIntArg(name, value string) (int, error) {
	return clibase.ParsePositiveIntArg(name, value)
}

func isTerminal(w any) bool                            { return clibase.IsTerminal(w) }
func colorize(enabled bool, code, value string) string { return clibase.Colorize(enabled, code, value) }
func colorUnifiedDiff(diffText string, enabled bool) string {
	return clibase.ColorUnifiedDiff(diffText, enabled)
}

func wantsJSON(args []string) bool                 { return clibase.WantsJSON(args) }
func parseJSONFields(raw string) ([]string, error) { return clibase.ParseJSONFields(raw) }
func writeJSON(w io.Writer, v any) error           { return clibase.WriteJSON(w, v) }
func (r Runner) writeJSONOutput(opts commandOptions, v any) error {
	return clibase.WriteJSONOutput(r.Stdout, opts, v)
}
func writeSchemaOutput[T any](r Runner, opts commandOptions, val T, textFn func() error) error {
	return clibase.WriteSchemaOutput(r.Stdout, opts, val, textFn)
}
func renderCommandOutput[T any](r Runner, opts commandOptions, val T, textFn func() error) error {
	return clibase.WriteSchemaOutput(r.Stdout, opts, val, textFn)
}

func withProgress(ctx context.Context, w io.Writer) context.Context {
	return clibase.WithProgress(ctx, w)
}
func startProgress(ctx context.Context, verb, unit string, total int) *progressReporter {
	return clibase.StartProgress(ctx, verb, unit, total)
}
func startProgressEvery(sink progressSink, verb, unit string, total int, delay, interval time.Duration) *progressReporter {
	return clibase.StartProgressEvery(sink, verb, unit, total, delay, interval)
}

func userError(code, message, hint string) error { return clibase.UserError(code, message, hint) }
func asCommandError(err error) (commandError, bool) {
	return clibase.AsError(err)
}
func isUserErrorCode(err error, code string) bool { return clibase.IsUserErrorCode(err, code) }
func withDomainErrorHints(err error, overrides domainErrorOverrides) error {
	return clibase.WithDomainErrorHints(err, overrides)
}
func extractDomainErrorOverrides(err error) (error, domainErrorOverrides) {
	return clibase.ExtractDomainErrorOverrides(err)
}
func applyDomainErrorOverride(cmdErr commandError, code codes.Code, stMsg string, overrides domainErrorOverrides) commandError {
	return clibase.ApplyDomainErrorOverride(cmdErr, code, stMsg, overrides)
}
func exitCodeForError(err error) int { return clibase.ExitCodeForError(err) }

func schemaOutputTypeForCommand(cmd *cobra.Command) (reflect.Type, bool) {
	return clibase.SchemaOutputTypeForCommand(cmd)
}
func markSchemaGroup(cmds ...*cobra.Command)    { clibase.MarkSchemaGroup(cmds...) }
func markSchemaExcluded(cmds ...*cobra.Command) { clibase.MarkSchemaExcluded(cmds...) }
func withSchemaCollapsed(cmd *cobra.Command, use, summary string) {
	clibase.WithSchemaCollapsed(cmd, use, summary)
}
func withSchemaOutput[T any](cmds ...*cobra.Command) { clibase.WithSchemaOutput[T](cmds...) }
func withSchemaOutputFields(cmd *cobra.Command, fields ...string) {
	clibase.WithSchemaOutputFields(cmd, fields...)
}
func isSchemaGroupCommand(cmd *cobra.Command) bool     { return clibase.IsSchemaGroupCommand(cmd) }
func isSchemaExcludedCommand(cmd *cobra.Command) bool  { return clibase.IsSchemaExcludedCommand(cmd) }
func isSchemaCollapsedCommand(cmd *cobra.Command) bool { return clibase.IsSchemaCollapsedCommand(cmd) }
func structJSONFields(t reflect.Type) []string         { return clibase.StructJSONFields(t) }

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

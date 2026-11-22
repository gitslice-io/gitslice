package clibase

import (
	"context"
	"io"

	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// Session is the authenticated RPC session surface available to domain command
// packages under internal/cli/cmd/*.
type Session interface {
	Config() workspace.UserConfig
	Conn() *grpc.ClientConn
	Ctx() context.Context
	AuthCtx(ctx context.Context) context.Context
	DefaultAccount() (string, error)
	ResolveAccount(raw string) (string, error)
	ActiveAccount() (string, error)
	PersonalAccountSlug() (string, error)
	ResolveSliceRef(value string, fallbackToWorkspace bool) (*corev1.SliceRef, error)
	ResolveOptionalSliceRef(value string) (*corev1.SliceRef, error)
	GetChangeset(id string) (*corev1.Changeset, error)

	Auth() corev1.AuthServiceClient
	Slice() corev1.SliceServiceClient
	Changeset() corev1.ChangesetServiceClient
	ChangesetStack() corev1.ChangesetStackServiceClient
	Repository() corev1.RepositoryServiceClient
	Blob() corev1.BlobServiceClient
	Agent() corev1.AgentServiceClient
	Check() corev1.CheckServiceClient
	Secret() corev1.SecretServiceClient
	Automation() corev1.AutomationServiceClient
	Feedback() corev1.FeedbackServiceClient
}

// App provides the shared CLI runtime capabilities (IO streams, config/env,
// output formatting, and Session execution) to domain command subpackages
// without introducing an import cycle back to internal/cli.
type App interface {
	Stdin() io.Reader
	Stdout() io.Writer
	Stderr() io.Writer
	Cwd() string
	HomeDir() string
	UserConfigPath() string
	Version() string
	Getenv(key string) string
	ColorEnabled(opts Options) bool

	ReadUserConfig() (workspace.UserConfig, error)
	ReadPartialUserConfig() (workspace.UserConfig, error)
	WriteUserConfig(cfg workspace.UserConfig) error
	ReadWorkspaceConfig() (workspace.Config, error)

	WithSession(ctx context.Context, fn func(s Session) error) error
	DialServer(ctx context.Context, addr string) (*grpc.ClientConn, error)
	WriteJSONOutput(opts Options, v any) error
}

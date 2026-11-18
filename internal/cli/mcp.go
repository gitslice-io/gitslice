package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/mcpserver"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

func (r Runner) mcpCommand(opts *commandOptions) *cobra.Command {
	slice := ""
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Gitslice to an agent over MCP (stdio): read, search and change slices without a workspace",
		Long: "gs mcp is a Model Context Protocol server on stdin and stdout, for agents that speak MCP. " +
			"Its tools read files, list folders, search, show history and blame, and make changesets " +
			"(write, delete, move, copy) and submit them, as the signed-in user. Errors come back structured: " +
			"a refused submit names its reason (a pending check, a conflict and its paths). " +
			"--slice sets the slice tools use when a call names none; inside a workspace it defaults to the workspace's. " +
			"Set GS_TRACE to a file path to log every server call and tool call there as JSON lines.\n\n" +
			"Add it to an MCP client as the command: gs mcp [--slice account/slice]",
		Args: maxArgs(0, "gs mcp [--slice account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMCP(cmd.Context(), *opts, slice)
		},
	}
	cmd.Flags().StringVar(&slice, "slice", "", "the slice tools use when a call names none (default: the workspace's)")
	return cmd
}

func (r Runner) newMCPBackend(cfg UserConfig, conn *grpc.ClientConn, cache *clientcache.ObjectCache, defaultSlice string) *mcpserver.Backend {
	return r.newSessionMCPBackend(newSession(r, cfg, conn, nil), cache, defaultSlice)
}

func (r Runner) newSessionMCPBackend(s *Session, cache *clientcache.ObjectCache, defaultSlice string) *mcpserver.Backend {
	repoClient := s.Repository()
	blobClient := s.Blob()
	return mcpserver.NewBackend(mcpserver.BackendDeps{
		Conn:         s.Conn(),
		AuthCtx:      s.AuthCtx,
		Cache:        cache,
		DefaultSlice: defaultSlice,
		ResolveSliceRef: func(ctx context.Context, value string) (*corev1.SliceRef, error) {
			return r.resolveSliceRefInput(ctx, s.Config(), s.Conn(), value)
		},
		FetchSingle: func(ctx context.Context, slice *corev1.SliceRef, commitID, path string) ([]byte, bool, error) {
			return fetchSingleRemoteTarget(ctx, repoClient, blobClient, slice, cache, commitID, remoteReadTarget{Path: path})
		},
		PrefetchToCache: func(ctx context.Context, slice *corev1.SliceRef, commitID string, targets []mcpserver.ReadTarget) (map[string][]byte, error) {
			conv := make([]remoteReadTarget, len(targets))
			for i, t := range targets {
				conv[i] = remoteReadTarget{Path: t.Path, ContentHash: t.ContentHash, Size: t.Size}
			}
			return prefetchRemoteFilesToCache(ctx, repoClient, blobClient, slice, cache, commitID, conv)
		},
		ReadCached: func(hash string) ([]byte, bool, error) {
			return readVerifiedCachedBytes(cache, hash)
		},
		UploadEdits: func(ctx context.Context, slice *corev1.SliceRef, edits []*corev1.FileEdit, byHash map[string][]byte) error {
			tm := NewTransferManager(nil, blobClient, slice, cache)
			_, err := tm.AttachAndUploadBlobs(ctx, edits, BlobBytesSources(byHash), nil, defaultUploadConcurrency())
			return err
		},
		LatestCheckRuns: LatestPatchsetCheckRuns,
		WebResourceURL:  webResourceURL,
	})
}

func (r Runner) runMCP(ctx context.Context, _ commandOptions, defaultSlice string) error {
	return r.withSession(ctx, func(s *Session) error {
		cache, err := r.objectCache()
		if err != nil {
			return err
		}
		if strings.TrimSpace(defaultSlice) == "" {
			if ws, err := r.readWorkspaceConfig(); err == nil && ws.Account != "" {
				defaultSlice = ws.Account + "/" + ws.Slice
			}
		}
		backend := r.newSessionMCPBackend(s, cache, defaultSlice)
		server := &mcpserver.Server{
			Tools:       backend.Tools(),
			Version:     cliVersionInfo().Version,
			Trace:       traceEvent,
			FormatError: mcpToolError,
			Out:         r.stdout(),
		}
		return server.Serve(ctx, r.Stdin)
	})
}

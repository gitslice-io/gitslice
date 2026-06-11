package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gitslice-io/gitslice/server"
)

func main() {
	cfg := server.ConfigFromEnv()
	flag.StringVar(&cfg.GRPCAddr, "grpc-addr", cfg.GRPCAddr, "gRPC listen address")
	flag.StringVar(&cfg.HTTPAddr, "http-addr", cfg.HTTPAddr, "HTTP JSON gateway listen address; disabled when empty")
	flag.StringVar(&cfg.HTTPAllowedOrigin, "http-allowed-origin", cfg.HTTPAllowedOrigin, "optional CORS Access-Control-Allow-Origin value for the HTTP JSON gateway")
	flag.StringVar(&cfg.GitHTTPAddr, "git-http-addr", cfg.GitHTTPAddr, "Git smart HTTP listen address; disabled when empty")
	flag.StringVar(&cfg.GitCacheRoot, "git-cache-root", cfg.GitCacheRoot, "Git projection cache root")
	flag.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "PostgreSQL connection URL")
	flag.StringVar(&cfg.ObjectStoreRoot, "object-store-root", cfg.ObjectStoreRoot, "prototype filesystem object store root")
	flag.BoolVar(&cfg.RunMigrations, "migrate", cfg.RunMigrations, "run PostgreSQL migrations and seed development fixtures")
	flag.IntVar(&cfg.PublishBatchSize, "publish-batch-size", cfg.PublishBatchSize, "pending publish batch size")
	flag.DurationVar(&cfg.PublishInterval, "publish-interval", cfg.PublishInterval, "pending publish poll interval")
	flag.IntVar(&cfg.IndexBatchSize, "index-batch-size", cfg.IndexBatchSize, "derived-index outbox batch size")
	flag.DurationVar(&cfg.IndexInterval, "index-interval", cfg.IndexInterval, "derived-index outbox poll interval")
	flag.BoolVar(&cfg.DevMode, "dev", cfg.DevMode, "enable development-only diagnostics such as pprof on the HTTP listener")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg); err != nil && ctx.Err() == nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gitslice.io/gitslice/server"
)

func main() {
	cfg := server.ConfigFromEnv()
	var migrateOnly bool
	flag.StringVar(&cfg.Network.GRPCAddr, "grpc-addr", cfg.Network.GRPCAddr, "gRPC listen address")
	flag.StringVar(&cfg.Network.HTTPAddr, "http-addr", cfg.Network.HTTPAddr, "HTTP JSON gateway listen address; disabled when empty")
	flag.StringVar(&cfg.Network.HTTPAllowedOrigin, "http-allowed-origin", cfg.Network.HTTPAllowedOrigin, "optional CORS Access-Control-Allow-Origin value for the HTTP JSON gateway")
	flag.StringVar(&cfg.Network.GitHTTPAddr, "git-http-addr", cfg.Network.GitHTTPAddr, "Git smart HTTP listen address; disabled when empty")
	flag.StringVar(&cfg.Storage.GitCacheRoot, "git-cache-root", cfg.Storage.GitCacheRoot, "Git projection cache root")
	flag.StringVar(&cfg.Storage.DatabaseURL, "database-url", cfg.Storage.DatabaseURL, "PostgreSQL connection URL")
	flag.StringVar(&cfg.Storage.ObjectStoreRoot, "object-store-root", cfg.Storage.ObjectStoreRoot, "prototype filesystem object store root")
	flag.BoolVar(&cfg.Storage.RunMigrations, "migrate", cfg.Storage.RunMigrations, "run PostgreSQL migrations and seed development fixtures")
	flag.IntVar(&cfg.Workers.PublishBatchSize, "publish-batch-size", cfg.Workers.PublishBatchSize, "pending publish batch size")
	flag.DurationVar(&cfg.Workers.PublishInterval, "publish-interval", cfg.Workers.PublishInterval, "pending publish poll interval")
	flag.IntVar(&cfg.Workers.IndexBatchSize, "index-batch-size", cfg.Workers.IndexBatchSize, "derived-index outbox batch size")
	flag.DurationVar(&cfg.Workers.IndexInterval, "index-interval", cfg.Workers.IndexInterval, "derived-index outbox poll interval")
	flag.BoolVar(&migrateOnly, "migrate-only", false, "run database migrations and exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if migrateOnly {
		if err := server.Migrate(ctx, cfg); err != nil {
			slog.Error("migrate failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := server.Run(ctx, cfg); err != nil && ctx.Err() == nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

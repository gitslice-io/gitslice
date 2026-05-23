package server

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	GRPCAddr              string
	HTTPAddr              string
	HTTPAllowedOrigin     string
	GitHTTPAddr           string
	GitCacheRoot          string
	DatabaseURL           string
	ObjectStoreRoot       string
	RunMigrations         bool
	PublishBatchSize      int
	PublishInterval       time.Duration
	DisableAsyncPublisher bool
}

func ConfigFromEnv() Config {
	return Config{
		GRPCAddr:              valueOrDefault(os.Getenv("GITSLICE_GRPC_ADDR"), "127.0.0.1:50051"),
		HTTPAddr:              os.Getenv("GITSLICE_HTTP_ADDR"),
		HTTPAllowedOrigin:     os.Getenv("GITSLICE_HTTP_ALLOWED_ORIGIN"),
		GitHTTPAddr:           os.Getenv("GITSLICE_GIT_HTTP_ADDR"),
		GitCacheRoot:          os.Getenv("GITSLICE_GIT_CACHE_ROOT"),
		DatabaseURL:           os.Getenv("GITSLICE_DATABASE_URL"),
		ObjectStoreRoot:       os.Getenv("GITSLICE_OBJECT_STORE_ROOT"),
		RunMigrations:         os.Getenv("GITSLICE_RUN_MIGRATIONS") != "0",
		PublishBatchSize:      intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BATCH_SIZE"), 128),
		PublishInterval:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_INTERVAL_MS"), 25)) * time.Millisecond,
		DisableAsyncPublisher: os.Getenv("GITSLICE_DISABLE_ASYNC_PUBLISHER") == "1",
	}
}

func (c Config) Validate() error {
	if c.GRPCAddr == "" {
		return fmt.Errorf("grpc address is required")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("GITSLICE_DATABASE_URL is required")
	}
	if c.ObjectStoreRoot == "" {
		return fmt.Errorf("GITSLICE_OBJECT_STORE_ROOT is required")
	}
	return nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func intValueOrDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

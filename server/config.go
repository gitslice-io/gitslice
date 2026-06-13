package server

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gitslice-io/gitslice/internal/auth/clerk"
	"github.com/gitslice-io/gitslice/internal/objectstore/r2"
)

type Config struct {
	GRPCAddr              string
	HTTPAddr              string
	HTTPAllowedOrigin     string
	GitHTTPAddr           string
	GitCacheRoot          string
	DatabaseURL           string
	ObjectStoreType       string
	ObjectStoreRoot       string
	R2                    r2.Config
	AuthProvider          string
	Clerk                 clerk.Config
	RunMigrations         bool
	PublishBatchSize      int
	PublishInterval       time.Duration
	IndexBatchSize        int
	IndexInterval         time.Duration
	DisableAsyncPublisher bool
	DisableIndexWorker    bool
	DevMode               bool
}

func ConfigFromEnv() Config {
	return Config{
		GRPCAddr:              valueOrDefault(os.Getenv("GITSLICE_GRPC_ADDR"), "127.0.0.1:50051"),
		HTTPAddr:              os.Getenv("GITSLICE_HTTP_ADDR"),
		HTTPAllowedOrigin:     os.Getenv("GITSLICE_HTTP_ALLOWED_ORIGIN"),
		GitHTTPAddr:           os.Getenv("GITSLICE_GIT_HTTP_ADDR"),
		GitCacheRoot:          os.Getenv("GITSLICE_GIT_CACHE_ROOT"),
		DatabaseURL:           os.Getenv("GITSLICE_DATABASE_URL"),
		ObjectStoreType:       os.Getenv("OBJECT_STORE_TYPE"),
		ObjectStoreRoot:       os.Getenv("GITSLICE_OBJECT_STORE_ROOT"),
		R2:                    r2.ConfigFromEnv(),
		AuthProvider:          os.Getenv("AUTH_PROVIDER"),
		Clerk:                 clerk.ConfigFromEnv(),
		RunMigrations:         os.Getenv("GITSLICE_RUN_MIGRATIONS") != "0",
		PublishBatchSize:      intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BATCH_SIZE"), 128),
		PublishInterval:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_INTERVAL_MS"), 25)) * time.Millisecond,
		IndexBatchSize:        intValueOrDefault(os.Getenv("GITSLICE_INDEX_BATCH_SIZE"), 128),
		IndexInterval:         time.Duration(intValueOrDefault(os.Getenv("GITSLICE_INDEX_INTERVAL_MS"), 25)) * time.Millisecond,
		DisableAsyncPublisher: os.Getenv("GITSLICE_DISABLE_ASYNC_PUBLISHER") == "1",
		DisableIndexWorker:    os.Getenv("GITSLICE_DISABLE_INDEX_WORKER") == "1",
		DevMode:               os.Getenv("GITSLICE_DEV_MODE") == "1",
	}
}

func (c Config) usesR2() bool {
	return c.ObjectStoreType == "r2"
}

func (c Config) Validate() error {
	if c.GRPCAddr == "" {
		return fmt.Errorf("grpc address is required")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("GITSLICE_DATABASE_URL is required")
	}
	// The filesystem object store needs a root; R2 supplies its own location.
	if !c.usesR2() && c.ObjectStoreRoot == "" {
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

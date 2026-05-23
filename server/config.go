package server

import (
	"fmt"
	"os"
)

type Config struct {
	GRPCAddr        string
	GitHTTPAddr     string
	GitCacheRoot    string
	DatabaseURL     string
	ObjectStoreRoot string
	RunMigrations   bool
}

func ConfigFromEnv() Config {
	return Config{
		GRPCAddr:        valueOrDefault(os.Getenv("GITSLICE_GRPC_ADDR"), "127.0.0.1:50051"),
		GitHTTPAddr:     os.Getenv("GITSLICE_GIT_HTTP_ADDR"),
		GitCacheRoot:    os.Getenv("GITSLICE_GIT_CACHE_ROOT"),
		DatabaseURL:     os.Getenv("GITSLICE_DATABASE_URL"),
		ObjectStoreRoot: os.Getenv("GITSLICE_OBJECT_STORE_ROOT"),
		RunMigrations:   os.Getenv("GITSLICE_RUN_MIGRATIONS") != "0",
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

package server

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/auth/clerk"
	"github.com/gitslice-io/gitslice/internal/auth/servicetoken"
	"github.com/gitslice-io/gitslice/internal/objectstore/r2"
	"github.com/gitslice-io/gitslice/internal/secretbox"
)

type Config struct {
	GRPCAddr                 string
	HTTPAddr                 string
	HTTPAllowedOrigin        string
	RequireStrictCORS        bool
	GitHTTPAddr              string
	GitCacheRoot             string
	DatabaseURL              string
	ObjectStoreType          string
	ObjectStoreRoot          string
	RequireR2                bool
	SecretsKey               string
	RequireSecretsKey        bool
	ObjectCacheBytes         int64
	R2                       r2.Config
	AuthProvider             string
	Clerk                    clerk.Config
	ServiceToken             servicetoken.Config
	RunMigrations            bool
	PublishBatchSize         int
	PublishInterval          time.Duration
	PublishBackoffMax        time.Duration
	IndexBatchSize           int
	IndexInterval            time.Duration
	IndexBackoffMax          time.Duration
	DisableAsyncPublisher    bool
	DisableIndexWorker       bool
	RateLimitDisabled        bool
	RateLimitPerSubjectRPS   float64
	RateLimitPerSubjectBurst int
	RateLimitHTTPPerIPRPS    float64
	RateLimitHTTPPerIPBurst  int
	MetricsToken             string
	RequireMetricsToken      bool
}

func ConfigFromEnv() Config {
	return Config{
		GRPCAddr:              valueOrDefault(os.Getenv("GITSLICE_GRPC_ADDR"), "127.0.0.1:50051"),
		HTTPAddr:              os.Getenv("GITSLICE_HTTP_ADDR"),
		HTTPAllowedOrigin:     os.Getenv("GITSLICE_HTTP_ALLOWED_ORIGIN"),
		RequireStrictCORS:     os.Getenv("GITSLICE_REQUIRE_STRICT_CORS") == "1",
		GitHTTPAddr:           os.Getenv("GITSLICE_GIT_HTTP_ADDR"),
		GitCacheRoot:          os.Getenv("GITSLICE_GIT_CACHE_ROOT"),
		DatabaseURL:           os.Getenv("GITSLICE_DATABASE_URL"),
		ObjectStoreType:       os.Getenv("OBJECT_STORE_TYPE"),
		ObjectStoreRoot:       os.Getenv("GITSLICE_OBJECT_STORE_ROOT"),
		RequireR2:             os.Getenv("GITSLICE_REQUIRE_R2") == "1",
		SecretsKey:            os.Getenv("GITSLICE_SECRETS_KEY"),
		RequireSecretsKey:     os.Getenv("GITSLICE_REQUIRE_SECRETS_KEY") == "1",
		ObjectCacheBytes:      int64ValueOrDefault(os.Getenv("GITSLICE_OBJECT_CACHE_BYTES"), 256<<20),
		R2:                    r2.ConfigFromEnv(),
		AuthProvider:          os.Getenv("AUTH_PROVIDER"),
		Clerk:                 clerk.ConfigFromEnv(),
		ServiceToken:          servicetoken.ConfigFromEnv(),
		RunMigrations:         os.Getenv("GITSLICE_RUN_MIGRATIONS") != "0",
		PublishBatchSize:      intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BATCH_SIZE"), 128),
		PublishInterval:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_INTERVAL_MS"), 25)) * time.Millisecond,
		PublishBackoffMax:     time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BACKOFF_MAX_MS"), 60000)) * time.Millisecond,
		IndexBatchSize:        intValueOrDefault(os.Getenv("GITSLICE_INDEX_BATCH_SIZE"), 128),
		IndexInterval:         time.Duration(intValueOrDefault(os.Getenv("GITSLICE_INDEX_INTERVAL_MS"), 25)) * time.Millisecond,
		IndexBackoffMax:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_INDEX_BACKOFF_MAX_MS"), 60000)) * time.Millisecond,
		DisableAsyncPublisher: os.Getenv("GITSLICE_DISABLE_ASYNC_PUBLISHER") == "1",
		DisableIndexWorker:    os.Getenv("GITSLICE_DISABLE_INDEX_WORKER") == "1",
		RateLimitDisabled:     os.Getenv("GITSLICE_RATELIMIT_DISABLED") == "1",
		// Per-subject limits are an anti-abuse ceiling, not fairness throttling.
		// They must sit well above a single authenticated user's legitimate bulk
		// traffic (e.g. `gs import` uploads blobs concurrently, one unary/stream
		// UploadBlob per blob), so the default is generous while still capping a
		// runaway client from saturating the publish pipeline / object store.
		RateLimitPerSubjectRPS:   floatValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_SUBJECT_RPS"), 500),
		RateLimitPerSubjectBurst: intValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_SUBJECT_BURST"), 1000),
		RateLimitHTTPPerIPRPS:    floatValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_HTTP_RPS"), 30),
		RateLimitHTTPPerIPBurst:  intValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_HTTP_BURST"), 60),
		MetricsToken:             os.Getenv("GITSLICE_METRICS_TOKEN"),
		RequireMetricsToken:      os.Getenv("GITSLICE_REQUIRE_METRICS_TOKEN") == "1",
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
	// In production the object store must be durable R2. GITSLICE_REQUIRE_R2=1
	// makes a non-R2 store a hard startup failure so a config slip can't silently
	// route writes to the ephemeral filesystem store: those Puts "succeed" and
	// then vanish on the next cold start, leaving Postgres refs pointing at tree
	// objects that no longer exist (an "object not found" on read).
	if c.RequireR2 && !c.usesR2() {
		return fmt.Errorf("GITSLICE_REQUIRE_R2 is set but OBJECT_STORE_TYPE is %q, not \"r2\": refusing to start with a non-durable object store", c.ObjectStoreType)
	}
	if c.RequireMetricsToken && strings.TrimSpace(c.MetricsToken) == "" {
		return fmt.Errorf("GITSLICE_REQUIRE_METRICS_TOKEN is set but GITSLICE_METRICS_TOKEN is empty")
	}
	if c.RequireStrictCORS {
		for _, origin := range strings.Split(c.HTTPAllowedOrigin, ",") {
			if strings.TrimSpace(origin) == "*" {
				return fmt.Errorf("GITSLICE_REQUIRE_STRICT_CORS is set but GITSLICE_HTTP_ALLOWED_ORIGIN contains a wildcard origin: refusing to start with a wildcard origin in strict mode")
			}
		}
	}
	if _, err := c.secretsBox(); err != nil {
		return err
	}
	// The filesystem object store needs a root; R2 supplies its own location.
	if !c.usesR2() && c.ObjectStoreRoot == "" {
		return fmt.Errorf("GITSLICE_OBJECT_STORE_ROOT is required")
	}
	return nil
}

func (c Config) secretsBox() (*secretbox.Box, error) {
	if c.SecretsKey == "" {
		if c.RequireSecretsKey {
			return nil, fmt.Errorf("GITSLICE_REQUIRE_SECRETS_KEY is set but GITSLICE_SECRETS_KEY is empty")
		}
		return nil, nil
	}
	box, err := secretbox.New(c.SecretsKey)
	if err != nil {
		return nil, fmt.Errorf("GITSLICE_SECRETS_KEY is invalid: %w", err)
	}
	return box, nil
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

func floatValueOrDefault(value string, fallback float64) float64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) {
		return fallback
	}
	return parsed
}

// int64ValueOrDefault parses a non-negative int64 from value, falling back on
// empty/invalid input. Unlike intValueOrDefault, 0 is allowed (it disables the
// object cache).
func int64ValueOrDefault(value string, fallback int64) int64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

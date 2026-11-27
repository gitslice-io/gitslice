package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/auth/clerk"
	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/objectstore/r2"
	"gitslice.io/gitslice/internal/secretbox"
)

type Config struct {
	Network      NetworkConfig
	Storage      StorageConfig
	Auth         AuthConfig
	Workers      WorkersConfig
	RateLimit    RateLimitConfig
	Integrations IntegrationsConfig
}

type NetworkConfig struct {
	GRPCAddr          string
	HTTPAddr          string
	HTTPAllowedOrigin string
	RequireStrictCORS bool
	GitHTTPAddr       string
	// WebBaseURL is the web app's origin, for links in trigger payloads and MCP
	// responses (GITSLICE_WEB_BASE_URL; defaults to the first allowed CORS origin).
	WebBaseURL string
	// SelfURL is the server's own URL that bypasses the CDN
	// (GITSLICE_SELF_URL, the Cloud Run service URL): the server asks itself
	// to build Git covers and push mirrors there, in a request of their own.
	SelfURL string
	// OutboundAllowPrivateTargets lets outbound requests use http and private
	// or loopback addresses. It is only for tests and local development.
	OutboundAllowPrivateTargets bool
}

type StorageConfig struct {
	DatabaseURL        string
	RunMigrations      bool
	ObjectStoreType    string
	ObjectStoreRoot    string
	RequireR2          bool
	ObjectCacheBytes   int64
	ObjectStoreLatency time.Duration
	R2                 r2.Config
	GitCacheRoot       string
	SecretsKey         string
	RequireSecretsKey  bool
}

type AuthConfig struct {
	Provider     string
	Clerk        clerk.Config
	ServiceToken servicetoken.Config
	// AgentSignupEnabled turns on the unauthenticated AuthService/RegisterAgent
	// RPC; AgentSignupPerHour caps registrations per client IP.
	AgentSignupEnabled bool
	AgentSignupPerHour int
	// OperatorSubjects may create organization accounts
	// (GITSLICE_OPERATOR_SUBJECTS: subject ids separated by commas,
	// semicolons or spaces).
	OperatorSubjects []string
	// FeedbackAccount is the account whose owners and admins read and answer
	// feedback, with the operators (GITSLICE_FEEDBACK_ACCOUNT; default
	// gitslice, "none" for operators only).
	FeedbackAccount string
}

type WorkersConfig struct {
	PublishBatchSize      int
	PublishInterval       time.Duration
	PublishBackoffMax     time.Duration
	IndexBatchSize        int
	IndexInterval         time.Duration
	IndexBackoffMax       time.Duration
	DisableAsyncPublisher bool
	DisableIndexWorker    bool
	DisableBlameWarmer    bool
	// GitBlobBackfill turns on the background job that computes Git blob ids
	// for files uploaded before ids were recorded. It reads every such file
	// once, so it is off until asked for.
	GitBlobBackfill bool
}

type RateLimitConfig struct {
	Disabled        bool
	PerSubjectRPS   float64
	PerSubjectBurst int
	HTTPPerIPRPS    float64
	HTTPPerIPBurst  int
	// TrustedProxyCIDRs lists proxy IPs/CIDRs permitted to supply
	// X-Forwarded-For (GITSLICE_TRUSTED_PROXIES). When empty, loopback,
	// private, and link-local proxy peers are trusted.
	TrustedProxyCIDRs []string
}

type IntegrationsConfig struct {
	MetricsToken        string
	RequireMetricsToken bool
	PostHogAPIKey       string
	PostHogHost         string
	PostHogEnvironment  string
	// TriggerDispatchURL is the Cloudflare trigger dispatcher's origin
	// (GITSLICE_TRIGGER_DISPATCH_URL), and TriggerDispatchSecret the key the
	// server and the dispatcher share (GITSLICE_TRIGGER_DISPATCH_SECRET).
	// Without both, http triggers run but worker triggers error
	// (design/34_triggers.md).
	TriggerDispatchURL    string
	TriggerDispatchSecret string
	// DoorbellURL is the Cloudflare doorbell Worker's origin
	// (GITSLICE_DOORBELL_URL), and DoorbellSecret the key the server and the
	// Worker share (GITSLICE_DOORBELL_SECRET). Without both, detached CI
	// daemons poll every 30 s (design/32_ci_dispatch_on_cloudflare.md).
	DoorbellURL    string
	DoorbellSecret string
	// CIRunnerDaemons are the daemons the doorbell starts Cloudflare runners
	// for when checks are queued (GITSLICE_CI_RUNNER_DAEMONS): they count as
	// reachable with no runner running.
	CIRunnerDaemons []string
}

func ConfigFromEnv() Config {
	return Config{
		Network: NetworkConfig{
			GRPCAddr:                    valueOrDefault(os.Getenv("GITSLICE_GRPC_ADDR"), "127.0.0.1:50051"),
			HTTPAddr:                    os.Getenv("GITSLICE_HTTP_ADDR"),
			HTTPAllowedOrigin:           os.Getenv("GITSLICE_HTTP_ALLOWED_ORIGIN"),
			RequireStrictCORS:           os.Getenv("GITSLICE_REQUIRE_STRICT_CORS") == "1",
			GitHTTPAddr:                 os.Getenv("GITSLICE_GIT_HTTP_ADDR"),
			WebBaseURL:                  os.Getenv("GITSLICE_WEB_BASE_URL"),
			SelfURL:                     os.Getenv("GITSLICE_SELF_URL"),
			OutboundAllowPrivateTargets: outboundPrivateTargets(),
		},
		Storage: StorageConfig{
			DatabaseURL:        os.Getenv("GITSLICE_DATABASE_URL"),
			RunMigrations:      os.Getenv("GITSLICE_RUN_MIGRATIONS") != "0",
			ObjectStoreType:    os.Getenv("OBJECT_STORE_TYPE"),
			ObjectStoreRoot:    os.Getenv("GITSLICE_OBJECT_STORE_ROOT"),
			RequireR2:          os.Getenv("GITSLICE_REQUIRE_R2") == "1",
			ObjectCacheBytes:   int64ValueOrDefault(os.Getenv("GITSLICE_OBJECT_CACHE_BYTES"), 256<<20),
			ObjectStoreLatency: millisecondsDuration(os.Getenv("GITSLICE_OBJECT_STORE_LATENCY_MS")),
			R2:                 r2.ConfigFromEnv(),
			GitCacheRoot:       os.Getenv("GITSLICE_GIT_CACHE_ROOT"),
			SecretsKey:         os.Getenv("GITSLICE_SECRETS_KEY"),
			RequireSecretsKey:  os.Getenv("GITSLICE_REQUIRE_SECRETS_KEY") == "1",
		},
		Auth: AuthConfig{
			Provider:           os.Getenv("AUTH_PROVIDER"),
			Clerk:              clerk.ConfigFromEnv(),
			ServiceToken:       servicetoken.ConfigFromEnv(),
			AgentSignupEnabled: envEnabled(os.Getenv("GITSLICE_AGENT_SIGNUP_ENABLED")),
			AgentSignupPerHour: intValueOrDefault(os.Getenv("GITSLICE_AGENT_SIGNUP_PER_HOUR"), 5),
			OperatorSubjects:   splitList(os.Getenv("GITSLICE_OPERATOR_SUBJECTS")),
			FeedbackAccount:    feedbackAccount(os.Getenv("GITSLICE_FEEDBACK_ACCOUNT")),
		},
		Workers: WorkersConfig{
			PublishBatchSize:      intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BATCH_SIZE"), 128),
			PublishInterval:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_INTERVAL_MS"), 25)) * time.Millisecond,
			PublishBackoffMax:     time.Duration(intValueOrDefault(os.Getenv("GITSLICE_PUBLISH_BACKOFF_MAX_MS"), 60000)) * time.Millisecond,
			IndexBatchSize:        intValueOrDefault(os.Getenv("GITSLICE_INDEX_BATCH_SIZE"), 128),
			IndexInterval:         time.Duration(intValueOrDefault(os.Getenv("GITSLICE_INDEX_INTERVAL_MS"), 25)) * time.Millisecond,
			IndexBackoffMax:       time.Duration(intValueOrDefault(os.Getenv("GITSLICE_INDEX_BACKOFF_MAX_MS"), 60000)) * time.Millisecond,
			DisableAsyncPublisher: os.Getenv("GITSLICE_DISABLE_ASYNC_PUBLISHER") == "1",
			DisableIndexWorker:    os.Getenv("GITSLICE_DISABLE_INDEX_WORKER") == "1",
			DisableBlameWarmer:    os.Getenv("GITSLICE_DISABLE_BLAME_WARMER") == "1",
			GitBlobBackfill:       os.Getenv("GITSLICE_GIT_BLOB_BACKFILL") == "1",
		},
		RateLimit: RateLimitConfig{
			Disabled: os.Getenv("GITSLICE_RATELIMIT_DISABLED") == "1",
			// Per-subject limits are an anti-abuse ceiling, not fairness throttling.
			// They must sit well above a single authenticated user's legitimate bulk
			// traffic (e.g. `gs import` uploads blobs concurrently, one unary/stream
			// UploadBlob per blob), so the default is generous while still capping a
			// runaway client from saturating the publish pipeline / object store.
			PerSubjectRPS:     floatValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_SUBJECT_RPS"), 500),
			PerSubjectBurst:   intValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_SUBJECT_BURST"), 1000),
			HTTPPerIPRPS:      floatValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_HTTP_RPS"), 30),
			HTTPPerIPBurst:    intValueOrDefault(os.Getenv("GITSLICE_RATELIMIT_HTTP_BURST"), 60),
			TrustedProxyCIDRs: splitList(os.Getenv("GITSLICE_TRUSTED_PROXIES")),
		},
		Integrations: IntegrationsConfig{
			MetricsToken:          os.Getenv("GITSLICE_METRICS_TOKEN"),
			RequireMetricsToken:   os.Getenv("GITSLICE_REQUIRE_METRICS_TOKEN") == "1",
			PostHogAPIKey:         os.Getenv("GITSLICE_POSTHOG_API_KEY"),
			PostHogHost:           os.Getenv("GITSLICE_POSTHOG_HOST"),
			PostHogEnvironment:    os.Getenv("GITSLICE_POSTHOG_ENV"),
			TriggerDispatchURL:    os.Getenv("GITSLICE_TRIGGER_DISPATCH_URL"),
			TriggerDispatchSecret: os.Getenv("GITSLICE_TRIGGER_DISPATCH_SECRET"),
			DoorbellURL:           os.Getenv("GITSLICE_DOORBELL_URL"),
			DoorbellSecret:        os.Getenv("GITSLICE_DOORBELL_SECRET"),
			CIRunnerDaemons:       splitList(os.Getenv("GITSLICE_CI_RUNNER_DAEMONS")),
		},
	}
}

// ApplyDefaults fills in zero-valued worker intervals and batch sizes with
// production defaults so programmatic callers (such as tests) do not have to
// populate them by hand.
func (c *Config) ApplyDefaults() {
	if c.Workers.PublishBatchSize <= 0 {
		c.Workers.PublishBatchSize = 128
	}
	if c.Workers.PublishInterval <= 0 {
		c.Workers.PublishInterval = defaultPublishInterval
	}
	if c.Workers.PublishBackoffMax <= 0 {
		c.Workers.PublishBackoffMax = defaultPublishMaxInterval
	}
	if c.Workers.IndexBatchSize <= 0 {
		c.Workers.IndexBatchSize = 128
	}
	if c.Workers.IndexInterval <= 0 {
		c.Workers.IndexInterval = defaultPublishInterval
	}
	if c.Workers.IndexBackoffMax <= 0 {
		c.Workers.IndexBackoffMax = 60 * time.Second
	}
}

func (c Config) gitCacheRoot() string {
	if c.Storage.GitCacheRoot != "" {
		return c.Storage.GitCacheRoot
	}
	if c.Storage.ObjectStoreRoot != "" {
		return filepath.Join(c.Storage.ObjectStoreRoot, "git-cache")
	}
	return filepath.Join(os.TempDir(), "gitslice-git-cache")
}

func outboundPrivateTargets() bool {
	if value, ok := os.LookupEnv("GITSLICE_OUTBOUND_ALLOW_PRIVATE_TARGETS"); ok {
		return value == "1"
	}
	// Compatibility fallback for existing local and production configuration.
	return os.Getenv("GITSLICE_WEBHOOK_ALLOW_PRIVATE_TARGETS") == "1"
}

// internalKey signs the server's requests to itself. It is derived from the
// secrets key, which every instance holds; nil without one.
func (c Config) internalKey() []byte {
	if c.Storage.SecretsKey == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(c.Storage.SecretsKey))
	mac.Write([]byte("gitslice internal requests v1"))
	return mac.Sum(nil)
}

func (c Config) usesR2() bool {
	return c.Storage.ObjectStoreType == "r2"
}

func (c Config) Validate() error {
	if c.Network.GRPCAddr == "" {
		return fmt.Errorf("grpc address is required")
	}
	if c.Storage.DatabaseURL == "" {
		return fmt.Errorf("GITSLICE_DATABASE_URL is required")
	}
	// In production the object store must be durable R2. GITSLICE_REQUIRE_R2=1
	// makes a non-R2 store a hard startup failure so a config slip can't silently
	// route writes to the ephemeral filesystem store: those Puts "succeed" and
	// then vanish on the next cold start, leaving Postgres refs pointing at tree
	// objects that no longer exist (an "object not found" on read).
	if c.Storage.RequireR2 && !c.usesR2() {
		return fmt.Errorf("GITSLICE_REQUIRE_R2 is set but OBJECT_STORE_TYPE is %q, not \"r2\": refusing to start with a non-durable object store", c.Storage.ObjectStoreType)
	}
	if c.Integrations.RequireMetricsToken && strings.TrimSpace(c.Integrations.MetricsToken) == "" {
		return fmt.Errorf("GITSLICE_REQUIRE_METRICS_TOKEN is set but GITSLICE_METRICS_TOKEN is empty")
	}
	if c.Network.RequireStrictCORS {
		for _, origin := range strings.Split(c.Network.HTTPAllowedOrigin, ",") {
			if strings.TrimSpace(origin) == "*" {
				return fmt.Errorf("GITSLICE_REQUIRE_STRICT_CORS is set but GITSLICE_HTTP_ALLOWED_ORIGIN contains a wildcard origin: refusing to start with a wildcard origin in strict mode")
			}
		}
	}
	if _, err := c.secretsBox(); err != nil {
		return err
	}
	if _, err := parseTrustedProxies(c.RateLimit.TrustedProxyCIDRs); err != nil {
		return err
	}
	// The filesystem object store needs a root; R2 supplies its own location.
	if !c.usesR2() && c.Storage.ObjectStoreRoot == "" {
		return fmt.Errorf("GITSLICE_OBJECT_STORE_ROOT is required")
	}
	return nil
}

// webBaseURL is where the web app lives: GITSLICE_WEB_BASE_URL, or the
// first allowed CORS origin that is not a wildcard.
func (c Config) webBaseURL() string {
	if c.Network.WebBaseURL != "" {
		return strings.TrimRight(c.Network.WebBaseURL, "/")
	}
	for _, origin := range strings.Split(c.Network.HTTPAllowedOrigin, ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" && !strings.Contains(origin, "*") {
			return strings.TrimRight(origin, "/")
		}
	}
	return ""
}

func (c Config) secretsBox() (*secretbox.Box, error) {
	if c.Storage.SecretsKey == "" {
		if c.Storage.RequireSecretsKey {
			return nil, fmt.Errorf("GITSLICE_REQUIRE_SECRETS_KEY is set but GITSLICE_SECRETS_KEY is empty")
		}
		return nil, nil
	}
	box, err := secretbox.New(c.Storage.SecretsKey)
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

func envEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true":
		return true
	default:
		return false
	}
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

func millisecondsDuration(value string) time.Duration {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > int64(math.MaxInt64)/int64(time.Millisecond) {
		return 0
	}
	return time.Duration(parsed) * time.Millisecond
}

// splitList splits a list separated by commas, semicolons or whitespace,
// dropping empty entries. Semicolons let Cloud Build pass a list inside its
// comma-delimited --set-env-vars value.
func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}

// feedbackAccount reads GITSLICE_FEEDBACK_ACCOUNT: unset means gitslice, and
// "none" means no account, so only operators read feedback.
func feedbackAccount(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "":
		return "gitslice"
	case "none":
		return ""
	}
	return value
}

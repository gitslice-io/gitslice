package server

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"
)

// baseValidConfig returns a Config that passes Validate() so individual tests can
// mutate just the fields they care about.
func baseValidConfig() Config {
	return Config{
		Network: NetworkConfig{
			GRPCAddr: "127.0.0.1:50051",
		},
		Storage: StorageConfig{
			DatabaseURL:     "postgres://localhost/db",
			ObjectStoreType: "r2",
		},
	}
}

func TestValidateRequireR2(t *testing.T) {
	tests := []struct {
		name        string
		requireR2   bool
		storeType   string
		storeRoot   string
		wantErr     bool
		errContains string
	}{
		{name: "require r2 with r2 store", requireR2: true, storeType: "r2", wantErr: false},
		{name: "require r2 with filesystem store", requireR2: true, storeType: "filesystem", storeRoot: "/tmp/x", wantErr: true, errContains: "GITSLICE_REQUIRE_R2"},
		{name: "require r2 with empty store type", requireR2: true, storeType: "", wantErr: true, errContains: "GITSLICE_REQUIRE_R2"},
		{name: "no require, filesystem needs root", requireR2: false, storeType: "filesystem", storeRoot: "", wantErr: true, errContains: "GITSLICE_OBJECT_STORE_ROOT"},
		{name: "no require, filesystem with root", requireR2: false, storeType: "filesystem", storeRoot: "/tmp/x", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Storage.RequireR2 = tt.requireR2
			cfg.Storage.ObjectStoreType = tt.storeType
			cfg.Storage.ObjectStoreRoot = tt.storeRoot
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() = nil, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestConfigFromEnvReadsRequireR2(t *testing.T) {
	t.Setenv("GITSLICE_REQUIRE_R2", "1")
	if !ConfigFromEnv().Storage.RequireR2 {
		t.Fatal("ConfigFromEnv() Storage.RequireR2 = false, want true when GITSLICE_REQUIRE_R2=1")
	}
	t.Setenv("GITSLICE_REQUIRE_R2", "0")
	if ConfigFromEnv().Storage.RequireR2 {
		t.Fatal("ConfigFromEnv() Storage.RequireR2 = true, want false when GITSLICE_REQUIRE_R2=0")
	}
}

func TestValidateRequireMetricsToken(t *testing.T) {
	tests := []struct {
		name         string
		requireToken bool
		token        string
		wantErr      bool
	}{
		{name: "required with token", requireToken: true, token: "metrics-token"},
		{name: "required without token", requireToken: true, wantErr: true},
		{name: "optional without token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Integrations.RequireMetricsToken = tt.requireToken
			cfg.Integrations.MetricsToken = tt.token
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !strings.Contains(err.Error(), "GITSLICE_REQUIRE_METRICS_TOKEN") {
					t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), "GITSLICE_REQUIRE_METRICS_TOKEN")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestValidateRequireStrictCORS(t *testing.T) {
	tests := []struct {
		name          string
		requireStrict bool
		allowedOrigin string
		wantErr       bool
	}{
		{name: "strict with wildcard", requireStrict: true, allowedOrigin: "*", wantErr: true},
		{name: "strict with explicit origin", requireStrict: true, allowedOrigin: "https://app.example.com"},
		{name: "strict with wildcard in list", requireStrict: true, allowedOrigin: "https://a.com,*", wantErr: true},
		{name: "non-strict with wildcard", allowedOrigin: "*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Network.RequireStrictCORS = tt.requireStrict
			cfg.Network.HTTPAllowedOrigin = tt.allowedOrigin
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !strings.Contains(err.Error(), "GITSLICE_REQUIRE_STRICT_CORS") {
					t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), "GITSLICE_REQUIRE_STRICT_CORS")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestConfigFromEnvReadsProductionHardeningConfig(t *testing.T) {
	t.Setenv("GITSLICE_REQUIRE_METRICS_TOKEN", "1")
	t.Setenv("GITSLICE_REQUIRE_STRICT_CORS", "1")
	cfg := ConfigFromEnv()
	if !cfg.Integrations.RequireMetricsToken {
		t.Fatal("ConfigFromEnv() Integrations.RequireMetricsToken = false, want true")
	}
	if !cfg.Network.RequireStrictCORS {
		t.Fatal("ConfigFromEnv() Network.RequireStrictCORS = false, want true")
	}

	t.Setenv("GITSLICE_REQUIRE_METRICS_TOKEN", "0")
	t.Setenv("GITSLICE_REQUIRE_STRICT_CORS", "0")
	cfg = ConfigFromEnv()
	if cfg.Integrations.RequireMetricsToken {
		t.Fatal("ConfigFromEnv() Integrations.RequireMetricsToken = true, want false when GITSLICE_REQUIRE_METRICS_TOKEN=0")
	}
	if cfg.Network.RequireStrictCORS {
		t.Fatal("ConfigFromEnv() Network.RequireStrictCORS = true, want false when GITSLICE_REQUIRE_STRICT_CORS=0")
	}
}

func TestValidateRequireSecretsKey(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	tests := []struct {
		name        string
		requireKey  bool
		key         string
		wantErr     bool
		errContains string
	}{
		{name: "required with valid key", requireKey: true, key: validKey},
		{name: "required without key", requireKey: true, wantErr: true, errContains: "GITSLICE_REQUIRE_SECRETS_KEY"},
		{name: "required with invalid key", requireKey: true, key: "invalid!", wantErr: true, errContains: "GITSLICE_SECRETS_KEY"},
		{name: "optional without key"},
		{name: "optional with valid key", key: validKey},
		{name: "optional with invalid key", key: "invalid!", wantErr: true, errContains: "GITSLICE_SECRETS_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Storage.RequireSecretsKey = tt.requireKey
			cfg.Storage.SecretsKey = tt.key
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestConfigFromEnvReadsSecretsKeyConfig(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	t.Setenv("GITSLICE_SECRETS_KEY", key)
	t.Setenv("GITSLICE_REQUIRE_SECRETS_KEY", "1")
	cfg := ConfigFromEnv()
	if cfg.Storage.SecretsKey != key {
		t.Fatalf("ConfigFromEnv() Storage.SecretsKey = %q, want configured key", cfg.Storage.SecretsKey)
	}
	if !cfg.Storage.RequireSecretsKey {
		t.Fatal("ConfigFromEnv() Storage.RequireSecretsKey = false, want true")
	}

	t.Setenv("GITSLICE_REQUIRE_SECRETS_KEY", "0")
	if ConfigFromEnv().Storage.RequireSecretsKey {
		t.Fatal("ConfigFromEnv() Storage.RequireSecretsKey = true, want false when GITSLICE_REQUIRE_SECRETS_KEY=0")
	}
}

func TestTrustedProxyCIDRsConfigAndValidation(t *testing.T) {
	t.Setenv("GITSLICE_TRUSTED_PROXIES", " 10.0.0.0/8 , 198.51.100.10 , 2001:db8::/32 ")
	cfg := ConfigFromEnv()
	if len(cfg.RateLimit.TrustedProxyCIDRs) != 3 {
		t.Fatalf("RateLimit.TrustedProxyCIDRs = %#v, want 3 entries", cfg.RateLimit.TrustedProxyCIDRs)
	}

	validCfg := baseValidConfig()
	validCfg.RateLimit.TrustedProxyCIDRs = cfg.RateLimit.TrustedProxyCIDRs
	if err := validCfg.Validate(); err != nil {
		t.Fatalf("Validate() with valid TrustedProxyCIDRs = %v, want nil", err)
	}

	for _, bad := range []string{"not-a-cidr", "10.0.0.0/99"} {
		badCfg := baseValidConfig()
		badCfg.RateLimit.TrustedProxyCIDRs = []string{bad}
		err := badCfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "GITSLICE_TRUSTED_PROXIES") {
			t.Fatalf("Validate(%q) error = %v, want GITSLICE_TRUSTED_PROXIES error", bad, err)
		}
	}
}

func TestConfigApplyDefaultsAndCIRunnerDaemonsSplitList(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	if cfg.Workers.PublishBatchSize != 128 || cfg.Workers.PublishInterval != defaultPublishInterval || cfg.Workers.PublishBackoffMax != defaultPublishMaxInterval {
		t.Fatalf("publish defaults = %+v", cfg.Workers)
	}
	if cfg.Workers.IndexBatchSize != 128 || cfg.Workers.IndexInterval != defaultPublishInterval || cfg.Workers.IndexBackoffMax != 60*time.Second {
		t.Fatalf("index defaults = %+v", cfg.Workers)
	}

	t.Setenv("GITSLICE_CI_RUNNER_DAEMONS", " runner-a ; runner-b, runner-c \t runner-d ")
	got := ConfigFromEnv().Integrations.CIRunnerDaemons
	want := []string{"runner-a", "runner-b", "runner-c", "runner-d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CIRunnerDaemons = %#v, want %#v", got, want)
	}
}

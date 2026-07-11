package server

import (
	"encoding/base64"
	"strings"
	"testing"
)

// baseValidConfig returns a Config that passes Validate() so individual tests can
// mutate just the object-store fields they care about.
func baseValidConfig() Config {
	return Config{
		GRPCAddr:        "127.0.0.1:50051",
		DatabaseURL:     "postgres://localhost/db",
		ObjectStoreType: "r2",
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
			cfg.RequireR2 = tt.requireR2
			cfg.ObjectStoreType = tt.storeType
			cfg.ObjectStoreRoot = tt.storeRoot
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
	if !ConfigFromEnv().RequireR2 {
		t.Fatal("ConfigFromEnv() RequireR2 = false, want true when GITSLICE_REQUIRE_R2=1")
	}
	t.Setenv("GITSLICE_REQUIRE_R2", "0")
	if ConfigFromEnv().RequireR2 {
		t.Fatal("ConfigFromEnv() RequireR2 = true, want false when GITSLICE_REQUIRE_R2=0")
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
			cfg.RequireSecretsKey = tt.requireKey
			cfg.SecretsKey = tt.key
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
	if cfg.SecretsKey != key {
		t.Fatalf("ConfigFromEnv() SecretsKey = %q, want configured key", cfg.SecretsKey)
	}
	if !cfg.RequireSecretsKey {
		t.Fatal("ConfigFromEnv() RequireSecretsKey = false, want true")
	}

	t.Setenv("GITSLICE_REQUIRE_SECRETS_KEY", "0")
	if ConfigFromEnv().RequireSecretsKey {
		t.Fatal("ConfigFromEnv() RequireSecretsKey = true, want false when GITSLICE_REQUIRE_SECRETS_KEY=0")
	}
}

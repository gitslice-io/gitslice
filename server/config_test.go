package server

import (
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

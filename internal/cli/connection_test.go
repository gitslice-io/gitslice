package cli

import (
	"testing"
)

func TestEnvironmentAliases(t *testing.T) {
	t.Setenv("GS_SERVER_ADDR", "127.0.0.1:60001")
	t.Setenv("GITSLICE_GRPC_ADDR", "127.0.0.1:50051")
	if got := defaultServerAddr(); got != "127.0.0.1:60001" {
		t.Fatalf("defaultServerAddr = %q, want GS_SERVER_ADDR", got)
	}

	t.Setenv("GS_WEB_URL", "http://127.0.0.1:60002")
	t.Setenv("GITSLICE_WEB_URL", "http://127.0.0.1:5173")
	if got := defaultWebURL(); got != "http://127.0.0.1:60002" {
		t.Fatalf("defaultWebURL = %q, want GS_WEB_URL", got)
	}

	t.Setenv("GS_GATEWAY_URL", "http://127.0.0.1:60003")
	t.Setenv("GITSLICE_GATEWAY_URL", "http://127.0.0.1:8082")
	if got := defaultGatewayURL(); got != "http://127.0.0.1:60003" {
		t.Fatalf("defaultGatewayURL = %q, want GS_GATEWAY_URL", got)
	}
}

func TestHostedDefaults(t *testing.T) {
	for _, name := range []string{
		"GS_SERVER_ADDR",
		"GITSLICE_GRPC_ADDR",
		"GITSLICE_SERVER_ADDR",
		"GS_WEB_URL",
		"GITSLICE_WEB_URL",
	} {
		t.Setenv(name, "")
	}

	if got := defaultServerAddr(); got != "api.gitslice.io:443" {
		t.Fatalf("defaultServerAddr = %q, want production API", got)
	}
	if got := defaultWebURL(); got != "https://gitslice.io" {
		t.Fatalf("defaultWebURL = %q, want production web", got)
	}
}

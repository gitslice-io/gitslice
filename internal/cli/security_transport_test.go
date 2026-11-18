package cli

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/agentd"
)

func TestResolveDialTargetTLSDefaultsAndBearerSecurity(t *testing.T) {
	t.Setenv("GS_TLS", "")
	t.Setenv("GS_INSECURE", "")

	for _, remoteAddr := range []string{
		"api.gitslice.io:8443",
		"api.gitslice.io:50051",
		"gitslice.example.com",
		"https://api.gitslice.io:9000",
		"grpcs://api.gitslice.io:9000",
	} {
		_, useTLS, explicitPlain := resolveDialTargetPolicy(remoteAddr, false)
		if !useTLS || explicitPlain {
			t.Fatalf("resolveDialTargetPolicy(%q) = (useTLS=%v, explicitPlain=%v), want useTLS=true", remoteAddr, useTLS, explicitPlain)
		}
	}

	for _, localAddr := range []string{
		"127.0.0.1:50051",
		"localhost:50051",
		"[::1]:50051",
	} {
		_, useTLS, _ := resolveDialTargetPolicy(localAddr, false)
		if useTLS {
			t.Fatalf("resolveDialTargetPolicy(%q) = useTLS=true, want false for local loopback", localAddr)
		}
	}

	if _, useTLS := resolveDialTarget("grpc://api.gitslice.io:50051"); useTLS {
		t.Fatal("explicit grpc:// scheme unexpectedly enabled TLS")
	}
	if _, useTLS, _ := resolveDialTargetPolicy("api.gitslice.io:50051", true); useTLS {
		t.Fatal("--insecure flag did not allow plaintext dial")
	}
	t.Setenv("GS_TLS", "0")
	if _, useTLS := resolveDialTarget("api.gitslice.io:50051"); useTLS {
		t.Fatal("GS_TLS=0 did not allow plaintext dial")
	}
	t.Setenv("GS_TLS", "")

	secureCreds := bearerTokenCreds{requireTLS: true}
	if !secureCreds.RequireTransportSecurity() {
		t.Fatal("expected RequireTransportSecurity() == true for remote target")
	}
	meta, err := secureCreds.GetRequestMetadata(authContext(context.Background(), UserConfig{Token: "gs_secret_tok"}))
	if err != nil || meta["authorization"] != "Bearer gs_secret_tok" {
		t.Fatalf("unexpected bearer metadata: %v (err=%v)", meta, err)
	}
}

func TestResolveRunChecksServerAddrPinsConfiguredServer(t *testing.T) {
	got, err := agentd.ResolveRunChecksServerAddr("https://api.gitslice.io:443", "evil.attacker.example:50051")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://api.gitslice.io:443" {
		t.Fatalf("got %q, want configured server address", got)
	}

	if _, err := agentd.ResolveRunChecksServerAddr("", "evil.attacker.example:50051"); err == nil {
		t.Fatal("expected untrusted non-loopback req.ServerAddr to be rejected when daemon ServerAddr is empty")
	}

	gotLoopback, err := agentd.ResolveRunChecksServerAddr("", "127.0.0.1:50051")
	if err != nil || gotLoopback != "127.0.0.1:50051" {
		t.Fatalf("expected loopback fallback in tests, got %q (err=%v)", gotLoopback, err)
	}
}

func TestNormalizeDoorbellURLRequiresWSSForRemoteTLS(t *testing.T) {
	t.Setenv("GS_INSECURE", "")

	if _, err := agentd.NormalizeDoorbellURL("ws://doorbell.gitslice.io/wait", true, false); err == nil {
		t.Fatal("expected ws:// doorbell URL to be rejected when requireTLS is true")
	}
	if _, err := agentd.NormalizeDoorbellURL("http://doorbell.gitslice.io/wait", false, false); err == nil {
		t.Fatal("expected http:// doorbell URL to non-loopback host to be rejected")
	}
	got, err := agentd.NormalizeDoorbellURL("https://doorbell.gitslice.io/wait?daemon=1", true, false)
	if err != nil || got != "wss://doorbell.gitslice.io/wait?daemon=1" {
		t.Fatalf("NormalizeDoorbellURL(https) = %q, err=%v", got, err)
	}
	gotLoop, err := agentd.NormalizeDoorbellURL("ws://127.0.0.1:9123/wait", false, false)
	if err != nil || gotLoop != "ws://127.0.0.1:9123/wait" {
		t.Fatalf("NormalizeDoorbellURL(loopback ws) = %q, err=%v", gotLoop, err)
	}
}

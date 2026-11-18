package cli

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/net/websocket"
)

func TestResolveDialTargetTLSDefaultsAndBearerSecurity(t *testing.T) {
	t.Setenv("GS_TLS", "")
	t.Setenv("GS_INSECURE", "")

	// Non-loopback custom ports MUST default to TLS.
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

	// Loopback addresses without scheme remain plaintext by default for local dev/test servers.
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

	// Explicit plaintext overrides work when requested.
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

	// Bearer token PerRPCCredentials enforces transport security when required.
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
	// Configured daemon ServerAddr always wins over an untrusted req.ServerAddr.
	got, err := resolveRunChecksServerAddr("https://api.gitslice.io:443", "evil.attacker.example:50051")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://api.gitslice.io:443" {
		t.Fatalf("got %q, want configured server address", got)
	}

	// Empty configured ServerAddr rejects non-loopback untrusted req.ServerAddr.
	if _, err := resolveRunChecksServerAddr("", "evil.attacker.example:50051"); err == nil {
		t.Fatal("expected untrusted non-loopback req.ServerAddr to be rejected when daemon ServerAddr is empty")
	}

	// Empty configured ServerAddr permits loopback in local unit tests.
	gotLoopback, err := resolveRunChecksServerAddr("", "127.0.0.1:50051")
	if err != nil || gotLoopback != "127.0.0.1:50051" {
		t.Fatalf("expected loopback fallback in tests, got %q (err=%v)", gotLoopback, err)
	}
}

func TestNormalizeDoorbellURLRequiresWSSForRemoteTLS(t *testing.T) {
	t.Setenv("GS_INSECURE", "")

	if _, err := normalizeDoorbellURL("ws://doorbell.gitslice.io/wait", true, false); err == nil {
		t.Fatal("expected ws:// doorbell URL to be rejected when requireTLS is true")
	}
	if _, err := normalizeDoorbellURL("http://doorbell.gitslice.io/wait", false, false); err == nil {
		t.Fatal("expected http:// doorbell URL to non-loopback host to be rejected")
	}
	got, err := normalizeDoorbellURL("https://doorbell.gitslice.io/wait?daemon=1", true, false)
	if err != nil || got != "wss://doorbell.gitslice.io/wait?daemon=1" {
		t.Fatalf("normalizeDoorbellURL(https) = %q, err=%v", got, err)
	}
	gotLoop, err := normalizeDoorbellURL("ws://127.0.0.1:9123/wait", false, false)
	if err != nil || gotLoop != "ws://127.0.0.1:9123/wait" {
		t.Fatalf("normalizeDoorbellURL(loopback ws) = %q, err=%v", gotLoop, err)
	}
}

func TestStandbyWaitsForInitialDoorbellReadyBeforeChoosingPollInterval(t *testing.T) {
	wsDone := make(chan struct{})
	srv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		// Small handshake delay still well inside doorbellInitialReadyWait (2s),
		// proving standby waits for bell.waitReady instead of racing isConnected()==false.
		<-wsDone
		_ = conn.Close()
	}))
	defer srv.Close()
	defer close(wsDone)

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	var polls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		if n >= 2 {
			// If standby mistakenly chose PollAfterMs (80ms) instead of SafetyPollAfterMs (1h),
			// a second poll will fire quickly.
			return &corev1.StandbyPollResponse{Pending: true}, nil
		}
		return &corev1.StandbyPollResponse{
			DaemonId:          "agent_race_test",
			PollAfterMs:       80,      // 80ms fallback if doorbell not connected yet
			SafetyPollAfterMs: 3600000, // 1h safety interval when doorbell IS connected
			DoorbellUrl:       wsURL,
			DoorbellToken:     "tok_test",
		}, nil
	})
	d.cfg.ServerAddr = "127.0.0.1:50051"

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.standby(ctx)
	}()

	// Wait 250ms (> 3x the 80ms fallback PollAfterMs): standby must still be sleeping on
	// the 1-hour SafetyPollAfterMs with only 1 poll issued!
	time.Sleep(250 * time.Millisecond)
	if got := polls.Load(); got != 1 {
		cancel()
		t.Fatalf("standby polled %d times within 250ms, want 1 (initial doorbell race regressed)", got)
	}
	if !d.doorbellOpen.Load() {
		cancel()
		t.Fatal("expected d.doorbellOpen to be true on first cycle after waitReady")
	}
	cancel()
	<-errCh
}

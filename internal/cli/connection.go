package cli

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/rpclimits"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

func (r Runner) resolveSlice(ctx context.Context, sliceRef string) (*corev1.Slice, error) {
	var resolved *corev1.Slice
	err := r.withSession(ctx, func(s *Session) error {
		slice, _, err := s.ResolveSlice(sliceRef)
		resolved = slice
		return err
	})
	return resolved, err
}

func (r Runner) authenticatedConn(ctx context.Context) (UserConfig, *grpc.ClientConn, context.Context, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, nil, nil, err
	}
	return cfg, conn, authContext(ctx, cfg), nil
}

func (r Runner) defaultSliceAccount(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (string, error) {
	if conn == nil {
		return "", userError("account_required", "account is required", "Run gs slice list <account>.")
	}
	if active, err := r.activeAccount(ctx, cfg, conn); err != nil {
		return "", err
	} else if active != "" {
		return active, nil
	}
	account, err := r.personalAccountSlug(ctx, conn)
	if err != nil {
		if isUserErrorCode(err, "no_account") {
			return "", userError("account_required", "account is required", "Run gs slice list <account>.")
		}
		return "", err
	}
	return account, nil
}

// activeAccount returns the account chosen with gs account use, if the user
// still belongs to it, or "".
func (r Runner) activeAccount(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) (string, error) {
	if strings.TrimSpace(cfg.ActiveAccount) == "" {
		return "", nil
	}
	// ctx already carries the caller's credentials, as for personalAccountSlug.
	status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		return "", err
	}
	if containsFold(status.Accounts, cfg.ActiveAccount) {
		return strings.TrimSpace(cfg.ActiveAccount), nil
	}
	return "", nil
}

type bearerTokenContextKey struct{}

// insecureContextKey carries --insecure (or Runner.Insecure) down a command's
// context, so concurrent Runners never share process-global transport policy.
type insecureContextKey struct{}

func withInsecure(ctx context.Context, insecure bool) context.Context {
	if !insecure {
		return ctx
	}
	return context.WithValue(ctx, insecureContextKey{}, true)
}

func insecureFromContext(ctx context.Context) bool {
	insecure, _ := ctx.Value(insecureContextKey{}).(bool)
	return insecure
}

// insecureTransportRequested reports whether plaintext connections to
// non-loopback servers were requested: by the caller (--insecure, carried in
// its context) or by GS_INSECURE.
func insecureTransportRequested(insecure bool) bool {
	if insecure {
		return true
	}
	v := strings.TrimSpace(os.Getenv("GS_INSECURE"))
	return v == "1" || strings.EqualFold(v, "true")
}

type bearerTokenCreds struct {
	requireTLS bool
}

func (c bearerTokenCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	token, _ := ctx.Value(bearerTokenContextKey{}).(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

func (c bearerTokenCreds) RequireTransportSecurity() bool {
	return c.requireTLS
}

func authContext(ctx context.Context, cfg UserConfig) context.Context {
	return context.WithValue(ctx, bearerTokenContextKey{}, cfg.Token)
}

func dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return dialWithOptions(ctx, addr)
}

// dialAgentStream is dial for the daemon's long-lived Connect stream. It adds
// transport-level gRPC keepalive so a black-holed but still-ESTABLISHED socket
// is torn down within ~Timeout instead of blocking a pending Send/Recv forever.
func dialAgentStream(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return dialStream(ctx, addr)
}

// dialStream is dial for a connection that carries one long call (a daemon's
// stream, an import): transport keepalive notices a connection that died
// without closing within about 30 seconds, instead of the call waiting on it
// for as long as the operating system lets it.
func dialStream(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return dialWithOptions(ctx, addr, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                20 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}))
}

const (
	clientStreamWindow = 4 << 20
	clientConnWindow   = 16 << 20
)

// dialLazy is dial without waiting for the server: the connection is made when
// the first call needs it, and a call made while the server is unreachable
// fails with Unavailable. A long-lived process that can work from local state,
// such as a lazy workspace's mount daemon, uses it so it starts offline.
func dialLazy(addr string, insecureRequested bool) (*grpc.ClientConn, error) {
	target, options := dialOptions(addr, insecureRequested)
	return grpc.DialContext(context.Background(), target, options...)
}

func dialWithOptions(ctx context.Context, addr string, extraOptions ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target, options := dialOptions(addr, insecureFromContext(ctx))
	options = append(options, grpc.WithBlock())
	options = append(options, extraOptions...)
	return grpc.DialContext(ctx, target, options...)
}

// dialOptions returns the target and the options every connection to the
// server uses, except whether to wait for it.
func dialOptions(addr string, insecureRequested bool) (string, []grpc.DialOption) {
	target, useTLS, explicitPlain := resolveDialTargetPolicy(addr, insecureRequested)
	host := extractDialHost(target)
	creds := insecure.NewCredentials()
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
	}
	requireBearerTLS := useTLS || (!explicitPlain && !isLoopbackHost(host))
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(bearerTokenCreds{requireTLS: requireBearerTLS}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
			grpc.MaxCallSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		),
		// HTTP/2 flow-control windows large enough that a big response
		// (a checkout's listing and files) streams at the link's speed
		// rather than 64 KiB per round trip.
		grpc.WithInitialWindowSize(clientStreamWindow),
		grpc.WithInitialConnWindowSize(clientConnWindow),
		grpc.WithChainUnaryInterceptor(retryUnaryInterceptor, traceUnaryInterceptor),
		grpc.WithChainStreamInterceptor(traceStreamInterceptor),
	}
	return target, options
}

// resolveDialTarget strips any scheme from a server address and decides whether
// to use TLS. TLS is enabled by default for all non-loopback hosts unless an
// explicit plaintext scheme (grpc://, http://), --insecure, or GS_TLS=0/false
// is set; loopback hosts remain plaintext by default unless port 443, an https/
// grpcs scheme, or GS_TLS=1/true is set.
func resolveDialTarget(addr string) (string, bool) {
	target, useTLS, _ := resolveDialTargetPolicy(addr, false)
	return target, useTLS
}

func resolveDialTargetPolicy(addr string, insecureRequested bool) (target string, useTLS bool, explicitPlain bool) {
	addr = strings.TrimSpace(addr)
	explicitTLS := false
	switch {
	case strings.HasPrefix(addr, "grpcs://"):
		addr, explicitTLS = strings.TrimPrefix(addr, "grpcs://"), true
	case strings.HasPrefix(addr, "https://"):
		addr, explicitTLS = strings.TrimPrefix(addr, "https://"), true
	case strings.HasPrefix(addr, "grpc://"):
		addr, explicitPlain = strings.TrimPrefix(addr, "grpc://"), true
	case strings.HasPrefix(addr, "http://"):
		addr, explicitPlain = strings.TrimPrefix(addr, "http://"), true
	}
	addr = strings.TrimSuffix(addr, "/")
	if v := strings.TrimSpace(os.Getenv("GS_TLS")); v != "" {
		switch {
		case v == "1" || strings.EqualFold(v, "true"):
			explicitTLS = true
			explicitPlain = false
		case v == "0" || strings.EqualFold(v, "false"):
			explicitPlain = true
			explicitTLS = false
		}
	}
	if insecureTransportRequested(insecureRequested) && !explicitTLS {
		explicitPlain = true
	}
	host := extractDialHost(addr)
	_, port, splitErr := net.SplitHostPort(addr)
	switch {
	case explicitTLS:
		useTLS = true
	case explicitPlain:
		useTLS = false
	case splitErr == nil && port == "443":
		useTLS = true
	case addr != "" && !isLoopbackHost(host):
		useTLS = true
	default:
		useTLS = false
	}
	return addr, useTLS, explicitPlain
}

func extractDialHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(addr, "[]")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackDialAddr(addr string) bool {
	target, _, _ := resolveDialTargetPolicy(addr, false)
	return isLoopbackHost(extractDialHost(target))
}

func serverUsesNonLoopbackTLS(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	target, useTLS := resolveDialTarget(addr)
	return useTLS && !isLoopbackHost(extractDialHost(target))
}

func defaultServerAddr() string {
	if value := os.Getenv("GS_SERVER_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_GRPC_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_SERVER_ADDR"); value != "" {
		return value
	}
	// Default to the production endpoint. resolveDialTarget enables TLS
	// automatically for :443. Point at staging or a local server with --server
	// or GS_SERVER_ADDR.
	return "api.gitslice.io:443"
}

func defaultWebURL() string {
	if value := os.Getenv("GS_WEB_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_WEB_URL"); value != "" {
		return value
	}
	// Hosted web app, used by the --clerk browser login flow (its /cli-login
	// page returns the token to the CLI). Override with GS_WEB_URL for a local
	// web dev server, e.g. http://127.0.0.1:5173.
	return "https://gitslice.io"
}

func defaultGatewayURL() string {
	if value := os.Getenv("GS_GATEWAY_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_GATEWAY_URL"); value != "" {
		return value
	}
	if value := os.Getenv("GS_HTTP_ADDR"); value != "" {
		if strings.Contains(value, "://") {
			return value
		}
		return "http://" + value
	}
	if value := os.Getenv("GITSLICE_HTTP_ADDR"); value != "" {
		if strings.Contains(value, "://") {
			return value
		}
		return "http://" + value
	}
	return "http://127.0.0.1:8082"
}

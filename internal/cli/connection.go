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
	"google.golang.org/grpc/metadata"
)

func (r Runner) resolveSlice(ctx context.Context, sliceRef string) (*corev1.Slice, error) {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return nil, err
	}
	return corev1.NewSliceServiceClient(conn).ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
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

func authContext(ctx context.Context, cfg UserConfig) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+cfg.Token)
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
func dialLazy(addr string) (*grpc.ClientConn, error) {
	target, options := dialOptions(addr)
	return grpc.DialContext(context.Background(), target, options...)
}

func dialWithOptions(ctx context.Context, addr string, extraOptions ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target, options := dialOptions(addr)
	options = append(options, grpc.WithBlock())
	options = append(options, extraOptions...)
	return grpc.DialContext(ctx, target, options...)
}

// dialOptions returns the target and the options every connection to the
// server uses, except whether to wait for it.
func dialOptions(addr string) (string, []grpc.DialOption) {
	target, useTLS := resolveDialTarget(addr)
	creds := insecure.NewCredentials()
	if useTLS {
		host := target
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		}
		creds = credentials.NewTLS(&tls.Config{ServerName: host})
	}
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
			grpc.MaxCallSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		),
		// HTTP/2 flow-control windows large enough that a big response
		// (a checkout's listing and files) streams at the link's speed
		// rather than 64 KiB per round trip.
		grpc.WithInitialWindowSize(clientStreamWindow),
		grpc.WithInitialConnWindowSize(clientConnWindow),
		grpc.WithChainUnaryInterceptor(retryUnaryInterceptor),
	}
	return target, options
}

// resolveDialTarget strips any scheme from a server address and decides whether
// to use TLS. TLS is used when the address has an https/grpcs scheme, targets
// port 443, or GS_TLS is set. This lets the CLI reach TLS endpoints such as
// api.gitslice.io:443 while defaulting to plaintext for local servers.
func resolveDialTarget(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	useTLS := false
	switch {
	case strings.HasPrefix(addr, "grpcs://"):
		addr, useTLS = strings.TrimPrefix(addr, "grpcs://"), true
	case strings.HasPrefix(addr, "https://"):
		addr, useTLS = strings.TrimPrefix(addr, "https://"), true
	case strings.HasPrefix(addr, "grpc://"):
		addr = strings.TrimPrefix(addr, "grpc://")
	case strings.HasPrefix(addr, "http://"):
		addr = strings.TrimPrefix(addr, "http://")
	}
	addr = strings.TrimSuffix(addr, "/")
	if !useTLS {
		if _, port, err := net.SplitHostPort(addr); err == nil && port == "443" {
			useTLS = true
		}
	}
	if v := os.Getenv("GS_TLS"); v == "1" || strings.EqualFold(v, "true") {
		useTLS = true
	}
	return addr, useTLS
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

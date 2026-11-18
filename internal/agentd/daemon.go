package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/mountd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// HostCallbacks wires CLI-owned operations (dialing, workspace init, changeset
// capture, TransferManager batch fetch, and confined file writes) into Daemon.
type HostCallbacks struct {
	ServerAddr string
	Token      string
	Home       string
	Insecure   bool
	Version    string
	Stdout     io.Writer
	Stderr     io.Writer

	Dial                func(ctx context.Context, addr string) (*grpc.ClientConn, error)
	DialStream          func(ctx context.Context, addr string) (*grpc.ClientConn, error)
	DialLazy            func(addr string, insecure bool) (*grpc.ClientConn, error)
	AuthContext         func(ctx context.Context) context.Context
	UsesNonLoopbackTLS  func(addr string) bool
	InsecureFromContext func(ctx context.Context) bool
	EnvServerAddr       func() string

	WorkspaceInit    func(ctx context.Context, workdir, conversationID, sliceRef string, envOverrides map[string]string) (string, error)
	CaptureChangeset func(ctx context.Context, workdir string, envOverrides map[string]string, title string) (captured bool, summary string, err error)

	ObjectCache     func() (*clientcache.ObjectCache, error)
	FetchCheckBatch BatchFetcherFunc
	CheckTreeWriter CheckTreeWriter
	ReportOnline    func(daemonID string) error
}

// Host is an alias for HostCallbacks.
type Host = HostCallbacks

func (h HostCallbacks) stderr() io.Writer {
	if h.Stderr != nil {
		return h.Stderr
	}
	return io.Discard
}

func (h HostCallbacks) stdout() io.Writer {
	if h.Stdout != nil {
		return h.Stdout
	}
	return io.Discard
}

func (h HostCallbacks) dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	if h.Dial != nil {
		return h.Dial(ctx, addr)
	}
	if IsLoopbackDialAddr(addr) {
		return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	return nil, fmt.Errorf("agent daemon dial callback is not configured")
}

func (h HostCallbacks) dialStream(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	if h.DialStream != nil {
		return h.DialStream(ctx, addr)
	}
	return h.dial(ctx, addr)
}

func (h HostCallbacks) dialLazy(addr string) (*grpc.ClientConn, error) {
	if h.DialLazy != nil {
		return h.DialLazy(addr, h.Insecure)
	}
	return h.dial(context.Background(), addr)
}

func (h HostCallbacks) authContext(ctx context.Context) context.Context {
	if h.AuthContext != nil {
		return h.AuthContext(ctx)
	}
	if tok := strings.TrimSpace(h.Token); tok != "" {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	return ctx
}

func (h HostCallbacks) usesNonLoopbackTLS(addr string) bool {
	if h.UsesNonLoopbackTLS != nil {
		return h.UsesNonLoopbackTLS(addr)
	}
	return strings.TrimSpace(addr) != "" && !IsLoopbackDialAddr(addr) && !h.Insecure
}

func (h HostCallbacks) insecureFromContext(ctx context.Context) bool {
	if h.InsecureFromContext != nil {
		return h.InsecureFromContext(ctx)
	}
	return h.Insecure
}

func (h HostCallbacks) envServerAddr() string {
	if h.EnvServerAddr != nil {
		return h.EnvServerAddr()
	}
	if v := strings.TrimSpace(os.Getenv("GS_SERVER_ADDR")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("GITSLICE_SERVER_ADDR"))
}

func (h HostCallbacks) objectCache() (*clientcache.ObjectCache, error) {
	if h.ObjectCache != nil {
		return h.ObjectCache()
	}
	dir := strings.TrimSpace(os.Getenv("GS_CLIENT_CACHE_DIR"))
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("GITSLICE_CLIENT_CACHE_DIR"))
	}
	if dir == "" {
		home := h.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		dir = filepath.Join(home, ".cache", "gitslice", "objects")
	}
	return clientcache.New(dir)
}

// AuthClient manages delegated child API keys for conversations.
type AuthClient interface {
	CreateApiKey(ctx context.Context, in *corev1.CreateApiKeyRequest, opts ...grpc.CallOption) (*corev1.CreateApiKeyResponse, error)
	RevokeApiKey(ctx context.Context, in *corev1.RevokeApiKeyRequest, opts ...grpc.CallOption) (*corev1.RevokeApiKeyResponse, error)
}

// Daemon supervises local agent conversations, standby polling, and CI check runs.
type Daemon struct {
	Host          HostCallbacks
	Auth          AuthClient // optional override for tests; nil dials Host.ServerAddr
	Runtime       Runtime
	BaseCtx       context.Context
	WorkingDir    string
	SendQueue     *SendQueue
	Name          string   // registered daemon name, as given
	DaemonID      string   // registered daemon id from server ack; guarded by mu
	AllowedSlices []string // optional canonical <account>/<slice> allowlist from --slice
	// InstanceID names this process to the server, which sends each check
	// run to one process of a daemon (design/32 layer 3).
	InstanceID string

	// Detaching (daemon_standby.go): a checks-only daemon with nothing to run
	// closes its stream and polls instead.
	DetachAfter        time.Duration
	Detaching          atomic.Bool // a detach was sent; the stream is about to end
	StandbyUnsupported atomic.Bool // the server cannot take a detached daemon
	DoorbellOpen       atomic.Bool // its next standby poll says the doorbell socket is open
	StandbyPollFn      func(context.Context) (*corev1.StandbyPollResponse, error)
	pollMu             sync.Mutex
	pollConn           *grpc.ClientConn
	Quiet              bool

	idleMu       sync.Mutex
	lastActivity time.Time
	activeWork   int
	idleShutdown bool

	// CheckSlots bounds how many checks run at once
	// (GITSLICE_CHECK_CONCURRENCY); nil runs every dispatched check at once.
	CheckSlots chan struct{}

	// Lazy mounts new conversation workspaces instead of checking them out.
	Lazy bool

	// Optional hooks for isolated lazy-workspace tests.
	IsMountedFn func(dir string) bool
	RunGSFn     func(ctx context.Context, dir string, envOverrides map[string]string, args ...string) (string, error)
	UnmountFn   func(ctx context.Context, conversationID string, rec mountd.Record) error

	mu                  sync.Mutex
	conversations       map[string]*Conversation
	reattachedKeys      map[string]reattachedConversationKey
	checkRuns           map[string]context.CancelFunc
	checkRunOutboxes    map[string]*CheckRunOutbox
	checkRunOutboxOrder []string
	recentCheckRuns     map[string]struct{}
	recentCheckRunOrder []string
}

// NewDaemon constructs a Daemon with initialized state maps and instance ID.
func NewDaemon(baseCtx context.Context, host HostCallbacks) *Daemon {
	return &Daemon{
		Host:            host,
		BaseCtx:         baseCtx,
		conversations:   map[string]*Conversation{},
		checkRuns:       map[string]context.CancelFunc{},
		recentCheckRuns: map[string]struct{}{},
		CheckSlots:      CheckSlotsFromEnv(),
		InstanceID:      NewInstanceID(),
	}
}

// Run supervises the daemon connection, reconnects, standby polling, and idle shutdown.
func (d *Daemon) Run(ctx context.Context, runtimeName string, idleTimeout time.Duration, ci bool) error {
	agentCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.BaseCtx = agentCtx
	defer d.ClosePollConnection()

	if !ci {
		d.ReattachLazyWorkspaces(agentCtx)
		d.LoadExistingConversations()
	}
	d.lastActivity = time.Now()
	if idleTimeout > 0 {
		go d.runIdleMonitor(agentCtx, cancel, idleTimeout)
	}
	defer func() {
		d.CancelAll()
		cancel()
	}()
	reportIdleExit := func() {
		if d.IdleShutdownRequested() && !d.Quiet {
			fmt.Fprintln(d.Host.stderr(), "agent daemon exited after idle timeout")
		}
	}

	reportedOnline := false
	for {
		err := d.ServeConnection(agentCtx, d.Name, runtimeName, !reportedOnline)
		if errors.Is(err, ErrAgentDetached) {
			reportedOnline = true
			// Work that arrived as the stream closed stays here to be done:
			// reconnect instead of waiting.
			if d.ChecksIdle() {
				if standbyErr := d.Standby(agentCtx); standbyErr != nil {
					reportIdleExit()
					return nil
				}
			}
			continue
		}
		if err == nil {
			if agentCtx.Err() != nil {
				reportIdleExit()
			}
			return nil
		}
		if agentCtx.Err() != nil {
			reportIdleExit()
			return nil
		}
		reportedOnline = true
		if !d.Quiet {
			fmt.Fprintf(d.Host.stderr(), "agent stream disconnected: %v; reconnecting...\n", err)
		}
		select {
		case <-agentCtx.Done():
			reportIdleExit()
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// ServeConnection opens one bidirectional Connect stream to the server and processes messages.
func (d *Daemon) ServeConnection(ctx context.Context, name, runtimeName string, reportOnline bool) error {
	conn, err := d.Host.dialStream(ctx, d.Host.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	client := corev1.NewAgentServiceClient(conn)
	stream, err := client.Connect(d.Host.authContext(connCtx))
	if err != nil {
		return err
	}

	sendQueue := NewSendQueue(connCtx, stream, SendBuffer)
	d.mu.Lock()
	d.SendQueue = sendQueue
	d.mu.Unlock()
	defer func() {
		cancel()
		_ = sendQueue.Wait()
		d.mu.Lock()
		if d.SendQueue == sendQueue {
			d.SendQueue = nil
		}
		d.mu.Unlock()
	}()

	if err := sendQueue.Send(connCtx, d.registerDaemonMessage(name, runtimeName, d.InstanceID, cap(d.CheckSlots))); err != nil {
		return err
	}
	registered, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := registered.GetRegistered()
	if ack == nil || strings.TrimSpace(ack.GetDaemonId()) == "" {
		return NewError("agent_protocol_error", "server did not acknowledge agent daemon registration", "Try restarting the daemon.")
	}
	d.SetDaemonID(ack.GetDaemonId())
	lastInbound := NewInboundState(time.Now())
	if reportOnline {
		if d.Host.ReportOnline != nil {
			if err := d.Host.ReportOnline(ack.GetDaemonId()); err != nil {
				return err
			}
		} else if !d.Quiet {
			fmt.Fprintf(d.Host.stdout(), "agent daemon %s online\n", ack.GetDaemonId())
		}
	}

	go d.resendPendingAll()
	go d.runHeartbeat(connCtx, cancel)
	if d.DetachAfter > 0 && !d.StandbyUnsupported.Load() {
		go d.runDetachWatch(connCtx)
	}
	go d.runInboundWatchdog(connCtx, cancel, lastInbound)

	for {
		msg, err := stream.Recv()
		if err != nil {
			cancel()
			if d.Detaching.Swap(false) && errors.Is(err, io.EOF) {
				return ErrAgentDetached
			}
			if streamEndedCleanly(ctx, err) {
				return nil
			}
			if queueErr := sendQueue.Err(); queueErr != nil && !errors.Is(queueErr, context.Canceled) {
				return queueErr
			}
			return err
		}
		lastInbound.Mark(time.Now())
		d.HandleServerMessage(connCtx, cancel, msg)
	}
}

func (d *Daemon) CancelAll() {
	d.mu.Lock()
	conversations := make([]*Conversation, 0, len(d.conversations))
	for _, conv := range d.conversations {
		conversations = append(conversations, conv)
	}
	d.mu.Unlock()
	for _, conv := range conversations {
		conv.cancelRun()
		conv.closeSession()
	}
}

func (d *Daemon) SetDaemonID(id string) {
	id = strings.TrimSpace(id)
	d.mu.Lock()
	d.DaemonID = id
	d.mu.Unlock()
}

func (d *Daemon) CurrentDaemonID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.DaemonID
}

func (d *Daemon) AllowsSlice(ref *corev1.SliceRef) bool {
	if len(d.AllowedSlices) == 0 {
		return true
	}
	key := CanonicalSliceKey(ref.GetAccount(), ref.GetSlice())
	return key != "" && slices.Contains(d.AllowedSlices, key)
}

func streamEndedCleanly(ctx context.Context, err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	return ctx.Err() != nil
}

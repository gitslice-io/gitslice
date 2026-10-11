package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// gs doctor checks, in order, what a command needs to work: this gs's
// version, the saved config, the server, the sign-in, the workspace, and
// access to a slice (the one named, else the workspace's). Each check says
// what it found and, when something is wrong, what to do about it.

type doctorCheck struct {
	Name string `json:"name"`
	// Status is ok, warn, fail or skip.
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Hint    string `json:"hint,omitempty"`
}

type doctorOutput struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
}

const doctorStepTimeout = 10 * time.Second

func (r Runner) doctorCommand(opts *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [account/slice]",
		Short: "Check version, config, server, sign-in, workspace and slice access",
		Long: "Check what gs needs to work, and say what to do about anything wrong: whether a newer gs is " +
			"released, the saved config, whether the server answers, whether the saved sign-in is accepted, " +
			"the workspace, and whether you can read a slice (the one named, else the workspace's).",
		Args: maxArgs(1, "gs doctor [account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			slice := ""
			if len(args) > 0 {
				slice = args[0]
			}
			return r.runDoctor(cmd.Context(), *opts, slice)
		},
	}
}

func (r Runner) runDoctor(ctx context.Context, opts commandOptions, sliceArg string) error {
	d := doctor{runner: r}
	d.checkVersion(ctx)
	cfg, cfgOK := d.checkConfig()
	conn := d.checkServer(ctx, cfg)
	if conn != nil {
		defer conn.Close()
	}
	status := d.checkAuth(ctx, cfg, conn)
	wsRef := d.checkWorkspace(ctx, cfg, conn)
	d.checkSlice(ctx, cfg, cfgOK, conn, status, firstNonEmpty(strings.TrimSpace(sliceArg), wsRef))

	out := doctorOutput{OK: true, Checks: d.checks}
	failed := 0
	for _, check := range d.checks {
		if check.Status == "fail" {
			failed++
			out.OK = false
		}
	}
	if opts.jsonOutput() {
		if err := r.writeJSONOutput(opts, out); err != nil {
			return err
		}
	} else if !opts.Quiet {
		r.writeDoctorText(out)
	}
	if failed > 0 {
		problems := "problems"
		if failed == 1 {
			problems = "problem"
		}
		return userError("doctor_found_problems", fmt.Sprintf("gs doctor found %d %s", failed, problems), "")
	}
	return nil
}

func (r Runner) writeDoctorText(out doctorOutput) {
	marks := map[string]string{"ok": "✓", "warn": "!", "fail": "✗", "skip": "-"}
	for _, check := range out.Checks {
		fmt.Fprintf(r.Stdout, "%s %s\n", marks[check.Status], check.Summary)
		if check.Hint != "" {
			fmt.Fprintf(r.Stdout, "    %s\n", check.Hint)
		}
	}
	if out.OK {
		fmt.Fprintln(r.Stdout, "no problems found")
	}
}

type doctor struct {
	runner Runner
	checks []doctorCheck
}

func (d *doctor) add(name, status, summary, hint string) {
	d.checks = append(d.checks, doctorCheck{Name: name, Status: status, Summary: summary, Hint: hint})
}

func (d *doctor) checkVersion(ctx context.Context) {
	current := cliVersionInfo().Version
	ctx, cancel := context.WithTimeout(ctx, doctorStepTimeout)
	defer cancel()
	base := strings.TrimRight(firstNonEmpty(os.Getenv("GS_DOWNLOAD_BASE"), defaultReleasesBase), "/")
	latest, err := latestReleaseTag(ctx, &http.Client{Timeout: doctorStepTimeout}, base)
	switch {
	case err != nil:
		d.add("version", "warn", fmt.Sprintf("gs %s; could not look up the latest release: %v", current, err), "Check your network; gs upgrade --check tries again.")
	case newerVersion(latest, current):
		if _, ok := parseVersion(current); !ok {
			d.add("version", "ok", fmt.Sprintf("gs %s (a source build); the latest release is %s", current, latest), "")
			return
		}
		d.add("version", "warn", fmt.Sprintf("gs %s; %s is available", current, latest), "Run gs upgrade.")
	default:
		d.add("version", "ok", fmt.Sprintf("gs %s is the latest release", current), "")
	}
}

func (d *doctor) checkConfig() (UserConfig, bool) {
	path := d.runner.userConfigPath()
	var cfg UserConfig
	if err := readJSONFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.add("config", "ok", "config: none yet at "+path+"; gs uses "+defaultServerAddr(), "")
			return UserConfig{}, true
		}
		d.add("config", "fail", "config "+path+" cannot be read: "+err.Error(), "Fix or delete the file, then sign in again with gs auth login.")
		return UserConfig{}, false
	}
	summary := "config " + path
	if env := strings.TrimSpace(os.Getenv("GS_SERVER_ADDR")); env != "" && cfg.ServerAddr != "" && env != cfg.ServerAddr {
		d.add("config", "warn", summary+fmt.Sprintf(": signed in to %s, but GS_SERVER_ADDR is %s", cfg.ServerAddr, env),
			"Commands use the server you signed in to. To use "+env+", sign in to it: gs auth login --server "+env+".")
		return cfg, true
	}
	d.add("config", "ok", summary, "")
	return cfg, true
}

// doctorServer is the server the CLI talks to: the one signed in to, else the
// default a sign-in would use.
func doctorServer(cfg UserConfig) string {
	return firstNonEmpty(strings.TrimSpace(cfg.ServerAddr), defaultServerAddr())
}

func (d *doctor) checkServer(ctx context.Context, cfg UserConfig) *grpc.ClientConn {
	addr := doctorServer(cfg)
	// A plain TCP connection first names the reason a server cannot be reached
	// (no such host, connection refused) rather than a dial timeout.
	target, _ := resolveDialTarget(addr)
	probe, err := (&net.Dialer{Timeout: doctorStepTimeout}).DialContext(ctx, "tcp", target)
	if err != nil {
		d.add("server", "fail", fmt.Sprintf("server %s cannot be reached: %v", addr, err), "Check the address (gs auth login --server <addr>) and your network.")
		return nil
	}
	_ = probe.Close()
	conn, err := dial(ctx, addr)
	if err != nil {
		d.add("server", "fail", fmt.Sprintf("server %s: %v", addr, err), "Check the address (gs auth login --server <addr>) and your network.")
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, doctorStepTimeout)
	defer cancel()
	started := time.Now()
	res, err := healthv1.NewHealthClient(conn).Check(callCtx, &healthv1.HealthCheckRequest{})
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		conn.Close()
		d.add("server", "fail", fmt.Sprintf("server %s did not answer: %s", addr, grpcstatus.Convert(err).Message()),
			"Check your network and the address. GS_TLS=1 forces TLS for addresses not on port 443.")
		return nil
	}
	if res.GetStatus() != healthv1.HealthCheckResponse_SERVING {
		d.add("server", "warn", fmt.Sprintf("server %s answered in %s but is %s", addr, elapsed, res.GetStatus()), "Try again shortly.")
		return conn
	}
	d.add("server", "ok", fmt.Sprintf("server %s answered in %s", addr, elapsed), "")
	return conn
}

func (d *doctor) checkAuth(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) *corev1.GetAuthStatusResponse {
	if strings.TrimSpace(cfg.Token) == "" {
		d.add("auth", "warn", "not signed in; public slices work without it", "Sign in with gs auth login, or gs auth register-agent for an agent.")
		return nil
	}
	kind, expires := describeToken(cfg.Token)
	if !expires.IsZero() && time.Now().After(expires) {
		d.add("auth", "fail", fmt.Sprintf("the saved %s expired %s", kind, expires.Local().Format(time.RFC1123)), "Sign in again with gs auth login.")
		return nil
	}
	if conn == nil {
		d.add("auth", "skip", "sign-in not checked: the server did not answer", "")
		return nil
	}
	callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), doctorStepTimeout)
	defer cancel()
	status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(callCtx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unauthenticated {
			d.add("auth", "fail", fmt.Sprintf("%s did not accept the saved %s", cfg.ServerAddr, kind),
				"It may have expired or been revoked, or be for another server. Sign in again with gs auth login (or gs auth register-agent for an agent).")
			return nil
		}
		d.add("auth", "fail", "could not check the sign-in: "+grpcstatus.Convert(err).Message(), "Try again; gs auth status shows the same check.")
		return nil
	}
	summary := "signed in as " + status.GetSubjectId()
	if len(status.GetAccounts()) > 0 {
		summary += " (accounts: " + strings.Join(status.GetAccounts(), ", ") + ")"
	}
	summary += " with " + kind
	if !expires.IsZero() {
		summary += ", valid until " + expires.Local().Format("2006-01-02 15:04")
	}
	switch {
	case status.GetNeedsUsername():
		d.add("auth", "warn", summary+"; no personal account yet", "Choose a username at "+webResourceURL("/")+" to create your account.")
	case !expires.IsZero() && time.Until(expires) < 72*time.Hour:
		d.add("auth", "warn", summary, "The sign-in expires soon; run gs auth login to renew it.")
	default:
		d.add("auth", "ok", summary, "")
	}
	return status
}

// describeToken says what kind of token is saved and, when the token says,
// when it expires. It never prints the token.
func describeToken(token string) (string, time.Time) {
	switch {
	case strings.HasPrefix(token, "gsk_"):
		return "agent API key", time.Time{}
	case strings.HasPrefix(token, "clitok"):
		return "CLI session token", time.Time{}
	}
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err == nil {
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
				return "session token", time.Unix(claims.Exp, 0)
			}
		}
	}
	return "token", time.Time{}
}

// checkWorkspace reports the workspace gs is in, and returns its slice.
func (d *doctor) checkWorkspace(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn) string {
	root, err := d.runner.workspaceRoot()
	if err != nil {
		d.add("workspace", "skip", "not in a workspace", "")
		return ""
	}
	ws, err := d.runner.readWorkspaceConfig()
	if err != nil {
		d.add("workspace", "fail", "workspace "+root+" cannot be read: "+err.Error(), "Run gs init <account/slice> in a new directory.")
		return ""
	}
	ref := ws.Account + "/" + ws.Slice
	summary := fmt.Sprintf("workspace %s at %s", ref, root)
	if _, conflicts, err := d.runner.readWorkspaceConflictState(); err == nil && conflicts {
		d.add("workspace", "warn", summary+" has unresolved sync conflicts", "Resolve the conflicted files, then run gs modify.")
		return ref
	}
	state, _ := d.runner.readWorkspaceState()
	base := firstNonEmpty(state.BaseCommitID, ws.BaseCommitID)
	if conn != nil && base != "" {
		callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), doctorStepTimeout)
		defer cancel()
		if head, err := corev1.NewRepositoryServiceClient(conn).GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef}); err == nil && head.GetCommitId() != "" && head.GetCommitId() != base {
			summary += fmt.Sprintf(" (based on %s; the latest is %s, gs sync updates it)", shortID(base), shortID(head.GetCommitId()))
		}
	}
	d.add("workspace", "ok", summary, "")
	return ref
}

// checkSlice reports whether the signed-in subject (or anyone, signed out)
// can read the slice, and why not.
func (d *doctor) checkSlice(ctx context.Context, cfg UserConfig, cfgOK bool, conn *grpc.ClientConn, status *corev1.GetAuthStatusResponse, value string) {
	if value == "" {
		return
	}
	if conn == nil || !cfgOK {
		d.add("slice", "skip", "slice "+value+" not checked: the server did not answer", "")
		return
	}
	var ref *corev1.SliceRef
	var err error
	if strings.ContainsAny(value, ":/") {
		ref, err = parseSliceRef(value)
	} else if status != nil && len(status.GetAccounts()) > 0 {
		ref, err = parseSliceRef(status.GetAccounts()[0] + "/" + value)
	} else {
		err = userError("invalid_slice_ref", "slice must be account/slice", "")
	}
	if err != nil {
		d.add("slice", "fail", "slice "+value+": not a slice reference", "Pass account/slice, such as gitslice/gitslice.")
		return
	}
	label := ref.Account + "/" + ref.Slice
	callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), doctorStepTimeout)
	defer cancel()
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		switch grpcstatus.Code(err) {
		case codes.NotFound:
			d.add("slice", "fail", "slice "+label+" does not exist", "Check the name; gs slice list "+ref.Account+" lists the account's slices you can see.")
		case codes.Unauthenticated:
			if strings.TrimSpace(cfg.Token) == "" {
				d.add("slice", "fail", "slice "+label+" is private and you are not signed in",
					"Sign in with gs auth login (or gs auth register-agent for an agent) as a member of "+ref.Account+".")
			} else {
				d.add("slice", "fail", "slice "+label+" is private and the saved sign-in was not accepted", "Sign in again with gs auth login.")
			}
		case codes.PermissionDenied:
			who := "you are"
			if status != nil && status.GetSubjectId() != "" {
				who = status.GetSubjectId() + " is"
			}
			d.add("slice", "fail", fmt.Sprintf("slice %s is private and %s not a member of %s", label, who, ref.Account),
				"If "+ref.Account+" is an agent you own, claim co-ownership at "+claimsURL()+" (signed in with the email the agent registered with); otherwise ask an owner of "+ref.Account+" to add you.")
		default:
			d.add("slice", "fail", "slice "+label+": "+grpcstatus.Convert(err).Message(), "Try again; gs slice info "+label+" runs the same lookup.")
		}
		return
	}
	visibility := firstNonEmpty(strings.TrimSpace(slice.GetDefinition().GetVisibility()), "private")
	summary := fmt.Sprintf("slice %s is %s", label, visibility)
	role := ""
	if status != nil {
		for _, m := range status.GetMemberships() {
			if strings.EqualFold(m.GetAccount(), ref.Account) {
				role = m.GetRole()
			}
		}
	}
	switch {
	case role != "":
		summary += "; you are " + role + " of " + ref.Account
		if !roleCanChange(role) {
			d.add("slice", "warn", summary+", so you can read it but not change it", "Ask an owner of "+ref.Account+" for the writer role.")
			return
		}
	case status != nil:
		summary += "; you can read it, but changesets from it need write access in " + ref.Account
	default:
		summary += "; sign in to change it"
	}
	d.add("slice", "ok", summary, "")
}

func roleCanChange(role string) bool {
	switch strings.ToLower(role) {
	case "owner", "admin", "member", "writer":
		return true
	}
	return false
}

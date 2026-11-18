package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (r Runner) runAgentStatus(ctx context.Context, opts commandOptions, forSlice string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	callCtx := authContext(ctx, cfg)
	request := &corev1.ListDaemonsRequest{}
	if strings.TrimSpace(forSlice) != "" {
		if request.ForSlice, err = r.resolveSliceRefInput(callCtx, cfg, conn, forSlice); err != nil {
			return err
		}
	}
	res, err := corev1.NewAgentServiceClient(conn).ListDaemons(callCtx, request)
	if err != nil {
		return err
	}
	out := agentStatusOutput{Daemons: make([]agentDaemonOutput, 0, len(res.GetDaemons()))}
	for _, daemon := range res.GetDaemons() {
		out.Daemons = append(out.Daemons, agentDaemonToOutput(daemon))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if len(out.Daemons) == 0 {
		fmt.Fprintln(r.Stdout, "no agent daemons")
		return nil
	}
	w := tabwriter.NewWriter(r.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tACCOUNT\tRUNTIME\tSTATUS\tLAST_SEEN\tCI_FOR\tEXPOSED_TO")
	for _, daemon := range out.Daemons {
		name := daemon.shownName()
		if daemon.DisplayName != "" && daemon.DisplayName != daemon.Name {
			name += " (" + daemon.Name + ")"
		}
		exposed := firstNonEmpty(strings.Join(daemon.ExposedTo, ", "), "-")
		if !daemon.Manageable {
			exposed = "(exposed to you)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", daemon.ID, name, daemon.Account, daemon.Runtime, daemon.Status, daemon.LastSeenAt, firstNonEmpty(strings.Join(daemon.CISlices, ", "), "-"), exposed)
	}
	return w.Flush()
}

// runAgentRename sets, or with an empty name clears, the name a daemon is
// shown by.
func (r Runner) runAgentRename(ctx context.Context, opts commandOptions, selector, newName string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAgentServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	res, err := client.ListDaemons(callCtx, &corev1.ListDaemonsRequest{})
	if err != nil {
		return err
	}
	daemon, err := pickDaemon(res.GetDaemons(), selector)
	if err != nil {
		return err
	}
	renamed, err := client.RenameDaemon(callCtx, &corev1.RenameDaemonRequest{DaemonId: daemon.GetId(), DisplayName: newName})
	if err != nil {
		if grpcstatus.Code(err) == codes.AlreadyExists {
			return userError("name_taken", grpcstatus.Convert(err).Message(), "Pick another name; gs agent status lists the account's daemons.")
		}
		return err
	}
	out := agentDaemonToOutput(renamed)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if renamed.GetDisplayName() == "" {
		fmt.Fprintf(r.Stdout, "%s is shown as %s again\n", renamed.GetId(), renamed.GetName())
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s is now shown as %s (it still registers as %s)\n", renamed.GetId(), renamed.GetDisplayName(), renamed.GetName())
	return nil
}

// runAgentExpose exposes a daemon to a slice, or with expose false withdraws it.
func (r Runner) runAgentExpose(ctx context.Context, opts commandOptions, selector, sliceArg string, expose bool) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAgentServiceClient(conn)
	res, err := client.ListDaemons(callCtx, &corev1.ListDaemonsRequest{})
	if err != nil {
		return err
	}
	daemon, err := pickDaemon(res.GetDaemons(), selector)
	if err != nil {
		return err
	}
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceArg)
	if err != nil {
		return err
	}
	var updated *corev1.AgentDaemon
	if expose {
		updated, err = client.ExposeDaemon(callCtx, &corev1.ExposeDaemonRequest{DaemonId: daemon.GetId(), Slice: ref})
	} else {
		updated, err = client.UnexposeDaemon(callCtx, &corev1.UnexposeDaemonRequest{DaemonId: daemon.GetId(), Slice: ref})
	}
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, agentDaemonToOutput(updated))
	}
	if opts.Quiet {
		return nil
	}
	name := firstNonEmpty(updated.GetDisplayName(), updated.GetName())
	if expose {
		fmt.Fprintf(r.Stdout, "%s (%s) is exposed to %s: its owners and admins can choose it with gs slice set-ci-daemon\n", name, updated.GetId(), sliceRefLabel(ref))
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s (%s) is no longer exposed to %s\n", name, updated.GetId(), sliceRefLabel(ref))
	if sliceCI := updated.GetCiSlices(); len(sliceCI) > 0 {
		for _, used := range sliceCI {
			if used.GetAccount() == ref.GetAccount() && used.GetSlice() == ref.GetSlice() {
				fmt.Fprintf(r.Stdout, "that slice still names it, so its checks are refused until it names another daemon\n")
			}
		}
	}
	return nil
}

// pickDaemon finds the daemon a user named: by id, else by the name it is
// shown by or the one it registered with.
func pickDaemon(daemons []*corev1.AgentDaemon, selector string) (*corev1.AgentDaemon, error) {
	selector = strings.TrimSpace(selector)
	for _, d := range daemons {
		if d.GetId() == selector {
			return d, nil
		}
	}
	var matches []*corev1.AgentDaemon
	for _, d := range daemons {
		if strings.EqualFold(d.GetDisplayName(), selector) || strings.EqualFold(d.GetName(), selector) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, userError("daemon_not_found", "no agent daemon "+selector, "gs agent status lists the daemons you can rename; use the id from its first column.")
	}
	ids := make([]string, len(matches))
	for i, d := range matches {
		ids[i] = d.GetId() + " (" + d.GetAccount() + ")"
	}
	return nil, userError("ambiguous_daemon", selector+" names more than one daemon: "+strings.Join(ids, ", "), "Use the daemon's id.")
}

func (r Runner) runAgentStop(opts commandOptions) error {
	out := map[string]string{
		"status": "manual",
		"hint":   "agent daemons stop when their gs agent start process receives Ctrl-C; installed daemons stop with gs agent uninstall <name>",
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if _, err := fmt.Fprintln(r.Stdout, "agent daemons stop when their gs agent start process receives Ctrl-C; installed daemons stop with gs agent uninstall <name>"); err != nil {
		return err
	}
	return nil
}

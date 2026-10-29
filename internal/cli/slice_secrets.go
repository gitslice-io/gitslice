package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (r Runner) runSliceSetCIDaemon(ctx context.Context, opts commandOptions, sliceRef string, daemonID string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	updated, err := corev1.NewSliceServiceClient(conn).SetSliceCIDaemon(callCtx, &corev1.SetSliceCIDaemonRequest{
		Slice:    ref,
		DaemonId: daemonID,
	})
	if grpcstatus.Code(err) == codes.NotFound && strings.Contains(err.Error(), "daemon not found") {
		return userError("daemon_not_found", "there is no daemon "+daemonID+" that you can use", "A CI daemon runs a slice's checks and holds its secrets, so you can name the daemons you registered or that belong to an account you own or administer, and the daemons their owners exposed to this slice. gs agent status --slice "+sliceRef+" lists them; the owner of any other daemon can expose it with gs agent expose <daemon> "+sliceRef+".")
	}
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, sliceToOutput(updated))
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated slice %s\n", sliceRefLabel(updated.Ref))
	return writeSliceText(r.Stdout, updated)
}

func (r Runner) runSliceSecretSet(ctx context.Context, opts commandOptions, sliceRef, name string, valueArg *string) error {
	if !storage.ValidSliceSecretName(name) {
		return userError("invalid_secret_name", "secret name must match ^[A-Z_][A-Z0-9_]*$", "Use an environment-variable style name such as CI_TOKEN.")
	}
	var value string
	if valueArg != nil {
		value = *valueArg
	} else {
		var err error
		if value, err = r.readSecretValue(name); err != nil {
			return err
		}
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	_, err = corev1.NewSliceServiceClient(conn).SetSliceSecret(callCtx, &corev1.SetSliceSecretRequest{
		Slice: ref,
		Name:  name,
		Value: value,
	})
	if err != nil {
		return secretError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"slice": sliceRefLabel(ref),
			"name":  name,
			"set":   true,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "set secret %s for slice %s\n", name, sliceRefLabel(ref))
	return nil
}

func (r Runner) runSliceSecretRemove(ctx context.Context, opts commandOptions, sliceRef, name string) error {
	if !storage.ValidSliceSecretName(name) {
		return userError("invalid_secret_name", "secret name must match ^[A-Z_][A-Z0-9_]*$", "Use an environment-variable style name such as CI_TOKEN.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	_, err = corev1.NewSliceServiceClient(conn).DeleteSliceSecret(callCtx, &corev1.DeleteSliceSecretRequest{
		Slice: ref,
		Name:  name,
	})
	if err != nil {
		return secretError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"slice":   sliceRefLabel(ref),
			"name":    name,
			"deleted": true,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "removed secret %s from slice %s\n", name, sliceRefLabel(ref))
	return nil
}

func (r Runner) runSliceSecretList(ctx context.Context, opts commandOptions, sliceRef string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	res, err := corev1.NewSliceServiceClient(conn).ListSliceSecrets(callCtx, &corev1.ListSliceSecretsRequest{
		Slice: ref,
	})
	if err != nil {
		return err
	}
	// With --json, account admins also learn the folders each one comes from.
	type sliceSecretOutput struct {
		Name     string   `json:"name"`
		SecretID string   `json:"secret_id"`
		Paths    []string `json:"paths"`
	}
	sources := make([]sliceSecretOutput, 0, len(res.Secrets))
	for _, secret := range res.Secrets {
		sources = append(sources, sliceSecretOutput{Name: secret.Name, SecretID: secret.SecretId, Paths: append([]string{}, secret.Paths...)})
	}
	if opts.jsonOutput() {
		out := map[string]any{"names": res.Names}
		if len(sources) > 0 {
			out["secrets"] = sources
		}
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	for _, name := range res.Names {
		fmt.Fprintln(r.Stdout, name)
	}
	return nil
}

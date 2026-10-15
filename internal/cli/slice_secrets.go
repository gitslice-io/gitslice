package cli

import (
	"context"
	"fmt"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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

func (r Runner) runSliceSecretSet(ctx context.Context, opts commandOptions, sliceRef, name, value string) error {
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
	_, err = corev1.NewSliceServiceClient(conn).SetSliceSecret(callCtx, &corev1.SetSliceSecretRequest{
		Slice: ref,
		Name:  name,
		Value: value,
	})
	if err != nil {
		return err
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
		return err
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
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"names": res.Names})
	}
	if opts.Quiet {
		return nil
	}
	for _, name := range res.Names {
		fmt.Fprintln(r.Stdout, name)
	}
	return nil
}

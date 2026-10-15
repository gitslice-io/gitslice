package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
)

func (r Runner) runGenServiceKey(opts commandOptions, outDir string) error {
	priv, pub, err := servicetoken.GenerateKeyPair()
	if err != nil {
		return err
	}
	if outDir != "" {
		privPath := filepath.Join(outDir, "service-key.pem")
		pubPath := filepath.Join(outDir, "service-key.pub.pem")
		if err := os.WriteFile(privPath, []byte(priv), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(pubPath, []byte(pub), 0o644); err != nil {
			return err
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, map[string]any{"private_key_path": privPath, "public_key_path": pubPath})
		}
		fmt.Fprintf(r.Stdout, "wrote %s (keep secret) and %s\n", privPath, pubPath)
		fmt.Fprintf(r.Stdout, "Configure the server with GITSLICE_SERVICE_JWT_PUBLIC_KEY (contents of %s).\n", pubPath)
		return nil
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"private_key_pem": priv, "public_key_pem": pub})
	}
	fmt.Fprintf(r.Stdout, "# Private key (keep secret; pass to `gs auth mint-service-token --key`):\n%s\n", priv)
	fmt.Fprintf(r.Stdout, "# Public key (set as server GITSLICE_SERVICE_JWT_PUBLIC_KEY):\n%s\n", pub)
	return nil
}

func (r Runner) runMintServiceToken(opts commandOptions, keyPath, subject, email, issuer string, ttl time.Duration) error {
	if keyPath == "" {
		return userError("invalid_args", "--key is required", "Generate one with gs auth gen-service-key.")
	}
	if subject == "" {
		return userError("invalid_args", "--subject is required", "Pass the subject id to authenticate as, e.g. --subject svc_test.")
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	token, err := servicetoken.Mint(string(keyPEM), subject, email, issuer, ttl)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"token": token})
	}
	fmt.Fprintln(r.Stdout, token)
	return nil
}
